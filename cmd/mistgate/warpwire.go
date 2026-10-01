package main

import (
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/warp"
)

// warpForFleet is the WARP module as the fleet module wants it (fleet.Warp): the module has everything except the node
// badge, which it offers as a function of the account row, not as a method.
type warpForFleet struct{ *warp.Service }

var _ fleet.Warp = warpForFleet{}

// Summary is Node.warp of a node card.
func (warpForFleet) Summary(a *store.WarpAccountRow, online bool, now time.Time) *adminv1.WarpSummary {
	return warp.Summary(a, online, now)
}
