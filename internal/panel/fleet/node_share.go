package fleet

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/nodeshare"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// idleAfter is how long a node that nobody receives may stay so before the nodes list labels it (Node.sharing.idle).
const idleAfter = 24 * time.Hour

// shareOf reads how one node is handed out; the zero value (every group) when the read fails, as the nodes list never fails on it.
func (f *Fleet) shareOf(ctx context.Context, nodeID string) store.NodeShare {
	shares, err := f.st.NodeShares(ctx)
	if err != nil {
		f.log.Warn("fleet: cannot read how a node is handed out", "code", "node_share_read_failed", "err", err)
	}
	return shares[nodeID]
}

// shareMsg is Node.sharing. enabled is the node's enabled inbounds: a node with a server that runs, handed to nobody for more than
// a day, is idle (a label for the list, never an alert).
func shareMsg(n store.NodeRow, enabled map[string]string, share store.NodeShare, now time.Time) *adminv1.NodeShareSummary {
	mode := share.Mode()
	return &adminv1.NodeShareSummary{
		Mode: nodeshare.Mode(mode), GroupsReceiving: uint32(share.Receiving), GroupsTotal: uint32(share.Groups),
		ChangedUnix: fleetUnix(share.ChangedAt),
		Idle:        mode == store.ShareNone && n.State == "active" && len(enabled) > 0 && !share.ChangedAt.IsZero() && now.Sub(share.ChangedAt) > idleAfter,
	}
}

// checkShareGroups refuses a share that names a group the panel does not have (NOT_FOUND).
func (f *Fleet) checkShareGroups(ctx context.Context, c store.NodeShareChoice) error {
	if len(c.GroupIDs) == 0 {
		return nil
	}
	groups, err := f.st.Access().Groups(ctx)
	if err != nil {
		return internalErr(f.log.Error, "list groups", err)
	}
	known := map[string]bool{}
	for _, g := range groups {
		known[g.ID] = true
	}
	for _, id := range c.GroupIDs {
		if !known[id] {
			return connect.NewError(connect.CodeNotFound, errors.New("group not found"))
		}
	}
	return nil
}

// auditShare adds to an audit row how the node ended up handed out: its mode and, for LISTED, the groups that receive it.
func (f *Fleet) auditShare(ctx context.Context, params map[string]string, nodeID string, asked store.NodeShareChoice) {
	shares, err := f.st.NodeShares(ctx)
	if err != nil {
		return
	}
	params["sharing"] = shares[nodeID].Mode()
	if len(asked.GroupIDs) > 0 {
		params["groups"] = strings.Join(asked.GroupIDs, ",")
	}
}
