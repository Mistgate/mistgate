package trusttunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/node/ratelimit"
	"github.com/mistgate/mistgate/internal/trusttunnel"
)

const (
	tunnelBufferSize = 32 << 10
	// maxCredentialConnections is how many HTTP/2 connections one credential may hold at once. A connection counts for every
	// credential that has a tunnel on it.
	maxCredentialConnections = 16
)

var tunnelBuffers = sync.Pool{New: func() any { return make([]byte, tunnelBufferSize) }}

// connectionState is one authenticated HTTP/2 connection. Every CONNECT is authorized on its own, so a connection can carry
// tunnels of several credentials; it lives as long as any installed credential still has a place on it.
type connectionState struct {
	in   *inbound
	conn net.Conn

	mu          sync.Mutex
	closed      bool
	expiring    bool
	active      int
	streams     map[*streamState]struct{}
	credentials map[*credState]time.Time    // credentials with a tunnel on this connection, and since when
	banned      map[*credState]struct{}     // kicked: no further tunnel of these on this connection
	udp         map[*credState]*streamState // the _udp2 stream of each credential on this connection
	idle        *time.Timer
}

// streamState is one CONNECT stream. Its resources are freed once, whoever ends it first; only the handler goroutine ever
// returns, and it does so last (finishStream), so nothing touches the ResponseWriter after the handler is gone.
type streamState struct {
	in   *inbound
	conn *connectionState
	cred *credState
	body io.ReadCloser
	w    http.ResponseWriter
	rc   *http.ResponseController
	ctx  context.Context

	cancel context.CancelFunc
	once   sync.Once
	mu     sync.Mutex
	up     net.Conn
	clean  func()
	done   bool // the handler is returning: the ResponseWriter is not to be used any more
	reset  bool
}

func (in *inbound) beginStream(r *http.Request, w http.ResponseWriter, conn net.Conn, cs *credState) (*streamState, bool) {
	if conn == nil {
		return nil, false
	}
	in.connMu.Lock()
	connection := in.conns[conn]
	if connection == nil {
		connection = &connectionState{
			in: in, conn: conn, streams: make(map[*streamState]struct{}),
			credentials: make(map[*credState]time.Time), banned: make(map[*credState]struct{}),
			udp: make(map[*credState]*streamState),
		}
		in.conns[conn] = connection
	}
	in.connMu.Unlock()

	ctx, cancel := context.WithCancel(r.Context())
	stream := &streamState{
		in: in, conn: connection, cred: cs, body: r.Body, w: w, rc: http.NewResponseController(w), ctx: ctx, cancel: cancel,
	}
	connection.mu.Lock()
	if _, banned := connection.banned[cs]; banned || connection.closed || connection.expiring {
		connection.mu.Unlock()
		cancel()
		return nil, false
	}
	cs.mu.Lock()
	if cs.removed.Load() || cs.expired(in.e.now()) {
		cs.mu.Unlock()
		connection.mu.Unlock()
		cancel()
		return nil, false
	}
	if _, exists := cs.connections[connection]; !exists {
		if len(cs.connections) >= maxCredentialConnections {
			cs.mu.Unlock()
			connection.mu.Unlock()
			cancel()
			return nil, false
		}
		cs.connections[connection] = in.e.now()
		cs.connects.Add(1)
	}
	connection.credentials[cs] = cs.connections[connection]
	cs.streams[stream] = struct{}{}
	cs.active++
	connection.streams[stream] = struct{}{}
	connection.active++
	if connection.idle != nil {
		connection.idle.Stop()
		connection.idle = nil
	}
	cs.mu.Unlock()
	connection.mu.Unlock()
	return stream, true
}

// finishStream runs when the handler is about to return.
func (in *inbound) finishStream(stream *streamState) {
	stream.release()
	connection := stream.conn
	connection.mu.Lock()
	delete(connection.streams, stream)
	if connection.udp[stream.cred] == stream {
		delete(connection.udp, stream.cred)
	}
	if connection.active > 0 {
		connection.active--
	}
	if !connection.closed && connection.active == 0 {
		connection.idle = time.AfterFunc(in.e.timing.connectionIdle, func() {
			connection.mu.Lock()
			expire := !connection.closed && connection.active == 0
			if expire {
				connection.expiring = true
			}
			connection.mu.Unlock()
			if expire {
				in.closeConnection(connection.conn)
			}
		})
	}
	connection.mu.Unlock()

	stream.cred.mu.Lock()
	delete(stream.cred.streams, stream)
	if stream.cred.active > 0 {
		stream.cred.active--
	}
	stream.cred.mu.Unlock()
}

func (in *inbound) closeConnection(conn net.Conn) {
	in.connMu.Lock()
	connection := in.conns[conn]
	delete(in.conns, conn)
	in.connMu.Unlock()
	if connection != nil {
		connection.shutdown()
	}
	_ = conn.Close()
}

func (connection *connectionState) shutdown() {
	connection.mu.Lock()
	if connection.closed {
		connection.mu.Unlock()
		return
	}
	connection.closed = true
	if connection.idle != nil {
		connection.idle.Stop()
		connection.idle = nil
	}
	streams := make([]*streamState, 0, len(connection.streams))
	for stream := range connection.streams {
		streams = append(streams, stream)
	}
	credentials := make([]*credState, 0, len(connection.credentials))
	for cs := range connection.credentials {
		credentials = append(credentials, cs)
	}
	connection.streams = nil
	connection.credentials = nil
	connection.banned = nil
	connection.udp = nil
	connection.mu.Unlock()

	for _, stream := range streams {
		stream.abort()
	}
	for _, cs := range credentials {
		cs.mu.Lock()
		delete(cs.connections, connection)
		cs.mu.Unlock()
	}
}

// claimUDP makes stream the _udp2 stream of its credential on this connection and returns the one it replaces, which the
// caller ends. A closed connection claims nothing: its streams are being ended already.
func (connection *connectionState) claimUDP(stream *streamState) *streamState {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.closed {
		return nil
	}
	previous := connection.udp[stream.cred]
	connection.udp[stream.cred] = stream
	return previous
}

// detach ends every tunnel of one credential on this connection, at once, and refuses its further CONNECTs here. Other
// credentials' tunnels on the same connection are untouched; the connection itself closes when no installed credential
// has a place on it any more. It reports whether the credential had a tunnel here.
func (connection *connectionState) detach(credential *credState) bool {
	connection.mu.Lock()
	if connection.closed {
		connection.mu.Unlock()
		return false
	}
	if _, exists := connection.credentials[credential]; !exists {
		connection.mu.Unlock()
		return false
	}
	delete(connection.credentials, credential)
	connection.banned[credential] = struct{}{}
	keepConnection := false
	for other := range connection.credentials {
		if !other.removed.Load() {
			keepConnection = true
			break
		}
	}
	if !keepConnection {
		connection.expiring = true
	}
	streams := make([]*streamState, 0)
	for stream := range connection.streams {
		if stream.cred == credential {
			streams = append(streams, stream)
		}
	}
	connection.mu.Unlock()

	credential.mu.Lock()
	delete(credential.connections, connection)
	credential.mu.Unlock()
	for _, stream := range streams {
		stream.abort()
	}
	if !keepConnection {
		connection.in.closeConnection(connection.conn)
	}
	return true
}

// closeSessions is Kick: it ends the credential's tunnels on every connection. A credential that is still installed may
// open new connections; on the connection it was kicked from it may not.
func (cs *credState) closeSessions() bool {
	cs.mu.Lock()
	connections := make([]*connectionState, 0, len(cs.connections))
	for connection := range cs.connections {
		connections = append(connections, connection)
	}
	streams := make([]*streamState, 0, len(cs.streams))
	for stream := range cs.streams {
		streams = append(streams, stream)
	}
	cs.mu.Unlock()
	closed := false
	for _, connection := range connections {
		closed = connection.detach(cs) || closed
	}
	for _, stream := range streams {
		stream.abort()
	}
	return closed || len(streams) != 0
}

func (stream *streamState) setUpstream(conn net.Conn) bool {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.ctx.Err() != nil {
		_ = conn.Close()
		return false
	}
	stream.up = conn
	return true
}

func (stream *streamState) setCleanup(clean func()) {
	stream.mu.Lock()
	if stream.ctx.Err() != nil {
		stream.mu.Unlock()
		clean()
		return
	}
	stream.clean = clean
	stream.mu.Unlock()
}

// abort ends the stream by force, from any goroutine and any number of times: its sockets and reads are closed, and a write
// stuck on a client that stopped reading is failed by resetting the HTTP/2 stream (closing the body does not free it).
func (stream *streamState) abort() {
	stream.free()
	stream.mu.Lock()
	if !stream.done && !stream.reset {
		stream.reset = true
		// A deadline in the past resets the stream at once; x/net does it before returning.
		_ = stream.rc.SetWriteDeadline(time.Unix(1, 0))
	}
	stream.mu.Unlock()
}

// release is the soft end, from the handler: the response finishes normally. It never touches the ResponseWriter, and after
// it abort no longer does either.
func (stream *streamState) release() {
	stream.mu.Lock()
	stream.done = true
	stream.mu.Unlock()
	stream.free()
}

func (stream *streamState) free() {
	stream.once.Do(func() {
		stream.cancel()
		if stream.body != nil {
			_ = stream.body.Close()
		}
		stream.mu.Lock()
		up, clean := stream.up, stream.clean
		stream.up, stream.clean = nil, nil
		stream.mu.Unlock()
		if up != nil {
			_ = up.Close()
		}
		if clean != nil {
			clean()
		}
	})
}

// clearDeadlines takes the stream out of the site's per-request Read/WriteTimeout. The tunnel has its own idle timer.
func (stream *streamState) clearDeadlines() error {
	if err := stream.rc.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	return stream.rc.SetWriteDeadline(time.Time{})
}

func (stream *streamState) flush() error {
	if err := stream.rc.Flush(); err == nil {
		return nil
	} else if !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	if flusher, ok := stream.w.(http.Flusher); ok {
		flusher.Flush()
		return nil
	}
	return http.ErrNotSupported
}

func (in *inbound) serveTCP(w http.ResponseWriter, stream *streamState, authority trusttunnel.Authority) {
	target := net.JoinHostPort(authority.Host, strconv.Itoa(int(authority.Port)))
	upstream, err := in.dialTCP(stream.ctx, target)
	if err != nil {
		if stream.ctx.Err() == nil {
			writeTunnelError(w, warningForDialError(err), authority.Host)
		}
		return
	}
	if !stream.setUpstream(upstream) {
		return
	}
	w.WriteHeader(http.StatusOK)
	if err := stream.flush(); err != nil {
		return
	}
	in.relayTCP(stream, upstream)
}

type dialResult struct {
	conn net.Conn
	err  error
}

// dialTCP asks the egress for a connection, for at most the connect timeout. A dial that finishes after the caller gave up
// is closed, never leaked.
func (in *inbound) dialTCP(ctx context.Context, target string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, in.e.timing.connect)
	defer cancel()
	result := make(chan dialResult, 1)
	go func() {
		conn, err := in.out.TCP(target)
		result <- dialResult{conn: conn, err: err}
	}()
	select {
	case got := <-result:
		if err := ctx.Err(); err != nil {
			if got.conn != nil {
				_ = got.conn.Close()
			}
			return nil, err
		}
		return got.conn, got.err
	case <-ctx.Done():
		go func() {
			if got := <-result; got.conn != nil {
				_ = got.conn.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

type copyResult struct {
	upload bool
	err    error
}

// relayTCP copies both ways until both are done. The client's half-close reaches the upstream as a half-close. The
// upstream's cannot reach the client as one: net/http ends an HTTP/2 response only when the handler returns, and that
// takes the request half with it. So after the upstream's EOF the client gets eofGrace of silence to finish what it was
// sending, then the stream ends and its END_STREAM is the client's EOF. The same soft end closes a tunnel that stayed
// silent for the idle time; a write stuck on a client that does not read is cut by a reset if the soft end does not free it.
func (in *inbound) relayTCP(stream *streamState, upstream net.Conn) {
	activity := make(chan struct{}, 1)
	done := make(chan copyResult, 2)
	go func() { done <- copyUpload(stream, upstream, activity) }()
	go func() { done <- copyDownload(stream, upstream, activity) }()

	wait := in.e.timing.streamIdle
	timer := time.NewTimer(wait)
	defer timer.Stop()
	rearm := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
	}
	finished, ending := 0, false
	var contextDone <-chan struct{} = stream.ctx.Done()
	for finished < 2 {
		select {
		case result := <-done:
			finished++
			switch {
			case ending:
			case result.err != nil && !errors.Is(result.err, io.EOF):
				stream.abort()
				contextDone = nil
			case !result.upload:
				wait = in.e.timing.eofGrace
				rearm()
			}
		case <-activity:
			if !ending {
				rearm()
			}
		case <-contextDone:
			stream.abort()
			contextDone = nil
		case <-timer.C:
			if ending {
				stream.abort()
				timer.Reset(time.Hour)
				break
			}
			ending, contextDone = true, nil
			stream.free()
			wait = in.e.timing.eofGrace
			rearm()
		}
	}
}

func copyUpload(stream *streamState, dst net.Conn, activity chan<- struct{}) copyResult {
	buffer := tunnelBuffers.Get().([]byte)
	defer tunnelBuffers.Put(buffer)
	for {
		n, readErr := stream.body.Read(buffer)
		if n > 0 {
			if stream.cred.expired(stream.in.e.now()) ||
				!ratelimit.WaitUp(stream.ctx, stream.cred.limit.Load(), stream.cred.person.Load(), uint64(n)) {
				return copyResult{upload: true, err: context.Canceled}
			}
			written, err := writeConn(dst, buffer[:n])
			stream.cred.up.Add(uint64(written))
			if written > 0 {
				signalActivity(activity)
			}
			if err != nil {
				return copyResult{upload: true, err: err}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				closeWrite(dst)
				return copyResult{upload: true}
			}
			return copyResult{upload: true, err: readErr}
		}
	}
}

func copyDownload(stream *streamState, src net.Conn, activity chan<- struct{}) copyResult {
	buffer := tunnelBuffers.Get().([]byte)
	defer tunnelBuffers.Put(buffer)
	for {
		n, readErr := src.Read(buffer)
		if n > 0 {
			if stream.cred.expired(stream.in.e.now()) ||
				!ratelimit.WaitDown(stream.ctx, stream.cred.limit.Load(), stream.cred.person.Load(), uint64(n)) {
				return copyResult{err: context.Canceled}
			}
			written, err := stream.w.Write(buffer[:n])
			stream.cred.down.Add(uint64(written))
			if written > 0 {
				signalActivity(activity)
				if flushErr := stream.flush(); err == nil {
					err = flushErr
				}
			}
			if err != nil {
				return copyResult{err: err}
			}
			if written != n {
				return copyResult{err: io.ErrShortWrite}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return copyResult{}
			}
			return copyResult{err: readErr}
		}
	}
}

func writeConn(dst net.Conn, b []byte) (int, error) {
	total := 0
	for total < len(b) {
		n, err := dst.Write(b[total:])
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

// closeWrite passes the client's half-close on. A connection that cannot half-close is left open: closing it would cut off
// the reply the client is still waiting for.
func closeWrite(conn net.Conn) {
	if half, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = half.CloseWrite()
	}
}

func signalActivity(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
