package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/node/warp"
)

// cmdCleanupNet removes what a crashed or stopped agent leaves in the network of the host: the UDP check and tunnel nft
// tables and every mgawg* / mgwarp link, and the WARP routes, rules, device and nft table. It
// needs no state and no panel, so the unit runs it from ExecStopPost after every stop: an orphaned "unreachable default"
// in the WARP table would black-hole every socket still bound to the WARP device, and a kernel-backend interface
// outlives its process. The agent re-creates all of it at its next start.
//
// The IPv6 addresses the agent holds for the panel are not part of that: they stay through a stop, a restart and an update,
// or the node's IPv6 entry would drop out of reach each time. Only --ipv6-addresses removes them (a hand clean of the
// server), and only the ones that ipv6-owned.json in the state directory names; the agent's retire does the same.
func cmdCleanupNet(args []string) int {
	fs := flag.NewFlagSet("cleanup-net", flag.ContinueOnError)
	ipv6 := fs.Bool("ipv6-addresses", false, "also remove the IPv6 addresses the agent added (those named in ipv6-owned.json), never the host's own")
	stateDir := fs.String("state-dir", envOr("MISTGATE_NODE_STATE_DIR", defaultStateDir), "state directory (for --ipv6-addresses)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "cleanup-net takes no arguments")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	errs := []error{hostctl.CleanupTunnels(ctx), hostctl.CleanupUDPCount(ctx), cleanupWarp(ctx)}
	if *ipv6 {
		errs = append(errs, cleanupOwnedIPv6(ctx, *stateDir))
	}
	if err := errors.Join(errs...); err != nil {
		fmt.Fprintln(os.Stderr, "cleanup-net:", err)
		return 1
	}
	return 0
}

// cleanupWarp runs warp.CleanupHost only when there is something of the WARP manager to remove. CleanupHost deletes only
// what is ours (our rule preferences, the device's routes, our floor route), but the table number, 51820, is also what
// wg-quick uses by default: on a node that never served WARP, this command (which runs after every stop of every node)
// should not even look at the routing of another tool. The traces are looked for by what only we create: the device, our rule preferences, our nft table.
func cleanupWarp(ctx context.Context) error {
	if !warpTraces(ctx) {
		return nil
	}
	return warp.CleanupHost(ctx, warp.Settings{})
}
