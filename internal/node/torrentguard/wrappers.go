package torrentguard

import (
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
)

var handshakePrefix = []byte("\x13BitTorrent protocol")

// WrapTCP holds only a possible BitTorrent handshake prefix and closes the connection when the complete
// signature is present. The prefix is released as soon as the stream differs.
func WrapTCP(conn net.Conn, attempt func()) net.Conn {
	return &tcpConn{Conn: conn, attempt: attempt}
}

type tcpConn struct {
	net.Conn
	attempt func()

	inspected atomic.Bool
	blocked   atomic.Bool
	closed    atomic.Bool

	mu     sync.Mutex
	prefix []byte
}

func (c *tcpConn) Write(p []byte) (int, error) {
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
		return len(p), nil
	case flush != nil:
		if err := writeAll(c.Conn, flush); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Xray provides one writer. CloseWrite runs only after that writer finishes, so prefix state does not need to serialize
// concurrent network writes.
func (c *tcpConn) CloseWrite() error {
	c.mu.Lock()
	prefix := append([]byte(nil), c.prefix...)
	c.prefix = nil
	c.inspected.Store(true)
	c.mu.Unlock()

	if c.closed.Load() || c.blocked.Load() {
		return net.ErrClosed
	}
	if len(prefix) != 0 {
		if err := writeAll(c.Conn, prefix); err != nil {
			return err
		}
	}
	closer, ok := c.Conn.(interface{ CloseWrite() error })
	if !ok {
		return errors.New("close-write is unsupported")
	}
	return closer.CloseWrite()
}

// inspect releases mu before callers write; never hold it around a network write, because Close must unblock that write.
func (c *tcpConn) inspect(p []byte) (flush []byte, block bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, b := range p {
		if b != handshakePrefix[len(c.prefix)] {
			flush = append(append(make([]byte, 0, len(c.prefix)+len(p)-i), c.prefix...), p[i:]...)
			c.prefix = nil
			c.inspected.Store(true)
			return flush, false
		}
		c.prefix = append(c.prefix, b)
		if len(c.prefix) == len(handshakePrefix) {
			c.prefix = nil
			c.blocked.Store(true)
			return nil, true
		}
	}
	return nil, false
}

func (c *tcpConn) Close() error {
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

// UDPConn is the common datagram surface used by node egresses and Hysteria2.
type UDPConn interface {
	ReadFrom([]byte) (int, string, error)
	WriteTo([]byte, string) (int, error)
	Close() error
}

type Attempt func(protocol Protocol, evidence Evidence, addr string)

// WrapUDP drops only datagrams carrying a validated BitTorrent signature.
func WrapUDP(conn UDPConn, attempt Attempt) UDPConn {
	return udpConn{UDPConn: conn, attempt: attempt}
}

type udpConn struct {
	UDPConn
	attempt Attempt
}

func (c udpConn) WriteTo(p []byte, addr string) (int, error) {
	if protocol, evidence, ok := ClassifyClientUDPRequest(p, portNumber(addr)); ok {
		if c.attempt != nil {
			c.attempt(protocol, evidence, addr)
		}
		return len(p), nil
	}
	return c.UDPConn.WriteTo(p, addr)
}

// Port returns a destination's numeric port without exposing its host. It returns an empty string when the address
// has no valid decimal port.
func Port(addr string) string {
	if port := portNumber(addr); port != 0 {
		return strconv.Itoa(int(port))
	}
	return ""
}

func portNumber(addr string) uint16 {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return 0
	}
	return uint16(n)
}
