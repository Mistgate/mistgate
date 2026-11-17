package front

import (
	"crypto/tls"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
)

// maxHello bounds what is read while looking for the ClientHello; a longer one goes to the default route as it is.
const maxHello = 16 << 10

// verdict says what the first bytes of a connection were.
type verdict int

const (
	helloOK       verdict = iota // a ClientHello was read (the name may be empty)
	helloNotTLS                  // the first record is not a TLS handshake record: HTTP, SSLv2, noise
	helloBad                     // a TLS record, but the hello does not parse
	helloTooLarge                // no complete hello in maxHello bytes
	helloTimeout                 // the read deadline passed first
	helloClosed                  // the client went away, or the connection was closed
)

var (
	errAbort    = errors.New("front: hello read")
	errTooLarge = errors.New("front: hello too large")
)

// helloReader is what crypto/tls reads the hello through: every byte is kept, and nothing written back leaves.
type helloReader struct {
	net.Conn
	buf      []byte
	err      error // what the connection itself failed with
	tooLarge bool
	name     string
	got      bool
}

func (r *helloReader) Read(p []byte) (int, error) {
	room := maxHello - len(r.buf)
	if room <= 0 {
		r.tooLarge = true
		return 0, errTooLarge
	}
	if len(p) > room {
		p = p[:room]
	}
	n, err := r.Conn.Read(p)
	r.buf = append(r.buf, p[:n]...)
	if err != nil {
		r.err = err
	}
	return n, err
}

// Write drops the alert crypto/tls sends when the handshake stops.
func (r *helloReader) Write(p []byte) (int, error) { return len(p), nil }

// helloConfig stops the handshake at the hello: crypto/tls joins records and segments and parses extensions as any Go
// TLS server does, and the handshake never goes further.
var helloConfig = &tls.Config{
	GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		if r, ok := h.Conn.(*helloReader); ok {
			r.name, r.got = h.ServerName, true
		}
		return nil, errAbort
	},
}

// readHello reads the ClientHello from conn without answering. buf holds every byte read, to be replayed to whoever gets
// the connection. With ECH the name is the outer SNI: there are no ECH keys here.
func readHello(conn net.Conn) (name string, buf []byte, v verdict) {
	r := &helloReader{Conn: conn}
	err := tls.Server(r, helloConfig).Handshake()
	switch {
	case r.got:
		return r.name, r.buf, helloOK
	case r.tooLarge:
		return "", r.buf, helloTooLarge
	case errors.Is(r.err, os.ErrDeadlineExceeded):
		return "", r.buf, helloTimeout
	case r.err != nil:
		return "", r.buf, helloClosed
	}
	var rec tls.RecordHeaderError
	if errors.As(err, &rec) {
		return "", r.buf, helloNotTLS
	}
	return "", r.buf, helloBad
}

// replayConn gives out the bytes already read, then the connection. RemoteAddr is the client's own.
type replayConn struct {
	net.Conn
	mu      sync.Mutex
	head    []byte
	drained atomic.Bool
}

func newReplayConn(conn net.Conn, head []byte) *replayConn {
	c := &replayConn{Conn: conn, head: head}
	c.drained.Store(len(head) == 0)
	return c
}

func (c *replayConn) Read(p []byte) (int, error) {
	if !c.drained.Load() {
		c.mu.Lock()
		if n := copy(p, c.head); n > 0 {
			c.head = c.head[n:]
			if len(c.head) == 0 {
				c.head = nil
				c.drained.Store(true)
			}
			c.mu.Unlock()
			return n, nil
		}
		c.drained.Store(true)
		c.mu.Unlock()
	}
	return c.Conn.Read(p)
}

func (c *replayConn) CloseWrite() error {
	closer, ok := c.Conn.(interface{ CloseWrite() error })
	if !ok {
		return errors.New("front: close-write is unsupported")
	}
	return closer.CloseWrite()
}

// SyscallConn lets the socket be inspected (TCP_INFO) before and after the head is read.
func (c *replayConn) SyscallConn() (syscall.RawConn, error) {
	sc, ok := c.Conn.(syscall.Conn)
	if !ok {
		return nil, errors.New("front: no raw connection")
	}
	return sc.SyscallConn()
}

// TcpMaskConn, RawConn and Splice are Xray's finalmask.TcpMaskConn: once the head has been read, the connection can be
// unwrapped to the socket, so Vision keeps splice and readv/writev on it. Before that the head would be lost.
func (*replayConn) TcpMaskConn()        {}
func (c *replayConn) RawConn() net.Conn { return c.Conn }
func (c *replayConn) Splice() bool      { return c.drained.Load() }
