package doctor

import (
	"sync"
	"time"
)

// ringSize holds 30 minutes of 10 s samples with room to spare; a fixed array, so memory stays flat for the
// agent's whole life.
const ringSize = 256

// Ring keeps the last ringSize host samples for cpu_softirq and the idle-load rule. Safe for concurrent use.
type Ring struct {
	mu   sync.Mutex
	buf  [ringSize]Sample
	next int
	n    int
}

// Add records a sample (the agent calls it once per stats interval).
func (r *Ring) Add(s Sample) {
	r.mu.Lock()
	r.buf[r.next] = s
	r.next = (r.next + 1) % ringSize
	r.n = min(r.n+1, ringSize)
	r.mu.Unlock()
}

// Since returns the samples taken at or after now-window, oldest first.
func (r *Ring) Since(now time.Time, window time.Duration) []Sample {
	r.mu.Lock()
	defer r.mu.Unlock()
	cut := now.Add(-window)
	var out []Sample
	for i := r.n; i > 0; i-- { // oldest to newest
		s := r.buf[(r.next-i+ringSize)%ringSize]
		if !s.At.Before(cut) {
			out = append(out, s)
		}
	}
	return out
}
