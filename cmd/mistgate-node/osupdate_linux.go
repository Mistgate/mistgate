//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mistgate/mistgate/internal/node/awgprep"
	"github.com/mistgate/mistgate/internal/node/osupdate"
)

const hostOSUpdateUsage = `usage: mistgate-node host os-update [--yes] [--action refresh|upgrade|reboot] [--mode security|all]
                                    [--reboot never|if-required|always] [--awg-required] [--job-id ID]
                                    [--state-dir DIR | --status-file FILE] [--timeout 60m]

  Runs one OS job on this server (root, Debian or Ubuntu with systemd). Without --yes it only prints what it would do.
    --action  refresh  refresh the package lists only (--reboot never)
              upgrade  (default) install updates, then reboot as --reboot says; needs --mode
              reboot   no package change, only the reboot (--reboot if-required or always)
    --mode    security (default for upgrade) installs what unattended-upgrade installs; all installs every update
              (apt-get upgrade, nothing is removed)
    --reboot  never (default) | if-required (only when a reboot is needed) | always
    --awg-required  treat the AmneziaWG kernel module as needed. It is treated so anyway whenever the module is loaded
  Before a reboot the job refuses, and reboots nothing, when: dpkg or apt is busy, a package install was interrupted, the
  kernel of the next boot cannot be told or is older than the running one, the AmneziaWG module is loaded (or --awg-required)
  and not built for a kernel the boot menu can end up on (the newest and each lower one not older than the running kernel),
  boot files of one of them are missing or /boot is nearly full, GRUB may not start its first entry (GRUB_DEFAULT, a saved or
  next entry, no grub.cfg), or the host booted less than 10 minutes ago. The dpkg lock is held from the last check to the reboot.
  A failing DKMS build of the module stops the job (awg_module_build_failed); nothing is removed or installed after it.
  The state goes to <state-dir>/os-update.json (the agent reads it, also after the reboot). Run it as its own unit, so that
  a dropped SSH session cannot interrupt dpkg:
    systemd-run --unit=mistgate-os-update --collect --property=KillMode=mixed --property=TimeoutStopSec=600 mistgate-node host os-update --yes ...
  Cancel before the install starts: systemctl stop mistgate-os-update. An apt that has started is let finish (KillMode=mixed
  signals only the job; TimeoutStopSec bounds the wait).
`

func cmdHostOSUpdate(args []string) int {
	fs := flag.NewFlagSet("host os-update", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, hostOSUpdateUsage) }
	yes := fs.Bool("yes", false, "run the job (without it the plan is only printed)")
	action := fs.String("action", osupdate.ActionUpgrade, "refresh, upgrade or reboot")
	mode := fs.String("mode", "", "security or all (upgrade only; default security)")
	reboot := fs.String("reboot", "never", "never, if-required or always")
	jobID := fs.String("job-id", "", "job id (the agent passes the panel's)")
	awgRequired := fs.Bool("awg-required", false, "AmneziaWG runs on the kernel module")
	stateDir := fs.String("state-dir", envOr("MISTGATE_NODE_STATE_DIR", defaultStateDir), "state directory")
	statusFile := fs.String("status-file", "", "status file (default <state-dir>/os-update.json)")
	timeout := fs.Duration("timeout", osupdate.JobTimeout, "limit of the job's waits and quick steps")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *mode == "" && *action == osupdate.ActionUpgrade {
		*mode = osupdate.ModeSecurity
	}
	if *jobID == "" {
		*jobID = fmt.Sprintf("manual-%d", time.Now().Unix())
	}
	spec, ref := osupdate.ParseSpec(*action, *mode, *reboot, *jobID)
	if ref != nil {
		fmt.Fprintln(os.Stderr, "host os-update:", ref.Reason)
		return 2
	}
	spec.AwgRequired = *awgRequired
	if *statusFile == "" {
		abs, err := filepath.Abs(*stateDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "host os-update: state directory:", err)
			return 2
		}
		*statusFile = filepath.Join(abs, osupdate.StatusFileName)
	}
	head := "Plan (runs as root, changes packages):"
	if spec.Reboot != osupdate.RebootNever {
		head = "Plan (runs as root, changes packages and can reboot this server):"
	}
	fmt.Println(head)
	for i, step := range planLines(spec) {
		fmt.Printf("  %d. %s\n", i+1, step)
	}
	if !*yes {
		fmt.Println("\nNothing was run. Read the plan and run the command again with --yes.")
		return 0
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "host os-update: must run as root")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	job := osupdate.Job{Spec: spec, StatusPath: *statusFile, Timeout: *timeout, Out: os.Stdout, Run: awgprep.ExecRunner}
	if err := job.Do(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "host os-update:", err)
		return 1
	}
	return 0
}

const rebootChecksText = "no busy or interrupted dpkg, the kernel of the next boot known and not older than the running one, the AmneziaWG module (whenever it is loaded) and the boot files for every kernel the boot menu can end up on, GRUB starting its first entry, free space in /boot, at least 10 minutes of uptime, and for the panel's jobs at most one reboot an hour and three a day"

// planLines say what the job does for a spec, in the order it does it. The text is fixed; only the spec's enumerated
// values pick the lines.
func planLines(s osupdate.Spec) []string {
	checks := "check the system (Ubuntu 22.04, 24.04, 26.04, Debian 12, 13 with systemd) and the free space"
	var out []string
	switch s.Action {
	case osupdate.ActionRefresh:
		out = []string{checks, "wait for other package managers (up to 15 minutes)", "apt-get update (the sources are never edited)"}
	case osupdate.ActionUpgrade:
		install := "unattended-upgrade (the security updates; it is installed first when missing)"
		if s.Mode == osupdate.ModeAll {
			install = "apt-get upgrade --with-new-pkgs (every update; nothing is removed, old kernels stay)"
		}
		out = []string{checks, "wait for other package managers (up to 15 minutes)", "finish interrupted package installs (dpkg --configure -a) when there are any",
			"apt-get update", install, "when a new kernel came: ask DKMS to build the AmneziaWG module for it (Ubuntu); a build that fails stops the job (awg_module_build_failed), and nothing is removed or installed"}
	case osupdate.ActionReboot:
		out = []string{checks}
	}
	switch s.Reboot {
	case osupdate.RebootIfRequired:
		out = append(out, "reboot, as the very last step, only when a reboot is needed and the checks pass: "+rebootChecksText)
	case osupdate.RebootAlways:
		out = append(out, "reboot, as the very last step, when the checks pass: "+rebootChecksText)
	default:
		out = append(out, "no reboot")
	}
	return out
}
