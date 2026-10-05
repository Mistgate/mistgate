// Package hysteria2 is the node's Hysteria2 engine (engine.Engine) on top of the hysteria core library
// (github.com/apernet/hysteria/core/v2).
//
// One Engine serves every hysteria2 inbound on the node; each inbound is one core server on its own UDP port.
//
//   - Users: an immutable credential index (sha256(token) -> credential) behind an atomic pointer. A users-only
//     Apply swaps it without touching the listener; a spec change restarts only that inbound.
//   - Kick: the core can only close a connection from inside LogTraffic, so dropping a credential from the index
//     (or an explicit Kick token, or an expired term) makes the next packet of that connection fail.
//   - Traffic: per-credential atomic counters, reported from the user's side (core tx = up, core rx = down).
//   - Masquerade: the decoy over HTTP/3 (core MasqHandler) and HTTPS on TCP (masq.go).
//   - Egress: Env.Egress (internal/node/egress), with the sniffing RequestHook on so sniffed domains replace
//     client-side IPs and are resolved by the node.
package hysteria2

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/apernet/hysteria/core/v2/server"
	"github.com/apernet/hysteria/extras/v2/correctnet"
	"github.com/apernet/hysteria/extras/v2/obfs"
	"github.com/apernet/hysteria/extras/v2/sniff"

	agentpb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/torrentguard"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// Protocol is the plugin id this engine serves.
const Protocol = "hysteria2"

// Factory is the engine.Factory the agent registers for "hysteria2".
var Factory engine.Factory = New

// BindIP is the local address the inbounds listen on (the QUIC UDP port and the TCP decoy). nil is all interfaces, which
// is what a node must do and the only value production ever has. Tests that run a real engine set it to a loopback
// address in TestMain: a test binary listening on every interface makes Windows ask for a firewall rule on each run.
var BindIP net.IP

// bindHost is BindIP as the host part of a listen address ("" for all interfaces).
func bindHost() string {
	if BindIP == nil {
		return ""
	}
	return BindIP.String()
}

type eng struct {
	env            engine.Env
	log            *slog.Logger
	now            func() time.Time
	out            func(name string) (engine.Egress, error)
	torrentEnabled atomic.Bool
	torrentVersion uint64 // guards inbound generations under mu so toggles replace existing outbound flows

	mu       sync.Mutex // guards everything below; never taken on the data path
	inbounds map[string]*inbound
	retired  []*credState // dropped credentials with counters not yet reported
	closed   bool
}

var _ engine.Engine = (*eng)(nil)

// New builds the engine. Env.Certs is required; Env.Egress defaults to egress.New(Env.DNS) for "direct".
func New(env engine.Env) (engine.Engine, error) {
	if env.Certs == nil {
		return nil, errors.New("hysteria2: Env.Certs is required")
	}
	e := &eng{env: env, log: env.Log, now: env.Now, out: env.Egress, inbounds: map[string]*inbound{}}
	if e.log == nil {
		e.log = slog.Default()
	}
	if e.now == nil {
		e.now = time.Now
	}
	if e.out == nil {
		direct := egress.New(env.DNS)
		e.out = func(name string) (engine.Egress, error) {
			if name != "" && name != "direct" {
				return nil, fmt.Errorf("egress %q is not available", name)
			}
			return direct, nil
		}
	}
	return e, nil
}

func (e *eng) Protocol() string { return Protocol }

func (e *eng) Version() string {
	v := "v2.12.3"
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/apernet/hysteria/core/v2" && d.Version != "" {
				v = d.Version
			}
		}
	}
	return "hysteria core " + v
}

func (e *eng) Capabilities() engine.Capabilities {
	return engine.Capabilities{RateLimitPerCred: true, HardExpiry: true}
}

// NodeSettings changes the protocol-level torrent detector. Reapplying inbounds closes outbound flows that
// were opened under the previous policy, so enabling the guard cannot leave old uninspected connections alive.
func (e *eng) NodeSettings(_ context.Context, st *agentpb.NodeSettings) bool {
	enabled := st != nil && st.GetTorrentBlockerEnabled()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.torrentEnabled.Load() == enabled {
		return false
	}
	e.torrentEnabled.Store(enabled)
	e.torrentVersion++
	return true
}

// inbound is one core server plus its credential index.
type inbound struct {
	e              *eng
	spec           plugin.InboundSpec
	specHash       string
	torrentVersion uint64
	cfg            settings
	creds          []plugin.UserCred // as delivered, for Observed (guarded by e.mu)

	idx    atomic.Pointer[index]
	ctx    context.Context // ends when the inbound stops; unblocks rate-limit waits
	cancel context.CancelFunc

	srv             server.Server
	tcp             *tcpMasq
	cert            engine.Cert
	certHeld        bool
	smu             sync.Mutex // guards sessions
	sessions        map[string]session
	mu              sync.Mutex // guards the fields below
	state           plugin.RunState
	requestMu       sync.Mutex
	pendingRequests map[string]pendingRequest
	detail          string
	since           time.Time
	restarts        uint32
	stopping        bool
	everServe       bool // a listener was started at some point for this incarnation
}

func (e *eng) Apply(ctx context.Context, spec plugin.InboundSpec, creds []plugin.UserCred) (engine.ApplyReport, error) {
	cfg, err := parseSpec(spec)
	if err != nil {
		return engine.ApplyReport{}, fmt.Errorf("hysteria2 inbound %q: %w", spec.ID, err)
	}
	creds = cloneCreds(creds)
	sh := statehash.Spec(spec)

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return engine.ApplyReport{}, errors.New("hysteria2: engine is closed")
	}
	old := e.inbounds[spec.ID]
	var oldIdx *index
	if old != nil {
		oldIdx = old.idx.Load()
	}
	ix, err := buildIndex(spec.ID, oldIdx, creds)
	if err != nil {
		return engine.ApplyReport{SpecHash: sh}, fmt.Errorf("hysteria2 inbound %q: %w", spec.ID, err)
	}

	// Same spec, and either serving or deliberately stopped: a users-only change. Swap the index.
	if old != nil && old.specHash == sh && old.torrentVersion == e.torrentVersion && old.settled() {
		e.swapIndex(old, oldIdx, ix, creds)
		return e.report(old, len(creds), false), nil
	}

	// New inbound, changed spec, or a retry of one that failed: (re)build it.
	in := &inbound{
		e: e, spec: spec, specHash: sh, torrentVersion: e.torrentVersion, cfg: cfg, creds: creds,
		sessions: map[string]session{}, state: plugin.RunStarting, since: e.now(),
	}
	in.ctx, in.cancel = context.WithCancel(context.Background())
	in.idx.Store(ix)
	restarted := false
	if old != nil {
		restarted = old.serving()
		in.restarts = old.restarts
		if restarted {
			in.restarts++
		}
		// Not a users-only change, so the old listener goes first (same port), but the cert claim is kept.
		old.stop(!spec.Enabled)
		e.retireMissing(oldIdx, ix)
	}
	e.inbounds[spec.ID] = in

	if !spec.Enabled {
		in.setState(plugin.RunStopped, "disabled")
		return e.report(in, len(creds), restarted), nil
	}
	if err := in.start(ctx); err != nil {
		e.log.Error("hysteria2 inbound failed to start", "inbound", spec.ID, "err", err)
		in.stop(true)
		in.setState(plugin.RunFailed, err.Error())
		return engine.ApplyReport{SpecHash: sh, CredCount: len(creds)}, fmt.Errorf("hysteria2 inbound %q: %w", spec.ID, err)
	}
	return e.report(in, len(creds), restarted), nil
}

func (e *eng) report(in *inbound, n int, restarted bool) engine.ApplyReport {
	r := engine.ApplyReport{SpecHash: in.specHash, CredCount: n, Restarted: restarted}
	if in.cert.Info != nil {
		r.Cert = in.cert.Info()
	}
	return r
}

// swapIndex publishes ix for in; credentials that vanished are retired (kicked at their next packet, their
// unreported counters flushed by the next Collect).
func (e *eng) swapIndex(in *inbound, oldIdx, ix *index, creds []plugin.UserCred) {
	in.creds = creds
	in.idx.Store(ix)
	e.retireMissing(oldIdx, ix)
}

func (e *eng) retireMissing(oldIdx, ix *index) {
	if oldIdx == nil {
		return
	}
	for id, cs := range oldIdx.byID {
		if ix.byID[id] != cs {
			cs.removed.Store(true)
			e.retired = append(e.retired, cs)
		}
	}
}

// settled: nothing to (re)start for the current spec.
func (in *inbound) settled() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.state == plugin.RunRunning || (in.state == plugin.RunStopped && !in.spec.Enabled)
}

func (in *inbound) serving() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.everServe
}

func (in *inbound) setState(s plugin.RunState, detail string) {
	in.mu.Lock()
	in.state, in.detail, in.since = s, detail, in.e.now()
	in.mu.Unlock()
}

func (in *inbound) start(ctx context.Context) error {
	spec, cfg := in.spec, in.cfg

	cert, err := in.e.env.Certs.Acquire(ctx, spec.ID, spec.TLS)
	if err != nil {
		return err
	}
	in.cert, in.certHeld = cert, true

	egr, err := in.e.out(spec.Egress)
	if err != nil {
		return err
	}

	pc, err := correctnet.ListenUDP("udp", &net.UDPAddr{IP: BindIP, Port: int(spec.Listen.Port)})
	if err != nil {
		return fmt.Errorf("listen udp %d: %w", spec.Listen.Port, err)
	}
	var conn net.PacketConn = pc
	switch cfg.obfs {
	case obfsSalamander:
		conn, err = obfs.WrapPacketConnSalamander(pc, cfg.obfsKey)
	case obfsGecko:
		conn, err = obfs.WrapPacketConnGecko(pc, obfs.GeckoOptions{Password: cfg.obfsKey})
	}
	if err != nil {
		pc.Close()
		return fmt.Errorf("obfs: %w", err)
	}

	masq := in.masqHandler()
	srv, err := server.NewServer(&server.Config{
		TLSConfig:             server.TLSConfig{GetCertificate: cert.GetCertificate},
		Conn:                  conn,
		RequestHook:           &sniff.Sniffer{}, // all ports, default 4 s timeout; client IPs are replaced by sniffed domains
		Outbound:              outbound{e: egr, in: in},
		CongestionConfig:      server.CongestionConfig{Type: "bbr", BBRProfile: cfg.bbrProfile},
		BandwidthConfig:       server.BandwidthConfig{MaxTx: cfg.maxTx, MaxRx: cfg.maxRx},
		IgnoreClientBandwidth: cfg.ignoreBW,
		DisableUDP:            !cfg.udp,
		Authenticator:         in,
		EventLogger:           in,
		TrafficLogger:         in,
		MasqHandler:           masq,
	})
	if err != nil {
		conn.Close() // NewServer only closes it on some failures
		return err
	}
	in.srv = srv
	in.mu.Lock()
	in.everServe = true
	in.mu.Unlock()
	go func() {
		err := srv.Serve()
		in.mu.Lock()
		defer in.mu.Unlock()
		if !in.stopping {
			in.state, in.detail, in.since = plugin.RunFailed, fmt.Sprintf("serve: %v", err), in.e.now()
			in.e.log.Error("hysteria2 inbound stopped unexpectedly", "inbound", spec.ID, "err", err)
		}
	}()

	detail := ""
	if cfg.tcpPort != 0 {
		// A busy TCP port must not take the VPN down: the inbound keeps serving QUIC, and the reason is in
		// Health().Detail so the panel can show that the decoy is degraded.
		m, err := startTCPMasq(cfg.tcpPort, cert.GetCertificate, masq, spec.Listen.Port)
		if err != nil {
			detail = fmt.Sprintf("masq_tcp: %v", err)
			in.e.log.Warn("hysteria2 tcp masquerade unavailable", "inbound", spec.ID, "port", cfg.tcpPort, "err", err)
		} else {
			in.tcp = m
		}
	}
	in.setState(plugin.RunRunning, detail)
	return nil
}

func (in *inbound) masqHandler() http.Handler {
	if in.cfg.masqNone {
		return decoy.NotFound()
	}
	if in.e.env.Masquerade != nil {
		if h := in.e.env.Masquerade(in.spec.ID); h != nil {
			return h
		}
	}
	return decoy.Handler()
}

// stop closes the listeners (sessions die with them) and optionally gives the certificate claim back.
func (in *inbound) stop(releaseCert bool) {
	in.mu.Lock()
	in.stopping = true
	in.mu.Unlock()
	in.cancel()
	if in.srv != nil {
		in.srv.Close()
	}
	if in.tcp != nil {
		in.tcp.Close()
	}
	if releaseCert && in.certHeld {
		in.e.env.Certs.Release(in.spec.ID)
		in.certHeld = false
	}
	in.mu.Lock()
	in.state, in.since = plugin.RunStopped, in.e.now()
	in.mu.Unlock()
}

func (e *eng) Remove(ctx context.Context, inboundID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	in := e.inbounds[inboundID]
	if in == nil {
		return nil
	}
	e.drop(in)
	return nil
}

// drop stops an inbound for good and queues its counters. Caller holds e.mu.
func (e *eng) drop(in *inbound) {
	in.stop(true)
	for _, cs := range in.idx.Load().byID {
		cs.removed.Store(true)
		e.retired = append(e.retired, cs)
	}
	delete(e.inbounds, in.spec.ID)
}

func (e *eng) Collect(ctx context.Context) (engine.Collected, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out engine.Collected
	flush := func(cs *credState) {
		up, down := cs.up.Swap(0), cs.down.Swap(0)
		if up != 0 || down != 0 {
			out.Traffic = append(out.Traffic, plugin.UserTraffic{CredID: cs.id, InboundID: cs.inboundID, Up: up, Down: down})
		}
	}
	for _, in := range e.sortedInbounds() {
		for _, cs := range in.idx.Load().byID {
			flush(cs)
		}
		in.smu.Lock()
		for _, s := range in.sessions {
			// A removed credential's connection is about to die at its next packet; do not show it online.
			if !s.cs.removed.Load() {
				out.Sessions = append(out.Sessions, plugin.Session{CredID: s.cs.id, InboundID: in.spec.ID, RemoteIP: s.ip, Since: s.since})
			}
		}
		in.smu.Unlock()
	}
	for _, cs := range e.retired {
		flush(cs)
	}
	e.retired = nil
	sort.Slice(out.Traffic, func(i, j int) bool {
		a, b := out.Traffic[i], out.Traffic[j]
		return a.InboundID < b.InboundID || (a.InboundID == b.InboundID && a.CredID < b.CredID)
	})
	sort.Slice(out.Sessions, func(i, j int) bool {
		a, b := out.Sessions[i], out.Sessions[j]
		return a.InboundID < b.InboundID || (a.InboundID == b.InboundID && (a.CredID < b.CredID || (a.CredID == b.CredID && a.Since.Before(b.Since))))
	})
	return out, nil
}

// Kick closes the open connections of these credentials at their next packet (Hysteria2 cannot do better).
// The credentials stay valid: a client that reconnects gets in.
func (e *eng) Kick(ctx context.Context, credIDs []string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	hit := map[string]struct{}{}
	for _, in := range e.inbounds {
		ix := in.idx.Load()
		for _, id := range credIDs {
			if cs := ix.byID[id]; cs != nil {
				if n := cs.conns.Load(); n > 0 {
					cs.kick(n, time.Now())
					hit[id] = struct{}{}
				}
			}
		}
	}
	return len(hit), nil
}

func (e *eng) Observed() []statehash.Inbound {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]statehash.Inbound, 0, len(e.inbounds))
	for _, in := range e.sortedInbounds() {
		out = append(out, statehash.Inbound{Spec: in.spec, Creds: in.creds})
	}
	return out
}

func (e *eng) Health() []plugin.EngineHealth {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []plugin.EngineHealth
	for _, in := range e.sortedInbounds() {
		in.mu.Lock()
		h := plugin.EngineHealth{InboundID: in.spec.ID, State: in.state, Detail: in.detail, Restarts: in.restarts, Since: in.since}
		in.mu.Unlock()
		if in.cert.Info != nil { // ACME: only known once the certificate is issued, so Apply could not report it
			ci := in.cert.Info()
			h.CertPinSHA256, h.CertNotAfter = ci.PinSHA256, ci.NotAfter
		}
		out = append(out, h)
	}
	return out
}

func (e *eng) Close(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	for _, in := range e.sortedInbounds() {
		e.drop(in)
	}
	return nil
}

func (e *eng) sortedInbounds() []*inbound {
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

func cloneCreds(in []plugin.UserCred) []plugin.UserCred {
	out := make([]plugin.UserCred, len(in))
	for i, c := range in {
		out[i] = c
		out[i].Data = append([]byte(nil), c.Data...)
	}
	return out
}

// TraceStream/UntraceStream are part of server.TrafficLogger; per-stream stats are not used.
func (in *inbound) TraceStream(server.HyStream, *server.StreamStats) {}
func (in *inbound) UntraceStream(server.HyStream)                    {}

// outbound adapts engine.Egress to server.Outbound. The two UDP interfaces have the same methods but are
// distinct named types, so Go needs this shim.
type outbound struct {
	e  engine.Egress
	in *inbound
}

func (o outbound) TCP(addr string) (net.Conn, error) {
	var userID string
	if o.in != nil && o.in.e.torrentEnabled.Load() {
		userID = o.in.takeRequestUserID(addr)
	}
	c, err := o.e.TCP(addr)
	if err != nil || o.in == nil || !o.in.e.torrentEnabled.Load() {
		return c, err
	}
	return newTorrentTCPConn(c, func() { o.in.reportTorrent(torrentguard.ProtocolBitTorrentTCP, "tcp", userID) }), nil
}
func (o outbound) CheckUDP(addr string) error { return o.e.CheckUDP(addr) }
func (o outbound) UDP(addr string) (server.UDPConn, error) {
	var userID string
	if o.in != nil && o.in.e.torrentEnabled.Load() {
		userID = o.in.takeRequestUserID(addr)
	}
	c, err := o.e.UDP(addr)
	if err != nil {
		return nil, err
	}
	if o.in != nil && o.in.e.torrentEnabled.Load() {
		return torrentUDPConn{UDPConn: c, attempt: func(protocol torrentguard.Protocol) {
			o.in.reportTorrent(protocol, "udp", userID)
		}}, nil
	}
	return c, nil
}

// reportTorrent names who tried, never where to: the destination stays on the node.
func (in *inbound) reportTorrent(protocol torrentguard.Protocol, transport, userID string) {
	if in == nil || in.e == nil || in.e.env.Event == nil {
		return
	}
	params := map[string]string{"protocol": transport, "torrent_protocol": string(protocol)}
	if userID != "" {
		params["user_id"] = userID
	}
	in.e.env.Event(engine.Event{Code: "torrent_attempt", InboundID: in.spec.ID, Warning: true, Params: params})
}
