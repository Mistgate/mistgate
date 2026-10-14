//go:build !js

package countryprobe

import (
	"context"
	"time"
)

// pollEvery is how often the VPS looks whether a run is due. A run is sent only if one is: that is decided by the stored rows
// and the last run's time, so a restart does not fire one by itself.
const pollEvery = time.Minute

// Run is the background loop of the VPS edition. The Cloudflare edition has no such source, so it has no loop either.
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
