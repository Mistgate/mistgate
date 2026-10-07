// Package securitylimit provides the shared backend for security-sensitive limits.
package securitylimit

import (
	"container/heap"
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

// Window describes a counted window. A positive Lockout locks the key as soon as a
// Record reaches Limit.
type Window struct {
	Name    string
	Limit   int
	Span    time.Duration
	Lockout time.Duration
}

// Limiter covers token buckets and bounded failure windows used by security checks.
type Limiter interface {
	Take(ctx context.Context, b Bucket, key string) (Decision, error)
	Peek(ctx context.Context, w Window, key string) (Decision, error)
	Record(ctx context.Context, w Window, key string) (Decision, error)
	Reset(ctx context.Context, w Window, key string) error
}

// DefaultMaxKeysPerName is the per-name cap used when NewMemory gets no positive cap.
const DefaultMaxKeysPerName = 8192

// Memory is the VPS implementation. Names isolate each limiter's key space.
type Memory struct {
	mu                 sync.Mutex
	now                func() time.Time
	maxKeysPerName     int
	buckets            map[limiterKey]*list.Element
	bucketOrderByName  map[string]*list.List
	windows            map[limiterKey]*list.Element
	windowOrderByName  map[string]*windowOrder
	windowCountsByName map[string]int
}

type limiterKey struct {
	name string
	key  string
}

type bucket struct {
	key    limiterKey
	tokens float64
	last   time.Time
}

type windowState struct {
	key             limiterKey
	start           time.Time
	n               int
	logged          bool
	lockedUntil     time.Time
	lockIndex       int
	unlockedElement *list.Element
}

type windowOrder struct {
	entries *list.List
	// unlocked holds the unlocked subset so a new key evicts it before a live lockout.
	unlocked *list.List
	locks    lockHeap
}

type lockHeap []*windowState

func (h lockHeap) Len() int           { return len(h) }
func (h lockHeap) Less(i, j int) bool { return h[i].lockedUntil.Before(h[j].lockedUntil) }
func (h lockHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].lockIndex = i
	h[j].lockIndex = j
}

func (h *lockHeap) Push(x any) {
	state := x.(*windowState)
	state.lockIndex = len(*h)
	*h = append(*h, state)
}

func (h *lockHeap) Pop() any {
	old := *h
	n := len(old)
	state := old[n-1]
	old[n-1] = nil
	state.lockIndex = -1
	*h = old[:n-1]
	return state
}

// NewMemory returns an in-memory limiter with the production clock and key cap when
// the corresponding arguments are nil or non-positive.
func NewMemory(now func() time.Time, maxKeysPerName int) *Memory {
	if now == nil {
		now = time.Now
	}
	if maxKeysPerName <= 0 {
		maxKeysPerName = DefaultMaxKeysPerName
	}
	return &Memory{
		now:                now,
		maxKeysPerName:     maxKeysPerName,
		buckets:            map[limiterKey]*list.Element{},
		bucketOrderByName:  map[string]*list.List{},
		windows:            map[limiterKey]*list.Element{},
		windowOrderByName:  map[string]*windowOrder{},
		windowCountsByName: map[string]int{},
	}
}

func (m *Memory) Take(ctx context.Context, spec Bucket, key string) (Decision, error) {
	if err := contextErr(ctx); err != nil {
		return Decision{}, err
	}
	if spec.Burst <= 0 || spec.Refill <= 0 {
		return Decision{}, errors.New("security limit: invalid token bucket")
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()

	order := m.bucketOrderByName[spec.Name]
	if order == nil {
		order = list.New()
		m.bucketOrderByName[spec.Name] = order
	}
	k := limiterKey{name: spec.Name, key: key}
	e := m.buckets[k]
	var b *bucket
	if e == nil {
		if order.Len() >= m.maxKeysPerName {
			m.removeBucket(order.Back())
		}
		b = &bucket{key: k, tokens: spec.Burst, last: now}
		e = order.PushFront(b)
		m.buckets[k] = e
	} else {
		order.MoveToFront(e)
		b = e.Value.(*bucket)
	}
	b.tokens = min(spec.Burst, b.tokens+float64(now.Sub(b.last))/float64(spec.Refill))
	b.last = now
	if b.tokens < 1 {
		return Decision{RetryAfter: time.Duration((1 - b.tokens) * float64(spec.Refill))}, nil
	}
	b.tokens--
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
	order := m.windowOrderByName[spec.Name]
	if order == nil {
		return Decision{Allowed: true, Remaining: spec.Limit}, nil
	}
	m.expireWindowLocks(order, now)
	e := m.windows[k]
	if e == nil {
		return Decision{Allowed: true, Remaining: spec.Limit}, nil
	}
	state := e.Value.(*windowState)
	d, expired := m.windowDecision(state, spec, now)
	if expired {
		m.removeWindow(e)
		return Decision{Allowed: true, Remaining: spec.Limit}, nil
	}
	order.entries.MoveToFront(e)
	if state.unlockedElement != nil {
		order.unlocked.MoveToFront(state.unlockedElement)
	}
	return d, nil
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
	order := m.getWindowOrder(spec.Name)
	m.expireWindowLocks(order, now)
	e := m.windows[k]
	if e == nil {
		if order.entries.Len() >= m.maxKeysPerName {
			oldest := order.unlocked.Back()
			if oldest == nil {
				oldest = order.entries.Back()
			}
			m.removeWindow(oldest)
		}
		state := &windowState{key: k, start: now, lockIndex: -1}
		e = order.entries.PushFront(state)
		state.unlockedElement = order.unlocked.PushFront(state)
		m.windows[k] = e
		m.windowCountsByName[spec.Name]++
	} else {
		state := e.Value.(*windowState)
		order.entries.MoveToFront(e)
		if state.unlockedElement != nil {
			order.unlocked.MoveToFront(state.unlockedElement)
		}
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
		if state.unlockedElement != nil {
			order.unlocked.Remove(state.unlockedElement)
			state.unlockedElement = nil
		}
		heap.Push(&order.locks, state)
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

func (m *Memory) Reset(ctx context.Context, spec Window, key string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := limiterKey{name: spec.Name, key: key}
	if e := m.windows[k]; e != nil {
		m.removeWindow(e)
	}
	return nil
}

// Size reports the number of remembered window keys in a namespace.
func (m *Memory) Size(name string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.windowCountsByName[name]
}

func (m *Memory) getWindowOrder(name string) *windowOrder {
	order := m.windowOrderByName[name]
	if order == nil {
		order = &windowOrder{entries: list.New(), unlocked: list.New()}
		m.windowOrderByName[name] = order
	}
	return order
}

func (m *Memory) expireWindowLocks(order *windowOrder, now time.Time) {
	for order.locks.Len() > 0 && !now.Before(order.locks[0].lockedUntil) {
		state := heap.Pop(&order.locks).(*windowState)
		if m.windows[state.key] != nil {
			state.unlockedElement = order.unlocked.PushBack(state)
		}
	}
}

func (m *Memory) removeBucket(e *list.Element) {
	b := e.Value.(*bucket)
	delete(m.buckets, b.key)
	m.bucketOrderByName[b.key.name].Remove(e)
}

func (m *Memory) removeWindow(e *list.Element) {
	state := e.Value.(*windowState)
	order := m.windowOrderByName[state.key.name]
	if state.lockIndex >= 0 {
		heap.Remove(&order.locks, state.lockIndex)
	}
	if state.unlockedElement != nil {
		order.unlocked.Remove(state.unlockedElement)
	}
	delete(m.windows, state.key)
	order.entries.Remove(e)
	m.windowCountsByName[state.key.name]--
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
