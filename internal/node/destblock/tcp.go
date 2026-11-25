package destblock

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"github.com/xtls/xray-core/common/protocol/tls"
)

// maxSniff is how many client bytes are held while the first TLS record is not complete; past it the stream passes unchecked.
const maxSniff = 16 << 10

// WrapTCP holds the client's first bytes of a connection to an IP address until the TLS ClientHello is complete. A name in
// list closes the connection (onBlock is called once) and no held byte reaches the server. Anything else, a stream that
// does not start with TLS first, is written out as it came, in order, and every later byte goes straight through.
//
// Write has one caller at a time (the copy loop of the protocol, after the sniffer's put-back bytes), as in torrentguard.
// Close and CloseWrite may come from other goroutines; the lock is never held over a network write.
func WrapTCP(conn net.Conn, list *List, onBlock func()) net.Conn {
	return &tcpConn{Conn: conn, list: list, onBlock: onBlock}
}

type tcpConn struct {
	net.Conn
	list    *List
	onBlock func()

	decided atomic.Bool // inspection is over: bytes go straight through
	blocked atomic.Bool
	closed  atomic.Bool

	mu  sync.Mutex
	buf []byte
}

func (c *tcpConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if c.closed.Load() || c.blocked.Load() {
		return 0, net.ErrClosed
	}
	if c.decided.Load() {
		return c.Conn.Write(p)
	}
	flush, block := c.inspect(p)
	switch {
	case block:
		_ = c.Conn.Close()
		if c.onBlock != nil {
			c.onBlock()
		}
	case flush != nil:
		if err := writeAll(c.Conn, flush); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// CloseWrite sends what is still held (the client closed its side before the hello was complete), then half-closes.
func (c *tcpConn) CloseWrite() error {
	c.mu.Lock()
	rest := c.buf
	c.buf = nil
	c.decided.Store(true)
	c.mu.Unlock()

	if c.closed.Load() || c.blocked.Load() {
		return net.ErrClosed
	}
	if len(rest) != 0 {
		if err := writeAll(c.Conn, rest); err != nil {
			return err
		}
	}
	closer, ok := c.Conn.(interface{ CloseWrite() error })
	if !ok {
		return errors.New("close-write is unsupported")
	}
	return closer.CloseWrite()
}

func (c *tcpConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	err := c.Conn.Close()
	c.mu.Lock()
	c.buf = nil
	c.mu.Unlock()
	return err
}

// inspect adds p to the held bytes and decides: more (nil, false), release flush, or block.
func (c *tcpConn) inspect(p []byte) (flush []byte, block bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.buf) == 0 && p[0] != 0x16 { // not a TLS handshake: nothing is ever held
		c.decided.Store(true)
		return p, false
	}
	c.buf = append(c.buf, p...)
	switch c.verdict() {
	case verdictBlock:
		c.buf = nil
		c.blocked.Store(true)
		return nil, true
	case verdictWait:
		if len(c.buf) < maxSniff {
			return nil, false
		}
	}
	flush, c.buf = c.buf, nil
	c.decided.Store(true)
	return flush, false
}

const (
	verdictWait = iota
	verdictPass
	verdictBlock
)

// verdict waits for the first TLS record to be complete (xray's SniffTLS answers "no clue" both for an incomplete record
// and for a malformed one, and the second must not be waited on), then reads the name from it.
func (c *tcpConn) verdict() int {
	b := c.buf
	if b[0] != 0x16 || (len(b) > 1 && b[1] != 3) {
		return verdictPass
	}
	if len(b) < 5 || len(b) < 5+int(b[3])<<8+int(b[4]) {
		return verdictWait
	}
	if h, err := tls.SniffTLS(b); err == nil && c.list.Match(h.Domain()) {
		return verdictBlock
	}
	return verdictPass
}

func writeAll(conn net.Conn, p []byte) error {
	for len(p) > 0 {
		n, err := conn.Write(p)
		p = p[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
