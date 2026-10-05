package egress

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apernet/hysteria/extras/v2/outbounds"
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

// IPv4OnlyWhen follows its switch on every dial: IPv6 destinations are refused while it is on, IPv4 ones are not
// touched, and the egress is back to normal when it goes off (no rebuild, no restart).
func TestIPv4OnlyWhenFollowsTheSwitch(t *testing.T) {
	addr6, _ := countingServer(t, "tcp6", "[::1]:0")
	addr4, _ := countingServer(t, "tcp", "127.0.0.1:0")
	var off atomic.Bool
	e := New(nil, AllowPrivate(), IPv4OnlyWhen(off.Load))
	dial := func(addr string) error {
		c, err := e.TCP(addr)
		if err == nil {
			c.Close()
		}
		return err
	}
	if err := dial(addr6); err != nil {
		t.Skipf("no IPv6 loopback here: %v", err)
	}
	off.Store(true)
	if err := dial(addr6); !errors.Is(err, ErrNoIPv4) {
		t.Fatalf("an IPv6 destination while IPv6 is off: %v", err)
	}
	if err := e.CheckUDP(addr6); !errors.Is(err, ErrNoIPv4) {
		t.Fatalf("CheckUDP of an IPv6 destination while IPv6 is off: %v", err)
	}
	if err := dial(addr4); err != nil {
		t.Fatalf("an IPv4 destination while IPv6 is off: %v", err)
	}
	off.Store(false)
	if err := dial(addr6); err != nil {
		t.Fatalf("IPv6 after the switch went back on: %v", err)
	}
}

// A name with both records resolves to its IPv4 address while IPv6 is off; one with an AAAA only is refused.
func TestIPv4OnlyWhenPicksTheARecord(t *testing.T) {
	var off atomic.Bool
	off.Store(true)
	s := &stage{res: &resolver{cache: map[string]cacheEntry{"dual.test": {v4: net.IPv4(192, 0, 2, 7), v6: net.ParseIP("2001:db8::7"), exp: time.Now().Add(time.Minute)},
		"six.test": {v6: net.ParseIP("2001:db8::8"), exp: time.Now().Add(time.Minute)}}},
		self: &selfAddrs{list: func() ([]netip.Addr, error) { return nil, nil }, now: time.Now}, v4When: off.Load}
	a := &outbounds.AddrEx{Host: "dual.test", Port: 443}
	if err := s.prepare(a); err != nil || a.ResolveInfo.IPv4 == nil || a.ResolveInfo.IPv6 != nil {
		t.Fatalf("dual-stack name: %+v, %v", a.ResolveInfo, err)
	}
	if err := s.prepare(&outbounds.AddrEx{Host: "six.test", Port: 443}); !errors.Is(err, ErrNoIPv4) {
		t.Fatalf("AAAA-only name: %v", err)
	}
	off.Store(false)
	b := &outbounds.AddrEx{Host: "dual.test", Port: 443}
	if err := s.prepare(b); err != nil || b.ResolveInfo.IPv6 == nil {
		t.Fatalf("with IPv6 on both addresses stay: %+v, %v", b.ResolveInfo, err)
	}
}
