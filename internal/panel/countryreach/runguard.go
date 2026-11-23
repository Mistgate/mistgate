package countryreach

import (
	"context"
	"sync"
	"time"
)

// RunGuard keeps the runs of one source from storing anything after the owner switched the source off or disconnected it. A run
// takes the generation before it reads the settings, is admitted by Track only while that generation is current, and stores
// through Do; Stop makes every run that started earlier stale and cancels the ones in flight. The zero value is ready.
type RunGuard struct {
	mu       sync.Mutex
	gen      uint64
	next     uint64
	inflight map[uint64]context.CancelFunc
}

// Generation is the version of the settings a run is about to read.
func (g *RunGuard) Generation() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gen
}

// Current says whether gen is still the generation of the settings.
func (g *RunGuard) Current(gen uint64) bool { return g.Generation() == gen }

// Track registers a run in flight so that Stop can cancel it. A run that took its generation before a Stop is not admitted. A
// timeout above zero bounds the run. Call the returned func when the run ends.
func (g *RunGuard) Track(ctx context.Context, gen uint64, timeout time.Duration) (context.Context, func(), bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if gen != g.gen {
		return ctx, func() {}, false
	}
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	if g.inflight == nil {
		g.inflight = map[uint64]context.CancelFunc{}
	}
	id := g.next
	g.next++
	g.inflight[id] = cancel
	return ctx, func() {
		g.mu.Lock()
		delete(g.inflight, id)
		g.mu.Unlock()
		cancel()
	}, true
}

// Stop cancels every run in flight and makes a run that started earlier store nothing.
func (g *RunGuard) Stop() {
	g.mu.Lock()
	g.gen++
	for _, cancel := range g.inflight {
		cancel()
	}
	g.mu.Unlock()
}

// Do runs f only while gen is current and says whether it did. f holds the guard's lock: a Stop waits for it, so a write that
// starts under a current generation is done before the owner's change is saved. f must not call the guard.
func (g *RunGuard) Do(gen uint64, f func()) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if gen != g.gen {
		return false
	}
	f()
	return true
}
