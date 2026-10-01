package fleet

import (
	"container/list"
	"net/netip"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
)

// maxLimiterKeys bounds the memory of the Enroll limiter: one entry per recent failing source.
const maxLimiterKeys = 8192

// failLimiter caps failed Enroll attempts per source: tokens carry 256 bits, so this only keeps a guesser from
// hammering the database. It is bounded in memory and O(1) per call: when it is full the source that failed
// longest ago is forgotten.
// An attacker who rotates more than maxLimiterKeys sources inside one span can flush a blocked
// source out early; the tokens are 256 bits, so the price of that is a few more failed reads.
type failLimiter struct {
	mu   sync.Mutex
	m    map[string]*list.Element // key -> element of order; Value is *failBucket
	ord  *list.List               // front = most recently failed
	max  int
	span time.Duration
	cap  int
}

type failBucket struct {
	key   string
	n     int
	since time.Time
}

func newFailLimiter(max int, span time.Duration) *failLimiter {
	return &failLimiter{m: map[string]*list.Element{}, ord: list.New(), max: max, span: span, cap: maxLimiterKeys}
}

func (l *failLimiter) blocked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.m[key]
	if e == nil {
		return false
	}
	b := e.Value.(*failBucket)
	return now.Sub(b.since) < l.span && b.n >= l.max
}

func (l *failLimiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.m[key]; e != nil {
		b := e.Value.(*failBucket)
		if now.Sub(b.since) >= l.span {
			b.n, b.since = 0, now
		}
		b.n++
		l.ord.MoveToFront(e)
		return
	}
	l.m[key] = l.ord.PushFront(&failBucket{key: key, n: 1, since: now})
	for l.ord.Len() > l.cap {
		old := l.ord.Back()
		delete(l.m, old.Value.(*failBucket).key)
		l.ord.Remove(old)
	}
}

// size is the number of sources currently remembered (tests).
func (l *failLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ord.Len()
}

// limiterKey is the source an Enroll attempt is counted against: the IPv4 address, or the IPv6 /64 (one
// subscriber is handed a whole /64, so a per-address limit is free to evade). The client address is the one
// the public listener resolved (the TCP peer, or what a trusted proxy reported); without it, the peer.
func limiterKey(clientIP netip.Addr, peer string) string {
	if !clientIP.IsValid() {
		if a, err := netip.ParseAddr(peer); err == nil {
			clientIP = a
		}
	}
	return auth.SourceKey(clientIP)
}
