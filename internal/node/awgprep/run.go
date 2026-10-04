package awgprep

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Failure is why a run did not end with a loaded module. Code is one of the Code* constants; Detail is one short English
// sentence (what the status file, the event and the UI carry, never package-manager output: that is in the journal of
// the job); Long, when set, is the fuller text the command prints to a person.
type Failure struct {
	Code, Detail, Long string
}

func (f *Failure) Error() string {
	if f.Long != "" {
		return f.Long
	}
	return f.Detail
}

// Short returns the Failure behind err (a plain error is a step failure), for the status file.
func Short(err error) (code, detail string) {
	var f *Failure
	if errors.As(err, &f) {
		return f.Code, clip(f.Detail, 200)
	}
	return CodeStepFailed, clip(err.Error(), 200)
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Runner executes one command and returns its combined output (also streamed to w by the real one).
type Runner func(ctx context.Context, w io.Writer, dir string, cmd ...string) (string, error)

// Sys is the part of the machine the run and the verification touch.
type Sys struct {
	WriteFile func(path, body string) error
	Exists    func(path string) bool
	// Probe asks the kernel for the genetlink family: version and maxattr, or an error when it is not loaded.
	Probe func() (genlVersion uint32, maxAttr uint32, err error)
	// AptBusy says whether another package manager holds the dpkg or apt lock. Nil = never.
	AptBusy func() bool
	// Sleep waits d or until ctx ends. Nil = the real clock.
	Sleep func(ctx context.Context, d time.Duration)
}

func (s Sys) sleep(ctx context.Context, d time.Duration) {
	if s.Sleep != nil {
		s.Sleep(ctx, d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// Progress is told before step i of n (1-based) starts. Optional.
type Progress func(i, n int)

// lockWords are what apt, dpkg and add-apt-repository print when another package manager holds the lock.
var lockWords = []string{"could not get lock", "unable to acquire the dpkg", "unable to lock directory", "is held by process", "waiting for cache lock"}

func lockBusy(out string) bool {
	out = strings.ToLower(out)
	for _, w := range lockWords {
		if strings.Contains(out, w) {
			return true
		}
	}
	return false
}

// ctxFailure is the Failure of a run whose context ended, or nil while it is alive.
func ctxFailure(ctx context.Context, limit time.Duration) *Failure {
	switch {
	case ctx.Err() == nil:
		return nil
	case errors.Is(context.Cause(ctx), context.DeadlineExceeded):
		return &Failure{Code: CodeTimeout, Detail: fmt.Sprintf("it did not finish within %d minutes and was stopped", max(int(limit/time.Minute), 1))}
	}
	return &Failure{Code: CodeInterrupted, Detail: "it was interrupted before it finished"}
}

// Run executes the plan (steps already have {src} replaced by src) and then verifies the module: the module is loaded
// (/sys/module, not modinfo: on ARM two trees can hold one name), speaks genl v3 with room for the 3.1 attributes, and
// is loaded at boot (modules-load.d). Nothing is rolled back on failure; the error says where it stopped. limit is only
// used to word a timeout (the caller owns the deadline of ctx).
func Run(ctx context.Context, w io.Writer, steps []Step, src string, run Runner, sys Sys, limit time.Duration, progress Progress) error {
	for i, s := range steps {
		if f := ctxFailure(ctx, limit); f != nil {
			return f
		}
		if progress != nil {
			progress(i+1, len(steps))
		}
		cmd := make([]string, len(s.Cmd))
		for j, a := range s.Cmd {
			cmd[j] = strings.ReplaceAll(a, "{src}", src)
		}
		fmt.Fprintf(w, "\n[%d/%d] %s\n  $ %s\n", i+1, len(steps), s.Desc, strings.Join(cmd, " "))
		out, err := run(ctx, w, s.Dir, cmd...)
		if err != nil {
			if f := ctxFailure(ctx, limit); f != nil {
				return f
			}
			f := &Failure{Code: CodeStepFailed,
				Detail: fmt.Sprintf("step %d of %d failed: %s (%v)", i+1, len(steps), s.Desc, err),
				Long:   fmt.Sprintf("step %d (%s) failed: %v (nothing is rolled back; fix the cause and run the command again)", i+1, strings.Join(cmd, " "), err)}
			if lockBusy(out) {
				f.Code = CodeAptLock
				f.Detail = fmt.Sprintf("step %d of %d: another package manager (apt, dpkg, unattended-upgrades) prevented access to the package lock", i+1, len(steps))
			}
			return f
		}
		if s.Check != nil {
			if err := s.Check(out); err != nil {
				var f *Failure
				if errors.As(err, &f) {
					return &Failure{Code: f.Code, Detail: f.Detail, Long: fmt.Sprintf("step %d: %v", i+1, err)}
				}
				return fmt.Errorf("step %d: %w", i+1, err)
			}
		}
	}
	return Verify(w, sys)
}

// Verify is also the whole of --verify-only. A module that works but cannot be set to load at boot is a failure: the
// node would lose it at the next reboot, and "kernel" mode has no fallback.
func Verify(w io.Writer, sys Sys) error {
	if !sys.Exists("/sys/module/amneziawg") {
		return &Failure{Code: CodeModuleNotLoad, Detail: "the module was built but is not loaded (Secure Boot refuses an unsigned module)",
			Long: "/sys/module/amneziawg is missing: the module is not loaded (modprobe amneziawg says why; Secure Boot rejects an unsigned module)"}
	}
	gv, ma, err := sys.Probe()
	if err != nil {
		return &Failure{Code: CodeModuleUnusable, Detail: "the module is loaded but does not answer on generic netlink",
			Long: fmt.Sprintf("the module is loaded but its genetlink family does not answer: %v", err)}
	}
	if gv != 3 || ma < 34 {
		return &Failure{Code: CodeModuleUnusable, Detail: fmt.Sprintf("the loaded module is too old for AmneziaWG 3.1 (genl v%d, maxattr %d)", gv, ma),
			Long: fmt.Sprintf("the module speaks genl v%d with maxattr %d: AWG 3.1 needs genl v3 and maxattr >= 34 (module v3.1.20260812 or newer); the agent would refuse a 3.1 profile on it", gv, ma)}
	}
	if err := sys.WriteFile(modulesLoadFile, "# Managed by mistgate-node awg prepare-kernel.\namneziawg\n"); err != nil {
		return &Failure{Code: CodeModuleUnusable, Detail: "the module works but could not be set to load at boot",
			Long: fmt.Sprintf("the module works but could not be set to load at boot: %v", err)}
	}
	fmt.Fprintf(w, "\nOK: the amneziawg kernel module is loaded (genl v%d, maxattr %d) and set to load at boot (%s).\n", gv, ma, modulesLoadFile)
	fmt.Fprintln(w, "Set the node's AWG backend to \"kernel\" (or leave it \"auto\", which takes the module when it is loaded) and restart the agent so it re-probes.")
	return nil
}

// WaitAptLock waits, at most max, until no other package manager holds the dpkg or apt lock, instead of letting the
// first apt-get fail at once. It returns nil when the lock is free or the plan uses no apt; an apt_lock Failure when
// it never was.
func WaitAptLock(ctx context.Context, w io.Writer, steps []Step, sys Sys, max time.Duration) error {
	uses := false
	for _, s := range steps {
		uses = uses || (len(s.Cmd) > 0 && strings.HasPrefix(s.Cmd[0], "apt"))
	}
	if !uses || sys.AptBusy == nil || !sys.AptBusy() {
		return nil
	}
	fmt.Fprintln(w, "another package manager holds the apt/dpkg lock; waiting for it")
	const poll = 3 * time.Second
	for tries := 0; sys.AptBusy(); tries++ { // counted, not timed: the wait is bounded and a test needs no clock
		if ctx.Err() != nil {
			return ctxFailure(ctx, max)
		}
		if time.Duration(tries)*poll >= max {
			return &Failure{Code: CodeAptLock, Detail: fmt.Sprintf("another package manager (apt, dpkg, unattended-upgrades) held the lock for more than %d minutes", int(max/time.Minute))}
		}
		sys.sleep(ctx, poll)
	}
	return nil
}
