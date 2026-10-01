package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/node/warp"
)

// cmdCleanupNet removes what a crashed or stopped agent leaves in the network of the host: the tunnel nft table and every
// mgawg* / mgwarp link (hostctl.CleanupTunnels), and the WARP routes, rules, device and nft table (warp.CleanupHost). It
// needs no state and no panel, so the unit runs it from ExecStopPost after every stop: an orphaned "unreachable default"
// in the WARP table would black-hole every socket still bound to the WARP device, and a kernel-backend interface
// outlives its process. The agent re-creates all of it at its next start.
func cmdCleanupNet(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "cleanup-net takes no arguments")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := errors.Join(hostctl.CleanupTunnels(ctx), cleanupWarp(ctx)); err != nil {
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
