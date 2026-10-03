package hysteria2

import (
	"io"
	"net"
	"sync"

	"github.com/apernet/hysteria/core/v2/server"
	"github.com/mistgate/mistgate/internal/node/torrentguard"
)

var torrentHandshakePrefix = []byte("\x13BitTorrent protocol")

// torrentTCPConn holds only a possible BitTorrent handshake prefix. It releases the prefix as soon as the
// stream differs, and closes this one outbound TCP connection when the complete signature is present.
type torrentTCPConn struct {
	net.Conn
	attempt func()

	mu      sync.Mutex
	prefix  []byte
	inspect bool
	blocked bool
	closed  bool
}

func newTorrentTCPConn(conn net.Conn, attempt func()) net.Conn {
	return &torrentTCPConn{Conn: conn, attempt: attempt, inspect: true}
}

func (c *torrentTCPConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.mu.Lock()
	if c.closed || c.blocked {
		c.mu.Unlock()
		return 0, net.ErrClosed
	}
	if !c.inspect {
		n, err := c.Conn.Write(p)
		c.mu.Unlock()
		return n, err
	}
	for i, b := range p {
		if b != torrentHandshakePrefix[len(c.prefix)] {
			// A non-match makes this ordinary TCP. Forward the saved prefix and this write in order.
			data := make([]byte, 0, len(c.prefix)+len(p)-i)
			data = append(data, c.prefix...)
			data = append(data, p[i:]...)
			c.prefix = nil
			c.inspect = false
			err := writeAll(c.Conn, data)
			c.mu.Unlock()
			if err != nil {
				return 0, err
			}
			return len(p), nil
		}
		c.prefix = append(c.prefix, b)
		if len(c.prefix) == len(torrentHandshakePrefix) {
			c.prefix = nil
			c.inspect = false
			c.blocked = true
			c.mu.Unlock()
			_ = c.Conn.Close()
			if c.attempt != nil {
				c.attempt()
			}
			// The bytes were consumed by the guard and the connection is closed. Reporting them accepted
			// lets Hysteria end this stream without retrying the same handshake on another write.
			return len(p), nil
		}
	}
	// This is at most 19 bytes. Holding only an exact signature prefix avoids sending a split handshake
	// before it can be identified; ordinary TLS/HTTP traffic fails the comparison immediately.
	c.mu.Unlock()
	return len(p), nil
}

func (c *torrentTCPConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.prefix = nil
	c.mu.Unlock()
	return c.Conn.Close()
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
