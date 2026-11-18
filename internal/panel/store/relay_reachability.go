package store

import (
	"context"
	"slices"
	"strings"
)

// RelayReachabilityInput is the access state needed to decide whether a node is reached only through relays.
type RelayReachabilityInput struct {
	NowUnix         int64
	HealthIntervalS uint32
	Inbounds        []RelayReachabilityInbound
	Groups          []RelayReachabilityGroup
	Users           []RelayReachabilityUser
	Relays          []RelayReachabilityRelay
}

type RelayReachabilityInbound struct {
	ID, NodeID, ProfileID, Protocol, State string
	Enabled, Direct, CDN, DirectBlocked    bool
}

type RelayReachabilityGroup struct {
	ID                          string
	ProfileIDs, DisabledNodeIDs []string
}

// RelayReachabilityUser is a person with the ids of their groups (the main one first, any may be unknown to the snapshot).
type RelayReachabilityUser struct {
	ID                 string
	GroupIDs           []string
	Disabled, AllNodes bool
	NodeIDs            []string
}

type RelayReachabilityRelay struct {
	ID, InboundID, EntryID, ExitID, State string
	Mode, ActiveMode                      string
	Enabled, ExitLive, AgentSupported     bool
	ReverseSupported, Checked, CheckOK    bool
	EntryUsable, BridgeOK, PathOK         bool
	BridgeAtUnix, MonitorAtUnix           int64
	MonitorFails                          uint32
}

// RelayReachability applies the shared relay-only access rule to one snapshot of access state.
type RelayReachability struct {
	Input RelayReachabilityInput

	usersByID       map[string]RelayReachabilityUser
	groupsByID      map[string]RelayReachabilityGroup
	inboundsByID    map[string]RelayReachabilityInbound
	inboundsByNode  map[string][]RelayReachabilityInbound
	relaysByID      map[string]RelayReachabilityRelay
	relaysByInbound map[string][]RelayReachabilityRelay
}

func NewRelayReachability(in RelayReachabilityInput) RelayReachability {
	r := RelayReachability{
		Input:           in,
		usersByID:       make(map[string]RelayReachabilityUser, len(in.Users)),
		groupsByID:      make(map[string]RelayReachabilityGroup, len(in.Groups)),
		inboundsByID:    make(map[string]RelayReachabilityInbound, len(in.Inbounds)),
		inboundsByNode:  make(map[string][]RelayReachabilityInbound),
		relaysByID:      make(map[string]RelayReachabilityRelay, len(in.Relays)),
		relaysByInbound: make(map[string][]RelayReachabilityRelay),
	}
	for _, u := range in.Users {
		r.usersByID[u.ID] = u
	}
	for _, g := range in.Groups {
		r.groupsByID[g.ID] = g
	}
	for _, x := range in.Inbounds {
		r.inboundsByID[x.ID] = x
		r.inboundsByNode[x.NodeID] = append(r.inboundsByNode[x.NodeID], x)
	}
	for _, relay := range in.Relays {
		r.relaysByID[relay.ID] = relay
		r.relaysByInbound[relay.InboundID] = append(r.relaysByInbound[relay.InboundID], relay)
	}
	return r
}

func RelayTargetInbound(protocol string, cdn bool) bool {
	return !cdn
}

func RelayPathNetwork(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "awg", "hysteria2":
		return "udp"
	default:
		return "tcp"
	}
}

func (r RelayReachability) RelayHealthy(id string) bool {
	relay, ok := r.relaysByID[id]
	return ok && relayReachabilityHealthy(relay, r.Input.NowUnix, r.Input.HealthIntervalS)
}

func (r RelayReachability) UserHasHealthyRelay(userID, inboundID string) bool {
	u, ok := r.usersByID[userID]
	if !ok || u.Disabled {
		return false
	}
	gs := r.groupsOf(u)
	x, ok := r.inboundsByID[inboundID]
	if !ok || !RelayTargetInbound(x.Protocol, x.CDN) {
		return false
	}
	for _, relay := range r.relaysByInbound[inboundID] {
		if relay.ExitID != x.NodeID || !relayReachabilityHealthy(relay, r.Input.NowUnix, r.Input.HealthIntervalS) || !relay.EntryUsable ||
			!gs.Gives(x.ProfileID, x.NodeID, relay.EntryID) || !relayUserCanUseNode(u, relay.EntryID) {
			continue
		}
		return true
	}
	return false
}

// groupsOf are the person's groups the snapshot knows, as access groups with the profiles and nodes they hand out.
func (r RelayReachability) groupsOf(u RelayReachabilityUser) AccessGroups {
	var out AccessGroups
	for _, id := range u.GroupIDs {
		if g, ok := r.groupsByID[id]; ok {
			out = append(out, AccessGroup{ID: g.ID, ProfileIDs: g.ProfileIDs, DisabledNodeIDs: g.DisabledNodeIDs})
		}
	}
	return out
}

func (r RelayReachability) UserGiven(userID, inboundID string) bool {
	u, ok := r.usersByID[userID]
	if !ok || u.Disabled {
		return false
	}
	x, ok := r.inboundsByID[inboundID]
	if !ok || !x.Enabled || x.State == "disabled" || !r.groupsOf(u).Gives(x.ProfileID, x.NodeID) || !relayUserCanUseNode(u, x.NodeID) {
		return false
	}
	return x.Direct || x.CDN || r.UserHasHealthyRelay(userID, inboundID)
}

func (r RelayReachability) NodeReachedThroughRelays(nodeID string, directBlocked map[string]bool) bool {
	served := false
	for _, x := range r.inboundsByNode[nodeID] {
		var users []string
		for _, u := range r.Input.Users {
			if r.UserGiven(u.ID, x.ID) {
				users = append(users, u.ID)
			}
		}
		if len(users) == 0 {
			continue
		}
		served = true
		blocked := x.DirectBlocked
		if v, ok := directBlocked[x.ID]; ok {
			blocked = v
		}
		if x.Direct && !blocked {
			return false
		}
		for _, userID := range users {
			if !r.UserHasHealthyRelay(userID, x.ID) {
				return false
			}
		}
	}
	return served
}

func relayReachabilityHealthy(r RelayReachabilityRelay, nowUnix int64, interval uint32) bool {
	if !r.Enabled || r.State != "active" || !r.AgentSupported || !r.ExitLive || r.MonitorFails >= 2 ||
		r.MonitorAtUnix <= 0 || r.MonitorAtUnix > nowUnix {
		return false
	}
	if r.ActiveMode == RelayModeReverse {
		if !r.ReverseSupported || !r.BridgeOK || !r.PathOK || r.BridgeAtUnix <= 0 || r.BridgeAtUnix > nowUnix || nowUnix-r.BridgeAtUnix > 90 {
			return false
		}
	} else if !r.Checked || !r.CheckOK {
		return false
	}
	if interval == 0 {
		interval = 300
	}
	return nowUnix-r.MonitorAtUnix <= int64(interval)*2+60
}

func relayUserCanUseNode(u RelayReachabilityUser, nodeID string) bool {
	return u.AllNodes || slices.Contains(u.NodeIDs, nodeID)
}

// RelayReachabilityUsers reads, for every person, only what the relay rule needs: group, switch-off, node list. One light
// pass instead of the user list's counters, for a check that runs in the health loop.
func (a Access) RelayReachabilityUsers(ctx context.Context) ([]RelayReachabilityUser, error) {
	rows, err := a.s.R.QueryContext(ctx, `SELECT id, group_id, disabled, all_nodes FROM user`)
	if err != nil {
		return nil, err
	}
	var out []RelayReachabilityUser
	idx := map[string]int{}
	for rows.Next() {
		var u RelayReachabilityUser
		var dis, all int
		var groupID string
		if err := rows.Scan(&u.ID, &groupID, &dis, &all); err != nil {
			rows.Close()
			return nil, err
		}
		u.GroupIDs = []string{groupID}
		u.Disabled, u.AllNodes = dis != 0, all != 0
		idx[u.ID] = len(out)
		out = append(out, u)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	xrows, err := a.s.R.QueryContext(ctx, `SELECT user_id, group_id FROM user_extra_group ORDER BY group_id`)
	if err != nil {
		return nil, err
	}
	for xrows.Next() {
		var uid, gid string
		if err := xrows.Scan(&uid, &gid); err != nil {
			xrows.Close()
			return nil, err
		}
		if i, ok := idx[uid]; ok {
			out[i].GroupIDs = append(out[i].GroupIDs, gid)
		}
	}
	xrows.Close()
	if err := xrows.Err(); err != nil {
		return nil, err
	}
	nrows, err := a.s.R.QueryContext(ctx, `SELECT user_id, node_id FROM user_node ORDER BY node_id`)
	if err != nil {
		return nil, err
	}
	defer nrows.Close()
	for nrows.Next() {
		var uid, nid string
		if err := nrows.Scan(&uid, &nid); err != nil {
			return nil, err
		}
		if i, ok := idx[uid]; ok {
			out[i].NodeIDs = append(out[i].NodeIDs, nid)
		}
	}
	return out, nrows.Err()
}
