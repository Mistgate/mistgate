// Package subhost is the intake of a subscription host (design/adr/0018-panel-behind-nat.md): it takes the connections the
// front of TCP 443 hands it for the subscription names and passes each, untouched, to the panel through the host's bridge
// entry. The front keeps no per-source limits on purpose (CGNAT); the intake does, and keeps capacity of its own for the
// agents' links, which it tells apart by the fleet nodes' addresses. A connection it cannot pass goes to the node's site
// (the front's default route), never to a reset, which would tell a prober the name is handled apart.
package subhost

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mistgate/mistgate/internal/node/bridge"
)

// The relay ids of the panel's two targets (its listener takes both; the host counts them apart).
const (
	RelaySub  = "sub"
	RelayLink = "link"
)

// Limits are the intake's bounds. The panel's bridge carries about 400 people at once (four HTTP/2 connections, two streams a
// person, 250 streams a connection, less the waiting pairs): past that, pairs run out anyway.
type Limits struct {
	MaxClients   int // people carried at once, both classes
	LinkReserve  int // of those, kept for agents' links: subscriptions take at most MaxClients-LinkReserve
	SubPerSource int // subscriptions carried at once from one address (an IPv6 one by its /64)
	// SubPerNetwork: subscriptions carried at once from one network, an IPv4 /24 or an IPv6 /48, so that a few dozen
	// addresses of one network cannot fill the subscription class. 0 = no such bound.
	SubPerNetwork int
	LinkPerSource int // links carried at once from one node's address
	SubIdle       time.Duration
	LinkIdle      time.Duration // agents ping every 20 s
}

// DefaultLimits are the limits for a fleet of linkSources node addresses.
func DefaultLimits(linkSources int) Limits {
	return Limits{MaxClients: 400, LinkReserve: min(256, 2*linkSources+16), SubPerSource: 16, SubPerNetwork: 64, LinkPerSource: 4,
		SubIdle: time.Minute, LinkIdle: 2 * time.Minute}
}

// Entry is what the intake needs of the host's bridge entry (*bridge.Entry).
type Entry interface {
	Connected(exitID string) bool
	Attach(ctx context.Context, exitID string, c bridge.Client) error
	DialExit(ctx context.Context, exitID, relayID string, idle time.Duration) (net.Conn, error)
}

// Front is what the intake needs of the front (*front.Front).
type Front interface {
	Divert(conn net.Conn) bool
	SetDown(id string, down bool)
}

// Stats are the intake's counters: people carried now, and since the start.
type Stats struct {
	ClientsSub, ClientsLink uint32
	PanelConnected          bool
	Diverted                uint64 // sent to the node's site instead
	RefusedPerSource        uint64 // of those, over a per-source or per-network limit
}

// Intake serves one route of a front. It is safe for concurrent use.
type Intake struct {
	entry   Entry
	front   Front
	routeID string
	log     *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu        sync.Mutex
	limits    Limits
	sources   []netip.Prefix
	sub, link int
	perSource map[netip.Addr]int // by source key (sourceKey)
	perNet    map[netip.Addr]int // subscriptions by network key (networkKey)
	down      bool               // what the front was last told
	told      bool               // the front was told at all (the first syncDown always tells it)
	closed    bool               // Close ran: nothing new is started (track)

	diverted, refusedPerSource atomic.Uint64
}

// New makes the intake of the front's route routeID; Serve runs it.
func New(entry Entry, front Front, routeID string, log *slog.Logger) *Intake {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Intake{entry: entry, front: front, routeID: routeID, log: log, ctx: ctx, cancel: cancel,
		limits: DefaultLimits(0), perSource: map[netip.Addr]int{}, perNet: map[netip.Addr]int{}}
}

// SetSources sets the fleet nodes' addresses (the link class) and the limits that follow from their number.
func (in *Intake) SetSources(sources []netip.Prefix) {
	in.mu.Lock()
	in.sources = append([]netip.Prefix(nil), sources...)
	in.limits = DefaultLimits(len(sources))
	in.mu.Unlock()
}

// SetLimits replaces the limits (tests).
func (in *Intake) SetLimits(l Limits) {
	in.mu.Lock()
	in.limits = l
	in.mu.Unlock()
}

// Serve accepts from ln until it is closed or the intake is. While the panel holds no connection the route is down: the
// front sends the subscription names to the site at once, without waiting for a pair.
func (in *Intake) Serve(ln net.Listener) {
	defer func() { _ = ln.Close() }()
	if !in.track() { // closed already
		return
	}
	defer in.wg.Done()
	if !in.track() {
		return
	}
	go in.watch()
	stop := context.AfterFunc(in.ctx, func() { _ = ln.Close() })
	defer stop()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || in.ctx.Err() != nil {
				return
			}
			continue
		}
		if !in.track() {
			_ = conn.Close()
			return
		}
		go func() {
			defer in.wg.Done()
			in.handle(conn)
		}()
	}
}

// watch tells the front whether the route can take connections.
func (in *Intake) watch() {
	defer in.wg.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		in.syncDown()
		select {
		case <-in.ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (in *Intake) syncDown() {
	down := !in.entry.Connected(bridge.PanelPeerID)
	in.mu.Lock()
	changed := down != in.down || !in.told
	in.down, in.told = down, true
	in.mu.Unlock()
	if changed {
		in.front.SetDown(in.routeID, down)
	}
}

// Close ends the intake: the streams it carries are cut (their people reconnect, to the site or to a new host) and it
// waits for them.
func (in *Intake) Close() {
	in.mu.Lock()
	in.closed = true
	in.mu.Unlock()
	in.cancel()
	in.wg.Wait()
}

// track counts one more goroutine Close must wait for; false once Close ran (the WaitGroup is never added to while Close
// waits on it).
func (in *Intake) track() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.closed {
		return false
	}
	in.wg.Add(1)
	return true
}

// Stats returns the counters.
func (in *Intake) Stats() Stats {
	in.mu.Lock()
	defer in.mu.Unlock()
	return Stats{ClientsSub: uint32(in.sub), ClientsLink: uint32(in.link), PanelConnected: !in.down,
		Diverted: in.diverted.Load(), RefusedPerSource: in.refusedPerSource.Load()}
}

func (in *Intake) handle(conn net.Conn) {
	addr := remoteAddrPort(conn)
	link, release, perSource := in.admit(addr)
	if release == nil {
		if perSource {
			in.refusedPerSource.Add(1)
		}
		in.divert(conn)
		return
	}
	defer release()
	if !in.entry.Connected(bridge.PanelPeerID) {
		in.divert(conn)
		return
	}
	in.mu.Lock()
	relay, idle := RelaySub, in.limits.SubIdle
	if link {
		relay, idle = RelayLink, in.limits.LinkIdle
	}
	in.mu.Unlock()
	err := in.entry.Attach(in.ctx, bridge.PanelPeerID, bridge.Client{Conn: conn, RelayID: relay, Idle: idle, Addr: addr})
	if err != nil {
		in.divert(conn)
	}
}

// divert sends conn to the node's site; with no site to send it to it is closed.
func (in *Intake) divert(conn net.Conn) {
	in.diverted.Add(1)
	if in.ctx.Err() != nil || !in.front.Divert(conn) {
		_ = conn.Close()
	}
}

// admit counts a person in, by class, source and (a subscription) network; release is nil when a limit is reached
// (perSource: the source's or the network's).
func (in *Intake) admit(addr netip.AddrPort) (link bool, release func(), perSource bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	key, network := sourceKey(addr.Addr()), networkKey(addr.Addr())
	for _, p := range in.sources {
		if addr.IsValid() && p.Contains(addr.Addr().Unmap()) {
			link = true
			break
		}
	}
	l := in.limits
	switch {
	case in.sub+in.link >= l.MaxClients:
		return link, nil, false
	case !link && in.sub >= l.MaxClients-l.LinkReserve:
		return link, nil, false
	case link && in.perSource[key] >= l.LinkPerSource, !link && in.perSource[key] >= l.SubPerSource,
		!link && l.SubPerNetwork > 0 && in.perNet[network] >= l.SubPerNetwork:
		return link, nil, true
	}
	in.perSource[key]++
	if link {
		in.link++
	} else {
		in.sub++
		in.perNet[network]++
	}
	return link, func() { in.leave(key, network, link) }, false
}

func (in *Intake) leave(key, network netip.Addr, link bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.perSource[key]--; in.perSource[key] <= 0 {
		delete(in.perSource, key)
	}
	if link {
		in.link--
		return
	}
	in.sub--
	if in.perNet[network]--; in.perNet[network] <= 0 {
		delete(in.perNet, network)
	}
}

// DialLink is the host's own link to the panel, inside the bridge: it counts as an agent's link and never goes out through
// the node's own public address.
func (in *Intake) DialLink(ctx context.Context) (net.Conn, error) {
	in.mu.Lock()
	if in.sub+in.link >= in.limits.MaxClients {
		in.mu.Unlock()
		return nil, bridge.ErrEntryBusy
	}
	in.link++
	idle := in.limits.LinkIdle
	in.mu.Unlock()
	conn, err := in.entry.DialExit(ctx, bridge.PanelPeerID, RelayLink, idle)
	if err != nil {
		in.mu.Lock()
		in.link--
		in.mu.Unlock()
		return nil, err
	}
	return &countedConn{Conn: conn, release: func() {
		in.mu.Lock()
		in.link--
		in.mu.Unlock()
	}}, nil
}

type countedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *countedConn) Close() error {
	c.once.Do(c.release)
	return c.Conn.Close()
}

// sourceKey is what the per-source limit counts by: an IPv4 address, or the /64 of an IPv6 one.
func sourceKey(a netip.Addr) netip.Addr {
	a = a.Unmap()
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.Addr()
	}
	return a
}

// networkKey is what the per-network limit counts by: the /24 of an IPv4 address, the /48 of an IPv6 one.
func networkKey(a netip.Addr) netip.Addr {
	a = a.Unmap()
	bits := 24
	if a.Is6() {
		bits = 48
	}
	p, _ := a.Prefix(bits)
	return p.Addr()
}

func remoteAddrPort(conn net.Conn) netip.AddrPort {
	if tcp, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		ap := tcp.AddrPort()
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
	}
	return netip.AddrPort{}
}
