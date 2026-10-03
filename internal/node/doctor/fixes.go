package doctor

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/mistgate/mistgate/internal/node/hostctl"
)

// The fix vocabulary of CommandResult.error for ApplyFix (agent.proto "DOCTOR").
const (
	ErrUnknownFix      = "unknown_fix"
	ErrBadParams       = "bad_params"
	ErrNotApplicable   = "not_applicable"
	ErrUnsupportedHost = "unsupported_host"
	ErrFixBusy         = "busy"
)

// FixOutcome is the answer to Apply; the agent turns it into a CommandResult.
type FixOutcome struct {
	OK       bool
	Affected uint32
	Detail   string // short English fact line: what would happen / what was done
	Params   map[string]string
	Err      string // "" when OK; else one of the Err* words or "failed: <message>"
}

var inboundIDRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func failed(err error) FixOutcome {
	return FixOutcome{Err: "failed: " + clip(firstLine(err.Error()), 150)}
}

func noop(detail string) FixOutcome {
	return FixOutcome{OK: true, Detail: detail, Params: p("noop", "1")}
}

// Apply runs one of the five fixes. The id is compared against a compiled list and never reaches a shell;
// params are validated (an inbound id must be one this agent runs). dryRun changes nothing: it only reads
// and describes. One fix at a time.
func (d *Doctor) Apply(ctx context.Context, fixID string, dryRun bool, params map[string]string) FixOutcome {
	known := false
	for _, id := range FixIDs() {
		known = known || id == fixID
	}
	if !known {
		return FixOutcome{Err: ErrUnknownFix}
	}
	if d.env.Unsupported != "" {
		return FixOutcome{Err: ErrUnsupportedHost}
	}
	if !d.fixing.CompareAndSwap(false, true) {
		return FixOutcome{Err: ErrFixBusy}
	}
	defer d.fixing.Store(false)

	env := d.env
	env.memo = &memo{}
	e := &env
	switch fixID {
	case FixJournaldVacuum:
		return fixJournald(ctx, e, dryRun, params)
	case FixApplyBaseline:
		return fixBaseline(ctx, e, dryRun, params)
	case FixRestartInbound:
		return fixRestart(ctx, e, dryRun, params)
	case FixReconnectWarp:
		return fixReconnectWarp(ctx, e, dryRun, params)
	default:
		return fixResolver(ctx, e, dryRun, params)
	}
}

// reconnect_warp retries the current health check when a configured tunnel is up/starting but its latest check failed.
// It reconnects only when that check reaches the manager's failure threshold and leaves the tunnel down.
func fixReconnectWarp(ctx context.Context, e *Env, dry bool, params map[string]string) FixOutcome {
	if !noParams(params) {
		return FixOutcome{Err: ErrBadParams}
	}
	if e.Warp == nil {
		return FixOutcome{Err: ErrUnsupportedHost}
	}
	w := e.Warp(ctx)
	if !w.Configured {
		return noop("WARP is no longer configured; no repair is needed")
	}
	if (w.State == "up" || w.State == "starting") && warpHasCurrentFailure(w.LastError) {
		if e.RecheckWarp == nil {
			return FixOutcome{Err: ErrUnsupportedHost}
		}
		if dry {
			return FixOutcome{OK: true, Affected: 1, Params: p("state", w.State), Detail: "would rerun WARP health checks and reconnect only if the existing failure threshold makes the tunnel down"}
		}
		rechecked, thresholdDown, err := e.RecheckWarp(ctx)
		if err != nil {
			return failed(err)
		}
		if !rechecked && !thresholdDown {
			return noop("WARP no longer needs a health check; no tunnel change was made")
		}

		// A recheck can be the third consecutive failure. Require the manager's explicit threshold result and current
		// Down state; Reconnect also has its own Down-state and host-route ownership guards.
		latest := e.Warp(ctx)
		if thresholdDown && latest.Configured && latest.State == "down" {
			if e.ReconnectWarp == nil {
				return FixOutcome{Err: ErrUnsupportedHost}
			}
			reconnected, err := e.ReconnectWarp(ctx)
			if err != nil {
				return failed(err)
			}
			if !reconnected {
				return noop("WARP recovered before the guarded reconnect; no change was made")
			}
			return FixOutcome{OK: true, Affected: 1, Params: p("state", "starting"), Detail: "WARP reached its failure threshold; the guarded reconnect started"}
		}
		if !rechecked {
			return noop("WARP is no longer down at the failure threshold; no tunnel change was made")
		}
		if (latest.State == "up" || latest.State == "starting") && warpHasCurrentFailure(latest.LastError) {
			return FixOutcome{OK: true, Affected: 1, Params: p("state", latest.State), Detail: "WARP health check still fails below the automatic failure threshold; the tunnel was not restarted"}
		}
		if latest.State == "down" {
			return FixOutcome{OK: true, Affected: 1, Params: p("state", latest.State), Detail: "WARP is Down, but this check did not reach the failure threshold; no reconnect was started"}
		}
		return FixOutcome{OK: true, Affected: 1, Params: p("state", latest.State), Detail: "WARP health check rerun; the tunnel was not restarted"}
	}
	if w.State != "down" {
		return noop("WARP is no longer failing; no reconnect is needed")
	}
	if e.ReconnectWarp == nil {
		return FixOutcome{Err: ErrUnsupportedHost}
	}
	if dry {
		return FixOutcome{OK: true, Affected: 1, Params: p("state", w.State), Detail: "would reconnect the configured WARP tunnel"}
	}
	reconnected, err := e.ReconnectWarp(ctx)
	if err != nil {
		return failed(err)
	}
	if !reconnected {
		return noop("WARP recovered before the reconnect; no change was made")
	}
	return FixOutcome{OK: true, Affected: 1, Params: p("state", "starting"), Detail: "WARP tunnel reconnected; waiting for a fresh handshake"}
}

// noParams rejects any parameter for the fixes that take none.
func noParams(params map[string]string) bool { return len(params) == 0 }

func mb(b int64) string { return strconv.FormatInt(b>>20, 10) }

// journald_vacuum: journalctl --vacuum-size=200M --vacuum-time=7d.
func fixJournald(ctx context.Context, e *Env, dry bool, params map[string]string) FixOutcome {
	if !noParams(params) {
		return FixOutcome{Err: ErrBadParams}
	}
	if e.Fixer == nil {
		return FixOutcome{Err: ErrUnsupportedHost}
	}
	if !e.has("journalctl") || !journal(e).found {
		return FixOutcome{Err: ErrNotApplicable}
	}
	before := journal(e).bytes
	if before <= journalCap {
		return noop("journal is " + human(uint64(before)) + ", within the cap")
	}
	pm := p("before_mb", mb(before), "target_mb", strconv.Itoa(journalCap>>20))
	if dry {
		return FixOutcome{OK: true, Detail: fmt.Sprintf("journal %s on disk; would vacuum to %d MB and 7 days", human(uint64(before)), journalCap>>20), Params: pm}
	}
	if err := e.Fixer.VacuumJournal(ctx); err != nil {
		return failed(err)
	}
	fresh := *e
	fresh.memo = &memo{}
	after := journal(&fresh).bytes
	pm["after_mb"] = mb(after)
	return FixOutcome{OK: true, Affected: 1, Params: pm, Detail: fmt.Sprintf("journal vacuumed from %s to %s", human(uint64(before)), human(uint64(after)))}
}

// apply_baseline: hostctl.ApplyBaseline (sysctl fq + bbr, journald cap, ssh guard). Idempotent.
func fixBaseline(ctx context.Context, e *Env, dry bool, params map[string]string) FixOutcome {
	if !noParams(params) {
		return FixOutcome{Err: ErrBadParams}
	}
	if e.ApplyBaseline == nil {
		return FixOutcome{Err: ErrUnsupportedHost}
	}
	diff, fixable, notes := baselineDiff(e)
	if len(diff) == 0 {
		return noop("baseline is already in place")
	}
	if len(fixable) == 0 {
		return FixOutcome{Err: ErrNotApplicable, Detail: noteText(notes)}
	}
	pm := p("changes", strings.Join(fixable, ","))
	if dry {
		return FixOutcome{OK: true, Params: pm, Detail: "would set: " + strings.Join(fixable, ", ")}
	}
	err := e.ApplyBaseline(ctx)
	_, left, _ := baselineDiff(e)
	n := uint32(len(fixable) - min(len(left), len(fixable)))
	if err != nil {
		out := failed(err)
		out.Affected, out.Params = n, pm
		return out
	}
	pm["remaining"] = strings.Join(left, ",")
	return FixOutcome{OK: true, Affected: n, Params: pm, Detail: fmt.Sprintf("baseline applied: %d item(s) set", n)}
}

// restart_inbound: one inbound (params{inbound_id}) or every FAILED one.
func fixRestart(ctx context.Context, e *Env, dry bool, params map[string]string) FixOutcome {
	for k := range params {
		if k != "inbound_id" {
			return FixOutcome{Err: ErrBadParams}
		}
	}
	if e.RestartInbound == nil {
		return FixOutcome{Err: ErrUnsupportedHost}
	}
	ibs := e.Inbounds()
	var ids []string
	if id := params["inbound_id"]; id != "" {
		if !inboundIDRe.MatchString(id) {
			return FixOutcome{Err: ErrBadParams}
		}
		found := false
		for _, in := range ibs {
			found = found || in.ID == id
		}
		if !found {
			return FixOutcome{Err: ErrBadParams}
		}
		ids = []string{id}
	} else {
		for _, in := range ibs {
			if in.Enabled && in.State == "failed" {
				ids = append(ids, in.ID)
			}
		}
		sorted(ids)
	}
	if len(ids) == 0 {
		return noop("no failed inbound to restart")
	}
	pm := p("inbounds", strings.Join(ids, ","), "disruptive", "1")
	if dry {
		return FixOutcome{OK: true, Params: pm, Detail: "would restart inbound(s) " + strings.Join(ids, ", ") + "; their sessions drop"}
	}
	var n uint32
	var errs []error
	for _, id := range ids {
		c, err := e.RestartInbound(ctx, id)
		n += c
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", id, err))
		}
	}
	if len(errs) > 0 {
		out := failed(errors.Join(errs...))
		out.Affected, out.Params = n, pm
		return out
	}
	return FixOutcome{OK: true, Affected: n, Params: pm, Detail: fmt.Sprintf("restarted %d inbound(s)", n)}
}

// set_resolver: point the host resolver at the node's resolvers (NodeSettings.dns_resolvers, else the country
// default). The previous configuration is backed up by the host and restored by Cleanup.
func fixResolver(ctx context.Context, e *Env, dry bool, params map[string]string) FixOutcome {
	if !noParams(params) {
		return FixOutcome{Err: ErrBadParams}
	}
	if e.Fixer == nil {
		return FixOutcome{Err: ErrUnsupportedHost}
	}
	plan, err := e.Fixer.ResolverPlan(ctx, ResolverTargets(e.Settings()))
	if errors.Is(err, hostctl.ErrUnsupported) {
		return FixOutcome{Err: ErrUnsupportedHost}
	}
	if err != nil {
		return failed(err)
	}
	pm := p("mode", plan.Mode, "before", strings.Join(plan.Before, ","), "after", strings.Join(plan.After, ","))
	if strings.Join(plan.Before, ",") == strings.Join(plan.After, ",") {
		out := noop("resolver is already " + strings.Join(plan.After, ", "))
		out.Params = pm
		out.Params["noop"] = "1"
		return out
	}
	if dry {
		return FixOutcome{OK: true, Params: pm, Detail: fmt.Sprintf("would set the resolver (%s) from %s to %s", plan.Mode, orNone(plan.Before), strings.Join(plan.After, ", "))}
	}
	if err := e.Fixer.SetResolver(ctx, ResolverTargets(e.Settings())); err != nil {
		return failed(err)
	}
	return FixOutcome{OK: true, Affected: 1, Params: pm, Detail: fmt.Sprintf("resolver (%s) set to %s", plan.Mode, strings.Join(plan.After, ", "))}
}

func orNone(s []string) string {
	if len(s) == 0 {
		return "(none)"
	}
	return strings.Join(s, ", ")
}
