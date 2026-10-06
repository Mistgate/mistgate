package fleet

import (
	"net/netip"

	"github.com/mistgate/mistgate/internal/panel/auth"
)

// maxLimiterKeys bounds the memory of the Enroll limiter: one entry per recent failing source.
const maxLimiterKeys = 8192

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
