package agent

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Why the previous run ended, for agent_started. Everything here is best effort: a missing marker, a host without systemd,
// a journal the agent may not read or a slow command only mean fewer params, never a failed or a delayed start (the probe
// has a hard time limit). All params are additions to agent_started; a panel that does not know them ignores them.
//
//	prev_exit        clean | unclean | unknown (the first start in this state directory)
//	crash            "1": the previous run ended uncleanly and this is not a boot or an update (a crash restart)
//	crash_loop       crash restarts in the last 30 minutes, this one included (only with crash)
//	exit_code        the exit status of the previous main process, when systemd's journal says it
//	exit_signal      the signal that killed it (KILL, SEGV, ...), when it was killed
//	exit_result      systemd's result for the unit: exit-code, signal, core-dump, oom-kill, watchdog, timeout
//	oom              "1": the previous run was killed for memory (systemd's oom-kill result or the kernel's own line)
//	systemd_restarts the unit's NRestarts counter

const (
	// fileCleanExit is written when Run ends by itself (stop, retire, self-update) and removed at the next start. A run that
	// never wrote it was killed, crashed or lost its host.
	fileCleanExit = "clean_exit"
	// fileCrashLog keeps the Unix times of the crash restarts of the last crashLoopWindow, one per line.
	fileCrashLog = "crash_log"

	crashLoopWindow = 30 * time.Minute
	maxCrashLog     = 20
	exitProbeLimit  = 3 * time.Second
)

// exitFacts is what the service manager and the logs say about the previous run.
type exitFacts struct {
	Restarts int    // NRestarts; -1 = unknown
	Result   string // systemd Result of the unit
	Code     string // "exited", "killed" or "dumped" (ExecMainCode as the journal words it)
	Status   string // the exit status, or the signal number for "killed"
	Signal   string // the signal's name when killed
	OOM      bool
}

// exitProbe reads exitFacts, best effort and within exitProbeLimit; tests replace it.
type exitProbe func(ctx context.Context) exitFacts

var (
	reMainExit  = regexp.MustCompile(`Main process exited, code=(\w+), status=(\d+)(?:/(\w+))?`)
	reFailed    = regexp.MustCompile(`Failed with result '([\w-]+)'`)
	reKernelOOM = regexp.MustCompile(`(?i)out of memory: killed process \d+ \(mistgate-node\)|memory cgroup out of memory: killed process \d+ \(mistgate-node\)`)
)

// parseExitFacts reads the output of `systemctl show --property=NRestarts,Result <unit>`, the unit's recent journal lines and
// the kernel's recent lines. The newest line of a kind wins.
func parseExitFacts(show, unitLog, kernelLog string) exitFacts {
	f := exitFacts{Restarts: -1}
	for _, line := range strings.Split(show, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "NRestarts":
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				f.Restarts = n
			}
		case "Result":
			if v != "" && v != "success" {
				f.Result = v
			}
		}
	}
	for _, line := range strings.Split(unitLog, "\n") {
		if m := reMainExit.FindStringSubmatch(line); m != nil {
			f.Code, f.Status, f.Signal = m[1], m[2], m[3]
			if m[1] == "exited" {
				f.Signal = ""
			}
		}
		if m := reFailed.FindStringSubmatch(line); m != nil {
			f.Result = m[1]
		}
	}
	if f.Result == "oom-kill" || reKernelOOM.MatchString(kernelLog) {
		f.OOM = true
	}
	return f
}

// previousExit decides how the previous run ended and returns the agent_started params for it. It also consumes the clean
// marker and keeps the crash log. updated: an update marker says this run replaced the old build on purpose.
func (a *Agent) previousExit(ctx context.Context, hadPrevious, updated bool, reason string) map[string]string {
	dir := a.cfg.StateDir
	marker := filepath.Join(dir, fileCleanExit)
	_, markerErr := os.Stat(marker)
	clean := markerErr == nil
	_ = os.Remove(marker)
	params := map[string]string{}
	switch {
	case clean || updated:
		params["prev_exit"] = "clean"
		return params
	case !hadPrevious:
		params["prev_exit"] = "unknown"
		return params
	}
	params["prev_exit"] = "unclean"
	// A host that lost power or was reset is not an agent crash, and the boot reason says so.
	if reason == reasonBoot {
		return params
	}
	params["crash"] = "1"
	now := a.now()
	params["crash_loop"] = strconv.Itoa(a.recordCrash(now))
	probe := a.exitProbe
	if probe == nil {
		probe = defaultExitProbe
	}
	pctx, cancel := context.WithTimeout(ctx, exitProbeLimit)
	defer cancel()
	f := probe(pctx)
	if f.Restarts >= 0 {
		params["systemd_restarts"] = strconv.Itoa(f.Restarts)
	}
	if f.Result != "" {
		params["exit_result"] = f.Result
	}
	if f.Code == "exited" && f.Status != "" {
		params["exit_code"] = f.Status
	}
	if f.Signal != "" {
		params["exit_signal"] = f.Signal
	}
	if f.OOM {
		params["oom"] = "1"
	}
	return params
}

// recordCrash adds this crash restart to the crash log, drops the ones older than the window and returns how many are left.
func (a *Agent) recordCrash(now time.Time) int {
	p := filepath.Join(a.cfg.StateDir, fileCrashLog)
	var keep []int64
	if f, err := os.Open(p); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if t, err := strconv.ParseInt(strings.TrimSpace(sc.Text()), 10, 64); err == nil && now.Sub(time.Unix(t, 0)) < crashLoopWindow && t <= now.Unix() {
				keep = append(keep, t)
			}
		}
		f.Close()
	}
	keep = append(keep, now.Unix())
	if len(keep) > maxCrashLog {
		keep = keep[len(keep)-maxCrashLog:]
	}
	var b strings.Builder
	for _, t := range keep {
		b.WriteString(strconv.FormatInt(t, 10))
		b.WriteByte('\n')
	}
	if err := writeFileAtomic(p, []byte(b.String()), 0o600); err != nil {
		a.log.Warn("could not keep the crash log", "err", err)
	}
	return len(keep)
}

// markCleanExit says this run ended by itself. Run calls it on every ordinary return; a crash or a kill never gets here.
func (a *Agent) markCleanExit() {
	body := strconv.FormatInt(a.now().Unix(), 10) + " " + a.cfg.Version + "\n"
	if err := writeFileAtomic(filepath.Join(a.cfg.StateDir, fileCleanExit), []byte(body), 0o600); err != nil {
		a.log.Warn("could not mark the clean exit", "err", err)
	}
}
