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

// sweepDebugCaptures prunes the on-demand debug captures of a node and their lines (24 hours). VPS only for the same reason: the
// edition on Cloudflare cannot start a capture, so its cron does not touch the tables.
func (s *Service) sweepDebugCaptures(ctx context.Context, now time.Time) {
	if n, err := s.st.PruneDebugCaptures(ctx, now.Add(-store.DebugCaptureKeep)); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune debug captures", "code", "prune_debug_captures_failed", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned debug captures", "count", n)
	}
}
