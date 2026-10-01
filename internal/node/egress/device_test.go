package egress

import (
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// countingServer accepts connections and counts them.
func countingServer(t *testing.T, network, addr string) (string, *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Skipf("cannot listen on %s: %v", addr, err)
	}
	t.Cleanup(func() { ln.Close() })
	n := new(atomic.Int64)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			c.Close()
		}
	}()
	return ln.Addr().String(), n
}

// A device that does not exist must fail the dial, never fall back to the direct route: that is what makes the WARP
// egress fail closed while the tunnel is paused or gone.
func TestWithDeviceMissingFailsClosed(t *testing.T) {
	addr, accepted := countingServer(t, "tcp", "127.0.0.1:0")
	e := New(nil, AllowPrivate(), WithDevice("mg3-nodev0"))
	for i := 0; i < 2; i++ { // the second try must rebuild after the failed first one, not cache the failure
		if c, err := e.TCP(addr); err == nil {
			c.Close()
			t.Fatal("dial through a missing device succeeded")
		}
	}
	if err := e.CheckUDP(addr); err == nil {
		t.Fatal("CheckUDP through a missing device succeeded")
	}
	if u, err := e.UDP(addr); err == nil {
		u.Close()
		t.Fatal("UDP through a missing device succeeded")
	}
	time.Sleep(100 * time.Millisecond)
	if accepted.Load() != 0 {
		t.Fatalf("%d connections reached the server around the missing device", accepted.Load())
	}
}

func TestIPv4OnlyRefusesV6(t *testing.T) {
	addr, _ := countingServer(t, "tcp6", "[::1]:0")
	plain := New(nil, AllowPrivate())
	c, err := plain.TCP(addr)
	if err != nil {
		t.Skipf("no IPv6 loopback here: %v", err)
	}
	c.Close()
	v4 := New(nil, AllowPrivate(), IPv4Only())
	if c, err := v4.TCP(addr); err == nil {
		c.Close()
		t.Fatal("an IPv4-only egress dialed an IPv6 address")
	} else if !strings.Contains(err.Error(), "IPv4") {
		t.Fatalf("unexpected error: %v", err)
	}
}
