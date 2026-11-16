// Package hosttunnel is the wire layer of the subscription host (issue #48). The panel, which may sit behind NAT, dials a
// TLS connection to the host node's TCP 443 with the subscription name as SNI and a ClientHello that imitates Chrome, and
// speaks HTTP/2 over it as the server; the node is the HTTP/2 client and opens one stream per forwarded connection: the
// raw TLS of a subscription client (KindTLS) or the node's own agent link (KindLink). Each stream is a POST to StreamPath
// whose request body carries node→panel bytes and whose response body carries panel→node bytes.
//
// The node tells the panel's hello from a browser's by a mark sealed in its session id with the tunnel secret (SealMark):
// the SNI must resolve in DNS (censoring networks drop TLS whose SNI does not), and a constant extension or a
// non-browser fingerprint would mark the connection to anyone watching.
package hosttunnel

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"
)

const (
	// StreamPath is the request path of every tunnel stream.
	StreamPath = "/s"
	// HeaderKind says what a stream carries: KindTLS or KindLink.
	HeaderKind = "Mg-Kind"
	// HeaderPeer is the address of the client whose connection a KindTLS stream carries (netip.AddrPort form).
	HeaderPeer = "Mg-Peer"
	// KindTLS is a subscription client's raw TLS connection, terminated by the panel.
	KindTLS = "tls"
	// KindLink is the host node's own agent link to the panel.
	KindLink = "link"
	// MaxStreams bounds the concurrent streams of one tunnel.
	MaxStreams = 1024
	// ReadIdle is how long a tunnel may be silent before the node pings the panel.
	ReadIdle = 15 * time.Second
	// PingTimeout is how long the node waits for the answer to that ping before it drops the tunnel.
	PingTimeout = 10 * time.Second
)

// maxHello bounds what PeekHello buffers while it looks for a ClientHello.
const maxHello = 16 << 10

var (
	errHelloSeen = errors.New("hosttunnel: client hello read")
	errHelloLong = errors.New("hosttunnel: client hello too long")
)

// recordingConn feeds a throwaway tls.Server: it keeps every byte read (up to maxHello) and swallows every write, so the
// alert the aborted handshake sends never reaches the client.
type recordingConn struct {
	net.Conn
	buf     bytes.Buffer
	sockErr error
}

func (r *recordingConn) Read(p []byte) (int, error) {
	room := maxHello - r.buf.Len()
	if room <= 0 {
		return 0, errHelloLong
	}
	if len(p) > room {
		p = p[:room]
	}
	n, err := r.Conn.Read(p)
	r.buf.Write(p[:n])
	if err != nil {
		r.sockErr = err
	}
	return n, err
}

func (r *recordingConn) Write(p []byte) (int, error) { return len(p), nil }

// Hello is what PeekHello reads of a ClientHello.
type Hello struct {
	ServerName string
	// raw is the ClientHello handshake message when it came in one TLS record, else nil.
	raw []byte
}

// PeekHello reads the ClientHello of c within timeout and returns what it says, and a connection that replays the bytes
// read so far before the rest of c. Bytes that are not a ClientHello give a zero Hello and a replay
// of them (err == nil): the caller passes such a connection on to its site. err != nil only when reading c failed; c is
// closed then.
func PeekHello(c net.Conn, timeout time.Duration) (hello Hello, replay net.Conn, err error) {
	if err := c.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		_ = c.Close()
		return Hello{}, nil, err
	}
	rec := &recordingConn{Conn: c}
	seen := false
	srv := tls.Server(rec, &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		seen = true
		hello = Hello{ServerName: h.ServerName}
		return nil, errHelloSeen
	}})
	_ = srv.Handshake()
	if rec.sockErr != nil && !seen {
		_ = c.Close()
		return Hello{}, nil, rec.sockErr
	}
	if seen {
		hello.raw = helloMessage(rec.buf.Bytes())
	}
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		_ = c.Close()
		return Hello{}, nil, err
	}
	return hello, &replayConn{Conn: c, head: rec.buf.Bytes()}, nil
}

// replayConn returns head before reading the connection.
type replayConn struct {
	net.Conn
	head []byte
}

func (r *replayConn) Read(p []byte) (int, error) {
	if len(r.head) > 0 {
		n := copy(p, r.head)
		r.head = r.head[n:]
		return n, nil
	}
	return r.Conn.Read(p)
}

// StreamConn is a net.Conn over one tunnel stream: reads come from r, writes go to w and are flushed one by one.
//
// Reads have deadlines of their own with net.Conn semantics: one that passed fails the pending Read with
// os.ErrDeadlineExceeded and a later one may be set (net/http does exactly that to a hijacked connection). An HTTP/2
// stream's own read deadline cannot serve: once it passes, the request body stays broken.
type StreamConn struct {
	r       io.ReadCloser
	w       io.Writer
	flush   func()
	local   net.Addr
	remote  net.Addr
	onClose func()

	// SetWrite carries the write deadline; nil = write deadlines are ignored.
	SetWrite func(time.Time) error

	// The pump reads r into buf and hands each read over (ready); Read takes it and gives the buffer back (consumed).
	pumpOnce sync.Once
	buf      []byte
	ready    chan struct{}
	consumed chan struct{}
	rmu      sync.Mutex // serialises Read; guards have, avail, rerr once handed over
	have     bool
	avail    []byte
	rerr     error
	rd       deadline

	wmu       sync.Mutex
	dmu       sync.Mutex // serialises SetWrite with the end of the stream: it must not run once the handler returned
	closed    atomic.Bool
	done      chan struct{}
	closeOnce sync.Once
}

// streamReadBuffer is the size of a StreamConn's read buffer.
const streamReadBuffer = 16 << 10

// NewStreamConn builds the connection. flush and onClose may be nil.
func NewStreamConn(r io.ReadCloser, w io.Writer, flush func(), local, remote net.Addr, onClose func()) *StreamConn {
	return &StreamConn{r: r, w: w, flush: flush, local: local, remote: remote, onClose: onClose,
		ready: make(chan struct{}, 1), consumed: make(chan struct{}, 1), done: make(chan struct{}), rd: deadline{cancel: make(chan struct{})}}
}

func (c *StreamConn) pump() {
	for {
		n, err := c.r.Read(c.buf)
		c.avail, c.rerr = c.buf[:n], err // Read touches them only after ready
		select {
		case c.ready <- struct{}{}:
		case <-c.done:
			return
		}
		if err != nil {
			return
		}
		select {
		case <-c.consumed:
		case <-c.done:
			return
		}
	}
}

func (c *StreamConn) Read(p []byte) (int, error) {
	c.pumpOnce.Do(func() {
		c.buf = make([]byte, streamReadBuffer)
		go c.pump()
	})
	c.rmu.Lock()
	defer c.rmu.Unlock()
	for !c.have {
		select {
		case <-c.ready:
			c.have = true
		case <-c.rd.wait():
			return 0, os.ErrDeadlineExceeded
		case <-c.done:
			return 0, net.ErrClosed
		}
	}
	if len(c.avail) == 0 && c.rerr != nil {
		return 0, c.rerr
	}
	n := copy(p, c.avail)
	c.avail = c.avail[n:]
	if len(c.avail) == 0 && c.rerr == nil {
		c.have = false
		c.consumed <- struct{}{}
	}
	return n, nil
}

func (c *StreamConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	n, err := c.w.Write(p)
	if n > 0 && c.flush != nil {
		c.flush()
	}
	return n, err
}

// Close is idempotent: it ends pending reads, closes the reader and runs onClose once.
func (c *StreamConn) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		close(c.done)
		_ = c.r.Close()
		if c.onClose != nil {
			c.onClose()
		}
	})
	return nil
}

// waitWrites returns once no Write is running; after Close none starts again. A write stuck on flow control is cut by
// unblock first.
func (c *StreamConn) waitWrites(unblock func()) {
	if c.wmu.TryLock() {
		c.wmu.Unlock()
	} else {
		unblock()
		c.wmu.Lock()
		c.wmu.Unlock() //nolint:staticcheck // an empty critical section: waits for the writer
	}
	c.dmu.Lock()
	c.dmu.Unlock() //nolint:staticcheck // waits for a running SetWriteDeadline; none starts after Close
}

func (c *StreamConn) LocalAddr() net.Addr  { return c.local }
func (c *StreamConn) RemoteAddr() net.Addr { return c.remote }

func (c *StreamConn) SetDeadline(t time.Time) error {
	return errors.Join(c.SetReadDeadline(t), c.SetWriteDeadline(t))
}

func (c *StreamConn) SetReadDeadline(t time.Time) error {
	c.rd.set(t)
	return nil
}

// SetWriteDeadline does nothing once the connection is closed: the stream's response writer is gone then (net/http still
// sets a deadline on a connection it closes, for the TLS close-notify).
func (c *StreamConn) SetWriteDeadline(t time.Time) error {
	c.dmu.Lock()
	defer c.dmu.Unlock()
	if c.SetWrite == nil || c.closed.Load() {
		return nil
	}
	return c.SetWrite(t)
}

// deadline is net.Pipe's: cancel is closed while the deadline has passed.
type deadline struct {
	mu     sync.Mutex
	timer  *time.Timer
	cancel chan struct{}
}

func (d *deadline) set(t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil && !d.timer.Stop() {
		<-d.cancel // the timer fired: wait for it to have closed cancel
	}
	d.timer = nil
	closed := isClosed(d.cancel)
	if t.IsZero() {
		if closed {
			d.cancel = make(chan struct{})
		}
		return
	}
	if dur := time.Until(t); dur > 0 {
		if closed {
			d.cancel = make(chan struct{})
		}
		cancel := d.cancel
		d.timer = time.AfterFunc(dur, func() { close(cancel) })
		return
	}
	if !closed {
		close(d.cancel)
	}
}

func (d *deadline) wait() chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cancel
}

func isClosed(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// tunnelAddr is an address that is not a socket address (the far end of the tunnel when its own is unknown).
type tunnelAddr string

func (a tunnelAddr) Network() string { return "tunnel" }
func (a tunnelAddr) String() string  { return string(a) }

func addrOf(s string) net.Addr {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return net.TCPAddrFromAddrPort(ap)
	}
	return tunnelAddr(s)
}

// ServerHandler serves the panel's end of a tunnel (an HTTP/2 server on the connection the panel dialed). Every valid
// stream becomes a connection handed to accept with its kind; accept returns false to refuse it (the stream is then
// closed). The handler holds the stream until the connection is closed or the stream ends.
func ServerHandler(accept func(kind string, c net.Conn) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != StreamPath {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		kind := r.Header.Get(HeaderKind)
		if kind != KindTLS && kind != KindLink {
			http.Error(w, "bad kind", http.StatusBadRequest)
			return
		}
		var remote net.Addr
		if p := r.Header.Get(HeaderPeer); p != "" {
			ap, err := netip.ParseAddrPort(p)
			if err != nil {
				http.Error(w, "bad peer", http.StatusBadRequest)
				return
			}
			remote = net.TCPAddrFromAddrPort(ap)
		} else if kind == KindTLS {
			http.Error(w, "bad peer", http.StatusBadRequest)
			return
		} else {
			remote = addrOf(r.RemoteAddr)
		}
		var local net.Addr = tunnelAddr(r.Host)
		if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && a != nil {
			local = a
		}
		rc := http.NewResponseController(w)
		w.WriteHeader(http.StatusOK)
		if err := rc.Flush(); err != nil {
			return
		}
		done := make(chan struct{})
		c := NewStreamConn(r.Body, w, func() { _ = rc.Flush() }, local, remote, func() { close(done) })
		c.SetWrite = rc.SetWriteDeadline
		if !accept(kind, c) {
			_ = c.Close()
		} else {
			select {
			case <-done:
			case <-r.Context().Done():
				_ = c.Close()
			}
		}
		// The response writer must not be touched once the handler returns: wait for a running write, cutting it short
		// when it is stuck on flow control.
		c.waitWrites(func() { _ = rc.SetWriteDeadline(time.Now()) })
	})
}

// OpenStream opens a stream of kind on the node's end of a tunnel. peer is the client a KindTLS stream carries (it must
// be valid then); a KindLink stream passes the zero value. ctx bounds opening the stream, not its life.
func OpenStream(ctx context.Context, cc *http2.ClientConn, host, kind string, peer netip.AddrPort) (net.Conn, error) {
	pr, pw := io.Pipe()
	streamCtx, cancel := context.WithCancel(context.Background())
	stop := context.AfterFunc(ctx, cancel)
	req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, "https://"+host+StreamPath, pr)
	if err != nil {
		stop()
		cancel()
		return nil, err
	}
	req.Header.Set(HeaderKind, kind)
	if peer.IsValid() {
		req.Header.Set(HeaderPeer, peer.String())
	}
	resp, err := cc.RoundTrip(req)
	if !stop() {
		// ctx ended while the stream opened: it is cancelled already, or about to be.
		if err == nil {
			_ = resp.Body.Close()
			err = ctx.Err()
		}
	}
	if err != nil {
		cancel()
		_ = pw.CloseWithError(err)
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		cancel()
		_ = pw.Close()
		return nil, fmt.Errorf("tunnel stream refused: %d", resp.StatusCode)
	}
	return NewStreamConn(resp.Body, pw, nil, tunnelAddr("node"), tunnelAddr(host), func() {
		_ = pw.Close()
		cancel()
	}), nil
}
