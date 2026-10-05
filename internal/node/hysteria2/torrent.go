package hysteria2

import (
	"io"
	"net"
	"sync"
	"sync/atomic"

	"github.com/apernet/hysteria/core/v2/server"
	"github.com/mistgate/mistgate/internal/node/torrentguard"
)

var torrentHandshakePrefix = []byte("\x13BitTorrent protocol")

// torrentTCPConn holds only a possible BitTorrent handshake prefix. It releases the prefix as soon as the
// stream differs, and closes this one outbound TCP connection when the complete signature is present.
// No lock is held while writing to the connection: a peer with a zero window must not keep Close waiting.
// Writes come from one goroutine (Hysteria's copy loop), as any writer of an ordered byte stream does.
type torrentTCPConn struct {
	net.Conn
	attempt func()

	inspected atomic.Bool // the stream is decided ordinary: writes go straight through
	blocked   atomic.Bool
	closed    atomic.Bool

	mu     sync.Mutex // guards prefix only, never across I/O
	prefix []byte
}

func newTorrentTCPConn(conn net.Conn, attempt func()) net.Conn {
	return &torrentTCPConn{Conn: conn, attempt: attempt}
}

func (c *torrentTCPConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if c.closed.Load() || c.blocked.Load() {
		return 0, net.ErrClosed
	}
	if c.inspected.Load() {
		return c.Conn.Write(p)
	}
	flush, block := c.inspect(p)
	switch {
	case block:
		_ = c.Conn.Close()
		if c.attempt != nil {
			c.attempt()
		}
		// The bytes were consumed by the guard and the connection is closed. Reporting them accepted
		// lets Hysteria end this stream without retrying the same handshake on another write.
		return len(p), nil
	case flush != nil:
		if err := writeAll(c.Conn, flush); err != nil {
			return 0, err
		}
	}
	// Otherwise this is a prefix of at most 19 bytes, held: an exact signature prefix is not sent before it can
	// be identified; ordinary TLS/HTTP traffic fails the comparison at once.
	return len(p), nil
}

// inspect compares p with the rest of the handshake signature. On a mismatch it returns the held prefix and p to send
// in order, and the stream is ordinary from then on; on a complete signature it reports a block.
func (c *torrentTCPConn) inspect(p []byte) (flush []byte, block bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, b := range p {
		if b != torrentHandshakePrefix[len(c.prefix)] {
			flush = append(append(make([]byte, 0, len(c.prefix)+len(p)-i), c.prefix...), p[i:]...)
			c.prefix = nil
			c.inspected.Store(true)
			return flush, false
		}
		c.prefix = append(c.prefix, b)
		if len(c.prefix) == len(torrentHandshakePrefix) {
			c.prefix = nil
			c.blocked.Store(true)
			return nil, true
		}
	}
	return nil, false
}

// Close closes the connection first, which also ends a Write stuck on a peer that reads nothing.
func (c *torrentTCPConn) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	err := c.Conn.Close()
	c.mu.Lock()
	c.prefix = nil
	c.mu.Unlock()
	return err
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

type torrentUDPConn struct {
	server.UDPConn
	attempt func(torrentguard.Protocol)
}

func (c torrentUDPConn) WriteTo(p []byte, addr string) (int, error) {
	if protocol, ok := torrentguard.DetectClientUDPRequest(p); ok {
		if c.attempt != nil {
			c.attempt(protocol)
		}
		// Drop only the datagram that carried a validated BitTorrent signature. Other UDP traffic on this
		// Hysteria session continues normally.
		return len(p), nil
	}
	return c.UDPConn.WriteTo(p, addr)
}
