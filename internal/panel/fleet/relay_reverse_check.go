package fleet

import (
	"context"
	"slices"

	"github.com/mistgate/mistgate/internal/panel/store"
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

// reverseReach asks the exit's tcpcheck/1 agent whether the entry's own HTTPS site answers on 443.
func (r relayRun) reverseReach(ctx context.Context) store.RelayStage {
	target, ok, err := r.reverseSiteTarget(ctx)
	if err != nil {
		r.f.log.Warn("relay reverse reach: read entry site", "code", "store_failed", "relay", r.link.ID, "err", err)
		return r.stage("fail", "failed", "")
	}
	if !ok {
		return r.stage("skipped", "entry_site_unavailable", "")
	}
	live := r.live[r.exit.ID]
	if !live.Connected || (r.f.cfg.Remote == nil && r.f.session(r.exit.ID) == nil) {
		return r.stage("fail", "exit_offline", "")
	}
	if !slices.Contains(live.AgentCaps, capTCPCheck) {
		return r.stage("skipped", "unsupported", r.exit.ID)
	}
	sender := portSender{node: r.exit, live: live}
	stage, _ := r.tcpStage(ctx, r.entry.Address, sender, target)
	if stage.Code == "refused" {
		return r.stage("skipped", "entry_site_unavailable", r.exit.ID)
	}
	return stage
}

func (r relayRun) reverseSiteTarget(ctx context.Context) (tcpTarget, bool, error) {
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
			if spec.Profile != target.Profile || spec.Spec.Protocol != "vless" || spec.Spec.Listen.Network != "tcp" || spec.Spec.Listen.Port != 443 {
				continue
			}
			if _, ownSite := vlessProbeName(spec.Spec); ownSite {
				return target, true, nil
			}
		}
	}
	return tcpTarget{}, false, nil
}
