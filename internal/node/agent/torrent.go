package agent

import (
	"context"
	"encoding/json"
	"net/netip"
	"sort"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/awg"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/hostctl"
)

type torrentClient struct {
	inboundID string
	userID    string
	ambiguous bool
}

func (a *Agent) syncTorrentGuardApplied(ctx context.Context, next *model, results []*pb.InboundResult) {
	running := make(map[string]bool, len(results))
	for _, result := range results {
		if result != nil && result.State == pb.InboundRunState_INBOUND_RUN_STATE_RUNNING {
			running[result.InboundId] = true
		}
	}
	var accepted []hostctl.Tunnel
	for _, id := range next.ids() {
		in := next.inbounds[id]
		if in == nil || !running[id] || !in.spec.Enabled || in.spec.Protocol != awg.Protocol || in.spec.Tunnel.IsZero() {
			continue
		}
		if tunnel, err := tunnelOf(in.spec); err == nil {
			accepted = append(accepted, tunnel)
		}
	}
	a.syncTorrentGuard(ctx, next, accepted, true)
}

// syncTorrentGuard installs the AWG packet queue after its tunnel interfaces have been reconciled. It only
// passes those exact active interfaces to the host runtime; Hysteria2 is inspected inside its own engine.
func (a *Agent) syncTorrentGuard(ctx context.Context, next *model, accepted []hostctl.Tunnel, tunnelsReady bool) {
	enabled := a.settings.Load().GetTorrentBlockerEnabled()
	guard, ok := a.host.(hostctl.TorrentGuardHost)
	if !ok {
		if enabled && a.torrentGuardErr == "" {
			a.torrentGuardErr = "torrent guard: this build does not provide the host packet queue"
			a.event(pb.Severity_SEVERITY_WARNING, "torrent_guard_degraded", "", map[string]string{"error": a.torrentGuardErr})
		}
		return
	}
	if !enabled && !a.torrentGuardActive && a.torrentGuardErr == "" {
		return
	}

	activeByIface := make(map[string]string, len(accepted))
	if enabled && tunnelsReady {
		for _, id := range next.ids() {
			in := next.inbounds[id]
			if in == nil || !in.spec.Enabled || in.spec.Protocol != awg.Protocol || in.spec.Tunnel.IsZero() {
				continue
			}
			iface := hostctl.TunnelIface(in.spec.Listen.Port)
			for _, tunnel := range accepted {
				if tunnel.Iface == iface {
					activeByIface[iface] = id
					break
				}
			}
		}
	}
	ifaces := make([]string, 0, len(activeByIface))
	for iface := range activeByIface {
		ifaces = append(ifaces, iface)
	}
	sort.Strings(ifaces)

	clients := make(map[netip.Addr]torrentClient)
	if len(ifaces) > 0 {
		for _, id := range next.ids() {
			in := next.inbounds[id]
			if in == nil || activeByIface[hostctl.TunnelIface(in.spec.Listen.Port)] != id {
				continue
			}
			for _, cred := range in.creds {
				var data struct {
					AllowedIPs []string `json:"allowed_ips"`
				}
				if err := json.Unmarshal(cred.Data, &data); err != nil {
					continue
				}
				for _, raw := range data.AllowedIPs {
					prefix, err := netip.ParsePrefix(raw)
					if err != nil || prefix.Bits() != prefix.Addr().BitLen() {
						continue // only exact client host addresses can identify an account
					}
					ip := prefix.Addr().Unmap()
					owner := torrentClient{inboundID: id, userID: cred.UserID}
					if old, exists := clients[ip]; exists && (old.inboundID != id || old.userID != owner.userID) {
						old.ambiguous = true
						clients[ip] = old
						continue
					}
					clients[ip] = owner
				}
			}
		}
	}

	wantActive := enabled && len(ifaces) > 0
	if !wantActive && !a.torrentGuardActive && a.torrentGuardErr == "" {
		return
	}
	if err := guard.SetTorrentGuard(ctx, ifaces, func(d hostctl.TorrentDetection) {
		inboundID, ok := activeByIface[d.TunnelIface]
		if !ok {
			return
		}
		params := map[string]string{"protocol": d.L4Protocol, "torrent_protocol": string(d.Signature)}
		// The client's tunnel address only finds the user; neither it nor the destination leaves the node.
		if owner, found := clients[d.TunnelIP.Unmap()]; found && owner.inboundID == inboundID && !owner.ambiguous && owner.userID != "" {
			params["user_id"] = owner.userID
		}
		a.engineEvent(engine.Event{Code: "torrent_attempt", InboundID: inboundID, Warning: true, Params: params})
	}); err != nil {
		msg := "torrent guard: " + shortMsg(err)
		if a.torrentGuardErr != msg {
			a.event(pb.Severity_SEVERITY_WARNING, "torrent_guard_degraded", "", map[string]string{"error": msg})
		}
		a.torrentGuardErr = msg
		a.torrentGuardActive = false
		return
	}
	a.torrentGuardErr = ""
	a.torrentGuardActive = wantActive
}
