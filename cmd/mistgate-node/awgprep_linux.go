//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mistgate/mistgate/internal/node/awgprep"
)

// `mistgate-node awg prepare-kernel`: the root-only step that puts the
// AmneziaWG kernel module on a node, implemented in internal/node/awgprep. The owner can run it by hand and read the plan
// it prints first (confirm with --yes). It is also what the agent starts, in the background, when the panel asks it to
// prepare the module (--status-file: the agent follows the run through that file; the agent itself never installs a package
// inside its hardened unit).

const awgUsage = `usage: mistgate-node awg prepare-kernel [--yes] [--verify-only] [--status-file FILE] [--timeout 45m]
       mistgate-node awg fix-source [--check]

  prepare-kernel   put the AmneziaWG kernel module on this node (root, Linux). Without --yes it only prints what it would
                   run; --yes runs it, with a hard timeout (default 45m) and without questions (apt is non-interactive and
                   waits up to 30 minutes for package-manager locks). --verify-only checks a module that is already there (loaded,
                   genl v3, loaded at boot). --status-file is for the agent: it reports progress and the result there.
                   The userspace backend needs no module.
  fix-source       repair the AmneziaWG module's source (/usr/src/amneziawg-*/compat/compat.h) when it is a file Mistgate knows to
                   break the build on some kernels (root, Linux). Any other file is left as it is and reported with the start of
                   its sha256. --check only reports.
`

// cmdAwgFixSource is `awg fix-source`: it exits 0 when every source is fine (ok, absent or patched now), 1 when one still needs
// the patch or is not a file Mistgate knows.
func cmdAwgFixSource(args []string) int {
	fs := flag.NewFlagSet("awg fix-source", flag.ContinueOnError)
	check := fs.Bool("check", false, "only report, change nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "awg fix-source: must run as root")
		return 1
	}
	dirs := awgprep.RealSourceDirs()
	if len(dirs) == 0 {
		fmt.Println("no AmneziaWG module source under /usr/src: nothing to do")
		return 0
	}
	code := 0
	for _, r := range awgprep.FixCompat(awgprep.RealSys(), dirs, *check, os.Stdout) {
		if r.State == awgprep.CompatNeedsPatch || r.State == awgprep.CompatUnknown || r.Err != "" {
			code = 1
		}
	}
	return code
}

func cmdAwg(args []string) int {
	if len(args) > 0 && args[0] == "fix-source" {
		return cmdAwgFixSource(args[1:])
	}
	if len(args) == 0 || args[0] != "prepare-kernel" {
		fmt.Fprint(os.Stderr, awgUsage)
		return 2
	}
	fs := flag.NewFlagSet("awg prepare-kernel", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "run the commands (without it the plan is only printed)")
	verify := fs.Bool("verify-only", false, "only check a module that is already installed")
	statusFile := fs.String("status-file", "", "write progress and the result here (used by the agent)")
	timeout := fs.Duration("timeout", awgprep.JobTimeout, "hard limit of the run")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "awg prepare-kernel: must run as root")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sys := awgprep.RealSys()
	if *verify {
		if err := awgprep.Verify(os.Stdout, sys); err != nil {
			fmt.Fprintln(os.Stderr, "awg prepare-kernel:", err)
			return 1
		}
		return 0
	}
	job := awgprep.Job{
		StatusPath: *statusFile, Timeout: *timeout, Out: os.Stdout, Env: awgprep.ReadEnv(ctx),
		Auto: *statusFile != "", // a run the panel asked for also refuses what only a person can fix halfway through
		Run:  awgprep.ExecRunner, Sys: sys,
	}
	steps, notes, err := job.Steps()
	if err != nil {
		if *statusFile != "" {
			_ = job.Do(ctx) // leaves the failed status the agent is waiting for
		}
		fmt.Fprintln(os.Stderr, "awg prepare-kernel:", err)
		return 1
	}
	fmt.Println("Plan (runs as root, installs packages and loads a kernel module):")
	for i, s := range steps {
		fmt.Printf("  %d. %s\n", i+1, s.Desc)
		for _, file := range s.Files {
			if file.Remove {
				fmt.Printf("       remove %s if present\n", file.Path)
			} else {
				fmt.Printf("       write %s\n", file.Path)
			}
		}
		if len(s.Cmd) > 0 {
			fmt.Printf("       $ %s\n", strings.Join(s.Cmd, " "))
		}
	}
	for _, n := range notes {
		fmt.Println("Note:", n)
	}
	if !*yes {
		fmt.Println("\nNothing was run. Read the plan and run the command again with --yes.")
		return 0
	}
	src, err := os.MkdirTemp("", "mg-awg-src-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "awg prepare-kernel:", err)
		return 1
	}
	defer os.RemoveAll(src)
	job.Src = filepath.Join(src, "module")
	started := time.Now()
	if err := job.Do(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "awg prepare-kernel:", err)
		return 1
	}
	fmt.Printf("(took %s)\n", time.Since(started).Round(time.Second))
	return 0
}
