//go:build !js

package blocklist

import (
	"context"
	"time"
)

// pollEvery is how often the VPS looks whether a check is due. Nothing is downloaded unless one is: that is decided by the
// stored results (a node without one, or one older than six hours), so a restart does not download anything by itself.
const pollEvery = time.Minute

// Run is the background loop of the VPS edition. On the Cloudflare edition the cron calls Tick instead; the Worker has no
// process to keep a loop in.
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
