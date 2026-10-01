// Package ratelimit holds the in-memory, bounded-memory state behind the public surface's abuse
// protection: a keyed map that evicts its least recently used entry when full, and a token
// bucket limiter built on it. Nothing here is persisted, logged or shared between processes.
//
// Evicting an entry forgets its state, which can only help a client (a fresh bucket). The
// bound is what matters: an attacker rotating IPv6 /64s or junk keys cannot grow the map, and an
// operation costs O(1) under one mutex. Shard the mutex if a profile ever shows it.
package ratelimit

import (
	"container/list"
	"math"
	"sync"
	"time"
)

// Map is a goroutine-safe map from string keys to V holding at most max entries.
type Map[V any] struct {
	mu  sync.Mutex
	max int
	m   map[string]*list.Element
	ll  *list.List // front = most recently used
}

type entry[V any] struct {
	key string
	val V
}

// NewMap returns a Map that keeps at most max entries (at least 1).
func NewMap[V any](max int) *Map[V] {
	if max < 1 {
		max = 1
	}
	return &Map[V]{max: max, m: map[string]*list.Element{}, ll: list.New()}
}

// Get returns the value for key and marks it recently used.
func (m *Map[V]) Get(key string) (v V, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.m[key]; ok {
		m.ll.MoveToFront(e)
		return e.Value.(*entry[V]).val, true
	}
	return v, false
}

// GetOrCreate returns the value for key, storing mk() first when there is none. The oldest
// entry is evicted when the map is full.
func (m *Map[V]) GetOrCreate(key string, mk func() V) V {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.m[key]; ok {
		m.ll.MoveToFront(e)
		return e.Value.(*entry[V]).val
	}
	for m.ll.Len() >= m.max {
		old := m.ll.Back()
		m.ll.Remove(old)
		delete(m.m, old.Value.(*entry[V]).key)
	}
	en := &entry[V]{key: key, val: mk()}
	m.m[key] = m.ll.PushFront(en)
	return en.val
}

// Delete forgets key.
func (m *Map[V]) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.m[key]; ok {
		m.ll.Remove(e)
		delete(m.m, key)
	}
}

// Len is the number of entries.
func (m *Map[V]) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ll.Len()
}

// Limiter is a per-key token bucket: a key may burst up to the burst size, then gets rate requests per
// second. Memory is bounded by the key cap given to New.
type Limiter struct {
	// Now is the clock; nil means time.Now (tests set it).
	Now   func() time.Time
	rate  float64
	burst float64
	m     *Map[*bucket]
}

type bucket struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// New returns a limiter allowing rate requests per second per key with bursts of burst, tracking at
// most maxKeys keys. A rate <= 0 disables it (Allow always says yes).
func New(rate float64, burst, maxKeys int) *Limiter {
	if burst < 1 {
		burst = 1
	}
	return &Limiter{rate: rate, burst: float64(burst), m: NewMap[*bucket](maxKeys)}
}

// Allow takes one token for key. When there is none it returns false and how long until there is one.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	if l.rate <= 0 {
		return true, 0
	}
	now := time.Now()
	if l.Now != nil {
		now = l.Now()
	}
	b := l.m.GetOrCreate(key, func() *bucket { return &bucket{tokens: l.burst, last: now} })
	b.mu.Lock()
	defer b.mu.Unlock()
	if dt := now.Sub(b.last).Seconds(); dt > 0 {
		b.tokens = math.Min(l.burst, b.tokens+dt*l.rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration(math.Ceil((1-b.tokens)/l.rate*1e3)) * time.Millisecond
}

// Keys is how many keys are tracked.
func (l *Limiter) Keys() int { return l.m.Len() }
