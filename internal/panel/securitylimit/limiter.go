// Package securitylimit provides the shared backend for security-sensitive limits.
package securitylimit

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"
)

// Decision describes a limiter operation. First is set on the first request that
// reaches a window limit, so callers can report that lockout once.
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
	Remaining  int
	First      bool
}

// Limiter covers token buckets and bounded failure windows used by security checks.
type Limiter interface {
	Take(ctx context.Context, name, key string, now time.Time, burst float64, refill time.Duration, cost float64) (Decision, error)
	CheckWindow(ctx context.Context, name, key string, now time.Time, limit int, window time.Duration, maxKeys int, touch bool) (Decision, error)
	RecordWindow(ctx context.Context, name, key string, now time.Time, limit int, window time.Duration, maxKeys int) (Decision, error)
	FailWindow(ctx context.Context, name, key string, now time.Time, window time.Duration, maxKeys int) error
	Reset(ctx context.Context, name, key string) error
}

// Memory is the VPS implementation. Names isolate each limiter's key space.
type Memory struct {
	mu           sync.Mutex
	buckets      map[limiterKey]*bucket
	bucketCounts map[string]int
	windows      map[limiterKey]*list.Element
	order        *list.List
	windowCounts map[string]int
}

type limiterKey struct {
	name string
	key  string
}

type bucket struct {
	tokens float64
	last   time.Time
	full   time.Duration
}

type window struct {
	key    limiterKey
	start  time.Time
	n      int
	logged bool
}

// NewMemory returns a fresh in-memory limiter backend.
func NewMemory() *Memory {
	return &Memory{
		buckets:      map[limiterKey]*bucket{},
		bucketCounts: map[string]int{},
		windows:      map[limiterKey]*list.Element{},
		order:        list.New(),
		windowCounts: map[string]int{},
	}
}

func (m *Memory) Take(ctx context.Context, name, key string, now time.Time, burst float64, refill time.Duration, cost float64) (Decision, error) {
	if err := contextErr(ctx); err != nil {
		return Decision{}, err
	}
	if burst <= 0 || refill <= 0 || cost <= 0 {
		return Decision{}, errors.New("security limit: invalid token bucket")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bucketCounts[name] > 4096 {
		for k, b := range m.buckets {
			if k.name == name && now.Sub(b.last) > b.full {
				delete(m.buckets, k)
				m.bucketCounts[name]--
			}
		}
	}
	k := limiterKey{name: name, key: key}
	b, ok := m.buckets[k]
	if !ok {
		b = &bucket{tokens: burst, last: now}
		m.buckets[k] = b
		m.bucketCounts[name]++
	}
	b.full = time.Duration(burst) * refill
	b.tokens = min(burst, b.tokens+float64(now.Sub(b.last))/float64(refill))
	b.last = now
	if b.tokens < cost {
		return Decision{RetryAfter: time.Duration((cost - b.tokens) * float64(refill))}, nil
	}
	b.tokens -= cost
	return Decision{Allowed: true}, nil
}

func (m *Memory) CheckWindow(ctx context.Context, name, key string, now time.Time, limit int, duration time.Duration, maxKeys int, touch bool) (Decision, error) {
	if err := contextErr(ctx); err != nil {
		return Decision{}, err
	}
	if limit <= 0 || duration <= 0 {
		return Decision{}, errors.New("security limit: invalid failure window")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := limiterKey{name: name, key: key}
	e := m.windows[k]
	if e == nil {
		return Decision{Allowed: true, Remaining: limit}, nil
	}
	w := e.Value.(*window)
	if touch {
		m.order.MoveToFront(e)
	}
	if now.Sub(w.start) >= duration {
		return Decision{Allowed: true, Remaining: limit}, nil
	}
	if w.n >= limit {
		return Decision{RetryAfter: w.start.Add(duration).Sub(now), First: !w.logged}, nil
	}
	return Decision{Allowed: true, Remaining: limit - w.n}, nil
}

func (m *Memory) RecordWindow(ctx context.Context, name, key string, now time.Time, limit int, duration time.Duration, maxKeys int) (Decision, error) {
	if err := contextErr(ctx); err != nil {
		return Decision{}, err
	}
	if limit <= 0 || duration <= 0 {
		return Decision{}, errors.New("security limit: invalid failure window")
	}
	if maxKeys < 1 {
		maxKeys = 1
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := limiterKey{name: name, key: key}
	e := m.windows[k]
	if e == nil {
		for m.windowCounts[name] >= maxKeys {
			var old *list.Element
			for candidate := m.order.Back(); candidate != nil; candidate = candidate.Prev() {
				if candidate.Value.(*window).key.name == name {
					old = candidate
					break
				}
			}
			if old == nil {
				break
			}
			m.removeWindow(old)
		}
		w := &window{key: k}
		e = m.order.PushFront(w)
		m.windows[k] = e
		m.windowCounts[name]++
	} else {
		m.order.MoveToFront(e)
	}
	w := e.Value.(*window)
	if now.Sub(w.start) >= duration {
		w.start, w.n, w.logged = now, 0, false
	}
	if w.n >= limit {
		first := !w.logged
		w.logged = true
		return Decision{RetryAfter: w.start.Add(duration).Sub(now), First: first}, nil
	}
	w.n++
	return Decision{Allowed: true, Remaining: limit - w.n}, nil
}

// FailWindow adds a failed attempt without a threshold check. It is paired with
// CheckWindow when the caller only counts failed operations, as Enroll does.
func (m *Memory) FailWindow(ctx context.Context, name, key string, now time.Time, duration time.Duration, maxKeys int) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if duration <= 0 {
		return errors.New("security limit: invalid failure window")
	}
	if maxKeys < 1 {
		maxKeys = 1
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := limiterKey{name: name, key: key}
	e := m.windows[k]
	if e == nil {
		for m.windowCounts[name] >= maxKeys {
			var old *list.Element
			for candidate := m.order.Back(); candidate != nil; candidate = candidate.Prev() {
				if candidate.Value.(*window).key.name == name {
					old = candidate
					break
				}
			}
			if old == nil {
				break
			}
			m.removeWindow(old)
		}
		w := &window{key: k}
		e = m.order.PushFront(w)
		m.windows[k] = e
		m.windowCounts[name]++
	} else {
		m.order.MoveToFront(e)
	}
	w := e.Value.(*window)
	if now.Sub(w.start) >= duration {
		w.start, w.n, w.logged = now, 0, false
	}
	w.n++
	return nil
}

func (m *Memory) Reset(ctx context.Context, name, key string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := limiterKey{name: name, key: key}
	if e := m.windows[k]; e != nil {
		m.removeWindow(e)
	}
	return nil
}

// Size reports the number of remembered window keys in a namespace.
func (m *Memory) Size(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.windowCounts[name]
}

func (m *Memory) removeWindow(e *list.Element) {
	w := e.Value.(*window)
	delete(m.windows, w.key)
	m.windowCounts[w.key.name]--
	m.order.Remove(e)
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
