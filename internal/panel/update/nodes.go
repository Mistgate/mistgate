package update

import (
	"context"
	"crypto/ed25519"
	"slices"
	"sort"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Capabilities of an agent that matter here (agent.proto "UPDATE").
const (
	capUpdate = "update/1"
	capGuard  = "update-guard/1"
)

// refBuilt is the build a node is compared with: the trusted bundle's, else this panel's own, so the page is useful
// before the first bundle exists.
func (s *Service) refBuilt(b *bundleState) int64 {
	if b != nil && b.trusted {
		return b.manifest.Built
	}
	return s.cfg.PanelBuilt
}

// nodeState picks the first state whose condition applies. updating: the node has a SENT or GATING step.
func nodeState(n store.NodeRow, connected, updating bool, ref int64) adminv1.NodeUpdateState {
	lu, hasLast := currentLastUpdate(n)
	switch {
	case updating:
		return adminv1.NodeUpdateState_NODE_UPDATE_STATE_UPDATING
	case !connected:
		return adminv1.NodeUpdateState_NODE_UPDATE_STATE_OFFLINE
	case n.AgentBuilt >= ref:
		return adminv1.NodeUpdateState_NODE_UPDATE_STATE_UP_TO_DATE
	case !slices.Contains(n.AgentCaps, capUpdate):
		return adminv1.NodeUpdateState_NODE_UPDATE_STATE_UNSUPPORTED
	case hasLast && lu.Outcome == "rolled_back":
		return adminv1.NodeUpdateState_NODE_UPDATE_STATE_ROLLED_BACK
	case hasLast && lu.Outcome == "failed":
		return adminv1.NodeUpdateState_NODE_UPDATE_STATE_FAILED
	}
	return adminv1.NodeUpdateState_NODE_UPDATE_STATE_OUTDATED
}

// currentLastUpdate is the node's last_update while it still describes the node. A rolled_back or failed record left the
// node on FromBuilt; a node that runs another build has been updated (or rolled back) since, and the record is history
// (Hello carries the newer outcome only at the next connect, so right after a good update the old record is stale).
func currentLastUpdate(n store.NodeRow) (store.LastUpdateRow, bool) {
	lu, ok := n.LastUpdate()
	if ok && lu.Outcome != "ok" && lu.FromBuilt != 0 && lu.FromBuilt != n.AgentBuilt {
		return store.LastUpdateRow{}, false
	}
	return lu, ok
}

// nodeView is one node as the page and the rollout see it.
type nodeView struct {
	row       store.NodeRow
	connected bool
	state     adminv1.NodeUpdateState
	inbounds  int    // enabled inbounds
	online    int    // distinct users online now
	arch      string // GOARCH from the node's facts; read only for a node that must be updated by hand
}

// nodes lists the active nodes with their update state. steps are those of the active rollout (nil = none).
func (s *Service) nodes(ctx context.Context, b *bundleState, steps []store.StepRow) ([]nodeView, error) {
	rows, err := s.st.Nodes(ctx, false)
	if err != nil {
		return nil, err
	}
	ref := s.refBuilt(b)
	busy := updatingSet(steps)
	online := s.fl.OnlineUsersByNode()
	var out []nodeView
	for _, n := range rows {
		if n.State != "active" { // pending: no agent yet
			continue
		}
		connected, _, _ := s.fl.Live(n.ID)
		in, err := s.st.FleetInbounds(ctx, n.ID, true)
		if err != nil {
			return nil, err
		}
		v := nodeView{row: n, connected: connected, state: nodeState(n, connected, busy[n.ID], ref), inbounds: len(in), online: online[n.ID]}
		if v.state == adminv1.NodeUpdateState_NODE_UPDATE_STATE_UNSUPPORTED { // the page builds its manual commands from it
			f, err := s.st.NodeFacts(ctx, n.ID)
			if err != nil {
				return nil, err
			}
			v.arch = f.Arch
		}
		out = append(out, v)
	}
	return out, nil
}

// view is the admin message of a node.
func (v nodeView) proto() *adminv1.NodeUpdate {
	m := &adminv1.NodeUpdate{
		NodeId: v.row.ID, Name: v.row.Name, Version: v.row.AgentVersion, Built: v.row.AgentBuilt,
		SupportsUpdate: slices.Contains(v.row.AgentCaps, capUpdate), CrashGuard: slices.Contains(v.row.AgentCaps, capGuard),
		State: v.state, Inbounds: uint32(v.inbounds), OnlineUsers: uint32(v.online), Address: v.row.Address, Arch: v.arch,
	}
	if lu, ok := currentLastUpdate(v.row); ok {
		m.LastUpdate = &adminv1.LastUpdate{Outcome: lu.Outcome, FromVersion: lu.FromVersion, FromBuilt: lu.FromBuilt,
			ToVersion: lu.ToVersion, ToBuilt: lu.ToBuilt, Reason: lu.Reason, AtUnix: lu.AtUnix}
	}
	return m
}

// problemRank orders the page: problems first (FAILED, ROLLED_BACK, UPDATING, OUTDATED), then the rest by name.
func problemRank(s adminv1.NodeUpdateState) int {
	switch s {
	case adminv1.NodeUpdateState_NODE_UPDATE_STATE_FAILED:
		return 0
	case adminv1.NodeUpdateState_NODE_UPDATE_STATE_ROLLED_BACK:
		return 1
	case adminv1.NodeUpdateState_NODE_UPDATE_STATE_UPDATING:
		return 2
	case adminv1.NodeUpdateState_NODE_UPDATE_STATE_OUTDATED:
		return 3
	}
	return 4
}

func sortNodeViews(v []nodeView) {
	sort.SliceStable(v, func(i, j int) bool {
		ri, rj := problemRank(v[i].state), problemRank(v[j].state)
		if ri != rj {
			return ri < rj
		}
		return lowerLess(v[i].row.Name, v[j].row.Name)
	})
}

// fingerprint is buildinfo.KeyFingerprint: what the owner compares with the one `mistgate release keygen` printed.
func fingerprint(k ed25519.PublicKey) string { return buildinfo.KeyFingerprint(k) }
