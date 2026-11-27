//go:build !js

package subhost

import (
	"context"
	"net/netip"
	"slices"
	"strings"

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
	addrs := s.addressesOf(ctx, n.Address)
	if a, err := netip.ParseAddr(n.IPv6Entry); err == nil {
		addrs = append(addrs, a)
	}
	for _, c := range n.IPv6().Checks {
		if a, err := netip.ParseAddr(c.Addr); err == nil {
			addrs = append(addrs, a)
		}
	}
	if !facts.HasIPv6 {
		addrs = slices.DeleteFunc(addrs, func(a netip.Addr) bool { return a.Unmap().Is6() })
	}
	return s.compareDNS(ctx, addrs, domain, true), nil
}

// addressesOf is a server's address as given (an IP, or a name that is resolved).
func (s *Service) addressesOf(ctx context.Context, address string) []netip.Addr {
	if a, err := netip.ParseAddr(strings.Trim(address, "[]")); err == nil {
		return []netip.Addr{a}
	}
	addrs, _ := s.cfg.Resolver.LookupNetIP(ctx, "ip", address)
	return addrs
}

// compareDNS compares the domain's addresses with want: at least one A record, every A record in want; with check6, every
// AAAA record in want too (without it AAAA records are not looked at: the server's IPv6 is not known yet).
func (s *Service) compareDNS(ctx context.Context, want []netip.Addr, domain string, check6 bool) dnsCheck {
	var want4, want6 []netip.Addr
	for _, a := range want {
		a = a.Unmap()
		switch {
		case a.Is4() && !slices.Contains(want4, a):
			want4 = append(want4, a)
		case a.Is6() && !slices.Contains(want6, a):
			want6 = append(want6, a)
		}
	}
	c := dnsCheck{}
	for _, a := range append(slices.Clone(want4), want6...) {
		c.expected = append(c.expected, a.String())
	}
	got, err := s.cfg.Resolver.LookupNetIP(ctx, "ip", domain)
	if err != nil {
		return c
	}
	any4, ok := false, true
	for _, a := range got {
		a = a.Unmap()
		c.found = append(c.found, a.String())
		if a.Is4() {
			any4 = true
			ok = ok && slices.Contains(want4, a)
		} else if check6 {
			ok = ok && slices.Contains(want6, a)
		}
	}
	c.matches = any4 && ok
	return c
}
