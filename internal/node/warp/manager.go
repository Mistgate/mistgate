package warp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

// Options configures a Manager. Everything has a default.
type Options struct {
	Log      *slog.Logger
	Settings Settings
	// DNS is the node's resolver list (engine.Env.DNS); the WARP egress resolves names like the direct one.
	DNS func() []string
	// Emit receives state-change events; nil drops them.
	Emit func(Event)
	Now  func() time.Time
	// Interval between health checks (30 s); FastInterval while the tunnel is still starting (5 s).
	Interval, FastInterval time.Duration
	// HandshakeWait is how long a fresh tunnel may go without a handshake before the next endpoint is tried (15 s).
	HandshakeWait time.Duration
	// ProbeAURL and ProbeBURL override the probe targets (tests); see DefaultProbeA and DefaultProbeB.
	ProbeAURL, ProbeBURL string
	// ProbeTimeout bounds one probe, dial included (6 s).
	ProbeTimeout time.Duration
	// AllowPrivate lets the tunnel egress dial private and documentation-range addresses (tests, dev).
	AllowPrivate bool
}

// ladderCooldown is how long the recovery ladder rests after it ran out of steps.
const ladderCooldown = 10 * time.Minute

// Manager owns the WARP tunnel of the node. The agent calls Apply with every DesiredState (nil = no WARP),
// SetRoutedSubnets with the client subnets of AWG inbounds whose egress is "warp", hands Egress() to the
// engines, runs Run in a goroutine and calls Cleanup on the way out. Safe for concurrent use.
type Manager struct {
	o     Options
	s     Settings
	log   *slog.Logger
	plane dataplane
	probe prober

	mu      sync.Mutex
	gen     uint64 // bumped when the tunnel is (re)built, so a probe result that raced with it is dropped
	guarded bool   // the preflight passed once: the routing table and rule preferences are ours to use
	spec    *plugin.WarpSpec
	ps      parsed
	subnets []netip.Prefix
	// routesDirty: the last reassert failed, so the host may not match subnets/spec. SetRoutedSubnets then retries
	// even for an unchanged list, which keeps the agent's dependent inbounds blocked until a reassert succeeds.
	routesDirty bool

	state      State
	backend    string
	idx        int // index into ps.cands
	ep         netip.AddrPort
	upAt       time.Time
	watchUntil time.Time // handshake watch deadline, zero = not watching
	rotations  int       // endpoints tried by the watch since the last Up
	rotatedAt  time.Time // when the endpoint last changed: a rotation needs HandshakeWait to show whether it worked
	nextTick   time.Time
	fails      int
	succ       int
	ladder     int
	ladderAt   time.Time
	note       string // the last recovery action, shown next to the failing check
	h          Health // last measured fields (State and Backend are filled in by Health())

	egAuto, egV4 *egress.Direct
}

// New builds the manager for this OS.
func New(o Options) (*Manager, error) {
	s := o.Settings.withDefaults()
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return newManager(o, newPlane(s, o.Log), nil), nil
}

func newManager(o Options, plane dataplane, pr prober) *Manager {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Interval <= 0 {
		o.Interval = 30 * time.Second
	}
	if o.FastInterval <= 0 {
		o.FastInterval = 5 * time.Second
	}
	if o.HandshakeWait <= 0 {
		o.HandshakeWait = 15 * time.Second
	}
	if o.ProbeAURL == "" {
		o.ProbeAURL = DefaultProbeA
	}
	if o.ProbeBURL == "" {
		o.ProbeBURL = DefaultProbeB
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	m := &Manager{o: o, s: o.Settings.withDefaults(), log: o.Log, plane: plane, probe: pr}
	if m.probe == nil {
		m.probe = &httpProber{dial: m.probeDial, aURL: o.ProbeAURL, bURL: o.ProbeBURL, timeout: o.ProbeTimeout}
	}
	return m
}

func (m *Manager) now() time.Time { return m.o.Now() }

// Configured reports whether the node has a WARP configuration (paused included). The agent refuses to start an
// inbound with egress "warp" while it is false.
func (m *Manager) Configured() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spec != nil
}

// Health returns the last measured state; ok=false when the node has no WARP (nothing to report).
func (m *Manager) Health() (Health, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.spec == nil {
		return Health{}, false
	}
	h := m.h
	h.State = m.state
	h.Backend = m.backend
	if m.ep.IsValid() && (m.state == StateStarting || m.state == StateUp || m.state == StateDown) {
		h.Endpoint = m.ep.String()
	}
	h.Failures = uint32(m.fails)
	if m.note != "" {
		h.LastError = joinNote(h.LastError, m.note)
	}
	return h, true
}

// Apply makes the node match spec: nil removes WARP (the tunnel goes, the client-subnet rules stay closed while
// subnets are still routed), Enabled=false pauses it. A spec equal to the applied one only re-asserts routing and
// repairs a vanished device; the WireGuard peer is never touched without a difference.
func (m *Manager) Apply(ctx context.Context, spec *plugin.WarpSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if spec == nil {
		return m.removeLocked(ctx)
	}
	if m.spec != nil && reflect.DeepEqual(*m.spec, *spec) {
		if m.state == StateUnavailable && m.spec.Enabled {
			return m.upLocked(ctx)
		}
		return m.ensureLocked(ctx)
	}
	ps, err := parseSpec(spec)
	if err != nil {
		return fmt.Errorf("warp: invalid spec: %w", err)
	}
	if err := m.guardLocked(ctx); err != nil {
		return err
	}
	cp := *spec
	cp.Ports = append([]uint16(nil), spec.Ports...)
	cp.Reserved = append([]byte(nil), spec.Reserved...)
	m.spec, m.ps = &cp, ps
	m.gen++
	m.idx, m.fails, m.succ, m.ladder = 0, 0, 0, 0
	m.h, m.note = Health{}, ""
	m.watchUntil = time.Time{}
	if !spec.Enabled {
		m.backend = ""
		err := m.plane.DownLink(ctx)
		m.setStateLocked(StateDisabled)
		if _, rerr := m.reassertLocked(ctx); err == nil {
			err = rerr
		}
		return err
	}
	return m.upLocked(ctx)
}

func (m *Manager) removeLocked(ctx context.Context) error {
	if m.spec == nil {
		if len(m.subnets) == 0 {
			return nil // a node without WARP does not touch the host on every reconcile
		}
		_, err := m.reassertLocked(ctx)
		return err
	}
	m.spec, m.ps = nil, parsed{}
	m.gen++
	m.backend, m.ep = "", netip.AddrPort{}
	m.watchUntil = time.Time{}
	m.h, m.note = Health{}, ""
	m.fails, m.succ, m.ladder = 0, 0, 0
	err := m.plane.DownLink(ctx)
	m.setStateLocked(StateNone)
	if _, rerr := m.reassertLocked(ctx); err == nil {
		err = rerr
	}
	return err
}

// SetRoutedSubnets sets the client subnets whose forwarded traffic must leave through WARP (AWG inbounds with
// egress "warp"). The rules stay while the tunnel is down or paused: those clients fail closed.
func (m *Manager) SetRoutedSubnets(ctx context.Context, subnets []netip.Prefix) error {
	norm := make([]netip.Prefix, 0, len(subnets))
	seen := map[netip.Prefix]bool{}
	for _, p := range subnets {
		if !p.IsValid() {
			return fmt.Errorf("warp: invalid subnet %v", p)
		}
		p = p.Masked()
		if !seen[p] {
			seen[p] = true
			norm = append(norm, p)
		}
	}
	sort.Slice(norm, func(i, j int) bool { return norm[i].String() < norm[j].String() })
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.routesDirty && (len(norm) == 0 && len(m.subnets) == 0 || reflect.DeepEqual(norm, m.subnets)) {
		return nil
	}
	if len(norm) > 0 {
		if err := m.guardLocked(ctx); err != nil {
			return err
		}
	}
	m.subnets = norm
	_, err := m.reassertLocked(ctx)
	return err
}

// guardLocked runs the preflight before the manager first touches the host: a routing table or rule preference that
// another tool uses must not be flushed by us, so Apply and SetRoutedSubnets refuse (and the owner sees
// warp_needs_attention) until the clash is resolved or Settings move the table. A FORWARD drop policy is only
// logged: it hurts forwarded AWG traffic, not the tunnel.
func (m *Manager) guardLocked(ctx context.Context) error {
	if m.guarded {
		return nil
	}
	for _, f := range m.plane.Preflight(ctx) {
		switch f.ID {
		case "table_in_use", "rule_pref_in_use":
			m.attention(f.ID)
			return fmt.Errorf("warp: %s: %s", f.ID, f.Detail)
		case "forward_drop":
			m.log.Warn("a FORWARD drop policy will stop forwarded client traffic", "detail", f.Detail)
		}
	}
	m.guarded = true
	return nil
}

// Preflight reports clashes with the host (table, rule preferences, device name, a FORWARD drop policy).
func (m *Manager) Preflight(ctx context.Context) []Finding {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.plane.Preflight(ctx)
}

// Cleanup removes everything the manager installed: rules, routes of the table, the device, the nft table. Called by
// the agent on shutdown (and by the stateless CleanupHost from the unit's ExecStopPost).
func (m *Manager) Cleanup(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.spec, m.ps, m.subnets, m.guarded, m.routesDirty = nil, parsed{}, nil, false, false
	m.gen++
	m.backend, m.ep = "", netip.AddrPort{}
	m.watchUntil = time.Time{}
	m.h, m.note = Health{}, ""
	m.setStateLocked(StateNone)
	return m.plane.Cleanup(ctx)
}

// upLocked builds the tunnel from the current spec. Called with the lock held and m.spec enabled.
func (m *Manager) upLocked(ctx context.Context) error {
	ps := m.ps
	if m.idx >= len(ps.cands) {
		m.idx = 0
	}
	ls := linkSpec{
		Backend: ps.backend, PrivateKey: ps.privKey, PeerKey: ps.peerKey,
		Endpoints: ps.cands, Endpoint: ps.cands[m.idx],
		AddrV4: ps.addrV4, AddrV6: ps.addrV6, MTU: ps.mtu, Reserved: ps.reserved, KeepaliveSeconds: keepaliveSeconds,
	}
	backend, ep, err := m.plane.Up(ctx, ls)
	if err != nil {
		m.backend = ""
		if errors.Is(err, ErrUnavailable) {
			m.h.LastError = "backend_unavailable"
			m.setStateLocked(StateUnavailable)
		} else {
			m.h.LastError = "up_failed: " + shortErr(err)
			m.setStateLocked(StateDown)
		}
		_, _ = m.reassertLocked(ctx) // stays closed
		return fmt.Errorf("warp: %w", err)
	}
	m.gen++
	m.backend, m.ep = backend, ep
	for i, c := range ps.cands {
		if c == ep {
			m.idx = i
		}
	}
	now := m.now()
	m.upAt, m.rotatedAt, m.watchUntil, m.rotations = now, now, now.Add(m.o.HandshakeWait), 0
	m.fails, m.succ, m.ladder = 0, 0, 0
	m.nextTick = now.Add(m.o.FastInterval)
	m.h.LastError = ""
	m.setStateLocked(StateStarting)
	_, err = m.reassertLocked(ctx)
	return err
}

// ensureLocked repairs a vanished device and re-asserts routing.
func (m *Manager) ensureLocked(ctx context.Context) error {
	if m.spec != nil && m.spec.Enabled {
		if st, err := m.plane.Stat(ctx); err == nil && !st.LinkPresent {
			return m.upLocked(ctx)
		}
	}
	_, err := m.reassertLocked(ctx)
	return err
}

func (m *Manager) reassertLocked(ctx context.Context) (bool, error) {
	rs := routeSpec{
		Configured: m.spec != nil || len(m.subnets) > 0,
		Subnets:    append([]netip.Prefix(nil), m.subnets...),
	}
	if m.spec != nil && m.spec.Enabled {
		rs.HasV6 = m.ps.hasV6()
		rs.Reserved = m.ps.reserved
		rs.Endpoint = m.ep
		rs.Kernel = m.backend == "kernel"
		if st, err := m.plane.Stat(ctx); err == nil {
			rs.LinkUp = st.LinkPresent && st.LinkUp
		}
	}
	changed, err := m.plane.Reassert(ctx, rs)
	m.routesDirty = err != nil
	return changed, err
}

func (m *Manager) setStateLocked(s State) {
	if s == m.state {
		return
	}
	from := m.state
	m.state = s
	m.log.Info("warp state", "state", s.String(), "from", from.String(), "backend", m.backend)
	if m.o.Emit != nil {
		m.o.Emit(Event{Code: "warp_state", Warn: s == StateDown || s == StateUnavailable,
			Params: map[string]string{"state": s.String(), "from": from.String()}})
	}
}

func (m *Manager) attention(reason string) {
	m.log.Warn("warp needs attention", "reason", reason)
	if m.o.Emit != nil {
		m.o.Emit(Event{Code: "warp_needs_attention", Warn: true, Params: map[string]string{"reason": reason}})
	}
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// Run drives the handshake watch and the health checks until ctx ends. It does not clean up; the agent calls
// Cleanup.
func (m *Manager) Run(ctx context.Context) {
	for {
		t := time.NewTimer(m.untilWake())
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
			m.poll(ctx)
		}
	}
}

func (m *Manager) untilWake() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	next := m.nextTick
	if !m.watchUntil.IsZero() && (next.IsZero() || m.watchUntil.Before(next)) {
		next = m.watchUntil
	}
	d := next.Sub(now)
	if next.IsZero() || d < 200*time.Millisecond {
		d = 200 * time.Millisecond
		if next.IsZero() {
			d = time.Second
		}
	}
	return d
}

// poll runs whatever is due. Exposed to the tests, which drive a fake clock with it.
func (m *Manager) poll(ctx context.Context) {
	m.mu.Lock()
	now := m.now()
	watch := !m.watchUntil.IsZero() && !now.Before(m.watchUntil)
	tick := m.spec != nil && !now.Before(m.nextTick)
	m.mu.Unlock()
	if watch {
		m.handshakeWatch(ctx)
	}
	if tick {
		m.healthTick(ctx)
	}
}

// handshakeWatch tries the next endpoint when a fresh tunnel has no handshake after HandshakeWait.
func (m *Manager) handshakeWatch(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.watchUntil.IsZero() || m.spec == nil || !m.spec.Enabled || m.state == StateUnavailable {
		m.watchUntil = time.Time{}
		return
	}
	now := m.now()
	if now.Before(m.watchUntil) {
		return
	}
	st, err := m.plane.Stat(ctx)
	if err == nil && handshakeFresh(st.Handshake, now) {
		m.watchUntil = time.Time{}
		m.nextTick = now // probe at once: two good checks make it UP
		return
	}
	m.rotations++
	if m.rotations >= len(m.ps.cands) {
		m.watchUntil = time.Time{} // every endpoint tried: the regular checks and the ladder take over
		m.h.LastError = "handshake_never"
		m.note = "every endpoint tried"
		return
	}
	m.rotateLocked(ctx, now, "watch")
	m.watchUntil = now.Add(m.o.HandshakeWait)
}

func handshakeFresh(hs, now time.Time) bool {
	return !hs.IsZero() && now.Sub(hs) <= handshakeMaxAge
}

// rotateLocked moves to the next endpoint candidate (port rotation, then the other address family).
func (m *Manager) rotateLocked(ctx context.Context, now time.Time, why string) bool {
	next := (m.idx + 1) % len(m.ps.cands)
	ep := m.ps.cands[next]
	if err := m.plane.SetEndpoint(ctx, ep); err != nil {
		m.note = "rotate " + ep.String() + " failed: " + shortErr(err)
		m.log.Warn("warp endpoint rotation failed", "endpoint", ep.String(), "err", err)
		return false
	}
	m.idx, m.ep, m.rotatedAt = next, ep, now
	m.note = "rotated to " + ep.String()
	m.log.Info("warp endpoint rotated", "endpoint", ep.String(), "why", why)
	_, _ = m.reassertLocked(ctx) // the nft "reserved" rule follows the endpoint
	return true
}

// mayRotate: the recovery ladder moves the endpoint only when the previous move had HandshakeWait to work and the
// tunnel did not handshake since: a fresh handshake with failing probes is not a port problem.
func (m *Manager) mayRotate(now time.Time) bool {
	hs := m.h.LastHandshake
	return now.Sub(m.rotatedAt) >= m.o.HandshakeWait && !(handshakeFresh(hs, now) && hs.After(m.rotatedAt))
}

// healthTick is one health check: device, routing, handshake age and the two probes.
func (m *Manager) healthTick(ctx context.Context) {
	m.mu.Lock()
	now := m.now()
	if m.spec == nil || !m.spec.Enabled {
		m.nextTick = now.Add(m.o.Interval)
		m.mu.Unlock()
		return
	}
	if m.state == StateUnavailable {
		m.nextTick = now.Add(m.o.Interval)
		_ = m.upLocked(ctx) // the host may have gained a backend (module loaded, /dev/net/tun mounted)
		m.mu.Unlock()
		return
	}
	st, err := m.plane.Stat(ctx)
	if err == nil && !st.LinkPresent {
		m.log.Warn("warp device vanished, recreating")
		if m.upLocked(ctx) == nil {
			st, err = m.plane.Stat(ctx)
		}
	}
	if repaired, rerr := m.reassertLocked(ctx); rerr != nil {
		m.log.Warn("warp routing reassert failed", "err", rerr)
	} else if repaired {
		m.log.Info("warp routing repaired")
	}
	gen := m.gen
	m.mu.Unlock()

	var (
		flag, colo   string
		aErr, bErr   error
		aTook, bTook time.Duration
		statFailed   = err != nil
		linkUsable   = err == nil && st.LinkPresent && st.LinkUp
	)
	if linkUsable {
		var wg sync.WaitGroup
		wg.Add(2)
		// real clock on purpose: m.now may be a test's fake one, and a latency is a duration, not a date
		go func() { defer wg.Done(); t0 := time.Now(); flag, colo, aErr = m.probe.A(ctx); aTook = time.Since(t0) }()
		go func() { defer wg.Done(); t0 := time.Now(); bErr = m.probe.B(ctx); bTook = time.Since(t0) }()
		wg.Wait()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.gen != gen || m.spec == nil || !m.spec.Enabled {
		return // the tunnel was rebuilt or removed while probing; this result is about the old one
	}
	now = m.now()
	reason := ""
	switch {
	case statFailed:
		reason = "stat_failed"
	case !linkUsable:
		reason = "link_down"
	case st.Handshake.IsZero():
		reason = "handshake_never"
	case !handshakeFresh(st.Handshake, now):
		reason = "handshake_stale"
	case aErr != nil:
		reason = "probe_cloudflare_failed"
	case flag != "on" && flag != "plus":
		reason = "warp_flag_" + orStr(flag, "missing")
	case bErr != nil:
		reason = "probe_other_failed"
	}
	m.h.LastHandshake = st.Handshake
	m.h.RxBytes, m.h.TxBytes = st.Rx, st.Tx
	m.h.CheckedAt = now
	if linkUsable {
		m.h.WarpFlag, m.h.Colo = flag, colo
		m.h.ProbeCloudflareOK = aErr == nil && (flag == "on" || flag == "plus")
		m.h.ProbeOtherOK = bErr == nil
		m.h.ProbeCloudflare = &ProbeResult{OK: m.h.ProbeCloudflareOK, Latency: aTook, At: now}
		m.h.ProbeOther = &ProbeResult{OK: m.h.ProbeOtherOK, Latency: bTook, At: now}
	} else {
		m.h.WarpFlag, m.h.Colo, m.h.ProbeCloudflareOK, m.h.ProbeOtherOK = "", "", false, false
		m.h.ProbeCloudflare, m.h.ProbeOther = nil, nil
	}
	interval := m.o.Interval
	if m.state == StateStarting {
		interval = m.o.FastInterval
	}
	m.nextTick = now.Add(interval)

	if reason == "" {
		m.fails, m.succ = 0, m.succ+1
		m.ladder = 0
		m.h.LastError = "" // the latest check passed; the ladder note (if any) stays until the tunnel is Up
		if m.state != StateUp && m.succ >= successesToUp {
			m.h.LastError, m.note = "", ""
			m.watchUntil = time.Time{}
			m.setStateLocked(StateUp)
		}
		return
	}
	m.succ, m.fails = 0, m.fails+1
	m.h.LastError = reason
	if m.fails >= failuresToDown {
		m.setStateLocked(StateDown)
		m.ladderStepLocked(ctx, now)
	}
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func joinNote(reason, note string) string {
	if reason == "" {
		return "ladder: " + note
	}
	return reason + "; ladder: " + note
}

// ladderStepLocked runs one step of the recovery ladder per failed check while DOWN: re-assert table, rules and
// device; rotate through the endpoints; ask the panel to re-read the account; then tell the owner it needs them
// (re-registration is never automatic). After the last step it rests for ladderCooldown.
func (m *Manager) ladderStepLocked(ctx context.Context, now time.Time) {
	n := len(m.ps.cands)
	switch {
	case m.ladder == 0:
		m.ladderAt = now
		if _, err := m.reassertLocked(ctx); err != nil {
			m.note = "reassert failed"
		} else {
			m.note = "reassert"
		}
	case m.ladder < n:
		if !m.mayRotate(now) || !m.rotateLocked(ctx, now, "ladder") {
			return // the same step again at the next failed check
		}
	case m.ladder == n:
		m.attention("refresh_requested")
		m.note = "refresh requested"
	case m.ladder == n+1:
		m.attention("down_after_ladder")
		m.note = "owner action needed"
	default:
		if now.Sub(m.ladderAt) < ladderCooldown {
			// resting: no events, no API calls, but keep walking the endpoints one per failed check, so a tunnel
			// whose outage ended comes back on whichever endpoint answers
			if m.mayRotate(now) {
				m.rotateLocked(ctx, now, "cooldown")
			}
			return
		}
		m.ladder = 0
		m.ladderStepLocked(ctx, now)
		return
	}
	m.ladder++
}

// --- egress ---------------------------------------------------------------------------------------------

// Egress is the engine.Egress for inbounds with egress "warp". Every dial goes through the tunnel device; while
// the node has no WARP, it is paused or unavailable, every dial fails with ErrNotActive, and a dead tunnel fails
// in the kernel ("no route to host"): never a silent direct exit.
func (m *Manager) Egress() engine.Egress { return tunnelEgress{m} }

type tunnelEgress struct{ m *Manager }

func (e tunnelEgress) TCP(addr string) (net.Conn, error) {
	eg, err := e.m.gated()
	if err != nil {
		return nil, err
	}
	return eg.TCP(addr)
}

func (e tunnelEgress) UDP(addr string) (engine.EgressUDP, error) {
	eg, err := e.m.gated()
	if err != nil {
		return nil, err
	}
	return eg.UDP(addr)
}

func (e tunnelEgress) CheckUDP(addr string) error {
	eg, err := e.m.gated()
	if err != nil {
		return err
	}
	return eg.CheckUDP(addr)
}

func (m *Manager) gated() (*egress.Direct, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.spec == nil || !m.spec.Enabled || m.state == StateUnavailable || m.state == StateNone || m.state == StateDisabled {
		return nil, ErrNotActive
	}
	return m.egressLocked(), nil
}

// egressLocked returns the dialer for the current address family set: IPv4 only while the account has no IPv6
// address (a v6 dial would only hang).
func (m *Manager) egressLocked() *egress.Direct {
	if m.ps.hasV6() {
		if m.egAuto == nil {
			m.egAuto = egress.New(m.o.DNS, m.egressOpts()...)
		}
		return m.egAuto
	}
	if m.egV4 == nil {
		m.egV4 = egress.New(m.o.DNS, append(m.egressOpts(), egress.IPv4Only())...)
	}
	return m.egV4
}

func (m *Manager) egressOpts() []egress.Option {
	opts := []egress.Option{egress.WithDevice(m.s.Iface)}
	if m.o.AllowPrivate {
		opts = append(opts, egress.AllowPrivate())
	}
	return opts
}

// probeDial is the dialer of the probes: the tunnel egress without the ErrNotActive gate, so a DOWN tunnel is still
// measured and can come back.
func (m *Manager) probeDial(addr string) (net.Conn, error) {
	m.mu.Lock()
	eg := m.egressLocked()
	m.mu.Unlock()
	return eg.TCP(addr)
}
