package subhost

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/bridge"
)

// fakeEntry holds every attached connection until release is closed.
type fakeEntry struct {
	connected atomic.Bool
	attachErr error
	mu        sync.Mutex
	relays    []string
	addrs     []netip.AddrPort
	release   chan struct{}
}

func (e *fakeEntry) Connected(string) bool { return e.connected.Load() }

func (e *fakeEntry) Attach(ctx context.Context, exitID string, c bridge.Client) error {
	if e.attachErr != nil {
		return e.attachErr
	}
	e.mu.Lock()
	e.relays, e.addrs = append(e.relays, c.RelayID), append(e.addrs, c.Addr)
	e.mu.Unlock()
	select {
	case <-e.release:
	case <-ctx.Done():
	}
	_ = c.Conn.Close()
	return nil
}

func (e *fakeEntry) DialExit(context.Context, string, string, time.Duration) (net.Conn, error) {
	a, b := net.Pipe()
	go func() { <-e.release; _ = b.Close() }()
	return a, nil
}

type fakeFront struct {
	diverted atomic.Int32
	mu       sync.Mutex
	down     []bool
}

func (f *fakeFront) Divert(conn net.Conn) bool { f.diverted.Add(1); _ = conn.Close(); return true }
func (f *fakeFront) SetDown(_ string, down bool) {
	f.mu.Lock()
	f.down = append(f.down, down)
	f.mu.Unlock()
}

// fromConn is a person's connection from addr.
type fromConn struct {
	net.Conn
	addr net.Addr
}

func (c fromConn) RemoteAddr() net.Addr { return c.addr }

func person(ip string, port int) net.Conn {
	a, _ := net.Pipe()
	return fromConn{Conn: a, addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: port}}
}

func newTestIntake(t *testing.T) (*Intake, *fakeEntry, *fakeFront) {
	t.Helper()
	e := &fakeEntry{release: make(chan struct{})}
	e.connected.Store(true)
	f := &fakeFront{}
	in := New(e, f, "~sub-host", nil)
	t.Cleanup(func() {
		select {
		case <-e.release:
		default:
			close(e.release)
		}
		in.Close()
	})
	return in, e, f
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

func TestIntakeLimitsByClassAndSource(t *testing.T) {
	in, e, f := newTestIntake(t)
	in.SetSources([]netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")})
	in.SetLimits(Limits{MaxClients: 6, LinkReserve: 2, SubPerSource: 2, LinkPerSource: 1})
	start := func(c net.Conn) { in.wg.Add(1); go func() { defer in.wg.Done(); in.handle(c) }() }

	// one IPv6 /64 is one source: the third connection from it goes to the site
	start(person("2001:db8:1::1", 1))
	start(person("2001:db8:1::2", 2))
	eventually(t, "two subscriptions", func() bool { return in.Stats().ClientsSub == 2 })
	start(person("2001:db8:1::3", 3))
	eventually(t, "a per-source divert", func() bool { return f.diverted.Load() == 1 && in.Stats().RefusedPerSource == 1 })

	// subscriptions stop at MaxClients-LinkReserve; the reserve stays for links
	start(person("198.51.100.1", 4))
	start(person("198.51.100.2", 5))
	eventually(t, "four subscriptions", func() bool { return in.Stats().ClientsSub == 4 })
	start(person("198.51.100.3", 6))
	eventually(t, "the subscription class is full", func() bool { return f.diverted.Load() == 2 })
	start(person("203.0.113.5", 7))
	start(person("203.0.113.6", 8))
	eventually(t, "two links in the reserve", func() bool { return in.Stats().ClientsLink == 2 })
	start(person("203.0.113.6", 9)) // over the total, and over the link source's one
	eventually(t, "full", func() bool { return f.diverted.Load() == 3 })

	e.mu.Lock()
	var links int
	for i, r := range e.relays {
		if r == RelayLink {
			links++
		}
		if !e.addrs[i].IsValid() {
			t.Error("the person's address was not passed")
		}
	}
	e.mu.Unlock()
	if links != 2 {
		t.Fatalf("relays %v", e.relays)
	}
	close(e.release)
	eventually(t, "everyone left", func() bool { s := in.Stats(); return s.ClientsSub == 0 && s.ClientsLink == 0 })
}

// A network (an IPv4 /24, an IPv6 /48) is bounded as a whole: many /64s of one /48 cannot fill the subscription class, and a
// link from a fleet node's address is not counted against its network.
func TestIntakeLimitsByNetwork(t *testing.T) {
	in, e, f := newTestIntake(t)
	in.SetSources([]netip.Prefix{netip.MustParsePrefix("198.51.100.7/32")})
	in.SetLimits(Limits{MaxClients: 20, LinkReserve: 2, SubPerSource: 2, SubPerNetwork: 3, LinkPerSource: 2})
	start := func(c net.Conn) { in.wg.Add(1); go func() { defer in.wg.Done(); in.handle(c) }() }

	start(person("2001:db8:1:1::1", 1))
	start(person("2001:db8:1:2::1", 2))
	start(person("2001:db8:1:3::1", 3))
	eventually(t, "three /64s of one /48", func() bool { return in.Stats().ClientsSub == 3 })
	start(person("2001:db8:1:4::1", 4)) // a fourth /64 of the same /48
	eventually(t, "the /48 is full", func() bool { return f.diverted.Load() == 1 && in.Stats().RefusedPerSource == 1 })
	start(person("2001:db8:2::1", 5)) // another /48
	eventually(t, "another network", func() bool { return in.Stats().ClientsSub == 4 })

	start(person("198.51.100.1", 6))
	start(person("198.51.100.2", 7))
	eventually(t, "two of one /24", func() bool { return in.Stats().ClientsSub == 6 })
	start(person("198.51.100.7", 8)) // a fleet node in that /24: a link, not counted against the network
	eventually(t, "the node's link", func() bool { return in.Stats().ClientsLink == 1 })
	start(person("198.51.100.3", 9))
	eventually(t, "the third of the /24", func() bool { return in.Stats().ClientsSub == 7 })
	start(person("198.51.100.4", 10))
	eventually(t, "the /24 is full", func() bool { return f.diverted.Load() == 2 })

	close(e.release)
	eventually(t, "everyone left", func() bool { s := in.Stats(); return s.ClientsSub == 0 && s.ClientsLink == 0 })
	in.mu.Lock()
	left := len(in.perNet) + len(in.perSource)
	in.mu.Unlock()
	if left != 0 {
		t.Fatalf("counters left: %d", left)
	}
}

// Without the panel the route is down (the front sends the names to the site at once), and a connection that came anyway, or
// that the entry cannot attach, goes to the site.
func TestIntakeWithoutThePanel(t *testing.T) {
	in, e, f := newTestIntake(t)
	e.connected.Store(false)
	ln := newPipeListener()
	go in.Serve(ln)
	eventually(t, "the route is down", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.down) == 1 && f.down[0]
	})
	ln.ch <- person("198.51.100.9", 1)
	eventually(t, "diverted", func() bool { return f.diverted.Load() == 1 })

	e.connected.Store(true)
	eventually(t, "the route is up again", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.down) == 2 && !f.down[1]
	})
	e.attachErr = bridge.ErrNoPair
	ln.ch <- person("198.51.100.9", 2)
	eventually(t, "no pair: diverted", func() bool { return f.diverted.Load() == 2 })
	if s := in.Stats(); s.ClientsSub != 0 || s.RefusedPerSource != 0 || s.Diverted != 2 {
		t.Fatalf("stats %+v", s)
	}
}

// The host's own link counts as a link and is released when it is closed; closing the intake cuts what it carries.
func TestIntakeDialLinkAndClose(t *testing.T) {
	in, _, _ := newTestIntake(t)
	conn, err := in.DialLink(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if in.Stats().ClientsLink != 1 {
		t.Fatal("the host's own link is not counted")
	}
	_ = conn.Close()
	_ = conn.Close()
	if in.Stats().ClientsLink != 0 {
		t.Fatal("a closed link is still counted")
	}
	in.wg.Add(1)
	go func() { defer in.wg.Done(); in.handle(person("198.51.100.1", 1)) }()
	eventually(t, "carried", func() bool { return in.Stats().ClientsSub == 1 })
	done := make(chan struct{})
	go func() { in.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cut the streams")
	}
}

type pipeListener struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{ch: make(chan net.Conn), done: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{} }

// The first look at the panel's connection always tells the front, also when it says "up" (a front left with a down route
// by an earlier intake).
func TestIntakeTellsTheFrontAtOnce(t *testing.T) {
	in, _, f := newTestIntake(t)
	in.syncDown()
	in.syncDown()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.down) != 1 || f.down[0] {
		t.Fatalf("front told %v, want [false] once", f.down)
	}
}

// Serve and Close may race: Close waits for everything Serve started, nothing is started after it (the front hears nothing
// more), and a Serve after Close returns at once and closes its listener (L5).
func TestIntakeServeAndCloseRace(t *testing.T) {
	for range 20 {
		e := &fakeEntry{release: make(chan struct{})}
		f := &fakeFront{}
		in := New(e, f, "~sub-host", nil)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		served := make(chan struct{})
		go func() { in.Serve(ln); close(served) }()
		in.Close()
		f.mu.Lock()
		told := len(f.down)
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		f.mu.Lock()
		if len(f.down) != told {
			t.Fatal("the front was told after Close")
		}
		f.mu.Unlock()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Fatal("Serve did not end after Close")
		}
		close(e.release)
	}
	in := New(&fakeEntry{release: make(chan struct{})}, &fakeFront{}, "~sub-host", nil)
	in.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	in.Serve(ln)
	if _, err := ln.Accept(); err == nil {
		t.Fatal("a Serve after Close left its listener open")
	}
}
