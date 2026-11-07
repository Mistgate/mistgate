//go:build linux

package agent

import (
	"context"
	"os"
	"os/exec"

	"github.com/mistgate/mistgate/internal/node/unit"
)

const maxProbeOutput = 64 << 10

func probeOutput(ctx context.Context, name string, args ...string) string {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil && len(out) == 0 {
		return ""
	}
	return string(out[:min(len(out), maxProbeOutput)])
}

// defaultExitProbe asks systemd and the journals. Without a service manager (no INVOCATION_ID: a foreground run, a test, a
// container) or without access to the journal it returns what it could read, which may be nothing.
func defaultExitProbe(ctx context.Context) exitFacts {
	if os.Getenv("INVOCATION_ID") == "" {
		return exitFacts{Restarts: -1}
	}
	show := probeOutput(ctx, "systemctl", "show", "--property=NRestarts,Result", unit.ServiceName)
	unitLog := probeOutput(ctx, "journalctl", "--unit="+unit.ServiceName, "--output=cat", "--no-pager", "--quiet", "--lines=60")
	kernel := probeOutput(ctx, "journalctl", "--dmesg", "--output=cat", "--no-pager", "--quiet", "--lines=300", "--since=-30min")
	return parseExitFacts(show, unitLog, kernel)
}
