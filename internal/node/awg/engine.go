// Package awg is the node's AmneziaWG engine (engine.Engine). One Engine serves every awg inbound of the node;
// one inbound is one profile = one network interface "mgawg<port>" on its own UDP port (obfuscation is a property
// of the interface, a peer that speaks other parameters cannot share it). Two backends sit behind awgBackend: the
// kernel module through our own genetlink client, and amneziawg-go inside the agent process (the default).
//
// Semantics the agent relies on:
//   - A peer is a credential. Users-only changes go to the live interface: a new key is added, a changed address or
//     preshared key is an update_only + replace_allowed_ips, a vanished key is removed. An unchanged active peer is
//     never removed and re-added (its client would freeze for ~15 s). The diff is taken against what the device
//     reports, so a peer deleted behind our back comes back with the next Apply.
//   - Any change of port, version, server key, obfuscation or tunnel address recreates the interface (Restarted).
//     Only the MTU is changed live.
//   - Traffic: per peer, from the device counters; Up = rx_bytes, Down = tx_bytes (the client's view). The previous
//     absolute value is kept per (inbound, public key); a smaller new value means the peer was recreated and the
//     delta is the new value. The final counters of a removed peer are flushed before it goes.
//   - The host layer (nftables masquerade, MSS clamp, ip_forward, the UDP counter) belongs to the agent
//     (hostctl.SetTunnels); this engine only builds the interface, its address and MTU.
package awg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// Reasons the engine reports (InboundResult.error / Health detail start with the code).
const (
	ReasonBackendUnavailable = "awg_backend_unavailable"
	ReasonModuleTooOld       = "awg_module_too_old"
)

const (
	// onlineWindow: a peer is a session while its last handshake is younger than reject_after_time.hi (180 s) + 10 s.
	onlineWindow = 190 * time.Second
	// kickDelay: Kick removes a peer and puts it back after this long (the client recovers by its own timer).
	kickDelay = time.Second
)

// Options configure a new Engine.
type Options struct {
	// Backend is NodeSettings.awg_backend: "auto" (the kernel module if it is already loaded, else userspace),
	// "kernel" or "userspace". "" means auto. Chosen once here; the agent does not install packages.
	Backend string
}

// Factory is the engine.Factory with the default (auto) backend choice; FactoryFor takes the setting.
var Factory engine.Factory = New

// New builds the engine with the auto backend choice.
func New(env engine.Env) (engine.Engine, error) { return NewWithOptions(env, Options{}) }

// FactoryFor returns a factory for the given NodeSettings.awg_backend.
func FactoryFor(backend string) engine.Factory {
	return func(env engine.Env) (engine.Engine, error) { return NewWithOptions(env, Options{Backend: backend}) }
}

// NewWithOptions builds the engine and detects the backend. No usable backend is not an error here: every
// awg inbound then fails with awg_backend_unavailable and BackendStatus says why (a node without AWG keeps its
// other engines).
func NewWithOptions(env engine.Env, opts Options) (*Engine, error) {
	mode := opts.Backend
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" && mode != "kernel" && mode != "userspace" {
		return nil, fmt.Errorf("awg: backend %q: want auto, kernel or userspace", mode)
	}
	log := env.Log
	if log == nil {
		log = slog.Default()
	}
	be, st := pickBackend(mode, log)
	return newEngine(env, be, st), nil
}

func newEngine(env engine.Env, be awgBackend, st BackendStatus) *Engine {
	e := &Engine{
		env: env, log: env.Log, now: env.Now, backend: be, status: st,
		inbounds: map[string]*inbound{}, byIface: map[string]string{}, retired: map[trafficKey]*plugin.UserTraffic{},
		kickDelay: kickDelay, portListening: portListening,
	}
	if e.log == nil {
		e.log = slog.Default()
	}
	if e.now == nil {
		e.now = time.Now
	}
	return e
}

// Engine implements engine.Engine.
type Engine struct {
	env     engine.Env
	log     *slog.Logger
	now     func() time.Time
	backend awgBackend // nil when unavailable
	status  BackendStatus

	kickDelay     time.Duration
	portListening func(port uint16) bool

	mu       sync.Mutex // guards everything below; there is no data path in this process for the kernel backend
	inbounds map[string]*inbound
	byIface  map[string]string // interface name -> inbound id
	retired  map[trafficKey]*plugin.UserTraffic
	closed   bool
}

var _ engine.Engine = (*Engine)(nil)

type trafficKey struct{ inbound, cred string }

type counters struct{ rx, tx uint64 }

// inbound is one interface.
type inbound struct {
	spec     plugin.InboundSpec
	specHash string
	cfg      nodeConfig
	creds    []plugin.UserCred // as delivered, for Observed

	up     bool // the interface exists (created by this process)
	gen    uint64
	peers  map[[32]byte]*wantPeer // the applied set
	prev   map[[32]byte]counters  // last absolute counters seen, per peer
	timers map[[32]byte]*time.Timer

	state    plugin.RunState
	detail   string
	since    time.Time
	restarts uint32
}

// Protocol implements engine.Engine.
func (e *Engine) Protocol() string { return Protocol }

// Version names the backend (EngineInfo.version).
func (e *Engine) Version() string {
	if e.backend == nil {
		return "amneziawg (no backend)"
	}
	return e.backend.Version()
}

// Capabilities: the interface is shared by all users of a profile, so no per-credential rate limit; a credential
// cannot expire by itself on the device (the agent withdraws it).
func (e *Engine) Capabilities() engine.Capabilities {
	return engine.Capabilities{RateLimitPerCred: false, HardExpiry: false}
}

// BackendStatus says which backend was chosen, or why there is none (for the doctor and the node page).
func (e *Engine) BackendStatus() BackendStatus { return e.status }

// Apply makes the inbound match (spec, creds).
func (e *Engine) Apply(ctx context.Context, spec plugin.InboundSpec, creds []plugin.UserCred) (engine.ApplyReport, error) {
	cfg, err := parseSpec(spec)
	if err != nil {
		return engine.ApplyReport{}, fmt.Errorf("awg inbound %q: %w", spec.ID, err)
	}
	creds = cloneCreds(creds)
	sh := statehash.Spec(spec)

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return engine.ApplyReport{}, errors.New("awg: engine is closed")
	}
	old := e.inbounds[spec.ID]

	if !spec.Enabled {
		restarts := uint32(0)
		if old != nil {
			restarts = old.restarts
			e.destroy(ctx, old)
		}
		in := &inbound{spec: spec, specHash: sh, creds: creds, state: plugin.RunStopped, detail: "disabled", since: e.now(), restarts: restarts}
		e.inbounds[spec.ID] = in
		return engine.ApplyReport{SpecHash: sh, CredCount: len(creds)}, nil
	}

	if e.backend == nil {
		return e.fail(old, spec, sh, creds, fmt.Errorf("%s: %s", ReasonBackendUnavailable, e.status.Reason))
	}
	if cfg.version == awgcfg.Version31 && !e.backend.Is31() {
		return e.fail(old, spec, sh, creds, fmt.Errorf("%s: %s cannot run AWG 3.1 (RandomTrailers/DisableCookies missing): needs amneziawg module v3.1.20260812 or newer, no silent fallback", ReasonModuleTooOld, e.backend.Version()))
	}
	peers, err := parsePeers(cfg, creds)
	if err != nil {
		return engine.ApplyReport{SpecHash: sh}, fmt.Errorf("awg inbound %q: %w", spec.ID, err)
	}
	if other := e.byIface[cfg.name]; other != "" && other != spec.ID {
		return engine.ApplyReport{SpecHash: sh}, fmt.Errorf("awg inbound %q: udp port %d is already served by inbound %q", spec.ID, cfg.port, other)
	}

	// Same interface identity and still there: a users-only (or MTU-only) change, applied live.
	if old != nil && old.up && old.state == plugin.RunRunning && old.cfg.identity == cfg.identity && e.backend.LinkUp(old.cfg.name) {
		if err := e.updateLive(ctx, old, cfg, peers); err != nil {
			e.log.Error("awg live update failed", "inbound", spec.ID, "err", err)
			return engine.ApplyReport{SpecHash: sh, CredCount: len(creds)}, fmt.Errorf("awg inbound %q: %w", spec.ID, err)
		}
		old.spec, old.specHash, old.creds, old.cfg = spec, sh, creds, cfg
		return engine.ApplyReport{SpecHash: sh, CredCount: len(creds)}, nil
	}

	// New inbound, changed identity, or a retry of one that failed or lost its interface: (re)build it.
	restarted, restarts := false, uint32(0)
	if old != nil {
		restarted = old.up
		restarts = old.restarts
		if restarted {
			restarts++
		}
		e.destroy(ctx, old)
	}
	in := &inbound{
		spec: spec, specHash: sh, cfg: cfg, creds: creds, peers: map[[32]byte]*wantPeer{}, prev: map[[32]byte]counters{},
		timers: map[[32]byte]*time.Timer{}, state: plugin.RunStarting, since: e.now(), restarts: restarts,
	}
	e.inbounds[spec.ID] = in
	if err := e.build(ctx, in, peers); err != nil {
		e.log.Error("awg inbound failed to start", "inbound", spec.ID, "err", err)
		e.destroy(ctx, in)
		in.state, in.detail, in.since = plugin.RunFailed, err.Error(), e.now()
		return engine.ApplyReport{SpecHash: sh, CredCount: len(creds)}, fmt.Errorf("awg inbound %q: %w", spec.ID, err)
	}
	in.state, in.detail, in.since = plugin.RunRunning, "", e.now()
	return engine.ApplyReport{SpecHash: sh, CredCount: len(creds), Restarted: restarted}, nil
}

// fail records a failed inbound (keeping nothing running for it) and returns the error.
func (e *Engine) fail(old *inbound, spec plugin.InboundSpec, sh string, creds []plugin.UserCred, err error) (engine.ApplyReport, error) {
	restarts := uint32(0)
	if old != nil {
		restarts = old.restarts
		e.destroy(context.Background(), old)
	}
	e.inbounds[spec.ID] = &inbound{spec: spec, specHash: sh, creds: creds, state: plugin.RunFailed, detail: err.Error(), since: e.now(), restarts: restarts}
	return engine.ApplyReport{SpecHash: sh, CredCount: len(creds)}, fmt.Errorf("awg inbound %q: %w", spec.ID, err)
}

// build creates the interface and its peers.
func (e *Engine) build(ctx context.Context, in *inbound, peers map[[32]byte]*wantPeer) error {
	c := in.cfg
	if err := e.backend.Create(ctx, c.name, deviceConfig{Port: c.port, PrivateKey: c.priv, Version: c.version, Obf: c.obf, Tunnel: c.tunnel}); err != nil {
		return err
	}
	in.up = true
	in.gen++
	e.byIface[c.name] = in.spec.ID
	return e.syncPeers(ctx, in, nil, peers)
}

// updateLive applies an MTU change and the credential diff to the running interface.
func (e *Engine) updateLive(ctx context.Context, in *inbound, cfg nodeConfig, peers map[[32]byte]*wantPeer) error {
	if cfg.tunnel.MTU != in.cfg.tunnel.MTU {
		if err := e.backend.SetMTU(ctx, cfg.name, int(cfg.tunnel.MTU)); err != nil {
			return fmt.Errorf("set mtu: %w", err)
		}
		in.cfg.tunnel.MTU = cfg.tunnel.MTU
	}
	have, err := e.backend.Stats(ctx, cfg.name)
	if err != nil {
		return fmt.Errorf("read peers: %w", err)
	}
	return e.syncPeers(ctx, in, have, peers)
}

// syncPeers moves the device from `have` (what it reports) to `want`. Removals go first in the first batch(es),
// then additions and updates; batches are at most peerBatch peers.
func (e *Engine) syncPeers(ctx context.Context, in *inbound, have []awgcfg.PeerStat, want map[[32]byte]*wantPeer) error {
	haveBy := make(map[[32]byte]awgcfg.PeerStat, len(have))
	for _, h := range have {
		haveBy[h.PublicKey] = h
	}
	var removes, rest []awgcfg.Peer
	for _, h := range sortedStats(have) {
		if want[h.PublicKey] == nil {
			e.flush(in, h)
			removes = append(removes, awgcfg.Peer{PublicKey: h.PublicKey, Remove: true})
		}
	}
	for _, key := range sortedKeys(want) {
		w := want[key]
		h, ok := haveBy[key]
		switch {
		case !ok:
			rest = append(rest, w.add())
		case !samePrefixes(h.AllowedIPs, w.allowed) || !samePSK(in.peers[key], w):
			rest = append(rest, w.update())
		}
	}
	ops := append(removes, rest...)
	for len(ops) > 0 {
		n := min(len(ops), peerBatch)
		if err := e.backend.SetPeers(ctx, in.cfg.name, false, ops[:n]); err != nil {
			return fmt.Errorf("set peers: %w", err)
		}
		ops = ops[n:]
	}
	in.peers = want
	return nil
}

// flush queues the counters a peer accumulated since the last Collect, before the peer leaves the device.
func (e *Engine) flush(in *inbound, h awgcfg.PeerStat) {
	if w := in.peers[h.PublicKey]; w != nil {
		e.addTraffic(in.spec.ID, w.credID, deltaOf(in.prev[h.PublicKey].rx, h.RxBytes), deltaOf(in.prev[h.PublicKey].tx, h.TxBytes))
	}
	delete(in.prev, h.PublicKey)
}

func (e *Engine) addTraffic(inboundID, credID string, up, down uint64) {
	if up == 0 && down == 0 {
		return
	}
	k := trafficKey{inboundID, credID}
	t := e.retired[k]
	if t == nil {
		t = &plugin.UserTraffic{CredID: credID, InboundID: inboundID}
		e.retired[k] = t
	}
	t.Up += up
	t.Down += down
}

// destroy flushes the counters, removes the interface and forgets it. Caller holds e.mu.
func (e *Engine) destroy(ctx context.Context, in *inbound) {
	for k, t := range in.timers {
		t.Stop()
		delete(in.timers, k)
	}
	if in.up && e.backend != nil {
		if st, err := e.backend.Stats(ctx, in.cfg.name); err == nil {
			for _, h := range st {
				e.flush(in, h)
			}
		}
		if err := e.backend.Destroy(ctx, in.cfg.name); err != nil {
			e.log.Warn("awg interface removal failed", "inbound", in.spec.ID, "iface", in.cfg.name, "err", err)
		}
	}
	if e.byIface[in.cfg.name] == in.spec.ID {
		delete(e.byIface, in.cfg.name)
	}
	in.up = false
	in.gen++
	in.state, in.since = plugin.RunStopped, e.now()
}

// Remove stops the inbound and forgets it.
func (e *Engine) Remove(ctx context.Context, inboundID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if in := e.inbounds[inboundID]; in != nil {
		e.destroy(ctx, in)
		delete(e.inbounds, inboundID)
	}
	return nil
}

// Collect returns traffic deltas since the previous Collect and the open sessions.
func (e *Engine) Collect(ctx context.Context) (engine.Collected, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out engine.Collected
	now := e.now()
	for _, in := range e.sortedInbounds() {
		if !in.up {
			continue
		}
		st, err := e.backend.Stats(ctx, in.cfg.name)
		if err != nil {
			e.log.Warn("awg stats failed", "inbound", in.spec.ID, "err", err)
			continue
		}
		for _, h := range st {
			w := in.peers[h.PublicKey]
			if w == nil {
				continue // not ours: a peer we did not add
			}
			p := in.prev[h.PublicKey]
			e.addTraffic(in.spec.ID, w.credID, deltaOf(p.rx, h.RxBytes), deltaOf(p.tx, h.TxBytes))
			in.prev[h.PublicKey] = counters{h.RxBytes, h.TxBytes}
			if online(h, now) {
				out.Sessions = append(out.Sessions, plugin.Session{CredID: w.credID, InboundID: in.spec.ID, Since: h.LastHS})
			}
		}
	}
	for _, t := range e.retired {
		out.Traffic = append(out.Traffic, *t)
	}
	e.retired = map[trafficKey]*plugin.UserTraffic{}
	sort.Slice(out.Traffic, func(i, j int) bool {
		a, b := out.Traffic[i], out.Traffic[j]
		return a.InboundID < b.InboundID || (a.InboundID == b.InboundID && a.CredID < b.CredID)
	})
	sort.Slice(out.Sessions, func(i, j int) bool {
		a, b := out.Sessions[i], out.Sessions[j]
		return a.InboundID < b.InboundID || (a.InboundID == b.InboundID && a.CredID < b.CredID)
	})
	return out, nil
}

// Kick closes the sessions of these credentials: the peer is removed and put back after a second if its
// credential is still valid (the client reconnects by its own timer, up to ~15 s). It returns how many
// credentials had a session.
func (e *Engine) Kick(ctx context.Context, credIDs []string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(credIDs) == 0 {
		return 0, nil
	}
	ask := make(map[string]bool, len(credIDs))
	for _, id := range credIDs {
		ask[id] = true
	}
	hit := map[string]bool{}
	now := e.now()
	for _, in := range e.sortedInbounds() {
		if !in.up {
			continue
		}
		st, err := e.backend.Stats(ctx, in.cfg.name)
		if err != nil {
			return len(hit), fmt.Errorf("awg inbound %q: %w", in.spec.ID, err)
		}
		var ops []awgcfg.Peer
		for _, h := range st {
			w := in.peers[h.PublicKey]
			if w == nil || !ask[w.credID] || !online(h, now) {
				continue
			}
			e.flush(in, h)
			ops = append(ops, awgcfg.Peer{PublicKey: h.PublicKey, Remove: true})
			hit[w.credID] = true
			e.scheduleReadd(in, h.PublicKey)
		}
		for len(ops) > 0 {
			n := min(len(ops), peerBatch)
			if err := e.backend.SetPeers(ctx, in.cfg.name, false, ops[:n]); err != nil {
				return len(hit), fmt.Errorf("awg inbound %q: kick: %w", in.spec.ID, err)
			}
			ops = ops[n:]
		}
	}
	return len(hit), nil
}

func (e *Engine) scheduleReadd(in *inbound, key [32]byte) {
	gen := in.gen
	if t := in.timers[key]; t != nil {
		t.Stop()
	}
	in.timers[key] = time.AfterFunc(e.kickDelay, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		delete(in.timers, key)
		w := in.peers[key]
		if e.closed || !in.up || in.gen != gen || w == nil {
			return // gone, recreated, or the credential no longer exists: nothing to put back
		}
		if err := e.backend.SetPeers(context.Background(), in.cfg.name, false, []awgcfg.Peer{w.add()}); err != nil {
			e.log.Warn("awg kick: putting the peer back failed", "inbound", in.spec.ID, "err", err)
		}
	})
}

// Observed reports what the device actually holds: a credential counts only while its public key and allowed ips
// are on the interface, so a peer that vanished shows up as a state-hash difference (state_drift). For an inbound
// that is not running (disabled, failed) the delivered credentials are reported, the failure is shown elsewhere.
func (e *Engine) Observed() []statehash.Inbound {
	e.mu.Lock()
	defer e.mu.Unlock()
	ins := e.sortedInbounds()
	out := make([]statehash.Inbound, 0, len(ins))
	for _, in := range ins {
		creds := in.creds
		if in.up && in.state == plugin.RunRunning {
			if st, err := e.backend.Stats(context.Background(), in.cfg.name); err == nil {
				creds = e.presentCreds(in, st)
			}
		}
		out = append(out, statehash.Inbound{Spec: in.spec, Creds: creds})
	}
	return out
}

func (e *Engine) presentCreds(in *inbound, st []awgcfg.PeerStat) []plugin.UserCred {
	haveBy := make(map[[32]byte]awgcfg.PeerStat, len(st))
	for _, h := range st {
		haveBy[h.PublicKey] = h
	}
	byCred := make(map[string]*wantPeer, len(in.peers))
	for _, w := range in.peers {
		byCred[w.credID] = w
	}
	var out []plugin.UserCred
	for _, c := range in.creds {
		w := byCred[c.CredID]
		if w == nil {
			continue
		}
		if h, ok := haveBy[w.pub]; ok && samePrefixes(h.AllowedIPs, w.allowed) {
			out = append(out, c)
		}
	}
	return out
}

// Health is the run state per inbound. A running inbound whose interface vanished (or, in userspace, whose UDP
// port is no longer bound) is reported failed; the next Apply (or a restart) rebuilds it.
func (e *Engine) Health() []plugin.EngineHealth {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []plugin.EngineHealth
	for _, in := range e.sortedInbounds() {
		if in.up && in.state == plugin.RunRunning {
			switch {
			case !e.backend.LinkUp(in.cfg.name):
				in.state, in.detail, in.since = plugin.RunFailed, "iface_down", e.now()
			case e.backend.Name() == "userspace" && !e.portListening(in.cfg.port):
				in.state, in.detail, in.since = plugin.RunFailed, "port_not_listening", e.now()
			}
		}
		out = append(out, plugin.EngineHealth{InboundID: in.spec.ID, State: in.state, Detail: in.detail, Restarts: in.restarts, Since: in.since})
	}
	return out
}

// InboundHealth is the AWG-specific health of one inbound (agent.v1.AwgHealth without the UDP packet counter,
// which is an nftables counter the host layer owns).
type InboundHealth struct {
	InboundID           string
	Backend             string
	BackendVersion      string
	IfaceUp             bool
	Peers               uint32
	PeersHandshaken     uint32 // completed a handshake at least once
	PeersOnline         uint32 // last handshake younger than 190 s
	NewestHandshakeUnix int64
	UnknownPeerEvents   uint32 // kernel only: initiations from unknown keys since the previous call
}

// HealthReporter is what the agent asserts on the engine to fill InboundHealth.awg.
type HealthReporter interface {
	AwgHealth() []InboundHealth
}

var _ HealthReporter = (*Engine)(nil)

// AwgHealth reports every inbound that has an interface. Calling it resets UnknownPeerEvents.
func (e *Engine) AwgHealth() []InboundHealth {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []InboundHealth
	now := e.now()
	for _, in := range e.sortedInbounds() {
		if !in.up || e.backend == nil {
			continue
		}
		h := InboundHealth{InboundID: in.spec.ID, Backend: e.backend.Name(), BackendVersion: e.backend.Version(), IfaceUp: e.backend.LinkUp(in.cfg.name)}
		if st, err := e.backend.Stats(context.Background(), in.cfg.name); err == nil {
			h.Peers = uint32(len(st))
			for _, p := range st {
				if p.LastHS.IsZero() {
					continue
				}
				h.PeersHandshaken++
				if online(p, now) {
					h.PeersOnline++
				}
				if u := p.LastHS.Unix(); u > h.NewestHandshakeUnix {
					h.NewestHandshakeUnix = u
				}
			}
		}
		if u, ok := e.backend.(unknownPeerSource); ok {
			h.UnknownPeerEvents = u.TakeUnknownPeers(in.cfg.name)
		}
		out = append(out, h)
	}
	return out
}

// Close removes every interface and closes the backend.
func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	for _, in := range e.sortedInbounds() {
		e.destroy(ctx, in)
	}
	if e.backend != nil {
		return e.backend.Close()
	}
	return nil
}

func (e *Engine) sortedInbounds() []*inbound {
	ids := make([]string, 0, len(e.inbounds))
	for id := range e.inbounds {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*inbound, len(ids))
	for i, id := range ids {
		out[i] = e.inbounds[id]
	}
	return out
}

// ---- small helpers ----

func deltaOf(prev, cur uint64) uint64 {
	if cur >= prev {
		return cur - prev
	}
	return cur // the counter restarted: the peer or the interface was recreated
}

func online(h awgcfg.PeerStat, now time.Time) bool {
	return !h.LastHS.IsZero() && now.Sub(h.LastHS) < onlineWindow
}

func samePrefixes(a, b []netip.Prefix) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]netip.Prefix(nil), a...)
	y := append([]netip.Prefix(nil), b...)
	less := func(s []netip.Prefix) func(i, j int) bool {
		return func(i, j int) bool { return s[i].Addr().Less(s[j].Addr()) }
	}
	sort.Slice(x, less(x))
	sort.Slice(y, less(y))
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// samePSK: a peer the engine has no record of counts as changed (its preshared key is unknown).
func samePSK(applied, want *wantPeer) bool {
	if applied == nil {
		return false
	}
	if (applied.psk == nil) != (want.psk == nil) {
		return false
	}
	return applied.psk == nil || *applied.psk == *want.psk
}

func sortedKeys(m map[[32]byte]*wantPeer) [][32]byte {
	keys := make([][32]byte, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return string(keys[i][:]) < string(keys[j][:]) })
	return keys
}

func sortedStats(s []awgcfg.PeerStat) []awgcfg.PeerStat {
	out := append([]awgcfg.PeerStat(nil), s...)
	sort.Slice(out, func(i, j int) bool { return string(out[i].PublicKey[:]) < string(out[j].PublicKey[:]) })
	return out
}

func cloneCreds(in []plugin.UserCred) []plugin.UserCred {
	out := make([]plugin.UserCred, len(in))
	for i, c := range in {
		out[i] = c
		out[i].Data = append([]byte(nil), c.Data...)
	}
	return out
}
