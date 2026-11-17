//go:build !js

package store

import (
	"context"
	"time"
)

// The WARP reach check is the VPS edition's (internal/panel/warp reach.go): the Cloudflare edition keeps only the column.

// WarpReachItem is one site's answer of a WARP reach run (agent.proto "WARP REACH"): the verdict (ok, blocked, unreachable or
// error), the HTTP status of the page it is about (0 = none) and the time the request took.
type WarpReachItem struct {
	Verdict            string
	HTTPStatus, Millis uint32
}

// SetWarpReach stores the node's latest WARP reach result (JSON, "" is not allowed). The account's updated_at is not touched:
// a result is not a change of the account. ErrNotFound when the node has no account.
func (s *Store) SetWarpReach(ctx context.Context, nodeID, reachJSON string, at time.Time) error {
	return s.warpExec(ctx, `UPDATE warp_account SET reach_json = ?, reach_at = ? WHERE node_id = ?`, reachJSON, unix(at), nodeID)
}
