package fleet

import (
	"slices"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// NodeSettings.ipv6_addresses and ipv6_addresses_managed go only to an agent that lists ipv6-addr/1, and only that agent's
// settings signature counts them: the signature of an older agent never moves. The flag goes with the list, an empty one
// too, so that an agent can tell "the panel says none" from a panel that never heard of the field.
func TestIPv6AddressesNeedTheCapability(t *testing.T) {
	n := store.NodeRow{ClientIPv6: true}
	oldSig := settingsSig(nodeSettings(n, nil))

	empty := nodeSettings(n, []string{capIPv6Addr})
	if !empty.Ipv6AddressesManaged || len(empty.Ipv6Addresses) != 0 {
		t.Fatalf("a capable agent is told %+v", empty)
	}
	if settingsSig(empty) == oldSig {
		t.Fatal("the signature of a capable agent does not say that the list is managed")
	}
	if settingsSig(nodeSettings(n, []string{capIPv6Addr})) != settingsSig(empty) {
		t.Fatal("the signature is not stable")
	}

	prev := ownedIPv6Addresses
	t.Cleanup(func() { ownedIPv6Addresses = prev })
	ownedIPv6Addresses = func(store.NodeRow) []string { return []string{"2001:db8:1::beef"} }

	for _, caps := range [][]string{nil, {capClientIPv6, capAWG}} {
		old := nodeSettings(n, caps)
		if len(old.Ipv6Addresses) != 0 || old.Ipv6AddressesManaged {
			t.Fatalf("an agent without ipv6-addr/1 (%v) was sent %v managed=%v", caps, old.Ipv6Addresses, old.Ipv6AddressesManaged)
		}
		if got := settingsSig(old); strings.Contains(got, "ipv6-addr") {
			t.Fatalf("signature of an agent without the capability = %q", got)
		}
	}
	if got := settingsSig(nodeSettings(n, nil)); got != oldSig {
		t.Fatalf("old agent signature %q, was %q", got, oldSig)
	}
	cur := nodeSettings(n, []string{capIPv6Addr})
	if !cur.Ipv6AddressesManaged || !slices.Equal(cur.Ipv6Addresses, []string{"2001:db8:1::beef"}) {
		t.Fatalf("settings of a new agent = %+v", cur)
	}
	if settingsSig(cur) == settingsSig(empty) {
		t.Fatal("a new address list does not change the settings signature")
	}
	other := nodeSettings(n, []string{capIPv6Addr})
	other.Ipv6Addresses = []string{"2001:db8:1::cafe"}
	if settingsSig(other) == settingsSig(cur) {
		t.Fatal("another address gives the same signature")
	}
}
