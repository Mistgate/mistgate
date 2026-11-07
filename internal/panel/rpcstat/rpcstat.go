// Package rpcstat counts the admin API's calls over the last few minutes: how many, how many failed, and how long the
// unary ones took. It is a fixed array of one-minute slots with a latency histogram each, so it never allocates after
// New and its memory does not grow with the traffic. The HTTP middleware records every /api call; OpsService
// GetPanelResources reads it.
package rpcstat

import (
	"sync"
	"time"
)

// Minutes is the window: the last Minutes whole minutes, the current one included.
const Minutes = 5

// bounds are the upper edges of the latency buckets, in milliseconds; one more bucket holds everything slower.
var bounds = [...]float64{1, 2, 5, 10, 20, 35, 50, 75, 100, 150, 200, 300, 500, 750, 1000, 1500, 2500, 5000, 10000}

type slot struct {
	minute                         int64 // unix minute the counts belong to
	requests, serverErr, clientErr uint64
	lat                            [len(bounds) + 1]uint32
}

// Window holds the counts. The zero value is not usable; a nil *Window records nothing and reads as empty.
type Window struct {
	now   func() time.Time
	mu    sync.Mutex
	slots [Minutes]slot
}

// New returns an empty window on the real clock (now nil) or on now.
func New(now func() time.Time) *Window {
	if now == nil {
		now = time.Now
	}
	return &Window{now: now}
}

// Record counts one call that ended with status after d. A stream's duration is how long it was open, not how long the panel
// took to answer, so it is counted but kept out of the latency.
func (w *Window) Record(status int, d time.Duration, stream bool) {
	if w == nil {
		return
	}
	m := w.now().Unix() / 60
	w.mu.Lock()
	s := &w.slots[m%Minutes]
	if s.minute != m {
		*s = slot{minute: m}
	}
	s.requests++
	switch {
	case status >= 500:
		s.serverErr++
	case status >= 400:
		s.clientErr++
	}
	if !stream {
		ms := float64(d) / float64(time.Millisecond)
		i := 0
		for i < len(bounds) && ms > bounds[i] {
			i++
		}
		s.lat[i]++
	}
	w.mu.Unlock()
}

// Snapshot is the window's counts.
type Snapshot struct {
	WindowS                              int
	Requests, ServerErrors, ClientErrors uint64
	// P95ms is the 95th percentile of the unary calls' time, read from the histogram (linear inside its bucket); 0 without
	// calls.
	P95ms float64
}

// Snapshot adds up the slots that are still inside the window.
func (w *Window) Snapshot() Snapshot {
	out := Snapshot{WindowS: Minutes * 60}
	if w == nil {
		return out
	}
	m := w.now().Unix() / 60
	var lat [len(bounds) + 1]uint64
	w.mu.Lock()
	for _, s := range w.slots {
		if s.minute <= m-Minutes || s.minute > m || s.requests == 0 {
			continue
		}
		out.Requests += s.requests
		out.ServerErrors += s.serverErr
		out.ClientErrors += s.clientErr
		for i, n := range s.lat {
			lat[i] += uint64(n)
		}
	}
	w.mu.Unlock()
	out.P95ms = p95(lat)
	return out
}

func p95(lat [len(bounds) + 1]uint64) float64 {
	var total uint64
	for _, n := range lat {
		total += n
	}
	if total == 0 {
		return 0
	}
	rank := float64(total) * 0.95
	var seen float64
	for i, n := range lat {
		if n == 0 {
			continue
		}
		if seen+float64(n) >= rank {
			lo := 0.0
			if i > 0 {
				lo = bounds[i-1]
			}
			if i == len(bounds) { // slower than the last edge: say the edge, not a guess
				return bounds[len(bounds)-1]
			}
			return lo + (bounds[i]-lo)*(rank-seen)/float64(n)
		}
		seen += float64(n)
	}
	return bounds[len(bounds)-1]
}
