//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mistgate/mistgate/internal/node/awgprep"
	"github.com/mistgate/mistgate/internal/node/nftinstall"
)

func cmdHostInstallNftables(args []string) int {
	fs := flag.NewFlagSet("host install-nftables", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "apply the change")
	statusFile := fs.String("status-file", "", "write task state here for the agent")
	timeout := fs.Duration("timeout", nftinstall.JobTimeout, "hard limit of the task")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*yes {
		fmt.Println("This command installs the nftables package with apt-get when the nft command is missing.")
		fmt.Println("It upgrades nothing else and restarts nothing. Run it again with --yes to continue.")
		return 0
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "host install-nftables: must run as root")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	job := nftinstall.Job{StatusPath: *statusFile, Timeout: *timeout, Out: os.Stdout, Run: awgprep.ExecRunner, Sys: awgprep.RealSys()}
	if err := job.Do(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "host install-nftables:", err)
		return 1
	}
	return 0
}
