package doctor

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/mistgate/mistgate/internal/node/unit"
)

func checkUnitGeneration(_ context.Context, e *Env) Result {
	params := p("unit_gen", strconv.Itoa(e.UnitGen), "current", strconv.Itoa(unit.Generation))
	if !e.UnderSystemd {
		reason := e.UnitSkipReason
		if reason == "other_unit" {
			params["reason"] = reason
			return Result{Status: Skip, Code: CodeUnitOtherUnit, Params: params, Detail: "agent is running under another systemd unit"}
		}
		params["reason"] = "not_systemd"
		return Result{Status: Skip, Code: CodeUnitManual, Params: params, Detail: "agent is not running under systemd"}
	}
	if e.UnitGen < 0 || e.UnitGen > unit.Generation {
		return Result{Status: Skip, Code: CodeUnitUnknown, Params: params, Detail: "systemd unit generation is unknown to this agent"}
	}
	if e.StateDir != "" {
		outcome, err := unit.ReadRefreshOutcome(e.path(filepath.Join(e.StateDir, unit.RefreshOutcomeFile)))
		if err == nil && outcome.ToGen == unit.Generation && outcome.At.After(e.AgentStartedAt) {
			switch outcome.Result {
			case unit.RefreshOK:
				_ = unit.RemoveRefreshOutcome(e.path(filepath.Join(e.StateDir, unit.RefreshOutcomeFile)))
				return Result{Status: OK, Code: CodeUnitCurrent, Params: params,
					Detail: fmt.Sprintf("systemd unit generation %d is current", unit.Generation)}
			case unit.RefreshRolledBack, unit.RefreshFailed:
				params["reason"] = outcome.Reason
				if params["reason"] == "" {
					params["reason"] = "unspecified"
				}
				params["from_gen"] = strconv.Itoa(outcome.FromGen)
				params["to_gen"] = strconv.Itoa(outcome.ToGen)
				fix := ""
				if e.UnitGen < unit.Generation && e.LaunchUnitRefresh != nil {
					fix = FixRefreshUnit
				}
				return Result{Status: Warn, Code: CodeUnitRefreshFailed, Params: params, FixID: fix,
					Detail: fmt.Sprintf("unit refresh from generation %d to %d failed: %s", outcome.FromGen, outcome.ToGen, params["reason"])}
			}
		}
	}
	if e.UnitGen == unit.Generation {
		return Result{Status: OK, Code: CodeUnitCurrent, Params: params, Detail: fmt.Sprintf("systemd unit generation %d is current", unit.Generation)}
	}
	return Result{Status: Warn, Code: CodeUnitOutdated, Params: params, FixID: FixRefreshUnit,
		Detail: fmt.Sprintf("systemd unit generation %d is older than the current generation %d", e.UnitGen, unit.Generation)}
}
