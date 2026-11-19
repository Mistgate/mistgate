package vless

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"time"
)

// errTargetLocal is why an inbound whose REALITY target was set on the node does not start: the name leads to this machine or to a
// private network, where REALITY would hand strangers' connections to something that is not a site. The text is the code the admin
// UI words itself (web lib/inbound-error.ts).
var errTargetLocal = errors.New("reality_target: local")

// The resolver and the node's own addresses are seams for the tests, which never touch the network.
var (
	lookupTargetAddrs = func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}
	interfaceAddrs = func() ([]netip.Addr, error) {
		list, err := net.InterfaceAddrs()
		if err != nil {
			return nil, err
		}
		var out []netip.Addr
		for _, a := range list {
			if p, err := netip.ParsePrefix(a.String()); err == nil {
				out = append(out, p.Addr())
			}
		}
		return out, nil
	}
)

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),     // "this network"
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
}

// nonPublic is an address no site of the internet lives at: this machine, a private or link-local network, carrier-grade NAT.
func nonPublic(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLoopback() || a.IsUnspecified() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsMulticast() {
		return true
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// checkTargetPublic refuses a target ("host:port") that is, or whose name resolves to, a non-public address or one of the node's own.
// Any one such address is enough: the dial may pick it. A name that does not resolve is not refused here: the probe after the start
// reports it as unreachable.
// ponytail: the name is resolved here and again by the engine at each stranger's connection, so a DNS answer that changes after the
// start gets through; pinning the dial to the checked addresses needs a dialer in the engine's REALITY outbound.
func checkTargetPublic(ctx context.Context, target string) error {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return nil
	}
	host = strings.Trim(host, "[]")
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if addrs, err = lookupTargetAddrs(ctx, host); err != nil {
			return nil
		}
	}
	own, _ := interfaceAddrs()
	for _, a := range addrs {
		a = a.Unmap().WithZone("")
		if nonPublic(a) {
			return errTargetLocal
		}
		for _, o := range own {
			if o.Unmap().WithZone("") == a {
				return errTargetLocal
			}
		}
	}
	return nil
}
