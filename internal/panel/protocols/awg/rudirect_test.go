package awg

import (
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func ruDirectNets(t *testing.T) (allowed, excluded []netip.Prefix) {
	t.Helper()
	for _, s := range RUDirectAllowedIPs() {
		allowed = append(allowed, netip.MustParsePrefix(s))
	}
	for _, g := range ruDirectExcluded {
		for _, f := range strings.Fields(g.nets) {
			excluded = append(excluded, netip.MustParsePrefix(f))
		}
	}
	return allowed, excluded
}

func covered(nets []netip.Prefix, a netip.Addr) bool {
	return slices.ContainsFunc(nets, func(p netip.Prefix) bool { return p.Contains(a) })
}

func TestRUDirectAllowedProperties(t *testing.T) {
	allowed, excluded := ruDirectNets(t)
	if len(allowed) > ruDirectMaxAllowed {
		t.Fatalf("%d entries: phones fail near 2400, the list must stay under %d", len(allowed), ruDirectMaxAllowed)
	}
	// the rebuild of 2026-11-24: 169 networks of the services, 1 366 entries
	if len(excluded) != 169 || len(allowed) != 1366 {
		t.Fatalf("excluded %d allowed %d, want 169 and 1366 (update the numbers with the list)", len(excluded), len(allowed))
	}
	for _, p := range excluded {
		n := uint32(addr4(p.Addr())) + uint32(1<<(32-p.Bits())-1)
		last := netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
		for _, a := range []netip.Addr{p.Addr(), last} {
			if covered(allowed, a) {
				t.Fatalf("%s of %s is inside the tunnel", a, p)
			}
		}
	}
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "203.0.113.5", "0.0.0.1", "255.255.255.254"} {
		if !covered(allowed, netip.MustParseAddr(s)) {
			t.Errorf("%s must go through the tunnel", s)
		}
	}
	for _, s := range []string{"5.45.196.64"} { // the checking host of Yandex: the node's address must not show there
		if covered(allowed, netip.MustParseAddr(s)) {
			t.Errorf("%s must go direct", s)
		}
	}
}

// The allowed networks and the excluded ones make the whole address space, with no overlap: the sizes add up to 2^32.
func TestRUDirectAllowedIsExactComplement(t *testing.T) {
	allowed, excluded := ruDirectNets(t)
	size := func(p netip.Prefix) uint64 { return 1 << (32 - p.Bits()) }
	var sum uint64
	for i, p := range allowed {
		sum += size(p)
		if i > 0 && addr4(allowed[i-1].Addr()) >= addr4(p.Addr()) {
			t.Fatalf("%s after %s: not in address order", p, allowed[i-1])
		}
		if p.Addr() != p.Masked().Addr() {
			t.Fatalf("%s has host bits", p)
		}
	}
	slices.SortFunc(excluded, func(a, b netip.Prefix) int { return int(int64(addr4(a.Addr())) - int64(addr4(b.Addr()))) })
	var end uint64
	for _, p := range excluded {
		lo := uint64(addr4(p.Addr()))
		if hi := lo + size(p); hi > end {
			sum += hi - max(lo, end)
			end = hi
		}
	}
	if sum != 1<<32 {
		t.Fatalf("allowed + excluded = %d addresses, want %d", sum, uint64(1)<<32)
	}
}
