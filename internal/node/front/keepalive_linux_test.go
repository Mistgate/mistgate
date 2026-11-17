//go:build linux

package front

import (
	"net"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func sockopts(t *testing.T, c net.Conn) (on, idle, interval, count int) {
	t.Helper()
	raw, err := c.(syscall.Conn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Control(func(fd uintptr) {
		on, _ = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_KEEPALIVE)
		idle, _ = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_KEEPIDLE)
		interval, _ = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_KEEPINTVL)
		count, _ = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_KEEPCNT)
	}); err != nil {
		t.Fatal(err)
	}
	return
}

// TestKeepAliveFollowsTheRoute: each route gets the keepalive its service has when it listens alone.
func TestKeepAliveFollowsTheRoute(t *testing.T) {
	r := newRig(t, nil, nil)
	r.f.SetRoutes([]Route{
		{ID: "a", Names: []string{"a.example.com"}},
		{ID: "b", Names: []string{"b.example.com"}, KeepAlive: &net.KeepAliveConfig{Enable: false}},
		{ID: "c", Names: []string{"c.example.com"}, KeepAlive: &net.KeepAliveConfig{Enable: true, Idle: 7 * time.Second, Interval: 3 * time.Second, Count: 2}},
	}, "a")
	for sni, want := range map[string][4]int{
		"a.example.com": {1, 15, 15, 9}, // nil: what the listener set, Go's default
		"b.example.com": {0, 0, 0, 0},
		"c.example.com": {1, 7, 3, 2},
	} {
		c := r.dial()
		_, _ = c.Write(helloOf(t, sni))
		got, err := accept(t, r.f.Listener(sni[:1]), 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		on, idle, interval, count := sockopts(t, got)
		if on != want[0] || (on == 1 && [3]int{idle, interval, count} != [3]int{want[1], want[2], want[3]}) {
			t.Errorf("%s: keepalive %d idle %d interval %d count %d, want %v", sni, on, idle, interval, count, want)
		}
		_ = got.Close()
	}
}
