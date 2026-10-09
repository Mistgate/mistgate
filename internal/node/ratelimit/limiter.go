// Package ratelimit provides the per-credential byte token bucket shared by node engines.
package ratelimit

import (
	"context"

	"golang.org/x/time/rate"
)

const minBurst = 64 * 1024

// Limiter keeps independent upload and download buckets. BPS is the configured limit in bits per second.
type Limiter struct {
	bps  uint64
	up   *rate.Limiter
	down *rate.Limiter
}

func New(bps uint64) *Limiter {
	bytes := bps / 8
	if bytes == 0 {
		bytes = 1
	}
	burst := int(bytes)
	if burst < minBurst {
		burst = minBurst
	}
	return &Limiter{bps: bps, up: rate.NewLimiter(rate.Limit(bytes), burst), down: rate.NewLimiter(rate.Limit(bytes), burst)}
}

func (l *Limiter) BPS() uint64 { return l.bps }

func (l *Limiter) WaitUp(ctx context.Context, n uint64) bool { return wait(ctx, l.up, n) }

func (l *Limiter) WaitDown(ctx context.Context, n uint64) bool { return wait(ctx, l.down, n) }

func wait(ctx context.Context, l *rate.Limiter, n uint64) bool {
	if n == 0 {
		return true
	}
	if n > uint64(l.Burst()) {
		n = uint64(l.Burst())
	}
	return l.WaitN(ctx, int(n)) == nil
}
