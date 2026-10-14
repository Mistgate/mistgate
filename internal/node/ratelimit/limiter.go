// Package ratelimit provides the byte token buckets shared by node engines: one per credential (the device limit) and
// one per user on the node (the person limit, see Registry).
package ratelimit

import (
	"context"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

const minBurst = 64 * 1024

// Limiter keeps independent upload and download buckets. BPS is the configured limit in bits per second; SetBPS
// changes it in place, so everything holding the limiter follows.
type Limiter struct {
	bps  atomic.Uint64
	up   *rate.Limiter
	down *rate.Limiter
	clk  clock
}

// clock is the time source of the waits; tests replace it so that a wait advances a fake time instead of sleeping.
type clock struct {
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) bool
}

var realClock = clock{now: time.Now, sleep: sleepCtx}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// bucket returns the refill rate (bytes/s) and the burst of a limit in bits per second.
func bucket(bps uint64) (rate.Limit, int) {
	bytes := bps / 8
	if bytes == 0 {
		bytes = 1
	}
	burst := int(bytes)
	if burst < minBurst {
		burst = minBurst
	}
	return rate.Limit(bytes), burst
}

func New(bps uint64) *Limiter { return newWithClock(bps, realClock) }

func newWithClock(bps uint64, clk clock) *Limiter {
	r, burst := bucket(bps)
	l := &Limiter{up: rate.NewLimiter(r, burst), down: rate.NewLimiter(r, burst), clk: clk}
	l.bps.Store(bps)
	return l
}

func (l *Limiter) BPS() uint64 { return l.bps.Load() }

// SetBPS changes the limit of both buckets in place. Tokens already in a bucket stay (capped by the new burst).
func (l *Limiter) SetBPS(bps uint64) {
	if l.bps.Swap(bps) == bps {
		return
	}
	r, burst := bucket(bps)
	for _, b := range []*rate.Limiter{l.up, l.down} {
		b.SetLimit(r)
		b.SetBurst(burst)
	}
}

func (l *Limiter) WaitUp(ctx context.Context, n uint64) bool { return l.wait(ctx, l.up, n) }

func (l *Limiter) WaitDown(ctx context.Context, n uint64) bool { return l.wait(ctx, l.down, n) }

// WaitUp waits until n upload bytes may pass both the device limiter and the person limiter; either may be nil
// (no such limit). The device bucket goes first: a device held back by its own limit must not sit on tokens of the
// person bucket that the person's other devices could use. No lock is held while waiting. False: ctx ended.
func WaitUp(ctx context.Context, device, person *Limiter, n uint64) bool {
	return (device == nil || device.WaitUp(ctx, n)) && (person == nil || person.WaitUp(ctx, n))
}

// WaitDown is WaitUp for the download direction.
func WaitDown(ctx context.Context, device, person *Limiter, n uint64) bool {
	return (device == nil || device.WaitDown(ctx, n)) && (person == nil || person.WaitDown(ctx, n))
}

// wait charges n bytes in parts no bigger than the burst (a reservation above it can never be granted) and sleeps until
// each is due: a write bigger than the bucket waits for all of it, not for one burst (a 128 KiB VLESS buffer charged
// as 64 KiB let a device run at twice its limit).
func (l *Limiter) wait(ctx context.Context, b *rate.Limiter, n uint64) bool {
	for n > 0 {
		part := min(n, uint64(b.Burst()))
		if !l.waitPart(ctx, b, part) {
			return false
		}
		n -= part
	}
	return true
}

// waitPart is rate.Limiter.WaitN for n ≤ burst bytes, with the clock made injectable.
func (l *Limiter) waitPart(ctx context.Context, b *rate.Limiter, n uint64) bool {
	now := l.clk.now()
	r := b.ReserveN(now, int(n))
	if !r.OK() { // cannot happen after the clamp; fail closed rather than pass unshaped
		return false
	}
	d := r.DelayFrom(now)
	if d <= 0 {
		return true
	}
	if !l.clk.sleep(ctx, d) {
		r.CancelAt(l.clk.now())
		return false
	}
	return true
}
