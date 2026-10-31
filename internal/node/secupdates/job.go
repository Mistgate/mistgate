package secupdates

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/node/awgprep"
)

const (
	UnitName       = "mistgate-security-updates"
	StatusFileName = "security-updates.json"

	// JobTimeout bounds the complete background task.
	JobTimeout = 20 * time.Minute
	// LockWait bounds the initial wait for a package manager already using dpkg.
	LockWait              = 10 * time.Minute
	aptLockTimeoutSeconds = int(LockWait / time.Second)
)

const CodeOverridden = "overridden"

var aptDailyTimers = []string{"apt-daily.timer", "apt-daily-upgrade.timer"}

// Job enables unattended security updates without applying upgrades itself.
type Job struct {
	StatusPath string
	Timeout    time.Duration
	LockWait   time.Duration
	Out        io.Writer
	Run        awgprep.Runner
	Sys        awgprep.Sys
	Now        func() time.Time
}

// Do runs the bounded job and leaves the shared awgprep status format at StatusPath when requested.
func (j Job) Do(ctx context.Context) error {
	limit := j.Timeout
	if limit <= 0 {
		limit = JobTimeout
	}
	lockWait := j.LockWait
	if lockWait <= 0 {
		lockWait = LockWait
	}
	now := j.Now
	if now == nil {
		now = time.Now
	}
	out := j.Out
	if out == nil {
		out = io.Discard
	}
	run := j.Run
	if run == nil {
		run = awgprep.ExecRunner
	}
	sys := j.Sys
	realSys := awgprep.RealSys()
	if sys.WriteFile == nil {
		sys.WriteFile = realSys.WriteFile
	}
	if sys.AptBusy == nil {
		sys.AptBusy = realSys.AptBusy
	}
	if sys.Sleep == nil {
		sys.Sleep = realSys.Sleep
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
		return &awgprep.Failure{Code: awgprep.CodeStepFailed, Detail: "could not write the security updates task status"}
	}

	err := runJob(ctx, out, run, sys, lockWait)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = &awgprep.Failure{Code: awgprep.CodeTimeout, Detail: fmt.Sprintf("the security updates task did not finish within %d minutes", max(int(limit/time.Minute), 1))}
		} else if errors.Is(ctx.Err(), context.Canceled) {
			err = &awgprep.Failure{Code: awgprep.CodeInterrupted, Detail: "the security updates task was interrupted before it finished"}
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
		return &awgprep.Failure{Code: awgprep.CodeStepFailed, Detail: "the security updates task finished but its status could not be written"}
	}
	fmt.Fprintln(out, "Automatic security updates are enabled.")
	return nil
}

func runJob(ctx context.Context, out io.Writer, run awgprep.Runner, sys awgprep.Sys, lockWait time.Duration) error {
	packageStatus, queryErr := run(ctx, out, "", "dpkg-query", "-W", "-f=${Status}", "unattended-upgrades")
	installed := queryErr == nil && strings.TrimSpace(packageStatus) == "install ok installed"
	if !installed {
		installArgs := installArgs()
		steps := []awgprep.Step{{Cmd: append([]string{"apt-get"}, installArgs...)}}
		if err := awgprep.WaitAptLock(ctx, out, steps, sys, lockWait); err != nil {
			return err
		}
		installOutput, installErr := run(ctx, out, "", append([]string{"apt-get"}, installArgs...)...)
		if installErr != nil {
			if awgprep.IsAptLockOutput(installOutput) {
				return &awgprep.Failure{Code: awgprep.CodeAptLock, Detail: "another package manager prevented access to the package lock"}
			}
			fmt.Fprintln(out, "package install failed; refreshing apt package lists and retrying once")
			_, _ = run(ctx, out, "", "apt-get", "update")
			installOutput, installErr = run(ctx, out, "", append([]string{"apt-get"}, installArgs...)...)
		}
		if installErr != nil {
			if awgprep.IsAptLockOutput(installOutput) {
				return &awgprep.Failure{Code: awgprep.CodeAptLock, Detail: "another package manager prevented access to the package lock"}
			}
			return &awgprep.Failure{Code: awgprep.CodeStepFailed, Detail: "installing unattended-upgrades failed"}
		}
	}

	periodic, err := readPeriodic(ctx, out, run)
	if err != nil {
		return &awgprep.Failure{Code: awgprep.CodeStepFailed, Detail: "could not read the effective APT::Periodic settings"}
	}
	if len(periodic.DisabledKeys()) > 0 {
		if sys.WriteFile == nil {
			return &awgprep.Failure{Code: awgprep.CodeStepFailed, Detail: "writing APT settings is unavailable"}
		}
		if err := sys.WriteFile(ConfigPath, periodic.ConfigForDisabled()); err != nil {
			return &awgprep.Failure{Code: awgprep.CodeStepFailed, Detail: "writing Mistgate's APT settings failed"}
		}
		periodic, err = readPeriodic(ctx, out, run)
		if err != nil {
			return &awgprep.Failure{Code: awgprep.CodeStepFailed, Detail: "could not verify the effective APT::Periodic settings"}
		}
		if len(periodic.DisabledKeys()) > 0 {
			return &awgprep.Failure{Code: CodeOverridden, Detail: "APT::Periodic settings remain disabled after writing Mistgate's override"}
		}
	}

	states, _ := readTimerStates(ctx, out, run)
	var unmask, enable []string
	for i, timer := range aptDailyTimers {
		state := "unknown"
		if i < len(states) {
			state = states[i]
		}
		if state == "masked" {
			unmask = append(unmask, timer)
		}
		if state != "enabled" {
			enable = append(enable, timer)
		}
	}
	if len(unmask) > 0 {
		args := append([]string{"systemctl", "unmask"}, unmask...)
		if _, err := run(ctx, out, "", args...); err != nil {
			return &awgprep.Failure{Code: awgprep.CodeStepFailed, Detail: "unmasking the apt-daily timers failed"}
		}
	}
	if len(enable) > 0 {
		args := append([]string{"systemctl", "enable", "--now"}, enable...)
		if _, err := run(ctx, out, "", args...); err != nil {
			return &awgprep.Failure{Code: awgprep.CodeStepFailed, Detail: "enabling the apt-daily timers failed"}
		}
	}
	return nil
}

func readPeriodic(ctx context.Context, out io.Writer, run awgprep.Runner) (Periodic, error) {
	args := append([]string{AptConfigCommand}, strings.Fields(AptPeriodicArgs)...)
	result, err := run(ctx, out, "", args...)
	if err != nil {
		return Periodic{}, err
	}
	return ParsePeriodicShell(result), nil
}

func readTimerStates(ctx context.Context, out io.Writer, run awgprep.Runner) ([]string, error) {
	args := append([]string{"systemctl", "is-enabled"}, aptDailyTimers...)
	result, err := run(ctx, out, "", args...)
	states := strings.Fields(strings.TrimSpace(result))
	for i := range states {
		states[i] = strings.ToLower(states[i])
	}
	return states, err // systemctl returns nonzero when a timer is disabled or masked; its lines are still useful.
}

func installArgs() []string {
	return []string{
		"-o", fmt.Sprintf("DPkg::Lock::Timeout=%d", aptLockTimeoutSeconds),
		"-o", "Dpkg::Options::=--force-confdef",
		"-o", "Dpkg::Options::=--force-confold",
		"install", "-y", "--no-install-recommends", "unattended-upgrades",
	}
}
