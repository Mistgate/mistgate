package secupdates

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/node/unit"
)

type systemdCommand func(context.Context, string, ...string) ([]byte, error)

// SystemdRun starts the host-side task as a transient root unit. systemd-run returns when the unit starts; the job's
// status file carries the eventual result.
func SystemdRun(exe, statusPath string) func(context.Context) error {
	return func(ctx context.Context) error {
		return launchSystemdRun(ctx, exe, statusPath, runSystemdCommand)
	}
}

func launchSystemdRun(ctx context.Context, exe, statusPath string, run systemdCommand) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{
		"--quiet", "--collect", "--no-ask-password", "--unit=" + UnitName,
		"--property=RuntimeMaxSec=1800", "--", exe, "host", "enable-security-updates", "--yes",
		"--status-file", statusPath, "--timeout", "20m",
	}
	out, err := run(cctx, "systemd-run", args...)
	if err == nil {
		return nil
	}
	if systemdUnitExists(out) {
		return unit.ErrBusy
	}
	message := strings.TrimSpace(string(out))
	if message == "" {
		message = err.Error()
	}
	return fmt.Errorf("systemd-run: %s", message)
}

func runSystemdCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

func systemdUnitExists(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "already exists") || strings.Contains(s, "already loaded") || strings.Contains(s, "unitexists")
}
