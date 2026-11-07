//go:build !js

package health

import (
	"context"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// sweepProbes prunes the on-demand path probes (7 days). It is VPS-only: the Cloudflare edition cannot run a probe, so its cron
// has nothing to prune and makes no extra D1 call for it.
func (s *Service) sweepProbes(ctx context.Context, now time.Time) {
	if n, err := s.st.PrunePathProbes(ctx, now.Add(-time.Duration(store.PathProbeKeepHours)*time.Hour)); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune path probes", "code", "prune_failed", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned path probes", "count", n)
	}
	if _, err := s.st.PrunePathSpeeds(ctx, now.Add(-time.Duration(store.PathSpeedKeepHours)*time.Hour)); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune path speeds", "code", "prune_failed", "err", err)
	}
}
