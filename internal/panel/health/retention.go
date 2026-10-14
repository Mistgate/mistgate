package health

import (
	"context"
	"time"
)

// Retention (see also the 00002 schema comments): raw check rounds live 25 h after the finished days
// were rolled up, daily aggregates 90 days, resolved alerts 90 days, events 90 days (info) or 400 days
// (warning, error). The sweep runs at start, after a minute, and hourly.
// traffic_bucket and node_traffic_hour (00002: 400 days) are not pruned here; their tables have no
// time index, so a DELETE would scan them while holding the only writer. Add an index in a migration first.

const (
	retentionEvery = time.Hour
	eventBatch     = 2000
	deviceKeep     = 90 * 24 * time.Hour // how long a revoked device (not a blocked one) stays as a row
)

func (s *Service) runRetention(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Minute):
	}
	t := time.NewTicker(retentionEvery)
	defer t.Stop()
	for {
		s.sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Retention runs one retention pass for scheduled callers.
func (s *Service) Retention(ctx context.Context) { s.sweep(ctx) }

// sweep runs every retention job once; a failure of one does not stop the others.
func (s *Service) sweep(ctx context.Context) {
	now := s.now()
	if err := s.st.RollupDaily(ctx, now); err != nil && ctx.Err() == nil {
		s.log.Warn("health: daily rollup", "err", err)
	}
	if n, err := s.st.PruneHealth(ctx, now, sampleKeep, dailyKeep, alertKeep); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned rows", "count", n)
	}
	if n, err := s.st.PruneEvents(ctx, now, infoEvents, warnEvents, eventBatch); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune events", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned events", "count", n)
	}
	if n, err := s.st.PruneCountryChecks(ctx, now.Add(-countryKeep)); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune country checks", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned country checks", "count", n)
	}
	if n, err := s.st.PruneBlocklistChecks(ctx, now.Add(-countryKeep)); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune block-list checks", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned block-list checks", "count", n)
	}
	if n, err := s.st.PruneTSPUChecks(ctx, now.Add(-countryKeep)); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune TSPU checks", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned TSPU checks", "count", n)
	}
	// A device revoked long ago is only a row now (stats of its past traffic never needed it); a blocked one stays.
	if n, err := s.st.PruneRevokedDevices(ctx, now.Add(-deviceKeep), eventBatch); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune devices", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned revoked devices", "count", n)
	}
	// the in-memory map of the last fixes only matters for five minutes
	s.fixMu.Lock()
	for k, at := range s.recentFix {
		if now.Sub(at) > fixResolveFor {
			delete(s.recentFix, k)
		}
	}
	s.fixMu.Unlock()
}
