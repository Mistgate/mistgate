package health

import "context"

// CountryNowRun is the work of a country source for a round out of turn, run in the background once the source has taken the
// request. It returns the state the source ends in ("done", "queued": asked, the answer comes by itself later, or "failed") and
// the reason of a failure.
type CountryNowRun func(ctx context.Context) (state, reason string)

// CountryNowSource is a source of the country check that can be asked for a round of one node out of turn (country_now.go). Plan
// is quick and runs inside the request: it says why the source will not run now (the reason words of adminv1.CountryNowSource) or
// returns the work to do. A source that keeps no other state has nothing else to do; a plan that takes a lock for its run
// releases it when the run ends, and when it returns a reason, at once.
type CountryNowSource struct {
	Source string
	Plan   func(ctx context.Context, nodeID string) (run CountryNowRun, skip string)
}
