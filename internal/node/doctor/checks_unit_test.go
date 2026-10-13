package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/unit"
)

func TestUnitGenerationCheck(t *testing.T) {
	for _, tc := range []struct {
		name       string
		generation int
		systemd    bool
		status     Status
		fix        string
	}{
		{"generation zero under systemd", 0, true, Warn, FixRefreshUnit},
		{"generation two under systemd", 2, true, Warn, FixRefreshUnit},
		{"current generation under systemd", unit.Generation, true, OK, ""},
		{"generation zero by hand", 0, false, Skip, ""},
		{"generation two by hand", 2, false, Skip, ""},
		{"current generation by hand", unit.Generation, false, Skip, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			r := run(t, f.doctor(func(e *Env) {
				e.UnitGen = tc.generation
				e.UnderSystemd = tc.systemd
			}), CheckUnitGeneration)
			want(t, r, tc.status, tc.fix)
			if r.Params["unit_gen"] != strconv.Itoa(tc.generation) || r.Params["current"] != strconv.Itoa(unit.Generation) {
				t.Errorf("generation params = %v", r.Params)
			}
			if !tc.systemd && r.Params["reason"] != "not_systemd" {
				t.Errorf("non-systemd reason = %q", r.Params["reason"])
			}
		})
	}
}

func TestUnitGenerationRefreshFailureOutcome(t *testing.T) {
	f := newFake(t)
	f.put("state/unit-refresh.outcome", `{"at":"2027-01-15T08:00:00Z","from_gen":2,"to_gen":3,"result":"rolled_back","reason":"service did not stay active"}`)
	e := f.env()
	e.StateDir = "state"
	e.UnitGen = 2
	e.UnderSystemd = true
	r := run(t, New(e), CheckUnitGeneration)
	if r.Status != Warn || r.Code != "unit_generation.refresh_failed" || r.Params["reason"] != "service did not stay active" ||
		r.Params["from_gen"] != "2" || r.Params["to_gen"] != "3" {
		t.Fatalf("refresh failure outcome = %+v", r)
	}
}

func TestUnitGenerationSkipsAnotherSystemdUnit(t *testing.T) {
	f := newFake(t)
	r := run(t, f.doctor(func(e *Env) {
		e.UnderSystemd = false
		e.UnitSkipReason = "other_unit"
	}), CheckUnitGeneration)
	if r.Status != Skip || r.Code != CodeUnitOtherUnit || r.Params["reason"] != "other_unit" || r.FixID != "" {
		t.Fatalf("other unit = %+v", r)
	}
}

func TestUnitGenerationRefreshOutcomeMustBeNewerThanAgentStart(t *testing.T) {
	f := newFake(t)
	f.put("state/unit-refresh.outcome", `{"at":"2027-01-15T08:00:00Z","from_gen":2,"to_gen":3,"result":"ok"}`)
	e := f.env()
	e.StateDir, e.UnitGen, e.UnderSystemd = "state", 2, true
	e.AgentStartedAt = time.Date(2027, 1, 15, 8, 0, 0, 0, time.UTC)
	r := run(t, New(e), CheckUnitGeneration)
	if r.Code != CodeUnitOutdated || r.Status != Warn {
		t.Fatalf("old outcome = %+v", r)
	}
}

func TestUnitGenerationSuccessfulOutcomeIsReportedAndRemoved(t *testing.T) {
	f := newFake(t)
	f.put("state/unit-refresh.outcome", `{"at":"2027-01-15T09:00:00Z","from_gen":2,"to_gen":3,"result":"ok"}`)
	e := f.env()
	e.StateDir, e.UnitGen, e.UnderSystemd = "state", 3, true
	e.AgentStartedAt = time.Date(2027, 1, 15, 8, 0, 0, 0, time.UTC)
	r := run(t, New(e), CheckUnitGeneration)
	if r.Status != OK || r.Code != CodeUnitCurrent {
		t.Fatalf("successful outcome = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(f.root, "state", "unit-refresh.outcome")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reported outcome remains: %v", err)
	}
}

func TestRefreshUnitFix(t *testing.T) {
	f := newFake(t)
	calls := 0
	d := f.doctor(func(e *Env) {
		e.UnitGen = 2
		e.UnderSystemd = true
		e.LaunchUnitRefresh = func(_ context.Context, beforeLaunch func()) error {
			calls++
			if beforeLaunch != nil {
				beforeLaunch()
			}
			return nil
		}
	})

	plan := d.Apply(context.Background(), FixRefreshUnit, true, nil)
	if !plan.OK || !strings.Contains(plan.Detail, "would start") || !strings.Contains(plan.Detail, "2") || !strings.Contains(plan.Detail, "3") ||
		!strings.Contains(plan.Detail, "agent restarts") || !strings.Contains(plan.Detail, "connections drop for a few seconds") {
		t.Fatalf("dry run = %+v", plan)
	}
	if calls != 0 {
		t.Fatalf("dry run launched %d refreshes", calls)
	}
	if out := d.Apply(context.Background(), FixRefreshUnit, false, map[string]string{"bin": "/tmp/other"}); out.Err != ErrBadParams {
		t.Fatalf("params were accepted: %+v", out)
	}
	out := d.Apply(context.Background(), FixRefreshUnit, false, nil)
	if !out.OK || !strings.Contains(out.Detail, "started") || strings.Contains(out.Detail, "applied") || calls != 1 {
		t.Fatalf("apply = %+v, launcher calls = %d", out, calls)
	}

	dBusy := f.doctor(func(e *Env) {
		e.UnitGen = 2
		e.UnderSystemd = true
		e.LaunchUnitRefresh = func(context.Context, func()) error { return unit.ErrBusy }
	})
	if out := dBusy.Apply(context.Background(), FixRefreshUnit, false, nil); out.Err != ErrFixBusy {
		t.Fatalf("busy launcher = %+v", out)
	}

	dHand := f.doctor(func(e *Env) {
		e.UnitGen = 2
		e.LaunchUnitRefresh = func(context.Context, func()) error { return errors.New("must not run") }
	})
	if out := dHand.Apply(context.Background(), FixRefreshUnit, false, nil); out.Err != ErrNotApplicable {
		t.Fatalf("manual process refresh = %+v", out)
	}

	currentCalls := 0
	dCurrent := f.doctor(func(e *Env) {
		e.UnitGen = unit.Generation
		e.UnderSystemd = true
		e.LaunchUnitRefresh = func(context.Context, func()) error {
			currentCalls++
			return nil
		}
	})
	if out := dCurrent.Apply(context.Background(), FixRefreshUnit, false, nil); !out.OK || out.Params["noop"] != "1" || currentCalls != 0 {
		t.Fatalf("current unit refresh = %+v, launcher calls=%d", out, currentCalls)
	}
	dCurrentBusy := f.doctor(func(e *Env) {
		e.UnitGen = unit.Generation
		e.UnderSystemd = true
		e.UnitRefreshBusy = func(context.Context) error { return unit.ErrBusy }
		e.LaunchUnitRefresh = func(context.Context, func()) error { t.Fatal("busy refresh launched"); return nil }
	})
	if out := dCurrentBusy.Apply(context.Background(), FixRefreshUnit, false, nil); out.Err != ErrFixBusy {
		t.Fatalf("current-generation apply while refresh is active = %+v", out)
	}
}

func TestRefreshUnitValidationFailsBeforeAckAndReleasesUpdateGate(t *testing.T) {
	f := newFake(t)
	launched, acked, released := false, false, false
	d := f.doctor(func(e *Env) {
		e.UnitGen = 2
		e.UnderSystemd = true
		e.UnitRefreshReserve = func(context.Context) (func(bool), error) {
			return func(started bool) { released = !started }, nil
		}
		e.LaunchUnitRefresh = func(context.Context, func()) error {
			launched = true
			return fmt.Errorf("%w: unsafe state path", unit.ErrPreflight)
		}
	})
	out := d.ApplyWithRefreshAck(context.Background(), FixRefreshUnit, false, nil, func(FixOutcome) { acked = true })
	if out.Err != ErrUnitRefreshInvalid || !launched || acked || !released {
		t.Fatalf("preflight outcome = %+v; launched=%v acked=%v released=%v", out, launched, acked, released)
	}
}

func TestRefreshUnitReservationBusyDoesNotLaunch(t *testing.T) {
	f := newFake(t)
	d := f.doctor(func(e *Env) {
		e.UnitGen = 2
		e.UnderSystemd = true
		e.UnitRefreshReserve = func(context.Context) (func(bool), error) { return nil, unit.ErrBusy }
		e.LaunchUnitRefresh = func(context.Context, func()) error { t.Fatal("busy refresh launched"); return nil }
	})
	if out := d.Apply(context.Background(), FixRefreshUnit, false, nil); out.Err != ErrFixBusy {
		t.Fatalf("busy reservation = %+v", out)
	}
}
