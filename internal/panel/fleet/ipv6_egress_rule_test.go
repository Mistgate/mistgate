package fleet

import (
	"net/netip"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// The client API tells a client about IPv6 by store.IPv6Doctor.Works, and the node page words the same states as the reasons an IPv6
// entry cannot be offered: the two must not drift apart. no_ipv6, ipv6_broken and not_checked are "not known to work"; a working
// uplink is "works", whether or not the agent names its address (agent_old).
func TestIPv6EgressRuleMatchesTheNodePageReasons(t *testing.T) {
	n := store.NodeRow{Address: "de1.example.com"}
	addr := []netip.Prefix{netip.MustParsePrefix("2001:db8::5/64")}
	for _, c := range []struct {
		name   string
		d      store.IPv6Doctor
		reason string
		works  bool
	}{
		{"no address", store.IPv6Doctor{State: store.IPv6None}, "no_ipv6", false},
		{"broken", store.IPv6Doctor{State: store.IPv6Broken}, "ipv6_broken", false},
		{"unknown", store.IPv6Doctor{State: store.IPv6Unknown}, "not_checked", false},
		{"not reported", store.IPv6Doctor{}, "not_checked", false},
		{"working, no address named", store.IPv6Doctor{State: store.IPv6OK}, "agent_old", true},
		{"working", store.IPv6Doctor{State: store.IPv6OK, Addrs: addr, Src: "2001:db8::5"}, "", true},
	} {
		if got := ipv6Unavailable(n, c.d); got != c.reason {
			t.Errorf("%s: reason %q, want %q", c.name, got, c.reason)
		}
		if got := c.d.Works(); got != c.works {
			t.Errorf("%s: Works = %v, want %v", c.name, got, c.works)
		}
	}
}
