//go:build !js

package subhost

import (
	"context"
	"net/netip"
	"slices"

	"github.com/mistgate/mistgate/internal/panel/store"
)

type dnsCheck struct {
	matches         bool
	found, expected []string
}

// checkDNS compares the domain's addresses with the node's: there must be at least one A record and all of them the node's
// IPv4; AAAA records must be the node's IPv6 when it has IPv6, and absent when it has none (a client would try them first).
// A lookup that fails does not match and finds nothing. The error is the store's.
func (s *Service) checkDNS(ctx context.Context, n store.NodeRow, domain string) (dnsCheck, error) {
	facts, err := s.cfg.Store.NodeFacts(ctx, n.ID)
	if err != nil {
		return dnsCheck{}, err
	}
	var want4, want6 []netip.Addr
	add := func(a netip.Addr) {
		a = a.Unmap()
		switch {
		case a.Is4() && !slices.Contains(want4, a):
			want4 = append(want4, a)
		case a.Is6() && facts.HasIPv6 && !slices.Contains(want6, a):
			want6 = append(want6, a)
		}
	}
	if a, err := netip.ParseAddr(n.Address); err == nil {
		add(a)
	} else if addrs, err := s.cfg.Resolver.LookupNetIP(ctx, "ip", n.Address); err == nil {
		for _, a := range addrs {
			add(a)
		}
	}
	if a, err := netip.ParseAddr(n.IPv6Entry); err == nil {
		add(a)
	}
	for _, c := range n.IPv6().Checks {
		if a, err := netip.ParseAddr(c.Addr); err == nil {
			add(a)
		}
	}
	c := dnsCheck{}
	for _, a := range append(slices.Clone(want4), want6...) {
		c.expected = append(c.expected, a.String())
	}
	got, err := s.cfg.Resolver.LookupNetIP(ctx, "ip", domain)
	if err != nil {
		return c, nil
	}
	any4, ok := false, true
	for _, a := range got {
		a = a.Unmap()
		c.found = append(c.found, a.String())
		if a.Is4() {
			any4 = true
			ok = ok && slices.Contains(want4, a)
		} else {
			ok = ok && slices.Contains(want6, a)
		}
	}
	c.matches = any4 && ok
	return c, nil
}
