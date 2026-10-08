package agent

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/awg"
	"github.com/mistgate/mistgate/internal/node/doctor"
	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/node/warp"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The L3 side of the agent (agent.proto "AWG AND WARP"): the node-level WARP manager,
// the firewall of the tunnel interfaces, the AWG health, and the capability strings. Everything here runs on the
// worker goroutine like the rest of the reconcile, except the health readers, which are safe from any goroutine.

// Capabilities this build can list in Hello.capabilities besides "doctor/1" and the update ones.
const (
	capAWG          = "awg/1"
	capWarp         = "warp/1"
	capTorrentGuard = "torrentguard/1"
	capUDPCheck     = "udpcheck/1"
	// capClientIPv6: the agent follows NodeSettings.client_ipv6_disabled (the tunnel firewall and the direct egress).
	capClientIPv6 = "client-ipv6/1"
	capWSLink     = "ws-link/1"
	// capUnit3 says the node runs from a systemd unit of generation 3: /dev/net/tun is reachable (the userspace AWG
	// and WARP backends need it) and ExecStopPost cleans the tunnel interfaces. A node without it keeps working with
	// the kernel backends and shows the hint "unit_outdated" on the awg_backend doctor check.
	capUnit3 = "unit/3"

	egressWarp = "warp"
	// errWarpNotConfigured is InboundResult.error of an inbound whose egress is WARP on a node without a WARP
	// configuration: it is not started, there is never a silent direct exit (agent.proto "AWG AND WARP").
	errWarpNotConfigured = "egress warp: not configured on this node"
)

// WarpManager is what the agent needs of internal/node/warp.Manager (the real one implements it; tests use a fake).
// The manager is handed to the engines through Config.Egress; the agent owns its lifecycle and feeds it the spec.
type WarpManager interface {
	// Apply makes the node match the spec: nil removes WARP, Enabled=false pauses it. Idempotent and cheap for a
	// spec it already runs (it only re-asserts the routing).
	Apply(ctx context.Context, spec *plugin.WarpSpec) error
	// Reconnect rebuilds a down tunnel without changing its account or routed subnets. False means it recovered already.
	Reconnect(ctx context.Context) (bool, error)
	// CheckNow retries one current failed check and reports whether WARP is down at its failure threshold.
	CheckNow(ctx context.Context) (checked, thresholdDown bool, err error)
	// SetRoutedSubnets sets the client subnets of the awg inbounds with egress "warp".
	SetRoutedSubnets(ctx context.Context, subnets []netip.Prefix) error
	// Configured says whether the node has a WARP configuration (paused included).
	Configured() bool
	// Health is the last measured state; ok=false when the node has no WARP.
	Health() (warp.Health, bool)
	// Preflight lists clashes with the host (routing table, rule preferences, a FORWARD drop policy); the doctor shows them.
	Preflight(ctx context.Context) []warp.Finding
	// Run drives the health checks until ctx ends.
	Run(ctx context.Context)
	// Cleanup removes everything the manager installed on the host.
	Cleanup(ctx context.Context) error
}

// SettingsAware is implemented by an engine whose configuration comes from NodeSettings (awg: the backend). The worker
// calls NodeSettings before every reconcile with the settings the agent holds; an engine that dropped its inbounds
// because of a change returns reset=true and every inbound of its protocol is applied again in the same reconcile.
type SettingsAware interface {
	NodeSettings(ctx context.Context, st *pb.NodeSettings) (reset bool)
}

// AwgBackend returns NodeSettings.awg_backend ("" = auto). The awg engine factory asks for it when it builds its
// backend, like the engines ask for DNS.
func (a *Agent) AwgBackend() string { return a.settings.Load().AwgBackend }

// ClientIPv6Disabled returns NodeSettings.client_ipv6_disabled: "IPv6 for clients" is off. The direct egress of the
// protocol engines asks for it on every dial, like the engines ask for DNS.
func (a *Agent) ClientIPv6Disabled() bool { return a.settings.Load().GetClientIpv6Disabled() }

// WarpEvent turns an event of the WARP manager into an agent Event (warp_state, warp_needs_attention). The manager
// is built before the agent, so its Emit option calls this through a closure.
func (a *Agent) WarpEvent(ev warp.Event) {
	sev := pb.Severity_SEVERITY_INFO
	if ev.Warn {
		sev = pb.Severity_SEVERITY_WARNING
	}
	a.event(sev, ev.Code, "", ev.Params)
	if ev.Code == "warp_state" && a.doc != nil {
		// The doctor's warp_path would otherwise keep the old state for up to doctorEvery (a red line for minutes after
		// WARP is back up). The manager calls this with its lock held and the doctor reads the manager: hence the goroutine.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			a.recheck(ctx, warpPathChecks)
		}()
	}
}

// warpPathChecks are the doctor checks that describe the WARP tunnel; they run again on every WARP state change.
var warpPathChecks = []string{doctor.CheckWarpPath}

func (a *Agent) warpConfigured() bool { return a.cfg.Warp != nil && a.cfg.Warp.Configured() }

func (a *Agent) reconnectWarp(ctx context.Context) *pb.CommandResult {
	if a.cfg.Warp == nil {
		return &pb.CommandResult{Error: "unsupported_host"}
	}
	reconnected, err := a.cfg.Warp.Reconnect(ctx)
	if err != nil {
		return &pb.CommandResult{Error: err.Error()}
	}
	var affected uint32
	if reconnected {
		affected = 1
	}
	return &pb.CommandResult{Ok: true, Affected: affected}
}

// capabilities is the Hello.capabilities list of this build.
func (a *Agent) capabilities() []string {
	caps := []string{capDoctor, capBandwidth, capClientIPv6, capWSLink}
	if _, ok := a.engines[awg.Protocol]; ok {
		caps = append(caps, capAWG)
	}
	if a.cfg.Warp != nil {
		caps = append(caps, capWarp)
	}
	if hostctl.TorrentGuardSupported() {
		caps = append(caps, capTorrentGuard)
	}
	if _, ok := a.host.(hostctl.UDPCounter); ok {
		caps = append(caps, capUDPCheck)
	}
	if a.cfg.UnitGen >= 3 {
		caps = append(caps, capUnit3)
	}
	if a.cfg.AwgPrepare != nil && a.engines[awg.Protocol] != nil {
		caps = append(caps, capAwgPrepare)
	}
	return append(caps, a.upd.Capabilities()...)
}

// engineSettings hands NodeSettings to the engines that follow it.
func (a *Agent) engineSettings(ctx context.Context, next *model) {
	st := a.settings.Load()
	for _, p := range a.protocols {
		sa, ok := a.engines[p].(SettingsAware)
		if !ok || !sa.NodeSettings(ctx, st) {
			continue
		}
		a.log.Info("engine settings changed, inbounds are applied again", "protocol", p)
		for id, in := range next.inbounds {
			if in.spec.Protocol == p {
				delete(a.held, id)
			}
		}
	}
}

// syncWarp hands the WARP configuration of the model to the manager. It runs on every reconcile: the manager
// compares with what it runs and only re-asserts the routing for an unchanged spec (repairing a vanished device).
func (a *Agent) syncWarp(ctx context.Context, next *model) {
	if a.cfg.Warp == nil {
		if next.warp != nil && !a.warpNoted {
			a.warpNoted = true
			a.log.Warn("the panel sent a WARP configuration, this build has no WARP manager")
		}
		return
	}
	err := a.cfg.Warp.Apply(ctx, next.warp)
	switch {
	case err == nil:
		a.warpErr = ""
	case a.warpErr != shortMsg(err):
		a.warpErr = shortMsg(err)
		a.log.Error("warp configuration not applied", "err", err)
		a.event(pb.Severity_SEVERITY_ERROR, "warp_needs_attention", "", map[string]string{"reason": "apply_failed", "error": a.warpErr})
	}
}

func shortMsg(err error) string {
	s := err.Error()
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// tunnelOf derives what the firewall needs from an enabled inbound that has a Tunnel.
func tunnelOf(spec plugin.InboundSpec) (hostctl.Tunnel, error) {
	t := spec.Tunnel
	if !t.AddrV4.IsValid() || !t.AddrV4.Addr().Is4() {
		return hostctl.Tunnel{}, errors.New("tunnel has no IPv4 address")
	}
	out := hostctl.Tunnel{
		Iface: hostctl.TunnelIface(spec.Listen.Port), Subnet4: t.AddrV4.Masked(), Addr4: t.AddrV4.Addr(),
		UDPPort: spec.Listen.Port, ViaWarp: spec.Egress == egressWarp,
	}
	if t.AddrV6.IsValid() {
		out.Subnet6, out.Addr6 = t.AddrV6.Masked(), t.AddrV6.Addr()
	}
	return out, hostctl.ValidateTunnel(out)
}

// syncTunnels installs the firewall of the tunnel interfaces and the WARP routes of their client subnets. It runs
// BEFORE the engines are applied: an interface must never exist for a moment without its isolation rules, and an
// inbound whose egress is WARP must never serve while its subnet is not routed into the WARP table (its clients would
// leave directly). When a step fails, the inbounds that depend on it are blocked for this reconcile: they are not
// started (or are removed) and say why in InboundResult.error, which is what "fail closed" means for them.
func (a *Agent) syncTunnels(ctx context.Context, next *model) {
	a.blocked = map[string]string{}
	var ts []hostctl.Tunnel
	var warpSubnets []netip.Prefix
	warpOf := map[string]bool{}
	for _, id := range next.ids() {
		spec := next.inbounds[id].spec
		if !spec.Enabled {
			continue
		}
		if spec.Egress == egressWarp && !a.warpConfigured() {
			a.blocked[id] = errWarpNotConfigured
			continue
		}
		if spec.Tunnel.IsZero() {
			continue
		}
		t, err := tunnelOf(spec)
		if err != nil {
			continue // the engine reports the bad spec itself
		}
		t.RejectV6 = a.settings.Load().GetClientIpv6Disabled() // "IPv6 for clients" off (renders only for a direct tunnel with IPv6)
		// Two tunnels may not share an interface or overlap in client subnets (the firewall could not tell their clients
		// apart, and one would reach the other). The panel prevents it; if it happens anyway only the later inbound (ids
		// are visited in order) is blocked, not every tunnel of the node.
		if other := overlapping(ts, t); other != "" {
			a.blocked[id] = "tunnel overlaps " + other + ": client subnets and interfaces must be distinct"
			continue
		}
		ts = append(ts, t)
		if t.ViaWarp {
			warpOf[id] = true
			warpSubnets = append(warpSubnets, t.Subnet4)
			if t.Subnet6.IsValid() {
				warpSubnets = append(warpSubnets, t.Subnet6)
			}
		}
	}

	if a.cfg.Warp != nil && (len(warpSubnets) > 0 || a.warpRouted) {
		if err := a.cfg.Warp.SetRoutedSubnets(ctx, warpSubnets); err != nil {
			msg := "warp routing: " + shortMsg(err)
			a.log.Error("warp routing of the client subnets failed", "err", err)
			for id := range warpOf {
				a.blocked[id] = msg
			}
			if a.warpRouteErr != msg {
				a.event(pb.Severity_SEVERITY_ERROR, "warp_needs_attention", "", map[string]string{"reason": "routing_failed", "error": msg})
			}
			a.warpRouteErr = msg
		} else {
			a.warpRouteErr = ""
			a.warpRouted = len(warpSubnets) > 0
		}
	}

	th, ok := a.host.(hostctl.TunnelHost)
	if !ok {
		a.syncTorrentGuard(ctx, next, nil, false)
		return
	}
	if len(ts) == 0 && !a.tunUsed {
		a.syncTorrentGuard(ctx, next, nil, true)
		return
	}
	if err := th.SetTunnels(ctx, ts); err != nil {
		msg := "tunnel firewall: " + shortMsg(err)
		a.log.Error("tunnel firewall not installed", "err", err)
		for _, t := range ts {
			for _, id := range next.ids() {
				if hostctl.TunnelIface(next.inbounds[id].spec.Listen.Port) == t.Iface && !next.inbounds[id].spec.Tunnel.IsZero() {
					a.blocked[id] = msg
				}
			}
		}
		if a.tunErr != msg {
			a.event(pb.Severity_SEVERITY_ERROR, "tunnel_failed", "", map[string]string{"error": msg})
		}
		a.tunErr = msg
		a.syncTorrentGuard(ctx, next, nil, false)
		return
	}
	a.tunErr = ""
	if h, ok := a.host.(hostctl.V6FallbackHost); ok {
		if mode := h.TunnelV6Fallback(); mode != a.tunV6 {
			previous := a.tunV6
			a.tunV6 = mode
			if mode != "" {
				a.log.Warn("the kernel refused nft reject: IPv6 from the tunnels is not rejected", "fallback", mode)
				a.event(pb.Severity_SEVERITY_WARNING, "tunnel_v6_fallback", "", map[string]string{"mode": mode})
			} else if previous != "" && tunnelRejectRequested(ts) {
				a.event(pb.Severity_SEVERITY_INFO, "tunnel_v6_recovered", "", nil)
			}
		}
	}
	a.tunUsed = len(ts) > 0
	a.syncTorrentGuard(ctx, next, ts, true)
}

func tunnelRejectRequested(ts []hostctl.Tunnel) bool {
	for _, t := range ts {
		if t.RejectV6 && t.Subnet6.IsValid() && !t.ViaWarp {
			return true
		}
	}
	return false
}

// blockedFail is the failure of an inbound that may not run now. A running one is stopped: no engine keeps serving
// when the host side of its path is gone.
func (a *Agent) blockedFail(ctx context.Context, spec plugin.InboundSpec, h *held, why string) error {
	if h != nil && h.res.Error == "" {
		if e := a.engines[spec.Protocol]; e != nil {
			if err := e.Remove(ctx, spec.ID); err != nil {
				a.log.Warn("remove blocked inbound", "inbound", spec.ID, "err", err)
			}
		}
	}
	return errors.New(why)
}

// awgHealth fills InboundHealth.awg from the awg engine and the UDP counters of the tunnel table. It resets the
// engine's unknown_peer_events, so it runs exactly once per batch.
func (a *Agent) awgHealth(ctx context.Context, byInbound map[string]*pb.InboundHealth, off time.Duration) {
	e, ok := a.engines[awg.Protocol].(awg.HealthReporter)
	if !ok {
		return
	}
	rows := e.AwgHealth()
	if len(rows) == 0 {
		return
	}
	var counters map[uint16]uint64
	if th, ok := a.host.(hostctl.TunnelHost); ok {
		c, err := th.TunnelCounters(ctx)
		if err != nil {
			a.log.Debug("tunnel counters", "err", err)
		}
		counters = c
	}
	m := a.snapshotModel()
	for _, r := range rows {
		ih := byInbound[r.InboundID]
		if ih == nil {
			continue
		}
		h := &pb.AwgHealth{
			Backend: r.Backend, BackendVersion: r.BackendVersion, IfaceUp: r.IfaceUp, Peers: r.Peers,
			PeersHandshaken: r.PeersHandshaken, PeersOnline: r.PeersOnline, UnknownPeerEvents: r.UnknownPeerEvents,
		}
		if r.NewestHandshakeUnix > 0 {
			h.NewestHandshakeUnix = time.Unix(r.NewestHandshakeUnix, 0).Add(off).Unix()
		}
		if in := m.inbounds[r.InboundID]; in != nil {
			h.UdpRxPackets = counters[in.spec.Listen.Port]
		}
		ih.Awg = h
	}
}

// l3Health adds the AWG health of the inbounds and the node-level WARP health to a stats batch.
func (a *Agent) l3Health(ctx context.Context, b *pb.StatsBatch, off time.Duration) {
	byID := make(map[string]*pb.InboundHealth, len(b.Health))
	for _, h := range b.Health {
		byID[h.InboundId] = h
	}
	a.awgHealth(ctx, byID, off)
	if a.cfg.Warp != nil {
		if h, ok := a.cfg.Warp.Health(); ok {
			b.Warp = warpHealthToPB(h, off)
		}
	}
}

// backendStatuser is what an awg engine offers the doctor (awg.Engine and the lazy wrapper of cmd/mistgate-node do).
type backendStatuser interface{ BackendStatus() awg.BackendStatus }

// awgStatuser returns the awg engine's status source without asking it anything: asking may make a lazy engine probe for
// its backend, which belongs to the first awg inbound (or the doctor check of a node that has one), not to the startup.
func (a *Agent) awgStatuser() (backendStatuser, bool) {
	s, ok := a.engines[awg.Protocol].(backendStatuser)
	return s, ok
}

// isBackendUnavailable says whether an engine error is the awg "no usable backend" failure.
func isBackendUnavailable(err error) bool {
	return err != nil && strings.Contains(err.Error(), awg.ReasonBackendUnavailable)
}

// overlapping returns the interface of the first accepted tunnel that t clashes with, or "".
func overlapping(accepted []hostctl.Tunnel, t hostctl.Tunnel) string {
	for _, o := range accepted {
		if o.Iface == t.Iface || o.Subnet4.Overlaps(t.Subnet4) || (o.Subnet6.IsValid() && t.Subnet6.IsValid() && o.Subnet6.Overlaps(t.Subnet6)) {
			return o.Iface
		}
	}
	return ""
}
