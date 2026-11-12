package osupdate

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/node/unit"
)

// UnitName is the transient systemd unit a job lives in: `journalctl -u mistgate-os-update` is where its output is. systemd
// refuses a second unit of the same name while one exists, which is one more guard against two jobs.
const UnitName = "mistgate-os-update"

type systemdCommand func(context.Context, string, ...string) ([]byte, error)

// SystemdRun starts a job as a transient root unit. The unit has no sandbox of its own (a package install needs the whole
// system) and none of the agent's: it is a separate process tree outside the agent's cgroup and its memory limit, so the
// agent's unit is not loosened for it and an agent restart does not stop the job. systemd-run returns when the unit starts;
// how the job ends is in its status file.
func SystemdRun(exe, statusPath string) func(context.Context, Spec) error {
	return func(ctx context.Context, spec Spec) error {
		return launchSystemdRun(ctx, exe, statusPath, spec, runSystemdCommand)
	}
}

// Args is the command line of the job: built from the enumerated fields of the Spec and two paths, nothing else.
// ParseSpec has checked every field before this, and the arguments never go through a shell.
func (s Spec) Args(exe, statusPath string) []string {
	args := []string{exe, "host", "os-update", "--yes", "--action", s.Action}
	if s.Mode != "" {
		args = append(args, "--mode", s.Mode)
	}
	args = append(args, "--reboot", s.Reboot, "--job-id", s.JobID, "--status-file", statusPath, "--timeout", JobTimeout.String())
	if s.AwgRequired {
		args = append(args, "--awg-required")
	}
	return args
}

func launchSystemdRun(ctx context.Context, exe, statusPath string, spec Spec, run systemdCommand) error {
	if _, ref := ParseSpec(spec.Action, spec.Mode, spec.Reboot, spec.JobID); ref != nil || spec.JobID == "" {
		return fmt.Errorf("refusing to launch an invalid job")
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// KillMode=mixed: `systemctl stop` (the way to cancel a job) and RuntimeMaxSec send SIGTERM to the job's main process only;
	// the apt and dpkg it started are not signalled, and the job waits for the step they are in. TimeoutStopSec is how long
	// systemd waits for that before it kills what is left. (The default, control-group, would SIGTERM dpkg in mid-transaction.)
	// Nice keeps the install from competing with the node's traffic.
	args := []string{
		"--quiet", "--collect", "--no-ask-password", "--unit=" + UnitName,
		"--description=Mistgate: operating system updates",
		fmt.Sprintf("--property=RuntimeMaxSec=%d", RuntimeMaxSec), "--property=KillMode=mixed", "--property=TimeoutStopSec=600", "--property=Nice=10", "--",
	}
	args = append(args, spec.Args(exe, statusPath)...)
	out, err := run(cctx, "systemd-run", args...)
	if err == nil {
		return nil
	}
	if unitExists(out) {
		return unit.ErrBusy
	}
	message := strings.TrimSpace(string(out))
	if message == "" {
		message = err.Error()
	}
	return fmt.Errorf("systemd-run: %s", message)
}

func runSystemdCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func unitExists(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "already exists") || strings.Contains(s, "already loaded") || strings.Contains(s, "unitexists")
}
