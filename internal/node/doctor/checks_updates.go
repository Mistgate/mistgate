package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/mistgate/mistgate/internal/node/secupdates"
)

var aptDailyTimers = []string{"apt-daily.timer", "apt-daily-upgrade.timer"}

func checkSecurityUpdates(ctx context.Context, e *Env) Result {
	osRelease, err := e.read("/etc/os-release")
	if err != nil || !isDebianOrUbuntu(osRelease) || !e.has(secupdates.AptConfigCommand) {
		return skip(CodeSecurityUpdatesNotAPT, "not a Debian or Ubuntu host: only apt is checked")
	}
	if _, err := e.stat("/run/systemd/system"); err != nil {
		return skip(CodeSecurityUpdatesNoSystemd, "systemd is not PID 1: the apt timers cannot run here")
	}

	packageStatus, packageErr := e.Run(ctx, "dpkg-query", "-W", "-f=${Status}", "unattended-upgrades")
	if packageErr != nil || strings.TrimSpace(string(packageStatus)) != "install ok installed" {
		return Result{Status: Warn, Code: CodeSecurityUpdatesNotInstalled,
			Detail: "unattended-upgrades is not installed: security fixes arrive only when someone runs apt by hand"}
	}

	config, configErr := e.Run(ctx, secupdates.AptConfigCommand, strings.Fields(secupdates.AptPeriodicArgs)...)
	periodic := secupdates.ParsePeriodicShell(string(config))
	off := periodic.OffDetails()
	if configErr != nil && len(off) == 0 {
		off = append(off, "apt-config failed")
	}

	timerOutput, _ := e.Run(ctx, "systemctl", append([]string{"is-enabled"}, aptDailyTimers...)...)
	off = append(off, disabledTimerDetails(string(timerOutput))...)
	if len(off) > 0 {
		return Result{Status: Warn, Code: CodeSecurityUpdatesOff,
			Detail: "automatic updates are off: " + strings.Join(off, ", "), Params: p("off", strings.Join(off, ", "))}
	}
	return Result{Status: OK, Code: CodeSecurityUpdatesOn, Detail: "automatic security updates are on"}
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

func disabledTimerDetails(output string) []string {
	states := strings.Fields(strings.TrimSpace(output))
	var off []string
	for i, timer := range aptDailyTimers {
		state := "unknown"
		if i < len(states) {
			state = strings.ToLower(states[i])
		}
		if state != "enabled" {
			off = append(off, fmt.Sprintf("%s %s", timer, state))
		}
	}
	return off
}
