// Package fronttest puts a real front on loopback in front of an engine under test: the engine's tests then show what
// reaches it through the shared port, not what a stand-in would.
package fronttest

import (
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/mistgate/mistgate/internal/node/front"
)

// Port is a front that binds the loopback port an engine asks for and gives the engine the route of its inbound. Set
// engine.Env.SharedListener to Listener.
type Port struct {
	t      testing.TB
	routes []front.Route
	mu     sync.Mutex
	f      *front.Front
}

// New returns a Port with these routes; the first is the default one. The front starts when the first engine asks for
// its route, and closes with the test.
func New(t testing.TB, routes ...front.Route) *Port {
	return &Port{t: t, routes: routes}
}

// Listener is engine.Env.SharedListener.
func (p *Port) Listener(id string, port uint16) net.Listener {
	p.t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.f == nil {
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
		if err != nil {
			p.t.Fatalf("front on port %d: %v", port, err)
		}
		p.f = front.New(ln, nil)
		p.f.SetRoutes(p.routes, p.routes[0].ID)
		p.t.Cleanup(func() { _ = p.f.Close() })
	}
	return p.f.Listener(id)
}

// Front is the running front, nil until an engine has asked for a route.
func (p *Port) Front() *front.Front {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.f
}
