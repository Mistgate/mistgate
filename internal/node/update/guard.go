package update

import "strings"

// GuardScript is the crash-loop guard the generation 2 systemd unit runs as ExecStartPre, one line of POSIX sh.
// Arguments: $1 the binary, $2 the state dir. While update.pending exists it counts the supervisor's starts in
// update.starts (a re-exec is not a supervisor start, so only real crashes count); the third start restores <bin>.prev
// and leaves update.rolledback{,.reason} for the restored build to report. It always exits 0: a broken guard must never
// keep the agent from starting.
//
// It avoids backslashes, single quotes and percent signs, so it survives systemd's command-line parsing once every
// $ is doubled (UnitGuardLine does that).
const GuardScript = `m="$2/update.pending"; c="$2/update.starts"; ` +
	`if [ ! -f "$m" ]; then rm -f "$c"; exit 0; fi; ` +
	`n=$(cat "$c" 2>/dev/null); case "$n" in ""|*[!0-9]*) n=0;; esac; n=$((n+1)); echo "$n" > "$c"; ` +
	`[ "$n" -ge 3 ] || exit 0; ` +
	`[ -f "$1.prev" ] || exit 0; ` +
	`mv -f "$1.prev" "$1" || exit 0; ` +
	`mv -f "$m" "$2/update.rolledback"; echo crash_loop > "$2/update.rolledback.reason"; rm -f "$c"; exit 0`

// UnitGuardLine is the ExecStartPre line for a unit: the script as one single-quoted word ($ doubled for systemd),
// then $0 and the two arguments. The leading "-" makes systemd ignore a failing guard.
func UnitGuardLine(bin, stateDir string) string {
	return "ExecStartPre=-/bin/sh -c '" + strings.ReplaceAll(GuardScript, "$", "$$") + "' mistgate-guard " + bin + " " + stateDir
}
