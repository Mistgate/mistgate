package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// fileLastVersion remembers which version started last in this state directory, so the next start can say whether it is
// an update (agent_started.prev_version differs) or a plain restart.
const fileLastVersion = "last_version"

// bootWindow is how young the host must be for an agent start to read as "the machine booted" rather than "the service
// restarted".
const bootWindow = 10 * time.Minute

// Why an agent started: agent_started.params.reason.
const (
	reasonUpdate  = "update"  // another version than last time, or an update marker is pending
	reasonBoot    = "boot"    // the host booted a moment ago
	reasonRestart = "restart" // anything else: the service was restarted (crash, systemctl, panel command)
)

// startReason decides the agent_started reason. updated = an update marker says this build was just installed;
// sinceBoot is the host's uptime (0 = unknown).
func startReason(prev, cur string, updated bool, sinceBoot time.Duration) string {
	switch {
	case updated || (prev != "" && prev != cur):
		return reasonUpdate
	case sinceBoot > 0 && sinceBoot < bootWindow:
		return reasonBoot
	}
	return reasonRestart
}

// announceStart queues one agent_started event for this process and records the version. It runs once at the start of
// Run, so it is the first event of the run and the engine starts that follow can be told apart from later ones.
func (a *Agent) announceStart(ctx context.Context) {
	p := filepath.Join(a.cfg.StateDir, fileLastVersion)
	b, _ := os.ReadFile(p)
	prev := strings.TrimSpace(string(b))
	updated := a.upMarker != nil
	if updated && prev == "" {
		prev = a.upMarker.FromVersion
	}
	var up time.Duration
	if f := a.host.Facts(ctx); !f.Boot.IsZero() {
		up = a.now().Sub(f.Boot)
	}
	reason := startReason(prev, a.cfg.Version, updated, up)
	params := map[string]string{"version": a.cfg.Version, "reason": reason}
	if prev != "" {
		params["prev_version"] = prev
	}
	a.event(pb.Severity_SEVERITY_INFO, "agent_started", "", params)
	if prev != a.cfg.Version {
		if err := writeFileAtomic(p, []byte(a.cfg.Version+"\n"), 0o600); err != nil {
			a.log.Warn("could not remember the started version", "err", err)
		}
	}
}
