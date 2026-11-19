//go:build !js

package globalping

import (
	"context"
	"time"
)

// Remaining is the tests the account may still create in the current window of its hourly limit, and how long until the window
// resets. GET /v1/limits is free and counts for nothing; the credits that pay for tests above the limit are never counted
// here, so a round out of turn that stays within this number never touches them. It is the country check's budget for a round
// asked by hand (health.CountryBudget).
func (s *source) Remaining(ctx context.Context) (int, time.Duration, error) {
	lim, err := s.c.limits(ctx)
	if err != nil {
		return 0, 0, err
	}
	c := lim.RateLimit.Measurements.Create
	return int(c.Remaining), time.Duration(c.Reset) * time.Second, nil
}
