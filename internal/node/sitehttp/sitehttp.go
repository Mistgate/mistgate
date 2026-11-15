// Package sitehttp holds the HTTP/2 settings and the anonymous-connection limit that every HTTPS site of a node shares.
// The settings are part of what a scanner can measure, so all sites must answer alike: change them here, never in one
// caller. The code is the bridge's site limiter (internal/node/bridge/site.go) moved into a package of its own, with the
// same numbers and the same eviction rule, so that a TrustTunnel port and a bridge port cannot be told apart by their
// SETTINGS or by how they behave under a flood.
package sitehttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"

	"golang.org/x/net/http2"
)

const (
	maxUploadBufferPerConn = 16 << 20
	// maxAnonymousConnections bounds the TLS connections of a site that have not (yet) proven to belong to a client: a
	// scanner or slow client. A connection that does prove it leaves this set for good, so no number of anonymous visitors
	// can take the place of a client's connections.
	maxAnonymousConnections = 32
	// maxAnonymousPerSource keeps one address from holding the whole set; unix-socket sources (REALITY) all share "".
	maxAnonymousPerSource = 8
)

type connKey struct{}

// ConfigureSiteHTTP2 keeps the standard Go HTTP/2 settings and raises only connection receive credit. It also bounds the
// anonymous connections of the site: past the limit the oldest connection of the busiest source is closed to make room, so
// a new connection (a client's, for one) is always taken in and a flood only evicts itself first.
//
// It must run before the server serves. Handlers reach the connection of a request with Conn, and call MarkAuthenticated
// once the peer has proven itself.
func ConfigureSiteHTTP2(server *http.Server) error {
	if server == nil {
		return errors.New("sitehttp: nil HTTP server")
	}
	if err := http2.ConfigureServer(server, &http2.Server{MaxUploadBufferPerConnection: maxUploadBufferPerConn}); err != nil {
		return err
	}
	previous := server.ConnState
	previousContext := server.ConnContext
	limiter := &connectionLimiter{anonymous: map[net.Conn]siteConn{}}
	server.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
		if previousContext != nil {
			ctx = previousContext(ctx, conn)
		}
		return context.WithValue(ctx, connKey{}, connRef{limiter: limiter, conn: conn})
	}
	server.ConnState = func(conn net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			for _, victim := range limiter.add(conn) {
				_ = victim.Close()
			}
		case http.StateClosed, http.StateHijacked:
			limiter.remove(conn)
		}
		if previous != nil {
			previous(conn, state)
		}
	}
	return nil
}

type connRef struct {
	limiter *connectionLimiter
	conn    net.Conn
}

// Conn returns the network connection that carries the request whose context is ctx; nil outside a configured server.
func Conn(ctx context.Context) net.Conn {
	ref, _ := ctx.Value(connKey{}).(connRef)
	return ref.conn
}

// MarkAuthenticated takes the connection of a request out of the anonymous set for good (it never returns) and gives it
// back; calling it again is harmless. It returns nil outside a configured server.
func MarkAuthenticated(ctx context.Context) net.Conn {
	ref, ok := ctx.Value(connKey{}).(connRef)
	if !ok {
		return nil
	}
	ref.limiter.remove(ref.conn)
	return ref.conn
}

type siteConn struct {
	seq    uint64
	source string
}

type connectionLimiter struct {
	mu        sync.Mutex
	seq       uint64
	anonymous map[net.Conn]siteConn
}

// add records a new anonymous connection and returns the connections to close to stay within the limits.
func (l *connectionLimiter) add(conn net.Conn) []net.Conn {
	source := connSource(conn)
	l.mu.Lock()
	defer l.mu.Unlock()
	var victims []net.Conn
	if source != "" {
		for l.count(source) >= maxAnonymousPerSource {
			victims = append(victims, l.evictOldest(source))
		}
	}
	for len(l.anonymous) >= maxAnonymousConnections {
		victims = append(victims, l.evictOldest(l.busiestSource()))
	}
	l.seq++
	l.anonymous[conn] = siteConn{seq: l.seq, source: source}
	return victims
}

func (l *connectionLimiter) remove(conn net.Conn) {
	l.mu.Lock()
	delete(l.anonymous, conn)
	l.mu.Unlock()
}

func (l *connectionLimiter) count(source string) int {
	n := 0
	for _, c := range l.anonymous {
		if c.source == source {
			n++
		}
	}
	return n
}

// busiestSource is the source with the most anonymous connections; the one with the oldest connection wins a tie.
func (l *connectionLimiter) busiestSource() string {
	counts := map[string]int{}
	oldest := map[string]uint64{}
	for _, c := range l.anonymous {
		counts[c.source]++
		if seq, ok := oldest[c.source]; !ok || c.seq < seq {
			oldest[c.source] = c.seq
		}
	}
	best, bestCount := "", -1
	for source, n := range counts {
		if n > bestCount || (n == bestCount && oldest[source] < oldest[best]) {
			best, bestCount = source, n
		}
	}
	return best
}

func (l *connectionLimiter) evictOldest(source string) net.Conn {
	var victim net.Conn
	var oldest uint64
	for conn, c := range l.anonymous {
		if c.source == source && (victim == nil || c.seq < oldest) {
			victim, oldest = conn, c.seq
		}
	}
	delete(l.anonymous, victim)
	return victim
}

// connSource names where a connection comes from: the address (an IPv6 one by its /64), "" for anything else.
func connSource(conn net.Conn) string {
	addr, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok || addr == nil || addr.IP == nil {
		return ""
	}
	if ip4 := addr.IP.To4(); ip4 != nil {
		return ip4.String()
	}
	return addr.IP.Mask(net.CIDRMask(64, 128)).String()
}
