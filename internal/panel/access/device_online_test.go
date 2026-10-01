package access

import (
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// The admin's device row says "online" for a handshake younger than awgOnlineWindow and never for a device that
// has not connected (last_seen_at zero). The fleet writes the handshake time (fleet.touchAwgDevices).
func TestAwgDeviceIsOnlineByItsLastHandshake(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := &Service{now: func() time.Time { return now }}
	for _, c := range []struct {
		seen   time.Time
		online bool
		unix   int64
	}{
		{time.Time{}, false, 0},
		{now.Add(-30 * time.Second), true, now.Add(-30 * time.Second).Unix()},
		{now.Add(-awgOnlineWindow + time.Second), true, now.Add(-awgOnlineWindow + time.Second).Unix()},
		{now.Add(-awgOnlineWindow - time.Second), false, now.Add(-awgOnlineWindow - time.Second).Unix()},
	} {
		d := s.deviceProto(store.AccessAWGDevice{AccessDevice: store.AccessDevice{LastSeenAt: c.seen}})
		if d.Online != c.online || d.LastHandshakeUnix != c.unix {
			t.Errorf("last seen %v: online %v handshake %d, want %v and %d", c.seen, d.Online, d.LastHandshakeUnix, c.online, c.unix)
		}
	}
}
