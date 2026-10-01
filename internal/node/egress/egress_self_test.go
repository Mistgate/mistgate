package egress

import (
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

// A VPN user must not reach the node through its own public address (Linux routes it via lo).
func TestBlocksOwnAddresses(t *testing.T) {
	dnsAddr, _ := fakeDNS(t, map[string]string{"me.example.test.": "203.0.113.7"})
	now := time.Now()
	own := []netip.Addr{netip.MustParseAddr("203.0.113.7"), netip.MustParseAddr("2001:db8::7")}
	e := New(func() []string { return []string{dnsAddr} })
	st := e.ad.PluggableOutbound.(*stage)
	st.self = &selfAddrs{now: func() time.Time { return now }, list: func() ([]netip.Addr, error) { return own, nil }}

	// 203.0.113.7 is public unicast for vet(), so only the own-address check can stop it.
	for _, addr := range []string{"203.0.113.7:22", "[2001:db8::7]:22", "[::ffff:203.0.113.7]:22", "me.example.test:22"} {
		if _, err := e.TCP(addr); !errors.Is(err, ErrBlocked) {
			t.Errorf("TCP %s: want ErrBlocked, got %v", addr, err)
		}
		if err := e.CheckUDP(addr); !errors.Is(err, ErrBlocked) {
			t.Errorf("CheckUDP %s: want ErrBlocked, got %v", addr, err)
		}
	}
	// Not ours: passes the check (the dial itself may fail, but never with ErrBlocked).
	if err := e.CheckUDP("203.0.113.8:53"); errors.Is(err, ErrBlocked) {
		t.Errorf("a foreign address must not be blocked: %v", err)
	}

	// A new address (floating IP) is picked up after selfTTL, not before.
	own = append(own, netip.MustParseAddr("198.51.100.9"))
	if err := e.CheckUDP("198.51.100.9:53"); errors.Is(err, ErrBlocked) {
		t.Error("list is cached: the new address must not be seen before the TTL")
	}
	now = now.Add(selfTTL + time.Second)
	if err := e.CheckUDP("198.51.100.9:53"); !errors.Is(err, ErrBlocked) {
		t.Errorf("new address must be blocked after the refresh, got %v", err)
	}
	// A failing refresh keeps the last copy.
	now = now.Add(selfTTL + time.Second)
	st.self.list = func() ([]netip.Addr, error) { return nil, errors.New("netlink down") }
	if err := e.CheckUDP("203.0.113.7:53"); !errors.Is(err, ErrBlocked) {
		t.Errorf("stale list must still block, got %v", err)
	}
}

// With no list at all the check fails closed; the real interface list never lets a real own address through.
func TestSelfAddrsFailClosedAndReal(t *testing.T) {
	bad := &selfAddrs{now: time.Now, list: func() ([]netip.Addr, error) { return nil, errors.New("x") }}
	if !bad.has(netip.MustParseAddr("8.8.8.8")) {
		t.Error("no interface list yet: must fail closed")
	}
	as, err := interfaceAddrs()
	if err != nil {
		t.Skip(err)
	}
	if len(as) == 0 {
		t.Skip("no interface addresses")
	}
	real := &selfAddrs{now: time.Now, list: interfaceAddrs}
	for _, a := range as {
		if !real.has(a) {
			t.Errorf("own address %s must be recognised", a)
		}
	}
	// And through the full stage with a real list: every own public address is refused.
	e := New(nil)
	for _, a := range as {
		if vet(net.IP(a.AsSlice())) == nil {
			continue // already refused as non-public
		}
		if _, err := e.TCP(net.JoinHostPort(a.String(), "22")); !errors.Is(err, ErrBlocked) {
			t.Errorf("own public address %s reachable: %v", a, err)
		}
	}
}
