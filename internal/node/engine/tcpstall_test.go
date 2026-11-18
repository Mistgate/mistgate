package engine

import "testing"

func TestTCPStallRule(t *testing.T) {
	for _, c := range []struct {
		name                      string
		retrans                   uint32
		sent, retransBytes, acked uint64
		unacked                   uint32
		stalled, first16K         bool
	}{
		{"all acknowledged", 5, 200 << 10, 10 << 10, 190<<10 + 1, 0, false, false},
		{"few retransmissions", 2, 64 << 10, 4 << 10, 16 << 10, 3, false, false},
		{"frozen after 16 KiB", 6, 40 << 10, 20 << 10, 16 << 10, 3, true, true},
		{"stalled late", 8, 9 << 20, 1 << 20, 6 << 20, 10, true, false},
		{"cut before 8 KiB", 4, 12 << 10, 6 << 10, 3 << 10, 2, true, false},
		{"old kernel, segments in flight", 4, 0, 0, 16 << 10, 2, true, true},
		{"old kernel, nothing in flight", 4, 0, 0, 16 << 10, 0, false, false},
	} {
		stalled, first := TCPStallRule(c.retrans, c.sent, c.retransBytes, c.acked, c.unacked)
		if stalled != c.stalled || first != c.first16K {
			t.Errorf("%s: stalled %v, first16K %v; want %v, %v", c.name, stalled, first, c.stalled, c.first16K)
		}
	}
}
