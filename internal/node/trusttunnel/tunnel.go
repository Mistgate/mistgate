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
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/trusttunnel"
)

const (
	tunnelBufferSize = 32 << 10
	// maxCredentialConnections is how many HTTP/2 or HTTP/3 connections one credential may hold at once. A connection counts
	// for every credential that has a tunnel on it. A new connection past the limit takes the place of the credential's
	// oldest connection that has no tunnel of it (a client that changed networks leaves such connections behind, silent
	// until the keepalive or the idle time ends them); when every connection has a tunnel, the CONNECT is refused.
	maxCredentialConnections = 16
)

var tunnelBuffers = sync.Pool{New: func() any { return make([]byte, tunnelBufferSize) }}

// connectionState is one authenticated HTTP/2 or HTTP/3 connection. Every CONNECT is authorized on its own, so a connection
// can carry tunnels of several credentials; it lives as long as any installed credential still has a place on it.
type connectionState struct {
	in        *inbound
	key       any
	closeConn func()

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
	out  io.Writer
	rc   *http.ResponseController
	ctx  context.Context
	h3   bool

	cancel         context.CancelFunc
	once           sync.Once
	mu             sync.Mutex
	up             net.Conn
	clean          func()
	done           bool // the handler is returning: the ResponseWriter is not to be used any more
	reset          bool
	rawH3          bool
	abortTransport func()
	closeTransport func()
}

// beginStream and beginStreamOnConn say why a stream was not accepted; only plugin.RefusedStreamLimit is told to the client.
func (in *inbound) beginStream(r *http.Request, w http.ResponseWriter, conn net.Conn, cs *credState) (*streamState, plugin.TunnelRefusal, bool) {
	if conn == nil {
		return nil, plugin.RefusedStreamClosed, false
	}
	return in.beginStreamOnConn(r, w, conn, func() { _ = conn.Close() }, w, cs)
}

func (in *inbound) beginStreamOnConn(r *http.Request, w http.ResponseWriter, key any, closeConn func(), out io.Writer, cs *credState) (*streamState, plugin.TunnelRefusal, bool) {
	if key == nil || r.Context().Err() != nil {
		in.refuse(plugin.RefusedStreamClosed, 0)
		return nil, plugin.RefusedStreamClosed, false
	}
	in.connMu.Lock()
	if r.Context().Err() != nil {
		in.connMu.Unlock()
		in.refuse(plugin.RefusedStreamClosed, 0)
		return nil, plugin.RefusedStreamClosed, false
	}
	connection := in.conns[key]
	if connection == nil {
		connection = &connectionState{
			in: in, key: key, closeConn: closeConn, streams: make(map[*streamState]struct{}),
			credentials: make(map[*credState]time.Time), banned: make(map[*credState]struct{}),
			udp: make(map[*credState]*streamState),
		}
		in.conns[key] = connection
	}
	in.connMu.Unlock()

	ctx, cancel := context.WithCancel(r.Context())
	stream := &streamState{
		in: in, conn: connection, cred: cs, body: r.Body, w: w, out: out, rc: http.NewResponseController(w), ctx: ctx,
		h3: r.ProtoMajor == 3, cancel: cancel,
	}
	for {
		connection.mu.Lock()
		if _, banned := connection.banned[cs]; banned {
			connection.mu.Unlock()
			cancel()
			in.refuse(plugin.RefusedStreamCredGone, 0)
			return nil, plugin.RefusedStreamCredGone, false
		}
		if connection.closed || connection.expiring {
			connection.mu.Unlock()
			cancel()
			in.refuse(plugin.RefusedStreamClosed, 0)
			return nil, plugin.RefusedStreamClosed, false
		}
		cs.mu.Lock()
		if cs.removed.Load() || cs.expired(in.e.now()) {
			cs.mu.Unlock()
			connection.mu.Unlock()
			cancel()
			in.refuse(plugin.RefusedStreamCredGone, 0)
			return nil, plugin.RefusedStreamCredGone, false
		}
		if _, exists := cs.connections[connection]; !exists {
			if len(cs.connections) >= maxCredentialConnections {
				// Another connection of the credential is detached with no lock held (detach takes the connection's lock before
				// the credential's); the limit is checked again afterwards, so two CONNECTs racing here never exceed it.
				victim := cs.idleConnection()
				cs.mu.Unlock()
				connection.mu.Unlock()
				if victim == nil {
					cancel()
					in.refuse(plugin.RefusedStreamLimit, 0)
					return nil, plugin.RefusedStreamLimit, false
				}
				if !victim.detach(cs) { // it is closing or being detached by someone else: it leaves the set either way
					cs.mu.Lock()
					delete(cs.connections, victim)
					cs.mu.Unlock()
				}
				continue
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
		return stream, 0, true
	}
}

// idleConnection is the oldest connection of the credential that has no tunnel of it, nil when there is none. The caller
// holds cs.mu.
func (cs *credState) idleConnection() *connectionState {
	busy := make(map[*connectionState]struct{}, len(cs.streams))
	for stream := range cs.streams {
		busy[stream.conn] = struct{}{}
	}
	var victim *connectionState
	var oldest time.Time
	for connection, since := range cs.connections {
		if _, live := busy[connection]; live {
			continue
		}
		if victim == nil || since.Before(oldest) {
			victim, oldest = connection, since
		}
	}
	return victim
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
				in.closeConnection(connection.key)
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

func (in *inbound) closeConnection(key any) {
	in.connMu.Lock()
	connection := in.conns[key]
	delete(in.conns, key)
	in.connMu.Unlock()
	if connection != nil {
		connection.shutdown()
		if connection.closeConn != nil {
			connection.closeConn()
		}
	}
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
		connection.in.closeConnection(connection.key)
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
// stuck on a client that stopped reading is failed by resetting only this HTTP/2 or HTTP/3 stream.
func (stream *streamState) abort() {
	stream.mu.Lock()
	if !stream.done && !stream.reset {
		stream.reset = true
		if stream.abortTransport != nil {
			stream.abortTransport()
		} else if !stream.h3 {
			// A deadline in the past resets an HTTP/2 stream at once; x/net does it before returning. An HTTP/3 stream whose
			// response has not started is reset by the handler (endedBeforeResponse), never by a failed write: the library
			// would still end it with a bare FIN, which the client takes for a protocol error of the whole connection.
			_ = stream.rc.SetWriteDeadline(time.Unix(1, 0))
		}
	}
	stream.mu.Unlock()
	stream.free()
}

// release is the soft end, from the handler: the response finishes normally. It never touches the ResponseWriter, and after
// it abort no longer does either.
func (stream *streamState) release() {
	stream.mu.Lock()
	stream.done = true
	closeTransport := stream.closeTransport
	stream.closeTransport = nil
	stream.mu.Unlock()
	stream.free()
	if closeTransport != nil {
		closeTransport()
	}
}

// endedBeforeResponse reports an HTTP/3 stream that was aborted while its response had not been taken over: the handler
// must end it with http.ErrAbortHandler, which the library turns into RESET_STREAM and not into a bare FIN.
func (stream *streamState) endedBeforeResponse() bool {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.h3 && stream.reset && !stream.rawH3
}

func (stream *streamState) useHTTP3(out io.Writer, abort, closeTransport func()) {
	stream.mu.Lock()
	stream.out, stream.rawH3 = out, true
	stream.abortTransport, stream.closeTransport = abort, closeTransport
	if stream.reset && abort != nil {
		abort()
	}
	stream.mu.Unlock()
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
	if stream.rawH3 {
		return nil
	}
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
			reason := classifyDial(err)
			in.refuse(reason, authority.Port)
			writeTunnelError(w, warningFor(reason), authority.Host)
		}
		return
	}
	if !stream.setUpstream(upstream) {
		return
	}
	w.WriteHeader(http.StatusOK)
	if err := stream.startHTTP3(); err != nil {
		return
	}
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
			written, err := stream.out.Write(buffer[:n])
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
