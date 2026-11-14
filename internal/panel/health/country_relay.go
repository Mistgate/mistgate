package health

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// countryRelayReachability reads the access state once for the evaluation; false when it cannot be read (the caller keeps
// what the alert says now rather than flip it on a failed read).
func (s *Service) countryRelayReachability(ctx context.Context, sn *snapshot, now time.Time) (store.RelayReachability, bool) {
	if len(sn.relayTargets) == 0 {
		return store.RelayReachability{}, true // no relay at all: nothing is reached through one
	}
	in := store.RelayReachabilityInput{NowUnix: now.Unix(), HealthIntervalS: uint32(s.interval(ctx).Seconds())}
	groups, err := s.st.Access().Groups(ctx)
	if err != nil {
		s.log.Warn("health: country relay groups", "err", err)
		return store.RelayReachability{}, false
	}
	for _, g := range groups {
		in.Groups = append(in.Groups, store.RelayReachabilityGroup{ID: g.ID, ProfileIDs: g.ProfileIDs, DisabledNodeIDs: g.DisabledNodeIDs})
	}
	users, err := s.st.Access().RelayReachabilityUsers(ctx)
	if err != nil {
		s.log.Warn("health: country relay users", "err", err)
		return store.RelayReachability{}, false
	}
	in.Users = users
	inbounds, err := s.st.Access().InboundsFull(ctx, "")
	if err != nil {
		s.log.Warn("health: country relay inbounds", "err", err)
		return store.RelayReachability{}, false
	}
	for _, full := range inbounds {
		t := sn.targets[full.Inbound.ID]
		cdn := t != nil && protocols.ServerCDN(t.spec)
		in.Inbounds = append(in.Inbounds, store.RelayReachabilityInbound{ID: full.Inbound.ID, NodeID: full.Inbound.NodeID,
			ProfileID: full.Inbound.ProfileID, Protocol: full.Profile.Protocol, State: full.Inbound.State,
			Enabled: full.Inbound.Enabled, Direct: full.Inbound.DirectInSubscriptions, CDN: cdn})
	}
	for _, target := range sn.relayTargets {
		link := target.relay.link
		check, hasCheck := link.Check()
		monitorAt, monitorFails := int64(0), uint32(0)
		if hasCheck && check.Monitor != nil {
			monitorAt, monitorFails = check.Monitor.AtUnix, check.Monitor.FailStreak
		}
		in.Relays = append(in.Relays, store.RelayReachabilityRelay{ID: link.ID, InboundID: link.InboundID, EntryID: link.ViaNodeID,
			ExitID: link.ExitNodeID, State: link.State, Enabled: link.Enabled, ExitLive: link.Live(),
			AgentSupported: slices.Contains(link.ViaNodeCaps, "relay-forward/1"), Checked: hasCheck && check.At != 0,
			CheckOK: hasCheck && check.At != 0 && check.OK, EntryUsable: link.ViaNodeState == "active",
			MonitorAtUnix: monitorAt, MonitorFails: monitorFails})
	}
	return store.NewRelayReachability(in), true
}

func countryBlockedDirectPaths(inbounds []store.RelayReachabilityInbound, nodeID, verdict string) map[string]bool {
	blocked := make(map[string]bool)
	for _, in := range inbounds {
		if in.NodeID != nodeID {
			continue
		}
		blocked[in.ID] = verdict == verdictBlocked || verdict == verdictTCPBlocked && store.RelayPathNetwork(in.Protocol) == "tcp"
	}
	return blocked
}

func countryRelayCond(c cond) cond {
	c.severity = sevInfo
	c.why = "health.alert.country_blocked.why.reached_through_relays"
	c.params = maps.Clone(c.params)
	c.params["why"] = "reached_through_relays"
	return c
}
