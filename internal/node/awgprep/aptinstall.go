package awgprep

import (
	"context"
	"fmt"
	"io"
	"time"
)

// AptInstall runs `apt-get args...` (an install) the way the agent's host jobs do: it waits up to lockWait for another
// package manager to let go of the dpkg and apt locks, never cuts an apt-get that has started when ctx ends (dpkg killed
// in the middle of a transaction leaves the host half configured; only the unit's RuntimeMaxSec stops a hung one), and
// on a failure that is not the lock refreshes the package lists and tries once more (a fresh cloud image has none).
// what names the package for the failure's sentence.
func AptInstall(ctx context.Context, out io.Writer, run Runner, sys Sys, lockWait time.Duration, what string, args []string) error {
	steps := []Step{{Cmd: append([]string{"apt-get"}, args...)}}
	if err := WaitAptLock(ctx, out, steps, sys, lockWait); err != nil {
		return err
	}
	install := func() (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return run(context.WithoutCancel(ctx), out, "", append([]string{"apt-get"}, args...)...)
	}
	lockFailure := &Failure{Code: CodeAptLock, Detail: "another package manager prevented access to the package lock"}
	output, err := install()
	if err != nil && ctx.Err() == nil {
		if IsAptLockOutput(output) {
			return lockFailure
		}
		fmt.Fprintln(out, "package install failed; refreshing apt package lists and retrying once")
		_, _ = run(ctx, out, "", "apt-get", "update")
		output, err = install()
	}
	if err != nil {
		if IsAptLockOutput(output) {
			return lockFailure
		}
		return &Failure{Code: CodeStepFailed, Detail: "installing " + what + " failed"}
	}
	return nil
}
