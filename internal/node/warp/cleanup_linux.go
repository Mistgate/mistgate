//go:build linux

package warp

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// CleanupHost removes everything a run of the manager leaves on the host, by name, preference and table number only
// (no state needed), so the unit's ExecStopPost can call it after a crash: an orphaned "unreachable default" would
// black-hole every socket that still selects the table. It removes only what is ours: the rules with our two
// preferences that look up the WARP table, the routes of the table that go through the device, the fail-closed
// "unreachable default metric 4096" and the "throw" routes of our client subnets, the device and the nft table; one
// failure does not stop the rest. The table number is a setting because it is wg-quick's default (51820) too:
// another tool's rules and routes in the same table are left alone.
func CleanupHost(ctx context.Context, s Settings) error {
	s = s.withDefaults()
	if err := s.Validate(); err != nil {
		return err
	}
	return cleanupHost(ctx, s, execRunner)
}

// ownRule: a rule of ours is one of the two kinds Reassert installs ("oif <iface>" at OifPref, "from <subnet>" at SubnetPref).
func ownRule(s Settings, r netlink.Rule) bool {
	switch r.Priority {
	case s.OifPref:
		return r.OifName == s.Iface
	case s.SubnetPref:
		return r.Src != nil && r.OifName == ""
	}
	return false
}

func cleanupHost(ctx context.Context, s Settings, run runner) error {
	var errs []error
	ifIdx := -1
	if l, err := netlink.LinkByName(s.Iface); err == nil {
		ifIdx = l.Attrs().Index
	}
	for _, fam := range families {
		subnets := map[string]bool{} // sources of our rules: their "throw" routes are ours too
		if rules, err := netlink.RuleList(fam); err == nil {
			for _, r := range rules {
				if r.Table != s.Table || !ownRule(s, r) {
					continue
				}
				if r.Src != nil {
					if pf, ok := prefixOfNet(r.Src); ok {
						subnets[pf.Masked().String()] = true
					}
				}
				if err := netlink.RuleDel(&r); err != nil {
					errs = append(errs, fmt.Errorf("delete rule: %w", err))
				}
			}
		} else if !(fam == netlink.FAMILY_V6 && v6Unsupported(err)) {
			errs = append(errs, fmt.Errorf("list rules: %w", err))
		}
		if routes, err := netlink.RouteListFiltered(fam, &netlink.Route{Table: s.Table}, netlink.RT_FILTER_TABLE); err == nil {
			for _, r := range routes {
				own := (ifIdx > 0 && r.LinkIndex == ifIdx) ||
					(isDefault(r) && r.Type == unix.RTN_UNREACHABLE && r.Priority == unreachableMetric) ||
					(r.Type == unix.RTN_THROW && r.Dst != nil && subnets[dstKey(r.Dst)])
				if !own {
					continue
				}
				if err := netlink.RouteDel(&r); err != nil {
					errs = append(errs, fmt.Errorf("delete route: %w", err))
				}
			}
		} else if !(fam == netlink.FAMILY_V6 && v6Unsupported(err)) {
			errs = append(errs, fmt.Errorf("list routes: %w", err))
		}
	}
	if ifIdx > 0 {
		if l, err := netlink.LinkByName(s.Iface); err == nil {
			if err := netlink.LinkDel(l); err != nil {
				errs = append(errs, fmt.Errorf("delete %s: %w", s.Iface, err))
			}
		}
	}
	if _, err := run(ctx, "", "nft", "list", "table", "inet", s.NftTable); err == nil {
		if out, err := run(ctx, "", "nft", "delete", "table", "inet", s.NftTable); err != nil {
			errs = append(errs, fmt.Errorf("nft delete table: %s: %w", trimOut(out), err))
		}
	} else if errors.Is(err, exec.ErrNotFound) {
		// no nft binary: there is no table of ours either
	}
	return errors.Join(errs...)
}
