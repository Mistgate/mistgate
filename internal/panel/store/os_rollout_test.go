//go:build !js

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOsRolloutLifecycle(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_790_000_000, 0)
	ro := OsRolloutRow{ID: NewID("osr_"), Status: RolloutRunning, Mode: OsModeSecurity, Reboot: OsRebootIfRequired, CreatedBy: "adm_1", CreatedAt: now}
	step := OsStepRow{NodeID: "nod_a", NodeName: "de1", State: OsStepPending, Params: map[string]string{"pending": "6"}}
	if err := st.CreateOsRollout(ctx, ro, []OsStepRow{step}); err != nil {
		t.Fatal(err)
	}
	// one active rollout at a time, enforced by the database
	second := ro
	second.ID = NewID("osr_")
	if err := st.CreateOsRollout(ctx, second, []OsStepRow{step}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second active rollout = %v, want ErrConflict", err)
	}
	if _, err := st.OsRollout(ctx, second.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the refused rollout left a row: %v", err)
	}
	active, err := st.ActiveOsRollout(ctx)
	if err != nil || active.ID != ro.ID || !active.Active() || active.Mode != OsModeSecurity || active.Reboot != OsRebootIfRequired {
		t.Fatalf("active = %+v, %v", active, err)
	}

	steps, err := st.OsRolloutSteps(ctx, ro.ID)
	if err != nil || len(steps) != 1 || steps[0].Params["pending"] != "6" || steps[0].Decided() {
		t.Fatalf("steps = %+v, %v", steps, err)
	}
	x := &steps[0]
	x.State, x.JobID, x.Sends, x.SentAt, x.BootBefore, x.PreFailed = OsStepSent, "osj_abc", 1, now, now.Add(-time.Hour), []string{"inb_1"}
	if err := st.SaveOsStep(ctx, x); err != nil {
		t.Fatal(err)
	}
	// a stale copy of the row does not overwrite a newer state
	stale := steps[0]
	stale.State = OsStepFailed
	stale.loadedState = OsStepPending
	if err := st.SaveOsStep(ctx, &stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("a stale save = %v, want ErrConflict", err)
	}
	got, _ := st.OsRolloutSteps(ctx, ro.ID)
	if got[0].State != OsStepSent || got[0].JobID != "osj_abc" || got[0].Sends != 1 || !got[0].SentAt.Equal(now) || !got[0].BootBefore.Equal(now.Add(-time.Hour)) || len(got[0].PreFailed) != 1 {
		t.Fatalf("stored step = %+v", got[0])
	}

	// pause, then a pause key is kept and any other status clears it
	if ok, err := st.SetOsRolloutStatus(ctx, ro.ID, []string{RolloutRunning}, RolloutPaused, "step_failed", map[string]string{"node": "de1"}, time.Time{}); err != nil || !ok {
		t.Fatalf("pause = %v, %v", ok, err)
	}
	if p, _ := st.ActiveOsRollout(ctx); p.Status != RolloutPaused || p.PauseKey != "step_failed" || p.PauseParams["node"] != "de1" {
		t.Fatalf("paused = %+v", p)
	}
	if ok, _ := st.SetOsRolloutStatus(ctx, ro.ID, []string{RolloutRunning}, RolloutDone, "", nil, now); ok {
		t.Fatal("a compare-and-set moved a rollout from a status it was not in")
	}
	if ok, err := st.SetOsRolloutStatus(ctx, ro.ID, []string{RolloutPaused}, RolloutFailed, "ignored", nil, now); err != nil || !ok {
		t.Fatalf("finish = %v, %v", ok, err)
	}
	if _, err := st.ActiveOsRollout(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("active after the end = %v", err)
	}
	last, err := st.LatestOsRollout(ctx)
	if err != nil || last.ID != ro.ID || last.Status != RolloutFailed || last.PauseKey != "" || !last.FinishedAt.Equal(now) {
		t.Fatalf("latest = %+v, %v", last, err)
	}
	// a finished one is old enough to go, and takes its steps along
	if n, err := st.PruneOsRollouts(ctx, 0, now.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("prune = %d, %v", n, err)
	}
	if left, _ := st.OsRolloutSteps(ctx, ro.ID); len(left) != 0 {
		t.Fatalf("steps outlived their rollout: %+v", left)
	}
}

func TestOsJobEventsAreReadByJobID(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_790_000_000, 0)
	add := func(code string, ts time.Time, params map[string]string) {
		t.Helper()
		if err := st.InsertEvent(ctx, EventRow{Time: ts, Severity: 1, Code: code, Source: "agent", NodeID: "nod_a", Params: params}); err != nil {
			t.Fatal(err)
		}
	}
	add("os_reboot_started", now, map[string]string{"job_id": "osj_one", "kernel_to": "6.8.0-150-generic"})
	add("os_update_done", now.Add(time.Minute), map[string]string{"job_id": "osj_one", "upgraded": "6"})
	add("os_update_done", now.Add(2*time.Minute), map[string]string{"job_id": "osj_two"})
	add("update_committed", now, map[string]string{"job_id": "osj_one"})
	evs, err := st.OsJobEvents(ctx, "nod_a", "osj_one", now.Add(-time.Minute))
	if err != nil || len(evs) != 2 || evs[0].Code != "os_reboot_started" || evs[1].Code != "os_update_done" || evs[1].Params["upgraded"] != "6" {
		t.Fatalf("events = %+v, %v", evs, err)
	}
	if evs, _ := st.OsJobEvents(ctx, "nod_b", "osj_one", now.Add(-time.Minute)); len(evs) != 0 {
		t.Fatalf("another node's events: %+v", evs)
	}
	if evs, _ := st.OsJobEvents(ctx, "nod_a", "osj_one", now.Add(time.Hour)); len(evs) != 0 {
		t.Fatalf("events older than the bound: %+v", evs)
	}
}
