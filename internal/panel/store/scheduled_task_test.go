//go:build !js

package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func scheduledFixture(t *testing.T, st *Store, id string, now time.Time) ScheduledTask {
	t.Helper()
	task := ScheduledTask{ID: id, Name: "Morning check " + id, Kind: "check", Enabled: true, ScheduleKind: "every", EveryMinutes: 15,
		Steps: `[{"tool":"fleet_digest"}]`, CreatedBy: "adm_owner", CreatedAt: now, UpdatedAt: now, NextRunAt: now.Add(-time.Minute)}
	if err := st.InsertScheduledTask(context.Background(), task, 50); err != nil {
		t.Fatal(err)
	}
	return task
}

// One run of a task at a time: the lease and the run row are taken together, a second claim loses, the outcome gives the lease
// back with the next time, and a manual run waits for its cooldown.
func TestScheduledTaskLeaseRunAndOutcome(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	scheduledFixture(t, st, "tsk_a", now)

	due, err := st.DueScheduledTasks(ctx, now, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("due: %+v %v", due, err)
	}
	run := ScheduledTaskRun{ID: "run_1", TaskID: "tsk_a", Trigger: "schedule", StartedAt: now}
	ok, err := st.ClaimScheduledTask(ctx, run, "lease_1", now, now.Add(10*time.Minute), time.Time{})
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if ok, _ := st.ClaimScheduledTask(ctx, ScheduledTaskRun{ID: "run_2", TaskID: "tsk_a", Trigger: "manual", StartedAt: now}, "lease_2", now, now.Add(time.Minute), now); ok {
		t.Fatal("a second run took the lease")
	}
	if _, err := st.ScheduledTaskRun(ctx, "run_2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the losing claim left a run: %v", err)
	}
	if due, _ := st.DueScheduledTasks(ctx, now, 10); len(due) != 0 {
		t.Fatal("a running task is due again")
	}
	task, _ := st.ScheduledTask(ctx, "tsk_a")
	if !task.Running(now) {
		t.Fatal("the task does not read as running")
	}

	fail := func(id string, at time.Time) {
		t.Helper()
		lease := "lease_" + id
		if ok, err := st.ClaimScheduledTask(ctx, ScheduledTaskRun{ID: id, TaskID: "tsk_a", Trigger: "schedule", StartedAt: at}, lease, at, at.Add(time.Minute), time.Time{}); err != nil || !ok {
			t.Fatalf("claim %s: %v %v", id, ok, err)
		}
		if err := st.FinishScheduledRun(ctx, ScheduledTaskRun{ID: id, TaskID: "tsk_a", Status: "failed", Reason: "tool_failed", FinishedAt: at}, lease, at.Add(15*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	// a problem, then three failures in a row: both alert conditions hold
	if err := st.FinishScheduledRun(ctx, ScheduledTaskRun{ID: "run_1", TaskID: "tsk_a", Status: "problem", Summary: "fleet_digest: totals.nodes_problem_now = 2", FinishedAt: now}, "lease_1", now.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	task, _ = st.ScheduledTask(ctx, "tsk_a")
	if task.Running(now) || !task.Problem || task.ProblemSummary == "" || !task.NextRunAt.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("after a problem: %+v", task)
	}
	for i := 0; i < 3; i++ {
		fail(fmt.Sprintf("run_f%d", i), now.Add(time.Duration(i+1)*time.Minute))
	}
	alerts, err := st.ScheduledTaskAlerts(ctx)
	if err != nil || len(alerts) != 1 || alerts[0].FailStreak != 3 || !alerts[0].Problem || alerts[0].FailReason != "tool_failed" {
		t.Fatalf("alerts: %+v %v", alerts, err)
	}
	// a skipped run changes neither; a clean one clears both
	skip := now.Add(10 * time.Minute)
	if ok, _ := st.ClaimScheduledTask(ctx, ScheduledTaskRun{ID: "run_s", TaskID: "tsk_a", Trigger: "schedule", StartedAt: skip}, "lease_s", skip, skip.Add(time.Minute), time.Time{}); !ok {
		t.Fatal("claim skip")
	}
	_ = st.FinishScheduledRun(ctx, ScheduledTaskRun{ID: "run_s", TaskID: "tsk_a", Status: "skipped", Reason: "budget_reached", FinishedAt: skip}, "lease_s", skip.Add(time.Minute))
	if task, _ = st.ScheduledTask(ctx, "tsk_a"); task.FailStreak != 3 || !task.Problem {
		t.Fatalf("a skip changed the state: %+v", task)
	}
	clean := now.Add(20 * time.Minute)
	if ok, _ := st.ClaimScheduledTask(ctx, ScheduledTaskRun{ID: "run_ok", TaskID: "tsk_a", Trigger: "manual", StartedAt: clean}, "lease_ok", clean, clean.Add(time.Minute), clean.Add(-time.Minute)); !ok {
		t.Fatal("a manual run after the cooldown was refused")
	}
	_ = st.FinishScheduledRun(ctx, ScheduledTaskRun{ID: "run_ok", TaskID: "tsk_a", Status: "ok", FinishedAt: clean}, "lease_ok", clean.Add(15*time.Minute))
	if alerts, _ = st.ScheduledTaskAlerts(ctx); len(alerts) != 0 {
		t.Fatalf("a clean run left alerts: %+v", alerts)
	}
	// the manual cooldown: the last manual run started at `clean`
	if ok, _ := st.ClaimScheduledTask(ctx, ScheduledTaskRun{ID: "run_m", TaskID: "tsk_a", Trigger: "manual", StartedAt: clean}, "lease_m", clean.Add(time.Second), clean.Add(time.Minute), clean.Add(-time.Minute)); ok {
		t.Fatal("a manual run inside the cooldown was taken")
	}
	runs, err := st.ScheduledTaskRuns(ctx, "tsk_a", 50)
	if err != nil || len(runs) != 6 || runs[0].ID != "run_ok" {
		t.Fatalf("runs: %d %v", len(runs), err)
	}
	last, err := st.LastScheduledRuns(ctx)
	if err != nil || last["tsk_a"].ID != "run_ok" {
		t.Fatalf("last run: %+v %v", last, err)
	}
}

// A panel that stopped during a run: on start the run reads as interrupted and the lease is free, without counting a failure.
func TestScheduledRunsRecoverAfterAStop(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	scheduledFixture(t, st, "tsk_b", now)
	if ok, _ := st.ClaimScheduledTask(ctx, ScheduledTaskRun{ID: "run_x", TaskID: "tsk_b", Trigger: "schedule", StartedAt: now}, "lease_x", now, now.Add(time.Hour), time.Time{}); !ok {
		t.Fatal("claim")
	}
	n, err := st.RecoverScheduledRuns(ctx, now.Add(time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("recover: %d %v", n, err)
	}
	run, _ := st.ScheduledTaskRun(ctx, "run_x")
	task, _ := st.ScheduledTask(ctx, "tsk_b")
	if run.Status != "failed" || run.Reason != "interrupted" || run.FinishedAt.IsZero() || task.Running(now) || task.FailStreak != 0 {
		t.Fatalf("after recovery: %+v %+v", run, task)
	}
	// the old lease cannot finish over a new run
	if err := st.FinishScheduledRun(ctx, ScheduledTaskRun{ID: "run_x", TaskID: "tsk_b", Status: "failed", FinishedAt: now}, "lease_x", now); err != nil {
		t.Fatal(err)
	}
	if task, _ = st.ScheduledTask(ctx, "tsk_b"); task.FailStreak != 0 {
		t.Fatalf("a stale lease counted: %+v", task)
	}
}

func TestScheduledTaskLimitUpdateAndDelete(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	task := scheduledFixture(t, st, "tsk_c", now)
	if err := st.InsertScheduledTask(ctx, ScheduledTask{ID: "tsk_d", Name: "x", Kind: "check", ScheduleKind: "every", EveryMinutes: 5, CreatedBy: "a", CreatedAt: now, UpdatedAt: now}, 1); !errors.Is(err, ErrScheduledTaskLimit) {
		t.Fatalf("over the limit: %v", err)
	}
	if _, err := st.W.ExecContext(ctx, `UPDATE scheduled_task SET problem = 1, fail_streak = 4 WHERE id = 'tsk_c'`); err != nil {
		t.Fatal(err)
	}
	task.Name, task.Enabled, task.NextRunAt = "Renamed", false, time.Time{}
	if err := st.UpdateScheduledTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	got, _ := st.ScheduledTask(ctx, "tsk_c")
	if got.Name != "Renamed" || got.Enabled || !got.NextRunAt.IsZero() || got.Problem || got.FailStreak != 0 {
		t.Fatalf("after update: %+v", got)
	}
	if err := st.UpdateScheduledTask(ctx, ScheduledTask{ID: "tsk_none", UpdatedAt: now}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of a missing task: %v", err)
	}
	_, _ = st.ClaimScheduledTask(ctx, ScheduledTaskRun{ID: "run_c", TaskID: "tsk_c", Trigger: "manual", StartedAt: now}, "l", now, now.Add(time.Minute), now)
	if ok, err := st.DeleteScheduledTask(ctx, "tsk_c"); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if runs, _ := st.ScheduledTaskRuns(ctx, "tsk_c", 50); len(runs) != 0 {
		t.Fatal("runs outlived their task")
	}
}

// Retention: the last 50 runs of a task, none older than 30 days, and the assistant task's conversation bounded alike.
func TestPruneScheduledTaskRuns(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	scheduledFixture(t, st, "tsk_e", now)
	if err := st.SetScheduledTaskConversation(ctx, "tsk_e", "cnv_task"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		at := now.Add(-time.Duration(i) * time.Hour)
		if _, err := st.W.ExecContext(ctx, `INSERT INTO scheduled_task_run (id, task_id, trigger_kind, status, started_at, finished_at) VALUES (?, 'tsk_e', 'schedule', 'ok', ?, ?)`,
			fmt.Sprintf("run_%02d", i), at.Unix(), at.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = st.W.ExecContext(ctx, `INSERT INTO scheduled_task_run (id, task_id, trigger_kind, status, started_at) VALUES ('run_old', 'tsk_e', 'schedule', 'ok', ?)`, now.Add(-40*24*time.Hour).Unix())
	_, _ = st.W.ExecContext(ctx, `INSERT INTO scheduled_task_run (id, task_id, trigger_kind, status, started_at) VALUES ('run_orphan', 'tsk_gone', 'schedule', 'ok', ?)`, now.Unix())
	for i := 0; i < 120; i++ {
		at := now.Add(-time.Duration(i) * time.Minute)
		if _, err := st.W.ExecContext(ctx, `INSERT INTO assistant_message (id, conversation_id, admin_id, role, body_enc, created_at) VALUES (?, 'cnv_task', 'adm_owner', 'user', X'00', ?)`,
			fmt.Sprintf("msg_%03d", i), at.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	_, _ = st.W.ExecContext(ctx, `INSERT INTO assistant_message (id, conversation_id, admin_id, role, body_enc, created_at) VALUES ('msg_person', 'cnv_person', 'adm_owner', 'user', X'00', ?)`, now.Add(-60*24*time.Hour).Unix())
	n, err := st.PruneScheduledTaskRuns(ctx, now.Add(-ScheduledRunsKeep))
	if err != nil || n != 12 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	runs, _ := st.ScheduledTaskRuns(ctx, "tsk_e", 100)
	if len(runs) != ScheduledRunsKept || runs[0].ID != "run_00" {
		t.Fatalf("kept %d runs, newest %s", len(runs), runs[0].ID)
	}
	var task, person int
	_ = st.R.QueryRowContext(ctx, `SELECT count(*) FROM assistant_message WHERE conversation_id = 'cnv_task'`).Scan(&task)
	_ = st.R.QueryRowContext(ctx, `SELECT count(*) FROM assistant_message WHERE conversation_id = 'cnv_person'`).Scan(&person)
	if task != scheduledMessagesKept || person != 1 {
		t.Fatalf("messages: task %d, a person's %d", task, person)
	}
}
