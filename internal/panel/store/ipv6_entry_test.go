package store

import "testing"

func TestIPv6StateKeepsTheNewestCheckPerAddressAndFewAddresses(t *testing.T) {
	var s IPv6State
	if s.JSON() != "{}" || ParseIPv6State("").JSON() != "{}" || ParseIPv6State("not json").JSON() != "{}" {
		t.Fatalf("an empty document is {}: %q", s.JSON())
	}
	for i, a := range []string{"2001:db8::1", "2001:db8::2", "2001:db8::3", "2001:db8::4", "2001:db8::5"} {
		s = s.With(IPv6Check{Addr: a, At: int64(i + 1)})
	}
	if len(s.Checks) != ipv6KeepChecks || s.Checks[0].Addr != "2001:db8::5" || s.Checks[3].Addr != "2001:db8::2" {
		t.Fatalf("checks: %+v", s.Checks)
	}
	s = s.With(IPv6Check{Addr: "2001:db8::3", At: 9, Pass: true})
	if len(s.Checks) != ipv6KeepChecks || s.Checks[0].Addr != "2001:db8::3" || !s.Checks[0].Pass || s.Checks[1].Addr != "2001:db8::5" {
		t.Fatalf("a newer check of the same address replaces the older: %+v", s.Checks)
	}
	if c, ok := s.CheckOf("2001:db8::3"); !ok || c.At != 9 {
		t.Errorf("CheckOf = %+v %v", c, ok)
	}
	if _, ok := s.CheckOf("2001:db8::1"); ok {
		t.Error("the oldest address was kept")
	}
	if c, ok := s.Latest(); !ok || c.Addr != "2001:db8::3" {
		t.Errorf("Latest = %+v", c)
	}
	if back := ParseIPv6State(s.JSON()); back.JSON() != s.JSON() {
		t.Errorf("round trip: %s != %s", back.JSON(), s.JSON())
	}
}

func TestIPv6StateEnabledAtIsBackwardCompatible(t *testing.T) {
	if got := ParseIPv6State(`{"checks":[]}`); got.EnabledAt != 0 {
		t.Errorf("old state has enabled_at %d, want zero", got.EnabledAt)
	}
	s := IPv6State{EnabledAt: 1790842000}.With(IPv6Check{Addr: "2001:db8::5", At: 1790842001, Pass: true})
	back := ParseIPv6State(s.JSON())
	if back.EnabledAt != s.EnabledAt || len(back.Checks) != 1 || back.Checks[0].Addr != "2001:db8::5" {
		t.Fatalf("state round trip = %+v", back)
	}
}
