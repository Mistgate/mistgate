package doctor

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mistgate/mistgate/internal/node/awgprep"
	"github.com/mistgate/mistgate/internal/node/secupdates"
)

var aptDailyTimers = []string{"apt-daily.timer", "apt-daily-upgrade.timer"}

type aptTimerState struct {
	name  string
	state string
}

type securityUpdatesSnapshot struct {
	supported bool
	skip      Result
	installed bool
	periodic  secupdates.Periodic
	configErr error
	timers    []aptTimerState
	status    awgprep.Status
	hasStatus bool
	// The transient unit of the job as systemd sees it (LoadState, ActiveState); "" when systemctl did not say. A unit
	// that does not exist is LoadState "not-found" with ActiveState "inactive".
	loadState   string
	activeState string
}

// jobRunning: systemd runs the job's unit right now, whatever its status file says yet (the job writes "running" only
// once it has started, so right after the launch the file may still hold the previous run).
func (s securityUpdatesSnapshot) jobRunning() bool {
	switch s.activeState {
	case "active", "activating", "deactivating", "reloading", "refreshing":
		return true
	}
	return false
}

// jobLoaded: systemd still holds a unit of that name, so systemd-run would refuse a new one ("Unit ... already exists").
func (s securityUpdatesSnapshot) jobLoaded() bool {
	return s.loadState != "" && s.loadState != "not-found"
}

// parseUnitShow reads LoadState and ActiveState from `systemctl show` output (Key=Value lines, in any order).
func parseUnitShow(out string) (load, active string) {
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "LoadState":
			load = strings.ToLower(strings.TrimSpace(v))
		case "ActiveState":
			active = strings.ToLower(strings.TrimSpace(v))
		}
	}
	return load, active
}

func inspectSecurityUpdates(ctx context.Context, e *Env) securityUpdatesSnapshot {
	osRelease, err := e.read("/etc/os-release")
	if err != nil || !isDebianOrUbuntu(osRelease) || !e.has(secupdates.AptConfigCommand) {
		return securityUpdatesSnapshot{skip: skip(CodeSecurityUpdatesNotAPT, "not a Debian or Ubuntu host: only apt is checked")}
	}
	if _, err := e.stat("/run/systemd/system"); err != nil {
		return securityUpdatesSnapshot{skip: skip(CodeSecurityUpdatesNoSystemd, "systemd is not PID 1: the apt timers cannot run here")}
	}

	s := securityUpdatesSnapshot{supported: true}
	if e.StateDir != "" {
		s.status, s.hasStatus, _ = awgprep.ReadStatus(filepath.Join(e.StateDir, secupdates.StatusFileName))
	}
	shown, _ := e.Run(ctx, "systemctl", "show", "--property=LoadState,ActiveState", secupdates.UnitName+".service")
	s.loadState, s.activeState = parseUnitShow(string(shown))

	packageStatus, packageErr := e.Run(ctx, "dpkg-query", "-W", "-f=${Status}", "unattended-upgrades")
	s.installed = packageErr == nil && strings.TrimSpace(string(packageStatus)) == "install ok installed"

	config, configErr := e.Run(ctx, secupdates.AptConfigCommand, strings.Fields(secupdates.AptPeriodicArgs)...)
	s.periodic, s.configErr = secupdates.ParsePeriodicShell(string(config)), configErr
	timerOutput, _ := e.Run(ctx, "systemctl", append([]string{"is-enabled"}, aptDailyTimers...)...)
	s.timers = parseAptTimerStates(string(timerOutput))
	return s
}

func checkSecurityUpdates(ctx context.Context, e *Env) Result {
	return checkSecurityUpdatesResult(inspectSecurityUpdates(ctx, e))
}

func checkSecurityUpdatesResult(s securityUpdatesSnapshot) Result {
	if !s.supported {
		return s.skip
	}
	if s.jobRunning() {
		return Result{Status: Warn, Code: CodeSecurityUpdatesEnabling, Detail: "turning automatic security updates on"}
	}
	if s.hasStatus && s.status.State == awgprep.StateRunning {
		return securityUpdatesEnableFailed("the background task was interrupted before it finished")
	}
	off := securityUpdatesOffDetails(s)
	needsChanges := !s.installed || len(off) > 0
	if s.hasStatus && s.status.State == awgprep.StateFailed && needsChanges {
		reason := strings.TrimSpace(s.status.Reason)
		if reason == "" {
			reason = "the background task failed"
		}
		return securityUpdatesEnableFailed(reason)
	}
	if !s.installed {
		return Result{Status: Warn, Code: CodeSecurityUpdatesNotInstalled,
			Detail: "unattended-upgrades is not installed: security fixes arrive only when someone runs apt by hand",
			FixID:  FixEnableSecurityUpdates}
	}
	if len(off) > 0 {
		return Result{Status: Warn, Code: CodeSecurityUpdatesOff,
			Detail: "automatic updates are off: " + strings.Join(off, ", "),
			Params: p("off", strings.Join(off, ", ")), FixID: FixEnableSecurityUpdates}
	}
	return Result{Status: OK, Code: CodeSecurityUpdatesOn, Detail: "automatic security updates are on"}
}

func securityUpdatesOffDetails(s securityUpdatesSnapshot) []string {
	off := s.periodic.OffDetails()
	if s.configErr != nil && len(off) == 0 {
		off = append(off, "apt-config failed")
	}
	for _, timer := range s.timers {
		if timer.state != "enabled" {
			off = append(off, fmt.Sprintf("%s %s", timer.name, timer.state))
		}
	}
	return off
}

func securityUpdatesEnableFailed(reason string) Result {
	reason = clip(strings.TrimSpace(reason), 150)
	return Result{Status: Warn, Code: CodeSecurityUpdatesEnableFailed,
		Detail: "turning updates on failed: " + reason, Params: p("reason", reason), FixID: FixEnableSecurityUpdates}
}

func parseAptTimerStates(output string) []aptTimerState {
	states := strings.Fields(strings.TrimSpace(output))
	timers := make([]aptTimerState, len(aptDailyTimers))
	for i, name := range aptDailyTimers {
		state := "unknown"
		if i < len(states) {
			state = strings.ToLower(states[i])
		}
		timers[i] = aptTimerState{name: name, state: state}
	}
	return timers
}

func isDebianOrUbuntu(osRelease string) bool {
	var id, idLike string
	for _, line := range strings.Split(osRelease, "\n") {
		name, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(name) {
		case "ID":
			id = strings.ToLower(value)
		case "ID_LIKE":
			idLike = strings.ToLower(value)
		}
	}
	for _, candidate := range append([]string{id}, strings.Fields(idLike)...) {
		if candidate == "debian" || candidate == "ubuntu" {
			return true
		}
	}
	return false
}
