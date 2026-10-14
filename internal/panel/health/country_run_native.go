//go:build !js

package health

import (
	"context"
	"time"
)

// countryPollInterval is how often the scheduler looks whether a country round is due; a round itself is up to a minute.
const countryPollInterval = 30 * time.Second

func (s *Service) runCountryChecks(ctx context.Context) {
	if s.cfg.Country == nil {
		return
	}
	t := time.NewTicker(countryPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.CountryTick(ctx) {
				s.evaluateSoon()
			}
		}
	}
}
