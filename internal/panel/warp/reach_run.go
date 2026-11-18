//go:build !js

package warp

import (
	"context"
	"time"
)

// RunReach is the loop of the VPS edition: the first check a minute after the panel starts, then one every reachEvery. The
// Cloudflare edition runs no loops (see internal/panel/app/background_js.go), so the check does not run there yet.
func (s *Service) RunReach(ctx context.Context) {
	t := time.NewTimer(reachFirst)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.CheckReach(ctx)
		t.Reset(reachEvery)
	}
}
