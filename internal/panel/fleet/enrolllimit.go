package fleet

import (
	"context"
	"net/netip"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
)

// maxLimiterKeys bounds the memory of the Enroll limiter: one entry per recent failing source.
const maxLimiterKeys = 8192

// failLimiter keeps the old unit-test surface around the shared in-memory window limiter.
type failLimiter struct {
	memory *securitylimit.Memory
	max    int
	span   time.Duration
	cap    int
}

func newFailLimiter(max int, span time.Duration) *failLimiter {
	return &failLimiter{memory: securitylimit.NewMemory(), max: max, span: span, cap: maxLimiterKeys}
}

func (l *failLimiter) blocked(key string, now time.Time) bool {
	d, err := l.memory.CheckWindow(context.Background(), "enrollment-failure", key, now, l.max, l.span, l.cap, false)
	return err != nil || !d.Allowed
}

func (l *failLimiter) fail(key string, now time.Time) {
	_ = l.memory.FailWindow(context.Background(), "enrollment-failure", key, now, l.span, l.cap)
}

func (l *failLimiter) size() int { return l.memory.Size("enrollment-failure") }

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
