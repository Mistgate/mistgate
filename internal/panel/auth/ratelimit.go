package auth

import (
	"context"
	"time"

	"github.com/mistgate/mistgate/internal/panel/securitylimit"
)

// limiter keeps the authentication package's test seam while delegating state to
// the shared backend. Production parameters remain at the auth call site.
type limiter struct {
	backend securitylimit.Limiter
	burst   float64
	refill  time.Duration
	now     func() time.Time
}

func newLimiter(burst int, refill time.Duration) *limiter {
	return &limiter{backend: securitylimit.NewMemory(), burst: float64(burst), refill: refill, now: time.Now}
}

func (l *limiter) Take(ctx context.Context, name, key string, now time.Time, burst float64, refill time.Duration, cost float64) (securitylimit.Decision, error) {
	if name == "auth" {
		burst, refill = l.burst, l.refill
	}
	return l.backend.Take(ctx, name, key, now, burst, refill, cost)
}

func (l *limiter) CheckWindow(ctx context.Context, name, key string, now time.Time, limit int, window time.Duration, maxKeys int, touch bool) (securitylimit.Decision, error) {
	return l.backend.CheckWindow(ctx, name, key, now, limit, window, maxKeys, touch)
}

func (l *limiter) RecordWindow(ctx context.Context, name, key string, now time.Time, limit int, window time.Duration, maxKeys int) (securitylimit.Decision, error) {
	return l.backend.RecordWindow(ctx, name, key, now, limit, window, maxKeys)
}

func (l *limiter) FailWindow(ctx context.Context, name, key string, now time.Time, window time.Duration, maxKeys int) error {
	return l.backend.FailWindow(ctx, name, key, now, window, maxKeys)
}

func (l *limiter) Reset(ctx context.Context, name, key string) error {
	return l.backend.Reset(ctx, name, key)
}

func (l *limiter) allow(key string) bool {
	d, _ := l.Take(context.Background(), "auth", key, l.now(), l.burst, l.refill, 1)
	return d.Allowed
}

func (l *limiter) allowRate(key string, burst float64, refill time.Duration) (bool, time.Duration) {
	d, _ := l.backend.Take(context.Background(), "api-token", key, l.now(), burst, refill, 1)
	return d.Allowed, d.RetryAfter
}
