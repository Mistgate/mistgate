package bridge

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"

	"golang.org/x/net/http2"
)

const (
	siteMaxUploadBufferPerConn = 16 << 20
	// siteMaxAnonymousConnections bounds the TLS connections of a site that have not (yet) proven to be an exit's bridge: a
	// scanner or slow client. A connection that does prove it leaves this set for good, so no number of anonymous visitors
	// can take the place of an exit's connections.
	siteMaxAnonymousConnections = 32
	// siteMaxAnonymousPerSource keeps one address from holding the whole set; unix-socket sources (REALITY) all share "".
	siteMaxAnonymousPerSource = 8
)

type siteConnKey struct{}

// ConfigureSiteHTTP2 keeps the standard Go HTTP/2 settings and raises only connection receive credit. It also bounds the
// anonymous connections of the site: past the limit the oldest connection of the busiest source is closed to make room, so
// a new connection (the exit's, for one) is always taken in and a flood only evicts itself first.
func ConfigureSiteHTTP2(server *http.Server) error {
	if server == nil {
		return errors.New("bridge: nil HTTP server")
	}
	if err := http2.ConfigureServer(server, &http2.Server{MaxUploadBufferPerConnection: siteMaxUploadBufferPerConn}); err != nil {
		return err
	}
	previous := server.ConnState
	previousContext := server.ConnContext
	limiter := &siteConnectionLimiter{anonymous: map[net.Conn]siteConn{}}
	server.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
		if previousContext != nil {
			ctx = previousContext(ctx, conn)
		}
		return context.WithValue(ctx, siteConnKey{}, siteConnRef{limiter: limiter, conn: conn})
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

type siteConnRef struct {
	limiter *siteConnectionLimiter
	conn    net.Conn
}

// markBridgeConnection takes the connection of a request out of the anonymous set; called once an exit has proven itself.
func markBridgeConnection(ctx context.Context) {
	if ref, ok := ctx.Value(siteConnKey{}).(siteConnRef); ok {
		ref.limiter.remove(ref.conn)
	}
}

type siteConn struct {
	seq    uint64
	source string
}

type siteConnectionLimiter struct {
	mu        sync.Mutex
	seq       uint64
	anonymous map[net.Conn]siteConn
}

// add records a new anonymous connection and returns the connections to close to stay within the limits.
func (l *siteConnectionLimiter) add(conn net.Conn) []net.Conn {
	source := connSource(conn)
	l.mu.Lock()
	defer l.mu.Unlock()
	var victims []net.Conn
	if source != "" {
		for l.count(source) >= siteMaxAnonymousPerSource {
			victims = append(victims, l.evictOldest(source))
		}
	}
	for len(l.anonymous) >= siteMaxAnonymousConnections {
		victims = append(victims, l.evictOldest(l.busiestSource()))
	}
	l.seq++
	l.anonymous[conn] = siteConn{seq: l.seq, source: source}
	return victims
}

func (l *siteConnectionLimiter) remove(conn net.Conn) {
	l.mu.Lock()
	delete(l.anonymous, conn)
	l.mu.Unlock()
}

func (l *siteConnectionLimiter) count(source string) int {
	n := 0
	for _, c := range l.anonymous {
		if c.source == source {
			n++
		}
	}
	return n
}

// busiestSource is the source with the most anonymous connections; the one with the oldest connection wins a tie.
func (l *siteConnectionLimiter) busiestSource() string {
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

func (l *siteConnectionLimiter) evictOldest(source string) net.Conn {
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
