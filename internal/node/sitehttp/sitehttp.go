// Package sitehttp holds the HTTP/2 settings and the anonymous-connection limit shared by HTTPS listeners. The settings
// are part of what a scanner can measure, so all HTTP/2 sites must answer alike: change them here, never in one caller.
// The reusable limiter applies the same source grouping and eviction rule to TCP and QUIC connections.
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
	limiter := NewAnonymousLimiter()
	server.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
		if previousContext != nil {
			ctx = previousContext(ctx, conn)
		}
		return context.WithValue(ctx, connKey{}, connRef{limiter: limiter, conn: conn})
	}
	server.ConnState = func(conn net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			limiter.Add(conn, conn.RemoteAddr(), func() { _ = conn.Close() })
		case http.StateClosed, http.StateHijacked:
			limiter.Remove(conn)
		}
		if previous != nil {
			previous(conn, state)
		}
	}
	return nil
}

type connRef struct {
	limiter *AnonymousLimiter
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
	ref.limiter.MarkAuthenticated(ref.conn)
	return ref.conn
}

type siteConn struct {
	seq    uint64
	source string
	close  func()
}

// AnonymousLimiter bounds connections that have not yet authenticated. Keys must be comparable and stable for the life of
// a connection. An authenticated key is removed from the set permanently until Remove is called when the connection ends.
// Add closes evicted connections after releasing the limiter lock.
type AnonymousLimiter struct {
	mu        sync.Mutex
	seq       uint64
	anonymous map[any]siteConn
	proven    map[any]struct{}
}

// NewAnonymousLimiter creates an empty limiter with the site-wide limits and eviction order.
func NewAnonymousLimiter() *AnonymousLimiter {
	return &AnonymousLimiter{anonymous: map[any]siteConn{}, proven: map[any]struct{}{}}
}

// Add records a new anonymous connection. It closes the oldest connection of the busiest source when the total limit is
// full, and closes a source's oldest connection when that source exceeds its own limit.
func (l *AnonymousLimiter) Add(key any, addr net.Addr, closeConn func()) {
	if key == nil {
		return
	}
	if closeConn == nil {
		closeConn = func() {}
	}
	source := connSource(addr)
	l.mu.Lock()
	if _, ok := l.proven[key]; ok {
		l.mu.Unlock()
		return
	}
	if _, ok := l.anonymous[key]; ok {
		l.mu.Unlock()
		return
	}
	var victims []func()
	if source != "" {
		for l.count(source) >= maxAnonymousPerSource {
			if closeVictim := l.evictOldest(source); closeVictim != nil {
				victims = append(victims, closeVictim)
			}
		}
	}
	for len(l.anonymous) >= maxAnonymousConnections {
		if closeVictim := l.evictOldest(l.busiestSource()); closeVictim != nil {
			victims = append(victims, closeVictim)
		}
	}
	l.seq++
	l.anonymous[key] = siteConn{seq: l.seq, source: source, close: closeConn}
	l.mu.Unlock()
	for _, closeVictim := range victims {
		closeVictim()
	}
}

// Crowded reports that the anonymous set is at least half full: the site is being scanned or flooded.
func (l *AnonymousLimiter) Crowded() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.anonymous) >= maxAnonymousConnections/2
}

// MarkAuthenticated removes key from the anonymous set and keeps it out until Remove observes the connection closing.
func (l *AnonymousLimiter) MarkAuthenticated(key any) {
	l.mu.Lock()
	if _, tracked := l.anonymous[key]; tracked {
		delete(l.anonymous, key)
		l.proven[key] = struct{}{}
	}
	l.mu.Unlock()
}

// Remove forgets a connection after it closes.
func (l *AnonymousLimiter) Remove(key any) {
	l.mu.Lock()
	delete(l.anonymous, key)
	delete(l.proven, key)
	l.mu.Unlock()
}

func (l *AnonymousLimiter) count(source string) int {
	n := 0
	for _, c := range l.anonymous {
		if c.source == source {
			n++
		}
	}
	return n
}

// busiestSource is the source with the most anonymous connections; the one with the oldest connection wins a tie.
func (l *AnonymousLimiter) busiestSource() string {
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

func (l *AnonymousLimiter) evictOldest(source string) func() {
	var victim any
	var oldest uint64
	for conn, c := range l.anonymous {
		if c.source == source && (victim == nil || c.seq < oldest) {
			victim, oldest = conn, c.seq
		}
	}
	if victim == nil {
		return nil
	}
	closeConn := l.anonymous[victim].close
	delete(l.anonymous, victim)
	return closeConn
}

// connSource names where a connection comes from: the address (an IPv6 one by its /64), "" for anything else.
func connSource(addr net.Addr) string {
	var ip net.IP
	switch addr := addr.(type) {
	case *net.TCPAddr:
		if addr != nil {
			ip = addr.IP
		}
	case *net.UDPAddr:
		if addr != nil {
			ip = addr.IP
		}
	}
	if ip == nil {
		return ""
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String()
}
