//go:build !js

package app

import (
	"context"
	"time"
)

// backgroundJobs are the loops the VPS process runs next to the listener (cmd/mistgate starts every one of them).
func backgroundJobs(c Config, s jobServices) []BackgroundJob {
	jobs := mcpBackgroundJobs(s.store, s.log, c.Clock)
	jobs = append(jobs, []BackgroundJob{
		{Name: "fleet", Run: func(ctx context.Context) { _ = s.fleet.Run(ctx) }},
		{Name: "access", Run: s.access.Run},
		{Name: "health", Run: s.health.Run},
		{Name: "blocklist", Run: s.blocklist.Run},
		{Name: "tspu-probe", Run: s.probe.Run},
		{Name: "updates", Run: s.update.Run},
		{Name: "backups", Run: s.backup.Run},
		{Name: "telegram", Run: s.telegram.Run},
		{Name: "provision", Run: func(ctx context.Context) {
			for ctx.Err() == nil {
				if err := s.provision.Run(ctx); err != nil && ctx.Err() == nil {
					s.log.Error("provision worker stopped; retrying", "err", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
			}
		}},
	}...)
	return append(jobs, c.BackgroundStarters...)
}
