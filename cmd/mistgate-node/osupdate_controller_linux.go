//go:build linux

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/node/awgprep"
	"github.com/mistgate/mistgate/internal/node/osupdate"
	"github.com/mistgate/mistgate/internal/node/secupdates"
	"github.com/mistgate/mistgate/internal/node/unit"
	"github.com/mistgate/mistgate/internal/node/update"
)

// hostWorkUnits are the transient units of the other jobs that touch dpkg or restart the agent. An OS job never runs next to
// one of them (and the agent refuses them while an OS job runs).
var hostWorkUnits = []string{awgprep.UnitName, secupdates.UnitName, unit.RefreshUnitName}

// newOSUpdate builds the agent's OS job controller: the same conditions as the security-updates launcher (Linux, root,
// systemd, systemd-run). Nil = the agent does not list "os-update/1".
func newOSUpdate(stateDir string, updater *update.Updater) *osupdate.Controller {
	if !osupdate.CapabilityAvailable() {
		return nil
	}
	stateDir, err := filepath.Abs(stateDir)
	if err != nil {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	status := filepath.Join(stateDir, osupdate.StatusFileName)
	return osupdate.New(osupdate.Config{
		StatusPath: status,
		Launch:     osupdate.SystemdRun(exe, status),
		Excluded: func(ctx context.Context) string {
			if updater != nil && updater.RefreshBusy() {
				return "an agent update or a unit refresh is going"
			}
			for _, name := range hostWorkUnits {
				if unitLoaded(ctx, name) {
					return "other host work is going (" + name + ")"
				}
			}
			return ""
		},
	})
}

// unitLoaded: systemd holds a unit of that name (a running transient unit, or a finished one not yet collected).
func unitLoaded(ctx context.Context, name string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "show", "--property=LoadState", "--value", name+".service").Output()
	if err != nil {
		return true // cannot tell: do not start a reboot-capable job next to something unknown
	}
	state := strings.TrimSpace(string(out))
	return state != "" && state != "not-found"
}
