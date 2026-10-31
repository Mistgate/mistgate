//go:build linux

package main

import (
	"context"
	"os/exec"

	"github.com/vishvananda/netlink"

	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/node/warp"
)

// cleanupOwnedIPv6 removes the IPv6 addresses that ipv6-owned.json in the state directory names.
func cleanupOwnedIPv6(ctx context.Context, stateDir string) error {
	return hostctl.CleanupOwnedIPv6(ctx, stateDir)
}

// warpTraces says whether the WARP manager left anything on this host: the device, a rule at one of its preferences that
// selects its table (the "oif" rule or a client-subnet rule), or its nft table. An orphaned "unreachable default" is always
// selected by one of those rules, so it is found through them.
func warpTraces(ctx context.Context) bool {
	if _, err := netlink.LinkByName(warp.DefaultIface); err == nil {
		return true
	}
	for _, fam := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		rules, err := netlink.RuleList(fam)
		if err != nil {
			continue
		}
		for _, r := range rules {
			if r.Table == warp.DefaultTable && (r.Priority == warp.DefaultOifPref || r.Priority == warp.DefaultSubnetPref) {
				return true
			}
		}
	}
	return exec.CommandContext(ctx, "nft", "list", "table", "inet", warp.NftTable).Run() == nil
}
