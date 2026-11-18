// Package front shares one TCP port between services by the SNI of the first ClientHello. It reads the hello without
// answering, picks a route by the exact name (the default route for anything it does not know) and hands the connection,
// with the bytes already read put back in front, to the route's net.Listener. It sends nothing itself and keeps no
// per-source limits: behind a relay, a bridge or CGNAT many clients share an address.
package front

import (
	"cmp"
	"container/list"
	"errors"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	helloWait  = 10 * time.Second // from accept to a complete hello: what every Go TLS site of the node allows
	routeWait  = 5 * time.Second  // how long a connection waits for its route to Accept (an engine restarting)
	maxPending = 1024             // connections without a complete hello per port; the oldest give way
	maxUnknown = 16               // unknown names remembered for Stats
)

// Route is one service on the port.
type Route struct {
	ID    string   // the inbound's id
	Names []string // exact SNI names, lower case, no trailing dot, ASCII; a name another route holds already is ignored
	// KeepAlive is set on a connection when this route is chosen, so each service keeps the keepalive it has when it
	// listens alone. nil leaves what the listener set.
	KeepAlive *net.KeepAliveConfig
}

// NameCount is an SNI name and how many connections came with it.
type NameCount struct {
	Name  string
	Count uint64
}

// Stats counts since New. The Default* fields say why a connection went to the default route; they count even when
// there is none, and then NoRoute counts too.
type Stats struct {
	Accepted      uint64
	Delivered     map[string]uint64 // by route id: handed to the route's Accept
	NoSNI         uint64
	UnknownName   uint64
	NotTLS        uint64
	BadHello      uint64
	HelloTooLarge uint64
	Timeouts      uint64 // closed: no complete hello in time
	Evicted       uint64 // closed: more than maxPending unfinished, this was the oldest
	RouteDown     uint64 // went to the default route: the route's service is not running (SetDown), or did not Accept in time
	Refused       uint64 // closed: nobody Accepted it in time (the default route too), or the route went away
	NoRoute       uint64 // closed: nowhere to send it
	Panics        uint64
	// Unknown is the most frequent names no route holds, most frequent first. A name that came in after the list was
	// full takes over the least frequent entry and its count, so counts may be too high after churn.
	Unknown []NameCount
}

// Front is the listener of a shared port.
type Front struct {
	ln  net.Listener
	log *slog.Logger

	// set by tests
	helloWait, routeWait time.Duration
	maxPending           int

	table atomic.Pointer[table]
	done  chan struct{}
	once  sync.Once
	wg    sync.WaitGroup

	mu   sync.Mutex // pend
	pend *list.List // of *pending, oldest first

	rmu     sync.Mutex // queues, unknown
	queues  map[string]*queue
	unknown map[string]uint64

	accepted, noSNI, unknownName, notTLS, badHello, tooLarge atomic.Uint64
	timeouts, evicted, refused, noRoute, panics, routeDown   atomic.Uint64
	lastOverloadLog, lastPanicLog                            atomic.Int64 // unix ns
}

type table struct {
	byName map[string]*route
	def    *route
}

type route struct {
	q  *queue
	ka *net.KeepAliveConfig
}

// queue is the hand-off point of a route. It belongs to the front, not to the engine's listener, so a route whose engine
// restarts keeps it and connections wait for the next Accept.
type queue struct {
	ch        chan net.Conn
	gone      chan struct{} // closed when the route is removed
	delivered atomic.Uint64
	down      atomic.Bool // the service behind the route is not running: its names go to the default route at once
}

// pending is a connection whose hello has not been handed over yet.
type pending struct {
	conn net.Conn
	el   *list.Element
	gone chan struct{} // closed when evicted
}

// New starts the front on ln, which the caller bound; Close closes it. A nil log discards.
func New(ln net.Listener, log *slog.Logger) *Front {
	f := newFront(ln, log)
	f.wg.Add(1)
	go f.acceptLoop()
	return f
}

func newFront(ln net.Listener, log *slog.Logger) *Front {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	f := &Front{
		ln: ln, log: log, helloWait: helloWait, routeWait: routeWait, maxPending: maxPending,
		done: make(chan struct{}), pend: list.New(), queues: map[string]*queue{}, unknown: map[string]uint64{},
	}
	f.table.Store(&table{byName: map[string]*route{}})
	return f
}

// SetRoutes replaces the routes. def is the id of the default route. A route whose id stays keeps its queue, so
// connections waiting for it, and its listener, are not disturbed; a route that is gone has its listeners closed.
func (f *Front) SetRoutes(routes []Route, def string) {
	f.rmu.Lock()
	defer f.rmu.Unlock()
	t := &table{byName: map[string]*route{}}
	keep := map[string]bool{}
	for _, r := range routes {
		if keep[r.ID] {
			continue
		}
		keep[r.ID] = true
		q := f.queues[r.ID]
		if q == nil {
			q = &queue{ch: make(chan net.Conn), gone: make(chan struct{})}
			f.queues[r.ID] = q
		}
		rt := &route{q: q}
		if r.KeepAlive != nil {
			ka := *r.KeepAlive
			rt.ka = &ka
		}
		for _, n := range r.Names {
			if n = normalize(n); n != "" && t.byName[n] == nil {
				t.byName[n] = rt
			}
		}
		if r.ID == def {
			t.def = rt
		}
	}
	for id, q := range f.queues {
		if !keep[id] {
			delete(f.queues, id)
			close(q.gone)
		}
	}
	f.table.Store(t)
}

// SetDown says whether the service of route id is not running (it failed, or never started). Its names then go to the default
// route at once, as a stranger's do, instead of waiting for an Accept that will not come. A route that is up but does not
// Accept in time (an engine restarting) sends the connection to the default route too, after routeWait. The default route
// itself has nowhere to fall back to.
func (f *Front) SetDown(id string, down bool) {
	f.rmu.Lock()
	q := f.queues[id]
	f.rmu.Unlock()
	if q != nil {
		q.down.Store(down)
	}
}

// Listener returns the listener of route id, nil if there is no such route. Closing it ends one engine's Accept; the
// route stays, and Listener gives the next engine a new one.
func (f *Front) Listener(id string) net.Listener {
	f.rmu.Lock()
	q := f.queues[id]
	f.rmu.Unlock()
	if q == nil {
		return nil
	}
	return &routeListener{f: f, q: q, done: make(chan struct{})}
}

// Stats returns the counters.
func (f *Front) Stats() Stats {
	s := Stats{
		Accepted: f.accepted.Load(), NoSNI: f.noSNI.Load(), UnknownName: f.unknownName.Load(), NotTLS: f.notTLS.Load(),
		BadHello: f.badHello.Load(), HelloTooLarge: f.tooLarge.Load(), Timeouts: f.timeouts.Load(),
		Evicted: f.evicted.Load(), RouteDown: f.routeDown.Load(), Refused: f.refused.Load(), NoRoute: f.noRoute.Load(), Panics: f.panics.Load(),
		Delivered: map[string]uint64{},
	}
	f.rmu.Lock()
	defer f.rmu.Unlock()
	for id, q := range f.queues {
		s.Delivered[id] = q.delivered.Load()
	}
	for name, n := range f.unknown {
		s.Unknown = append(s.Unknown, NameCount{name, n})
	}
	slices.SortFunc(s.Unknown, func(a, b NameCount) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), strings.Compare(a.Name, b.Name))
	})
	return s
}

// Close stops accepting, closes the connections that have no route yet and ends every route's Accept. Connections
// already handed over are the engines'.
func (f *Front) Close() error {
	f.once.Do(func() {
		close(f.done)
		_ = f.ln.Close()
		f.mu.Lock()
		for e := f.pend.Front(); e != nil; e = e.Next() {
			_ = e.Value.(*pending).conn.Close()
		}
		f.mu.Unlock()
	})
	f.wg.Wait()
	return nil
}

func (f *Front) acceptLoop() {
	defer f.wg.Done()
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// a transient failure (out of file descriptors) must not leave the port without a listener
			select {
			case <-f.done:
				return
			case <-time.After(50 * time.Millisecond):
			}
			continue
		}
		f.accepted.Add(1)
		f.wg.Add(1)
		go f.handle(f.admit(conn))
	}
}

// admit records a connection that has no route yet; past maxPending the oldest one is closed.
func (f *Front) admit(conn net.Conn) *pending {
	p := &pending{conn: conn, gone: make(chan struct{})}
	var victim *pending
	f.mu.Lock()
	if f.pend.Len() >= f.maxPending {
		victim = f.pend.Remove(f.pend.Front()).(*pending)
		victim.el = nil
		close(victim.gone)
	}
	p.el = f.pend.PushBack(p)
	f.mu.Unlock()
	if victim != nil {
		_ = victim.conn.Close()
		f.evicted.Add(1)
		f.warn(&f.lastOverloadLog, "front_overloaded", "pending_max", f.maxPending, "evicted_total", f.evicted.Load())
	}
	return p
}

func (f *Front) release(p *pending) {
	f.mu.Lock()
	if p.el != nil {
		f.pend.Remove(p.el)
		p.el = nil
	}
	f.mu.Unlock()
}

func (f *Front) handle(p *pending) {
	defer f.wg.Done()
	defer f.release(p)
	handed := false
	defer func() {
		if r := recover(); r != nil {
			f.panics.Add(1)
			f.warn(&f.lastPanicLog, "front_panic", "panic", r)
		}
		if !handed {
			_ = p.conn.Close()
		}
	}()
	handed = f.route(p)
}

// route reads the hello and hands the connection over; false means the caller closes it. Closing without a word is FIN,
// or RST when the client's bytes are still unread: the kernel decides, as with any Go server.
func (f *Front) route(p *pending) bool {
	conn := p.conn
	_ = conn.SetReadDeadline(time.Now().Add(f.helloWait))
	name, head, v := readHello(conn)
	select {
	case <-p.gone:
		return false
	default:
	}
	name = normalize(name)
	t := f.table.Load()
	var rt *route
	switch v {
	case helloTimeout:
		f.timeouts.Add(1)
		return false
	case helloClosed:
		return false
	case helloOK:
		if rt = t.byName[name]; rt == nil {
			if name == "" {
				f.noSNI.Add(1)
			} else {
				f.unknownName.Add(1)
				f.noteUnknown(name)
			}
		}
	case helloNotTLS:
		f.notTLS.Add(1)
	case helloBad:
		f.badHello.Add(1)
	case helloTooLarge:
		f.tooLarge.Add(1)
	}
	if rt != nil && rt.q.down.Load() && rt != t.def && t.def != nil {
		f.routeDown.Add(1)
		rt = nil
	}
	if rt == nil {
		if rt = t.def; rt == nil {
			f.noRoute.Add(1)
			return false
		}
	}
	_ = conn.SetReadDeadline(time.Time{})
	ok, refused := f.handOff(p, rt, head)
	// The route's engine did not Accept in time or is gone: a visitor of its name gets what a stranger gets, not a closed
	// connection (the default route may be down too, then there is nothing left to try).
	if !ok && refused && t.def != nil && rt != t.def {
		f.routeDown.Add(1)
		ok, refused = f.handOff(p, t.def, head)
	}
	if refused {
		f.refused.Add(1)
	}
	return ok
}

// handOff gives the connection, with the bytes already read in front, to rt's Accept. When it fails, refused says the route
// did not take it (down, not Accepting in routeWait, gone), as opposed to the connection being evicted or the front closing.
func (f *Front) handOff(p *pending, rt *route, head []byte) (ok, refused bool) {
	if rt.q.down.Load() {
		return false, true
	}
	if tcp, isTCP := p.conn.(*net.TCPConn); isTCP && rt.ka != nil {
		_ = tcp.SetKeepAliveConfig(*rt.ka)
	}
	out := newReplayConn(p.conn, head)
	timer := time.NewTimer(f.routeWait)
	defer timer.Stop()
	rt.q.delivered.Add(1) // before the hand-off, so whoever accepted it already sees it counted
	select {
	case rt.q.ch <- out:
		return true, false
	case <-timer.C:
		refused = true
	case <-rt.q.gone:
		refused = true
	case <-p.gone:
	case <-f.done:
	}
	rt.q.delivered.Add(^uint64(0))
	return false, refused
}

// noteUnknown counts a name no route holds, if it looks like a host name: the names are the client's to choose.
func (f *Front) noteUnknown(name string) {
	if len(name) > 253 || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789-._") != "" {
		return
	}
	f.rmu.Lock()
	defer f.rmu.Unlock()
	if _, ok := f.unknown[name]; ok || len(f.unknown) < maxUnknown {
		f.unknown[name]++
		return
	}
	least, n := "", ^uint64(0)
	for k, c := range f.unknown {
		if c < n {
			least, n = k, c
		}
	}
	delete(f.unknown, least)
	f.unknown[name] = n + 1
}

// warn logs at most once a minute per kind: strangers can make these happen at line rate.
func (f *Front) warn(last *atomic.Int64, msg string, args ...any) {
	now, prev := time.Now().UnixNano(), last.Load()
	if prev != 0 && now-prev < int64(time.Minute) {
		return
	}
	if last.CompareAndSwap(prev, now) {
		f.log.Warn(msg, args...)
	}
}

func normalize(name string) string { return strings.ToLower(strings.TrimSuffix(name, ".")) }

// routeListener is what an engine accepts from.
type routeListener struct {
	f    *Front
	q    *queue
	once sync.Once
	done chan struct{}
}

func (l *routeListener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, net.ErrClosed
	default:
	}
	select {
	case c := <-l.q.ch:
		return c, nil
	case <-l.done:
	case <-l.q.gone:
	case <-l.f.done:
	}
	return nil, net.ErrClosed
}

func (l *routeListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

// Addr is the shared port's address.
func (l *routeListener) Addr() net.Addr { return l.f.ln.Addr() }
