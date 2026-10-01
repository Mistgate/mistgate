//go:build linux

package warp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Reassert makes the WARP routing match rs, all through netlink (no `ip` exec):
//
//	table T:  default dev <iface>            (only while the device is up; v6 only with an IPv6 account address)
//	          unreachable default metric 4096   (always: the fail-closed floor, both families)
//	          throw <client subnet>             (one per subnet: traffic to a client, e.g. the node's own replies and ICMP
//	                                            errors from the subnet's .1 address, which the "from subnet" rule would
//	                                            otherwise capture, continues with the main table and reaches the client)
//	rules:    pref OifPref    oif <iface>           lookup T   (sockets bound to the device: hysteria DeviceName)
//	          pref SubnetPref from <client subnet>  lookup T   (one per AWG subnet with egress "warp")
//
// Everything in table T or pointing at it that is not in this list is removed; foreign rules and tables are
// never touched. Idempotent: a second call with the same rs changes nothing.
func (p *linuxPlane) Reassert(ctx context.Context, rs routeSpec) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var errs []error
	changed := false
	for _, step := range []func(routeSpec) (bool, error){p.syncRoutes, p.syncRules} {
		c, err := step(rs)
		changed = changed || c
		if err != nil {
			errs = append(errs, err)
		}
	}
	c, err := p.syncNft(ctx, rs)
	changed = changed || c
	if err != nil {
		errs = append(errs, err)
	}
	if rs.LinkUp {
		if err := p.setRPFilter(); err != nil {
			p.log.Warn("cannot set rp_filter=2 on the WARP device", "err", err)
		}
	}
	return changed, errors.Join(errs...)
}

var families = []int{netlink.FAMILY_V4, netlink.FAMILY_V6}

func defaultNet(fam int) *net.IPNet {
	if fam == netlink.FAMILY_V6 {
		return &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)}
	}
	return &net.IPNet{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(0, 32)}
}

func isDefault(r netlink.Route) bool {
	if r.Dst == nil {
		return true
	}
	ones, _ := r.Dst.Mask.Size()
	return ones == 0
}

// v6Unsupported: IPv6 is compiled out or disabled on this host, which is not an error for the WARP path.
func v6Unsupported(err error) bool {
	return errors.Is(err, unix.EAFNOSUPPORT) || errors.Is(err, unix.EPROTONOSUPPORT)
}

// ifIndex is the index of the WARP device, 0 when it does not exist.
func (p *linuxPlane) ifIndex() int {
	if l, err := netlink.LinkByName(p.s.Iface); err == nil {
		return l.Attrs().Index
	}
	return 0
}

// ownRoute: a route of the WARP table that Reassert installs: the "throw" routes of the client subnets, the
// "unreachable default metric 4096" floor, and a default exactly via the WARP device without a gateway. Anything
// else in the table (wg-quick uses 51820 too) belongs to another tool: Preflight reports it, syncRoutes leaves it.
func ownRoute(r netlink.Route, ifIdx int) bool {
	switch {
	case r.Type == unix.RTN_THROW:
		return true
	case !isDefault(r):
		return false
	case r.Type == unix.RTN_UNREACHABLE:
		return r.Priority == unreachableMetric
	case r.Type == unix.RTN_UNICAST:
		return ifIdx > 0 && r.LinkIndex == ifIdx && r.Gw == nil && r.Via == nil && len(r.MultiPath) == 0
	}
	return false
}

func (p *linuxPlane) syncRoutes(rs routeSpec) (bool, error) {
	ifIdx := p.ifIndex()
	var link netlink.Link
	if rs.Configured && rs.LinkUp {
		link, _ = netlink.LinkByName(p.s.Iface)
	}
	changed := false
	var errs []error
	for _, fam := range families {
		cur, err := netlink.RouteListFiltered(fam, &netlink.Route{Table: p.s.Table}, netlink.RT_FILTER_TABLE)
		if err != nil {
			if fam == netlink.FAMILY_V6 && v6Unsupported(err) {
				continue
			}
			errs = append(errs, fmt.Errorf("list routes: %w", err))
			continue
		}
		wantThrow := map[string]bool{}
		if rs.Configured {
			for _, sn := range rs.Subnets {
				if (fam == netlink.FAMILY_V6) == sn.Addr().Is6() {
					wantThrow[sn.Masked().String()] = true
				}
			}
		}
		haveThrow := map[string]bool{}
		wantU := rs.Configured
		wantD := rs.Configured && link != nil && (fam == netlink.FAMILY_V4 || rs.HasV6)
		haveU, haveD := false, false
		foreign := 0
		for _, r := range cur {
			switch {
			case !ownRoute(r, ifIdx):
				foreign++ // never deleted: it is another tool's
			case wantU && !haveU && isDefault(r) && r.Type == unix.RTN_UNREACHABLE:
				haveU = true
			case wantD && !haveD && isDefault(r) && r.Type == unix.RTN_UNICAST:
				haveD = true
			case r.Type == unix.RTN_THROW && r.Dst != nil && wantThrow[dstKey(r.Dst)] && !haveThrow[dstKey(r.Dst)]:
				haveThrow[dstKey(r.Dst)] = true
			default:
				if err := netlink.RouteDel(&r); err != nil {
					errs = append(errs, fmt.Errorf("delete route: %w", err))
				}
				changed = true
			}
		}
		if foreign > 0 && rs.Configured {
			errs = append(errs, fmt.Errorf("routing table %d holds %d route(s) that are not ours (family %d)", p.s.Table, foreign, fam))
		}
		if wantU && !haveU {
			err := netlink.RouteReplace(&netlink.Route{Dst: defaultNet(fam), Table: p.s.Table, Type: unix.RTN_UNREACHABLE, Priority: unreachableMetric, Family: fam})
			if err != nil {
				errs = append(errs, fmt.Errorf("unreachable default: %w", err))
			}
			changed = true
		}
		for k := range wantThrow {
			if haveThrow[k] {
				continue
			}
			pf := netip.MustParsePrefix(k)
			dst := &net.IPNet{IP: pf.Addr().AsSlice(), Mask: net.CIDRMask(pf.Bits(), pf.Addr().BitLen())}
			if err := netlink.RouteReplace(&netlink.Route{Dst: dst, Table: p.s.Table, Type: unix.RTN_THROW, Family: fam}); err != nil {
				errs = append(errs, fmt.Errorf("throw %s: %w", k, err))
			}
			changed = true
		}
		if wantD && !haveD {
			// RouteAdd, not Replace: a foreign default with the same key must not be overwritten (EEXIST fails the reassert)
			err := netlink.RouteAdd(&netlink.Route{Dst: defaultNet(fam), LinkIndex: link.Attrs().Index, Table: p.s.Table, Scope: netlink.SCOPE_LINK, Family: fam})
			if err != nil {
				errs = append(errs, fmt.Errorf("default via %s: %w", p.s.Iface, err))
			}
			changed = true
		}
	}
	return changed, errors.Join(errs...)
}

func dstKey(n *net.IPNet) string {
	if pf, ok := prefixOfNet(n); ok {
		return pf.Masked().String()
	}
	return ""
}

type ruleKey struct {
	fam  int
	pref int
	src  string
	oif  string
}

func ruleKeyOf(fam int, r netlink.Rule) ruleKey {
	k := ruleKey{fam: fam, pref: r.Priority, oif: r.OifName}
	if r.Src != nil {
		if pf, ok := prefixOfNet(r.Src); ok {
			k.src = pf.Masked().String()
		}
	}
	return k
}

func (p *linuxPlane) wantedRules(rs routeSpec) map[ruleKey]bool {
	want := map[ruleKey]bool{}
	if !rs.Configured {
		return want
	}
	for _, fam := range families {
		want[ruleKey{fam: fam, pref: p.s.OifPref, oif: p.s.Iface}] = true
	}
	for _, sn := range rs.Subnets {
		fam := netlink.FAMILY_V4
		if sn.Addr().Is6() {
			fam = netlink.FAMILY_V6
		}
		want[ruleKey{fam: fam, pref: p.s.SubnetPref, src: sn.Masked().String()}] = true
	}
	return want
}

func (p *linuxPlane) syncRules(rs routeSpec) (bool, error) {
	want := p.wantedRules(rs)
	changed := false
	var errs []error
	have := map[ruleKey]bool{}
	for _, fam := range families {
		cur, err := netlink.RuleList(fam)
		if err != nil {
			if fam == netlink.FAMILY_V6 && v6Unsupported(err) {
				continue
			}
			errs = append(errs, fmt.Errorf("list rules: %w", err))
			continue
		}
		for _, r := range cur {
			if r.Table != p.s.Table {
				continue // not ours
			}
			k := ruleKeyOf(fam, r)
			if want[k] && !have[k] {
				have[k] = true
				continue
			}
			if err := netlink.RuleDel(&r); err != nil {
				errs = append(errs, fmt.Errorf("delete rule: %w", err))
			}
			changed = true
		}
	}
	for k := range want {
		if have[k] {
			continue
		}
		r := netlink.NewRule()
		r.Family, r.Priority, r.Table = k.fam, k.pref, p.s.Table
		if k.oif != "" {
			r.OifName = k.oif
		}
		if k.src != "" {
			pf := netip.MustParsePrefix(k.src)
			r.Src = &net.IPNet{IP: pf.Addr().AsSlice(), Mask: net.CIDRMask(pf.Bits(), pf.Addr().BitLen())}
		}
		if err := netlink.RuleAdd(r); err != nil {
			if k.fam == netlink.FAMILY_V6 && v6Unsupported(err) {
				continue
			}
			errs = append(errs, fmt.Errorf("add rule pref %d: %w", k.pref, err))
		}
		changed = true
	}
	return changed, errors.Join(errs...)
}
