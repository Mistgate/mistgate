package fleet

import (
	"cmp"
	"context"
	"slices"

	"github.com/mistgate/mistgate/internal/agentcap"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/tcpcheck"
)

// acquireRelayPortChecks takes both ends in node order so checks for relays that share an entry or an exit cannot deadlock.
func (f *Fleet) acquireRelayPortChecks(ctx context.Context, nodeIDs ...string) (func(), error) {
	nodeIDs = slices.Compact(slices.Sorted(slices.Values(nodeIDs)))
	locks := make([]*portCheckLock, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		lock, _, _, err := f.acquirePortCheck(ctx, id)
		if err != nil {
			for i := len(locks) - 1; i >= 0; i-- {
				<-locks[i].gate
			}
			return nil, err
		}
		locks = append(locks, lock)
	}
	return func() {
		for i := len(locks) - 1; i >= 0; i-- {
			<-locks[i].gate
		}
	}, nil
}

// bridgeSiteName is the name the self-signed certificate of the agent's own bridge site carries (internal/node/agent/bridge.go
// bridgeSiteCertName); a probe or a dial that has the site's pin does not depend on it.
const bridgeSiteName = "localhost"

// reverseReach asks the exit's tcpcheck/1 agent whether the selected bridge endpoint answers with the entry's TLS identity.
func (r relayRun) reverseReach(ctx context.Context) store.RelayStage {
	stage := r.reverseReachAt(ctx)
	stage.Port = r.entry.BridgeEndpointPort() // the probe says something about this port and no other
	return stage
}

// reverseStage is r.stage with the family of the entry address the reverse probes were sent to.
func (r relayRun) reverseStage(status, code, sender string) store.RelayStage {
	st := r.stage(status, code, sender)
	st.Family = r.entryFamily
	return st
}

func (r relayRun) reverseReachAt(ctx context.Context) store.RelayStage {
	if !r.entryOK {
		return r.reverseStage("fail", "no_ipv6_address", "")
	}
	target, ok, err := r.reverseSiteTarget(ctx)
	if err != nil {
		r.f.log.Warn("relay reverse reach: read entry site", "code", "store_failed", "relay", r.link.ID, "err", err)
		return r.reverseStage("fail", "failed", "")
	}
	if !ok {
		return r.reverseStage("skipped", "entry_site_unavailable", "")
	}
	live := r.live[r.exit.ID]
	if !r.f.reachable(live, r.exit.ID) {
		return r.reverseStage("fail", "exit_offline", "")
	}
	if !slices.Contains(live.AgentCaps, agentcap.TCPCheck) {
		return r.reverseStage("skipped", "unsupported", r.exit.ID)
	}
	sender := portSender{node: r.exit, live: live}
	stage, _ := r.tcpStage(ctx, r.entryHost, sender, target)
	if stage.Code == "refused" {
		return r.reverseStage("skipped", "entry_site_unavailable", r.exit.ID)
	}
	stage.Family = r.entryFamily
	return stage
}

func (r relayRun) reverseSiteTarget(ctx context.Context) (tcpTarget, bool, error) {
	if r.entry.BridgePort != 0 {
		pin := r.link.BridgeSitePin(r.entry.BridgePort, r.now)
		if pin == "" {
			return tcpTarget{}, false, nil
		}
		// The probe of a TLS site always names a server (tcpcheck.Validate refuses a probe without one, which made every probe of a
		// bridge site on a port of its own fail as "failed"); the site is told by its pin, the name is only what the handshake says.
		return tcpTarget{Port: r.entry.BridgePort, Role: tcpRoleSite, Kind: tcpcheck.KindTLS, SNI: bridgeSiteName, Pin: pin}, true, nil
	}
	specs, pins, err := r.f.tcpSpecsOf(ctx, r.entry)
	if err != nil {
		return tcpTarget{}, false, err
	}
	for _, target := range tcpTargets(specs, pins) {
		if target.Port != 443 {
			continue
		}
		if target.Role == tcpRoleSite {
			return target, true, nil
		}
		if target.Role != tcpRoleVLESS {
			continue
		}
		for _, spec := range specs {
			if spec.Profile != target.Profile || spec.Spec.Protocol != "vless" || spec.Spec.Listen.Network != "tcp" || cmp.Or(spec.PublicPort, spec.Spec.Listen.Port) != 443 {
				continue
			}
			if _, ownSite := vlessProbeName(spec.Spec); ownSite {
				return target, true, nil
			}
		}
	}
	return tcpTarget{}, false, nil
}
