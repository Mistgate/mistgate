//go:build !js

package cheburcheck

import (
	"context"
	"time"
)

const pollEvery = 30 * time.Second

// Run is the VPS polling loop. The edge build calls Tick from its cron job instead.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("cheburcheck: tick", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
