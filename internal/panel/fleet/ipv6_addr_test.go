package fleet

import (
	"slices"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// NodeSettings.ipv6_addresses goes only to an agent that lists ipv6-addr/1, and only that agent's settings signature
// counts it: the signature of an older agent never moves, and with an empty list (today, always) nobody's does.
func TestIPv6AddressesNeedTheCapability(t *testing.T) {
	n := store.NodeRow{ClientIPv6: true}
	oldSig := settingsSig(nodeSettings(n, nil))
	newSig := settingsSig(nodeSettings(n, []string{capIPv6Addr}))
	if oldSig != newSig || len(nodeSettings(n, []string{capIPv6Addr}).Ipv6Addresses) != 0 {
		t.Fatalf("an empty list moved the signature: %q vs %q", oldSig, newSig)
	}

	prev := ownedIPv6Addresses
	t.Cleanup(func() { ownedIPv6Addresses = prev })
	ownedIPv6Addresses = func(store.NodeRow) []string { return []string{"2001:db8:1::beef"} }

	old := nodeSettings(n, []string{capClientIPv6, capAWG})
	if len(old.Ipv6Addresses) != 0 {
		t.Fatalf("an agent without ipv6-addr/1 was sent %v", old.Ipv6Addresses)
	}
	if got := settingsSig(old); got != settingsSig(nodeSettings(n, []string{capClientIPv6, capAWG, capIPv6Addr + "-x"})) {
		t.Fatalf("signature of an agent without the capability = %q", got)
	}
	if got := settingsSig(nodeSettings(n, nil)); got != oldSig {
		t.Fatalf("old agent signature %q, was %q", got, oldSig)
	}
	cur := nodeSettings(n, []string{capIPv6Addr})
	if !slices.Equal(cur.Ipv6Addresses, []string{"2001:db8:1::beef"}) {
		t.Fatalf("settings of a new agent = %v", cur.Ipv6Addresses)
	}
	if settingsSig(cur) == oldSig {
		t.Fatal("a new address list does not change the settings signature")
	}
	other := nodeSettings(n, []string{capIPv6Addr})
	other.Ipv6Addresses = []string{"2001:db8:1::cafe"}
	if settingsSig(other) == settingsSig(cur) {
		t.Fatal("another address gives the same signature")
	}
}
