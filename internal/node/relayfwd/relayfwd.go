// Package relayfwd carries a relay's port to an exit on another address family, which the kernel's DNAT cannot do: a rule
// cannot translate an IPv4 packet into an IPv6 one. The agent listens on the port itself instead, dials the exit for every
// connection (or keeps one session per client address for UDP) and passes the bytes on unchanged — the traffic is encrypted
// end to end, exactly as with the kernel forward.
//
// The package is the plain L4 half of the bridge in internal/node/bridge: no pairing, no authentication, no budget. The entry
// holds no keys and decrypts nothing; its own load counts the traffic twice, as it does for any relay.
package relayfwd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DialTimeout bounds the connection to the exit's server; the exit is another data centre, not a local peer.
	DialTimeout = 10 * time.Second
	// IdleUDP is how long a UDP session lives without a datagram from its client. QUIC keepalives and AmneziaWG's are well
	// inside it; a client that comes back from a network change starts a session of its own.
	IdleUDP = 2 * time.Minute
	// MaxSessionsPerForward bounds the UDP sessions of one port; a NAT table of the kernel has a limit too.
	MaxSessionsPerForward = 4096
	// MaxConnsPerForward bounds the open TCP connections of one port.
	MaxConnsPerForward = 4096
	// udpBuffer is one datagram, the largest a UDP socket can carry.
	udpBuffer = 64 * 1024
	// acceptRetry is how soon a failed Accept is tried again.
	acceptRetry = 50 * time.Millisecond
	// sweepEvery is how often idle UDP sessions are closed.
	sweepEvery = 30 * time.Second
)

// Forward is one port carried in userspace: the exit is always a literal address of the other family.
type Forward struct {
	ID      string
	Network string // "tcp" | "udp"
	Port    uint16
	To      netip.AddrPort
	// Address is a listen address for tests; production listens on every local address (both families, so a client that picks
	// the entry's AAAA record reaches the port too).
	Address string
}

// Counters are the bytes of one relay since its forward was installed.
type Counters struct {
	Bytes       uint64 // people to the exit's server
	BytesBack   uint64 // the exit's server to people
	Packets     uint64 // datagrams towards the exit (TCP counts none: the kernel's segments are not ours to count)
	PacketsBack uint64
}

// Server runs the forwards of one node. Sync is called from the agent's worker goroutine; Counters may be read from the doctor.
type Server struct {
	mu       sync.Mutex
	forwards map[string]*forward
	closed   bool
	dial     func(ctx context.Context, network, addr string) (net.Conn, error) // nil = the real dialer, a test seam
	now      func() time.Time
	log      atomic.Pointer[slog.Logger]
}

// New returns a server with no forwards.
func New() *Server {
	return &Server{forwards: map[string]*forward{}, now: time.Now}
}

// SetLogger sets the logger of the forwards; nil is quiet. Call it before Sync.
func (s *Server) SetLogger(log *slog.Logger) { s.log.Store(log) }

// Sync makes the running forwards exactly the wanted list: a forward that is gone or changed (its target, its network, its
// port) is stopped and started again. Per-id errors are returned for the forwards that could not be started; the others run.
func (s *Server) Sync(want []Forward) map[string]error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		errs := make(map[string]error, len(want))
		for _, f := range want {
			errs[f.ID] = errors.New("closed")
		}
		return errs
	}
	desired := make(map[string]Forward, len(want))
	errs := map[string]error{}
	for _, f := range want {
		if f.ID == "" {
			continue
		}
		if f.Network != "tcp" && f.Network != "udp" {
			errs[f.ID] = errors.New("bad_network")
			continue
		}
		if f.Port == 0 || !f.To.IsValid() {
			errs[f.ID] = errors.New("bad_target")
			continue
		}
		if _, dup := desired[f.ID]; dup {
			errs[f.ID] = errors.New("duplicate")
			continue
		}
		desired[f.ID] = f
	}
	var stopped []*forward
	for id, old := range s.forwards {
		next, ok := desired[id]
		if ok && next == old.cfg {
			delete(desired, id)
			continue
		}
		delete(s.forwards, id)
		stopped = append(stopped, old)
	}
	s.mu.Unlock()
	for _, old := range stopped {
		old.stop()
	}
	for id, f := range desired {
		fw := newForward(f, s)
		if err := fw.start(); err != nil {
			errs[id] = err
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			fw.stop()
			errs[id] = errors.New("closed")
			continue
		}
		s.forwards[id] = fw
		s.mu.Unlock()
	}
	return errs
}

// Counters is a snapshot of every running forward, by relay id.
func (s *Server) Counters() map[string]Counters {
	s.mu.Lock()
	list := make([]*forward, 0, len(s.forwards))
	for _, f := range s.forwards {
		list = append(list, f)
	}
	s.mu.Unlock()
	out := make(map[string]Counters, len(list))
	for _, f := range list {
		out[f.cfg.ID] = f.counters()
	}
	return out
}

// Close stops every forward and refuses new ones.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	list := make([]*forward, 0, len(s.forwards))
	for _, f := range s.forwards {
		list = append(list, f)
	}
	s.forwards = map[string]*forward{}
	s.mu.Unlock()
	for _, f := range list {
		f.stop()
	}
}

// forward is one running port.
type forward struct {
	cfg    Forward
	srv    *Server
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	tcp net.Listener
	udp *net.UDPConn

	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	sessions map[string]*udpSession
	lastErr  string

	bytesUp, bytesDown     atomic.Uint64
	packetsUp, packetsDown atomic.Uint64
}

func newForward(cfg Forward, s *Server) *forward {
	ctx, cancel := context.WithCancel(context.Background())
	return &forward{cfg: cfg, srv: s, ctx: ctx, cancel: cancel, conns: map[net.Conn]struct{}{}, sessions: map[string]*udpSession{}}
}

func (f *forward) listenAddress() string {
	if f.cfg.Address != "" {
		return f.cfg.Address
	}
	return net.JoinHostPort("", fmt.Sprint(f.cfg.Port))
}

func (f *forward) start() error {
	if f.cfg.Network == "udp" {
		return f.startUDP()
	}
	ln, err := net.Listen("tcp", f.listenAddress())
	if err != nil {
		f.noteErr(err)
		return errors.New("listen_failed")
	}
	f.tcp = ln
	f.wg.Add(1)
	go f.acceptLoop(ln)
	return nil
}

func (f *forward) stop() {
	f.cancel()
	if f.tcp != nil {
		_ = f.tcp.Close()
	}
	if f.udp != nil {
		_ = f.udp.Close()
	}
	f.mu.Lock()
	for c := range f.conns {
		_ = c.Close()
	}
	for _, s := range f.sessions {
		_ = s.conn.Close()
	}
	f.sessions = map[string]*udpSession{}
	f.mu.Unlock()
	f.wg.Wait()
}

// acceptLoop takes people's connections and pairs each with a connection to the exit.
func (f *forward) acceptLoop(ln net.Listener) {
	defer f.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-f.ctx.Done():
				return
			case <-time.After(acceptRetry):
			}
			continue
		}
		f.mu.Lock()
		full := len(f.conns) >= MaxConnsPerForward
		if !full {
			f.conns[conn] = struct{}{}
		}
		f.mu.Unlock()
		if full {
			_ = conn.Close()
			continue
		}
		f.wg.Add(1)
		go f.serveConn(conn)
	}
}

// serveConn opens the exit's server over its own family and passes the bytes on in both directions until both sides end.
func (f *forward) serveConn(client net.Conn) {
	defer f.wg.Done()
	defer func() {
		f.mu.Lock()
		delete(f.conns, client)
		f.mu.Unlock()
		_ = client.Close()
	}()
	dialCtx, cancel := context.WithTimeout(f.ctx, DialTimeout)
	up, err := f.dial(dialCtx, "tcp", f.cfg.To.String())
	cancel()
	if err != nil {
		f.noteErr(err)
		return
	}
	defer func() {
		f.mu.Lock()
		delete(f.conns, up)
		f.mu.Unlock()
		_ = up.Close()
	}()
	f.mu.Lock()
	f.conns[up] = struct{}{}
	f.mu.Unlock()

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(&countingWriter{w: up, bytes: &f.bytesUp, packets: &f.packetsUp}, client)
		closeWrite(up)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(&countingWriter{w: client, bytes: &f.bytesDown, packets: &f.packetsDown}, up)
		closeWrite(client)
		done <- struct{}{}
	}()
	<-done
	// One direction ended: the other gets a short grace for what is in flight, then both close (defer).
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	case <-f.ctx.Done():
	}
}

// dial is the exit's dialer; a test replaces it.
func (f *forward) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if f.srv.dial != nil {
		return f.srv.dial(ctx, network, addr)
	}
	return (&net.Dialer{Timeout: DialTimeout, KeepAlive: 30 * time.Second}).DialContext(ctx, network, addr)
}

func (f *forward) counters() Counters {
	return Counters{Bytes: f.bytesUp.Load(), BytesBack: f.bytesDown.Load(), Packets: f.packetsUp.Load(), PacketsBack: f.packetsDown.Load()}
}

// noteErr logs a failure once: the sweep retries every half minute and a dead exit would fill the journal.
func (f *forward) noteErr(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	f.mu.Lock()
	changed := f.lastErr != msg
	f.lastErr = msg
	f.mu.Unlock()
	if log := f.srv.log.Load(); changed && log != nil {
		log.Warn("relay forward in userspace failed", "code", "relay_forward_failed", "relay", f.cfg.ID, "network", f.cfg.Network,
			"port", f.cfg.Port, "err", msg)
	}
}

// closeWrite ends one direction without cutting the other; a connection that cannot is left to the grace of serveConn.
func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

// countingWriter counts a direction as it flows, not when the copy ends: the row of a relay must move while people use it. The
// wrapper also keeps io.Copy from splicing the two connections into the kernel, where neither the bytes nor the write calls
// would be ours to count.
type countingWriter struct {
	w       io.Writer
	bytes   *atomic.Uint64
	packets *atomic.Uint64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.bytes.Add(uint64(n))
		c.packets.Add(1)
	}
	return n, err
}

// udpSession is one client address talking to the exit through the forward.
type udpSession struct {
	conn   *net.UDPConn
	client netip.AddrPort
	last   atomic.Int64 // unix nano
}

func (f *forward) startUDP() error {
	addr, err := net.ResolveUDPAddr("udp", f.listenAddress())
	if err != nil {
		f.noteErr(err)
		return errors.New("listen_failed")
	}
	pc, err := net.ListenUDP("udp", addr)
	if err != nil {
		f.noteErr(err)
		return errors.New("listen_failed")
	}
	f.udp = pc
	f.wg.Add(2)
	go f.udpLoop(pc)
	go f.sweepLoop()
	return nil
}

// udpLoop moves people's datagrams to their session's socket. The reply path is one goroutine per session.
func (f *forward) udpLoop(pc *net.UDPConn) {
	defer f.wg.Done()
	buf := make([]byte, udpBuffer)
	for {
		n, client, err := pc.ReadFromUDPAddrPort(buf)
		if err != nil {
			select {
			case <-f.ctx.Done():
				return
			case <-time.After(acceptRetry):
			}
			continue
		}
		s := f.session(client)
		if s == nil {
			continue
		}
		s.last.Store(f.srv.now().UnixNano())
		if _, err := s.conn.Write(buf[:n]); err != nil {
			f.dropSession(client)
			continue
		}
		f.bytesUp.Add(uint64(n))
		f.packetsUp.Add(1)
	}
}

// session is the exit socket of one client address, made on its first datagram.
func (f *forward) session(client netip.AddrPort) *udpSession {
	key := client.String()
	f.mu.Lock()
	if s := f.sessions[key]; s != nil {
		f.mu.Unlock()
		return s
	}
	if len(f.sessions) >= MaxSessionsPerForward {
		f.evictOldestLocked()
	}
	f.mu.Unlock()

	conn, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(f.cfg.To))
	if err != nil {
		f.noteErr(err)
		return nil
	}
	s := &udpSession{conn: conn, client: client}
	s.last.Store(f.srv.now().UnixNano())
	f.mu.Lock()
	if f.ctx.Err() != nil {
		f.mu.Unlock()
		_ = conn.Close()
		return nil
	}
	f.sessions[key] = s
	f.mu.Unlock()
	f.wg.Add(1)
	go f.replyLoop(s)
	return s
}

// replyLoop returns the exit's datagrams to the client of its session.
func (f *forward) replyLoop(s *udpSession) {
	defer f.wg.Done()
	buf := make([]byte, udpBuffer)
	for {
		n, err := s.conn.Read(buf)
		if err != nil {
			f.dropSession(s.client)
			return
		}
		s.last.Store(f.srv.now().UnixNano())
		if _, err := f.udp.WriteToUDPAddrPort(buf[:n], s.client); err != nil {
			f.dropSession(s.client)
			return
		}
		f.bytesDown.Add(uint64(n))
		f.packetsDown.Add(1)
	}
}

func (f *forward) dropSession(client netip.AddrPort) {
	key := client.String()
	f.mu.Lock()
	s := f.sessions[key]
	delete(f.sessions, key)
	f.mu.Unlock()
	if s != nil {
		_ = s.conn.Close()
	}
}

// evictOldestLocked closes the session that has been quiet the longest; the caller holds f.mu.
func (f *forward) evictOldestLocked() {
	var oldest *udpSession
	var key string
	for k, s := range f.sessions {
		if oldest == nil || s.last.Load() < oldest.last.Load() {
			oldest, key = s, k
		}
	}
	if oldest != nil {
		delete(f.sessions, key)
		_ = oldest.conn.Close()
	}
}

// sweepLoop closes the sessions whose client went away.
func (f *forward) sweepLoop() {
	defer f.wg.Done()
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-f.ctx.Done():
			return
		case <-t.C:
			cut := f.srv.now().Add(-IdleUDP).UnixNano()
			f.mu.Lock()
			for k, s := range f.sessions {
				if s.last.Load() < cut {
					delete(f.sessions, k)
					_ = s.conn.Close()
				}
			}
			f.mu.Unlock()
		}
	}
}
