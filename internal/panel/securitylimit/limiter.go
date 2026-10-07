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

// Bucket describes a token bucket.
type Bucket struct {
	Name   string
	Burst  float64
	Refill time.Duration
}

// Window describes a counted window. A positive Lockout locks the key as soon as
// a Record reaches Limit.
type Window struct {
	Name    string
	Limit   int
	Span    time.Duration
	Lockout time.Duration
}

// Limiter covers token buckets and bounded failure windows used by security checks.
type Limiter interface {
	Take(ctx context.Context, b Bucket, key string, cost float64) (Decision, error)
	Peek(ctx context.Context, w Window, key string) (Decision, error)
	Record(ctx context.Context, w Window, key string) (Decision, error)
	Reset(ctx context.Context, name, key string) error
}

const defaultMaxKeysPerName = 8192

// Memory is the VPS implementation. Names isolate each limiter's key space.
type Memory struct {
	mu                sync.Mutex
	now               func() time.Time
	maxKeysPerName    int
	buckets           map[limiterKey]*bucket
	bucketCounts      map[string]int
	windows           map[limiterKey]*list.Element
	windowOrderByName map[string]*list.List
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

type windowState struct {
	key         limiterKey
	start       time.Time
	n           int
	logged      bool
	lockedUntil time.Time
}

// NewMemory returns a fresh in-memory limiter backend with production defaults.
// NewMemoryWithOptions lets callers supply a clock and per-name window cap.
func NewMemory() *Memory {
	return NewMemoryWithOptions(time.Now, defaultMaxKeysPerName)
}

// NewMemoryWithOptions returns an in-memory limiter with an injected clock and a
// per-name cap for remembered windows. A cap below one is treated as one.
func NewMemoryWithOptions(now func() time.Time, maxKeysPerName int) *Memory {
	if now == nil {
		now = time.Now
	}
	if maxKeysPerName < 1 {
		maxKeysPerName = 1
	}
	return &Memory{
		now:               now,
		maxKeysPerName:    maxKeysPerName,
		buckets:           map[limiterKey]*bucket{},
		bucketCounts:      map[string]int{},
		windows:           map[limiterKey]*list.Element{},
		windowOrderByName: map[string]*list.List{},
	}
}

func (m *Memory) Take(ctx context.Context, spec Bucket, key string, cost float64) (Decision, error) {
	if err := contextErr(ctx); err != nil {
		return Decision{}, err
	}
	if spec.Burst <= 0 || spec.Refill <= 0 || cost <= 0 {
		return Decision{}, errors.New("security limit: invalid token bucket")
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bucketCounts[spec.Name] > 4096 {
		for k, b := range m.buckets {
			if k.name == spec.Name && now.Sub(b.last) > b.full {
				delete(m.buckets, k)
				m.bucketCounts[spec.Name]--
			}
		}
	}
	k := limiterKey{name: spec.Name, key: key}
	b, ok := m.buckets[k]
	if !ok {
		b = &bucket{tokens: spec.Burst, last: now}
		m.buckets[k] = b
		m.bucketCounts[spec.Name]++
	}
	b.full = time.Duration(spec.Burst) * spec.Refill
	b.tokens = min(spec.Burst, b.tokens+float64(now.Sub(b.last))/float64(spec.Refill))
	b.last = now
	if b.tokens < cost {
		return Decision{RetryAfter: time.Duration((cost - b.tokens) * float64(spec.Refill))}, nil
	}
	b.tokens -= cost
	return Decision{Allowed: true}, nil
}

func (m *Memory) Peek(ctx context.Context, spec Window, key string) (Decision, error) {
	if err := contextErr(ctx); err != nil {
		return Decision{}, err
	}
	if err := validateWindow(spec); err != nil {
		return Decision{}, err
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	k := limiterKey{name: spec.Name, key: key}
	e := m.windows[k]
	if e == nil {
		return Decision{Allowed: true, Remaining: spec.Limit}, nil
	}
	m.windowOrderByName[spec.Name].MoveToFront(e)
	state := e.Value.(*windowState)
	if d, expired := m.windowDecision(state, spec, now); expired {
		return Decision{Allowed: true, Remaining: spec.Limit}, nil
	} else {
		return d, nil
	}
}

func (m *Memory) Record(ctx context.Context, spec Window, key string) (Decision, error) {
	if err := contextErr(ctx); err != nil {
		return Decision{}, err
	}
	if err := validateWindow(spec); err != nil {
		return Decision{}, err
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	k := limiterKey{name: spec.Name, key: key}
	e := m.windows[k]
	if e == nil {
		order := m.windowOrderByName[spec.Name]
		if order == nil {
			order = list.New()
			m.windowOrderByName[spec.Name] = order
		}
		if order.Len() >= m.maxKeysPerName {
			if oldest := order.Back(); oldest != nil {
				m.removeWindow(oldest)
			}
		}
		if m.windowOrderByName[spec.Name] == nil {
			order = list.New()
			m.windowOrderByName[spec.Name] = order
		}
		state := &windowState{key: k, start: now}
		e = order.PushFront(state)
		m.windows[k] = e
	} else {
		m.windowOrderByName[spec.Name].MoveToFront(e)
	}
	state := e.Value.(*windowState)
	if _, expired := m.windowDecision(state, spec, now); expired {
		state.start = now
		state.n = 0
		state.logged = false
		state.lockedUntil = time.Time{}
	}
	if now.Before(state.lockedUntil) {
		return Decision{RetryAfter: state.lockedUntil.Sub(now)}, nil
	}
	if spec.Lockout > 0 && state.n+1 >= spec.Limit {
		state.n++
		state.logged = true
		state.lockedUntil = now.Add(spec.Lockout)
		return Decision{RetryAfter: spec.Lockout, First: true}, nil
	}
	if state.n >= spec.Limit {
		first := !state.logged
		state.logged = true
		return Decision{RetryAfter: state.start.Add(spec.Span).Sub(now), First: first}, nil
	}
	state.n++
	return Decision{Allowed: true, Remaining: spec.Limit - state.n}, nil
}

func validateWindow(spec Window) error {
	if spec.Limit <= 0 || spec.Span <= 0 || spec.Lockout < 0 {
		return errors.New("security limit: invalid failure window")
	}
	return nil
}

// windowDecision returns the current decision and whether the state has expired.
// A lockout outlives the counting span when its deadline is later.
func (m *Memory) windowDecision(state *windowState, spec Window, now time.Time) (Decision, bool) {
	if now.Before(state.lockedUntil) {
		return Decision{RetryAfter: state.lockedUntil.Sub(now)}, false
	}
	if now.Sub(state.start) >= spec.Span || (!state.lockedUntil.IsZero() && !now.Before(state.lockedUntil)) {
		return Decision{}, true
	}
	if state.n >= spec.Limit {
		return Decision{RetryAfter: state.start.Add(spec.Span).Sub(now), First: !state.logged}, false
	}
	return Decision{Allowed: true, Remaining: spec.Limit - state.n}, false
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
	if order := m.windowOrderByName[name]; order != nil {
		return order.Len()
	}
	return 0
}

func (m *Memory) removeWindow(e *list.Element) {
	state := e.Value.(*windowState)
	delete(m.windows, state.key)
	order := m.windowOrderByName[state.key.name]
	order.Remove(e)
	if order.Len() == 0 {
		delete(m.windowOrderByName, state.key.name)
	}
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
