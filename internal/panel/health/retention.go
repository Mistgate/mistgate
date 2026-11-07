package health

import (
	"context"
	"time"

	"github.com/mistgate/mistgate/internal/panel/retention"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Retention (see also the 00002 schema comments): raw check rounds live 25 h after the finished days
// were rolled up, daily aggregates 90 days, resolved alerts 90 days, events 90 days (info) or 400 days
// (warning, error), audit rows 400 days (audit_ts serves the scan), the journal 7 days, host load 90 days, the hourly traffic
// tables 400 days. These are the defaults: the owner's retention.* settings (internal/panel/retention) replace them, read in one
// batched call per sweep. The sweep runs at start, after a minute, and hourly. traffic_bucket is pruned through
// traffic_bucket_hour (migration 00106), node_traffic_hour through its own hour index.

const (
	retentionEvery = time.Hour
	eventBatch     = 2000
	deviceKeep     = 90 * 24 * time.Hour                            // how long a revoked device (not a blocked one) stays as a row
	linkKeep       = time.Duration(store.LinkKeepHours) * time.Hour // connection counters and their longest RPC window
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
	pol := s.retentionPolicy(ctx)
	if err := s.st.RollupDaily(ctx, now); err != nil && ctx.Err() == nil {
		s.log.Warn("health: daily rollup", "err", err)
	}
	if n, err := s.st.PruneHealth(ctx, now, pol.Duration("check_samples_hours"), pol.Duration("check_daily_days"), pol.Duration("alerts_days")); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned rows", "count", n)
	}
	if n, err := s.st.PruneEvents(ctx, now, pol.Duration("events_info_days"), pol.Duration("events_warn_days"), eventBatch); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune events", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned events", "count", n)
	}
	if n, err := s.st.PruneAudit(ctx, now.Add(-pol.Duration("audit_days")), eventBatch); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune audit", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned audit rows", "count", n)
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
	if n, err := s.st.PruneExtChecks(ctx, now.Add(-countryKeep)); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune external checks", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned external checks", "count", n)
	}
	if n, err := s.st.PruneClientProbeReports(ctx, now.Add(-countryKeep)); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune kl!ck reports", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned kl!ck reports", "count", n)
	}
	// The connection counters (link_hour) are hour buckets; retain them for the maximum RPC window.
	if n, err := s.st.PruneLinkHours(ctx, now.Add(-linkKeep)); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune connection counters", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned connection counters", "count", n)
	}
	if n, err := s.st.PruneNodeJournal(ctx, now.Add(-pol.Duration("journal_days"))); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune node journal", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned node journal", "count", n)
	}
	// The hourly host load (node_host_hour) is kept 90 days by default.
	if n, err := s.st.PruneNodeHostHours(ctx, now.Add(-pol.Duration("host_history_days"))); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune host load", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned host load hours", "count", n)
	}
	s.sweepProbes(ctx, now) // path probes, 7 days; VPS only
	// The hourly traffic tables: 400 days by default.
	if n, err := s.st.PruneTrafficBuckets(ctx, now.Add(-pol.Duration("traffic_bucket_days"))); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune traffic buckets", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned traffic buckets", "count", n)
	}
	if n, err := s.st.PruneNodeTrafficHours(ctx, now.Add(-pol.Duration("node_traffic_hour_days"))); err != nil && ctx.Err() == nil {
		s.log.Warn("health: prune node traffic", "err", err)
	} else if n > 0 {
		s.log.Info("health: pruned node traffic hours", "count", n)
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

// retentionPolicy reads the owner's retention settings in one call. A failed read keeps the defaults: the sweep still runs.
func (s *Service) retentionPolicy(ctx context.Context) retention.Policy {
	stored, err := s.st.SettingValues(ctx, retention.Keys())
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("health: read retention settings", "code", "retention_settings_unreadable", "err", err)
		}
		return retention.Default()
	}
	return retention.Parse(stored)
}
