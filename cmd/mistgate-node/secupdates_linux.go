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
	"github.com/mistgate/mistgate/internal/node/secupdates"
)

const hostSecurityUpdatesUsage = `usage: mistgate-node host enable-security-updates [--yes] [--status-file FILE] [--timeout 20m]

  enable-security-updates  install unattended-upgrades if needed, enable APT periodic security updates and the apt-daily
                           timers. Without --yes this only describes the changes. It does not install upgrades or reboot.
                           --status-file is for the agent to follow the background task.
`

func cmdHost(args []string) int {
	if len(args) == 0 || args[0] != "enable-security-updates" {
		fmt.Fprint(os.Stderr, hostSecurityUpdatesUsage)
		return 2
	}
	fs := flag.NewFlagSet("host enable-security-updates", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "apply the changes")
	statusFile := fs.String("status-file", "", "write task state here for the agent")
	timeout := fs.Duration("timeout", secupdates.JobTimeout, "hard limit of the task")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if !*yes {
		fmt.Println("This command can install unattended-upgrades, enable APT periodic settings and enable the apt-daily timers.")
		fmt.Println("It does not install package upgrades or reboot. Read the command and run it again with --yes to continue.")
		return 0
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "host enable-security-updates: must run as root")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	job := secupdates.Job{StatusPath: *statusFile, Timeout: *timeout, Out: os.Stdout, Run: awgprep.ExecRunner, Sys: awgprep.RealSys()}
	if err := job.Do(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "host enable-security-updates:", err)
		return 1
	}
	return 0
}
