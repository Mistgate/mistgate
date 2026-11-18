package trusttunnel

import (
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/sitehttp"
)

// TCP stalls of HTTP/2 connections (agent.proto LinkCounters tcp_stalls, tcp_stalls_16k; capability
// "trusttunnel-tcp-stalls/1"): the same rule as VLESS (engine.TCPStallRule), counted on the inbound's row. A connection
// counts when it has served a request, so a client that never finished the TLS handshake (a scanner) is not a stall.

// tcpStall is the seam the closes go through (tests replace it, while connections of an earlier test may still be closing).
var tcpStall atomic.Value

func init() { tcpStall.Store(engine.TCPStallOf) }

func readStall(c net.Conn) (stalled, first16K bool) {
	return tcpStall.Load().(func(net.Conn) (bool, bool))(c)
}

// stallListener hands out connections that read their TCP_INFO as they close.
type stallListener struct {
	net.Listener
	count func(stalled, first16K bool)
}

func (l stallListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &stallConn{Conn: c, count: l.count}, nil
}

type stallConn struct {
	net.Conn
	count   func(stalled, first16K bool)
	through atomic.Bool // a request reached the handler
	once    sync.Once
}

// Close reads the socket before it is closed: a closed socket has no TCP_INFO.
func (c *stallConn) Close() error {
	c.once.Do(func() {
		if c.through.Load() {
			c.count(readStall(c.Conn))
		}
	})
	return c.Conn.Close()
}

// markThrough says the HTTP/2 connection of the request got past the handshake.
func markThrough(r *http.Request) {
	if tc, ok := sitehttp.Conn(r.Context()).(*tls.Conn); ok {
		if sc, ok := tc.NetConn().(*stallConn); ok {
			sc.through.Store(true)
		}
	}
}

func (in *inbound) countStall(stalled, first16K bool) {
	if stalled {
		in.tcpStalls.Add(1)
		if first16K {
			in.tcpStalls16K.Add(1)
		}
	}
}
