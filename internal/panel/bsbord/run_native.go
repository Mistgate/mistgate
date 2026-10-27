//go:build !js

package bsbord

import (
	"context"
	"time"
)

// Run is the VPS scheduler. The edge calls Tick from its bounded cron pass.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		s.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
