package auth

import (
	"sync"
	"time"
)

// limiter is a per-key token bucket: burst tokens, one more every refill.
type limiter struct {
	burst  float64
	refill time.Duration
	now    func() time.Time

	mu sync.Mutex
	m  map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
	full   time.Duration // how long an empty bucket takes to fill again, for the cleanup
}

func newLimiter(burst int, refill time.Duration) *limiter {
	return &limiter{burst: float64(burst), refill: refill, now: time.Now, m: map[string]*bucket{}}
}

func (l *limiter) allow(key string) bool {
	ok, _ := l.allowRate(key, l.burst, l.refill)
	return ok
}

// allowRate takes one token of key's bucket, which holds burst tokens and gets one more every refill (the rate
// may differ per key: API tokens each have their own). When there is none it also returns how long until
// the next one.
func (l *limiter) allowRate(key string, burst float64, refill time.Duration) (bool, time.Duration) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.m) > 4096 { // drop buckets that have fully refilled, i.e. carry no state
		for k, b := range l.m {
			if now.Sub(b.last) > b.full {
				delete(l.m, k)
			}
		}
	}
	b, ok := l.m[key]
	if !ok {
		b = &bucket{tokens: burst, last: now}
		l.m[key] = b
	}
	b.full = time.Duration(burst) * refill
	b.tokens = min(burst, b.tokens+float64(now.Sub(b.last))/float64(refill))
	b.last = now
	if b.tokens < 1 {
		return false, time.Duration((1 - b.tokens) * float64(refill))
	}
	b.tokens--
	return true, 0
}
