package update

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/osupdate"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Two kinds of "not now" of an OS job, both known to the panel before it asks:
//
//   - when a reboot becomes possible again (osRebootWait): the node refuses a reboot soon after its boot (too_soon) and a reboot
//     the panel asked for more than once an hour or three times in 24 hours (reboot_limit); the screen turns the Reboot action
//     off until then instead of letting the owner click into a refusal;
//   - a refusal that only asks to wait (osRetryLater): nothing was changed on the node and nothing is wrong with it, so the step
//     ends as SKIPPED (try_later), the rollout ends with it, and nothing stays open for the owner to close.

const (
	blockRebootWait = "reboot_wait"
	osErrTryLater   = "try_later"
)

// osRetryLater are the codes of a refusal that only says "not now": a job of another kind is running (busy), another package
// manager holds the lock (apt_lock), the host booted a moment ago (too_soon), the reboot limit is spent (reboot_limit). Every
// other code needs a look at the node and keeps its stop (or its FAILED step).
var osRetryLater = []string{osupdate.CodeBusy, osupdate.CodeAptLock, osupdate.CodeTooSoon, osupdate.CodeRebootLimit}

// osJobAskedToWait: the job of the snapshot failed only because the node asked to wait, with nothing installed or rebooted.
func osJobAskedToWait(j *agentv1.OsJob) bool {
	return j.GetUpgraded() == 0 && j.GetReboot() != osupdate.RebootDone &&
		slices.Contains(osRetryLater, j.GetCode()) && (j.GetHeldReason() == "" || slices.Contains(osRetryLater, j.GetHeldReason()))
}

// osRebootWait is the time from which a reboot of the node is possible again, and the code of the limit that holds it longest;
// the zero time when it is possible now.
//
//	too_soon      bootAt + osupdate.MinUptime (the panel's record of the node's last boot stands for the agent's uptime)
//	reboot_limit  of the panel's reboots of the last 24 hours (newest last): the newest + 1 hour (MaxRebootsHour of them within
//	              an hour block), and the MaxRebootsDay-th newest + 24 hours once there are that many
//
// Both limits are "less than": a reboot is possible exactly at the time returned.
func osRebootWait(bootAt time.Time, reboots []time.Time, now time.Time) (time.Time, string) {
	var at time.Time
	var code string
	hold := func(t time.Time, c string) {
		if t.After(now) && t.After(at) {
			at, code = t, c
		}
	}
	if !bootAt.IsZero() {
		hold(notAfter(bootAt, now).Add(osupdate.MinUptime), osupdate.CodeTooSoon) // a boot "in the future" is a skewed clock: no longer than the limit itself
	}
	var recent []time.Time // newest first
	for _, r := range reboots {
		if d := now.Sub(r); d < 24*time.Hour {
			recent = append(recent, notAfter(r, now))
		}
	}
	slices.SortFunc(recent, func(a, b time.Time) int { return b.Compare(a) })
	if len(recent) >= osupdate.MaxRebootsHour {
		hold(recent[osupdate.MaxRebootsHour-1].Add(time.Hour), osupdate.CodeRebootLimit)
	}
	if len(recent) >= osupdate.MaxRebootsDay {
		hold(recent[osupdate.MaxRebootsDay-1].Add(24*time.Hour), osupdate.CodeRebootLimit)
	}
	return at, code
}

func notAfter(t, limit time.Time) time.Time {
	if t.After(limit) {
		return limit
	}
	return t
}

// osRebootsOf lists when the panel's OS jobs rebooted the node in the last 24 hours. A read that fails counts as none: the node
// still refuses a reboot that is too soon, and the screen only shows the reason one click late.
func (s *Service) osRebootsOf(ctx context.Context, nodeID string, now time.Time) []time.Time {
	all, err := s.osReboots(ctx, now)
	if err != nil {
		return nil
	}
	return all[nodeID]
}

func (s *Service) osReboots(ctx context.Context, now time.Time) (map[string][]time.Time, error) {
	all, err := s.st.OsRebootsSince(ctx, now.Add(-24*time.Hour))
	if err != nil {
		s.log.Warn("update: read OS reboot times", "code", "update_os_read_reboot_times", "err", err)
	}
	return all, err
}

// osNothingChanged: the failed or held job only asked to wait, so its step ends as SKIPPED. A reboot_only run installs nothing,
// so for it the node's code alone decides. A run that installs counts only when the node's own record says that no package was
// installed and no reboot done (the snapshot of the job; the events carry no package count), and the failure came before the
// reboot step.
func (p *osPass) osNothingChanged(v osJobView) (string, bool) {
	if v.reboot == osupdate.RebootDone {
		return "", false
	}
	codes := []string{v.code}
	if v.reasons != "" {
		codes = strings.Split(v.reasons, ",")
	} else if v.code == "" {
		codes = []string{v.heldReason}
	}
	for _, c := range codes {
		if !slices.Contains(osRetryLater, c) {
			return "", false
		}
	}
	if p.ro.Mode != store.OsModeRebootOnly {
		if n, err := strconv.ParseUint(v.upgraded, 10, 32); !v.snapshot || err != nil || n > 0 || v.reboot == osupdate.RebootHeld {
			return "", false
		}
	}
	return codes[0], true
}

// skipTryLater ends the step as SKIPPED with the node's code; finishOsIfDone then ends the rollout.
func (p *osPass) skipTryLater(x *store.OsStepRow, code string) {
	p.finish(x, store.OsStepSkipped, osErrTryLater, map[string]string{"code": osCode(code)})
}

// ---------------------------------------------------------------------------------------------------
// What the check after the job still waits for

// gate_* params of a GATING step (admin update.proto OsStep.params).
const (
	gateFor    = "gate_for"
	gateNames  = "gate_names"
	gateMore   = "gate_more"
	gateMaxS   = "gate_max_s"
	gateShown  = 5
	gateNameLn = 40
)

var gateKeys = []string{gateFor, gateNames, gateMore, gateMaxS}

// gateWaitParams says what the check waits for: the profiles that have not passed yet (names; at most gateShown of them), else
// the kind of thing it lacks, and how long the check may take. The same values give no write.
func (s *Service) gateWaitParams(g gateResult) map[string]string {
	out := map[string]string{gateMaxS: strconv.Itoa(int(s.cfg.OsGateWait / time.Second))}
	switch {
	case len(g.waiting) > 0:
		names := make([]string, 0, gateShown)
		for _, n := range g.waiting[:min(len(g.waiting), gateShown)] {
			names = append(names, truncateRunes(n, gateNameLn))
		}
		out[gateFor], out[gateNames] = "profiles", jsonStringList(names)
		if more := len(g.waiting) - len(names); more > 0 {
			out[gateMore] = strconv.Itoa(more)
		}
	case g.soft == errWarpDown:
		out[gateFor] = "warp"
	case g.soft == osErrJobNotSettled:
		out[gateFor] = "job"
	case g.soft != "":
		out[gateFor] = "settings"
	}
	return out
}

// noteGateWait stores what the check waits for on the GATING step, when it changed.
func (p *osPass) noteGateWait(x *store.OsStepRow, g gateResult) {
	want := p.s.gateWaitParams(g)
	same := true
	for _, k := range gateKeys {
		if x.Params[k] != want[k] {
			same = false
		}
	}
	if same {
		return
	}
	updated := *x
	updated.Params = make(map[string]string, len(x.Params)+len(want))
	for k, v := range x.Params {
		if !slices.Contains(gateKeys, k) {
			updated.Params[k] = v
		}
	}
	for k, v := range want {
		updated.Params[k] = v
	}
	if p.save(&updated) {
		*x = updated
	}
}

// withoutGateWait is the params of a finished step: the wait is over, whatever it was.
func withoutGateWait(params map[string]string) map[string]string {
	for _, k := range gateKeys {
		delete(params, k)
	}
	return params
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func jsonStringList(in []string) string {
	b, _ := json.Marshal(in)
	return string(b)
}
