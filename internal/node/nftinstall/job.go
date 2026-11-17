// Package nftinstall puts the nftables package on a server that lacks it (a cloud image of Debian 13 has no nft command,
// and the agent's firewall steps need it). The doctor's fix install_nftables starts it as a transient root unit, outside
// the agent's service sandbox, the way the security updates job runs; the status file carries the result back.
package nftinstall

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/mistgate/mistgate/internal/node/awgprep"
	"github.com/mistgate/mistgate/internal/node/nftmissing"
)

const (
	UnitName       = "mistgate-install-nftables"
	StatusFileName = "install-nftables.json"

	// JobTimeout bounds the task's waits; an apt-get that has started is never cut by it (awgprep.AptInstall), only the
	// unit's RuntimeMaxSec (30 minutes) stops a hung one.
	JobTimeout = 20 * time.Minute
	// LockWait bounds the wait for a package manager already using dpkg (a fresh image runs cloud-init and
	// unattended-upgrades in its first minutes).
	LockWait = 10 * time.Minute
)

// Package is what the job installs.
const Package = "nftables"

// Job installs nftables without upgrading anything else and without touching a running service.
type Job struct {
	StatusPath string
	Timeout    time.Duration
	LockWait   time.Duration
	Out        io.Writer
	Run        awgprep.Runner
	Sys        awgprep.Sys
	Now        func() time.Time
	// Installed says whether nft is there (nil = nftmissing.Installed); HasApt whether apt-get is (nil = on PATH).
	Installed func() bool
	HasApt    func() bool
}

// Do runs the bounded job and leaves the shared awgprep status format at StatusPath when asked.
func (j Job) Do(ctx context.Context) error {
	limit, lockWait, now, out, run, sys := j.Timeout, j.LockWait, j.Now, j.Out, j.Run, j.Sys
	if limit <= 0 {
		limit = JobTimeout
	}
	if lockWait <= 0 {
		lockWait = LockWait
	}
	if now == nil {
		now = time.Now
	}
	if out == nil {
		out = io.Discard
	}
	if run == nil {
		run = awgprep.ExecRunner
	}
	if sys.AptBusy == nil {
		sys.AptBusy = awgprep.RealSys().AptBusy
	}
	installed, hasApt := j.Installed, j.HasApt
	if installed == nil {
		installed = nftmissing.Installed
	}
	if hasApt == nil {
		hasApt = func() bool { _, err := exec.LookPath("apt-get"); return err == nil }
	}

	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	status := awgprep.Status{State: awgprep.StateRunning, Started: now().Unix(), Updated: now().Unix()}
	writeStatus := func() error {
		if j.StatusPath == "" {
			return nil
		}
		status.Updated = now().Unix()
		return awgprep.WriteStatus(j.StatusPath, status)
	}
	if err := writeStatus(); err != nil {
		return &awgprep.Failure{Code: awgprep.CodeStepFailed, Detail: "could not write the nftables task status"}
	}

	err := func() error {
		if installed() {
			fmt.Fprintln(out, "nft is already installed")
			return nil
		}
		if !hasApt() {
			return &awgprep.Failure{Code: awgprep.CodeStepFailed, Detail: "this server has no apt-get: install the nftables package with its package manager"}
		}
		if err := awgprep.AptInstall(ctx, out, run, sys, lockWait, Package, InstallArgs()); err != nil {
			return err
		}
		if !installed() {
			return &awgprep.Failure{Code: awgprep.CodeStepFailed, Detail: "the nftables package was installed but the nft command is still missing"}
		}
		return nil
	}()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = &awgprep.Failure{Code: awgprep.CodeTimeout, Detail: fmt.Sprintf("the nftables task did not finish within %d minutes", max(int(limit/time.Minute), 1))}
		} else if errors.Is(ctx.Err(), context.Canceled) {
			err = &awgprep.Failure{Code: awgprep.CodeInterrupted, Detail: "the nftables task was interrupted before it finished"}
		}
		code, reason := awgprep.Short(err)
		status.State, status.Code, status.Reason, status.Finished = awgprep.StateFailed, code, reason, now().Unix()
		if writeErr := writeStatus(); writeErr != nil {
			fmt.Fprintf(out, "could not write failed task status: %v\n", writeErr)
		}
		return err
	}
	status.State, status.Finished = awgprep.StateDone, now().Unix()
	if err := writeStatus(); err != nil {
		return &awgprep.Failure{Code: awgprep.CodeStepFailed, Detail: "the nftables task finished but its status could not be written"}
	}
	fmt.Fprintln(out, "nftables is installed; the agent picks up nft by itself within a minute.")
	return nil
}

// InstallArgs is the apt-get argument list: no questions, no recommended packages, the dpkg lock waited for.
func InstallArgs() []string {
	return []string{
		"-o", fmt.Sprintf("DPkg::Lock::Timeout=%d", int(LockWait/time.Second)),
		"-o", "Dpkg::Options::=--force-confdef",
		"-o", "Dpkg::Options::=--force-confold",
		"install", "-y", "--no-install-recommends", Package,
	}
}
