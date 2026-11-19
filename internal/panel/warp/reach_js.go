//go:build js

package warp

import (
	"context"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The Cloudflare edition runs no WARP reach check (reach.go, reach_rpc.go and reach_run.go are the VPS edition's): GetWarp says the
// check is off and carries no result.
func (s *Service) reachView(context.Context, store.NodeRow, store.WarpAccountRow) (*adminv1.WarpReachView, string) {
	return nil, "disabled"
}

// reachCheck is the state of the check on a node (reach_now.go): none here, and a request for one is ignored.
func (s *Service) reachCheck(context.Context, store.NodeRow, store.WarpAccountRow, bool) *adminv1.WarpReachCheck {
	return nil
}
