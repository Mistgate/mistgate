//go:build !js

package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// The rows behind scheduled_task and scheduled_task_run (migration 00113): the owner's scheduled checks of the fleet
// (internal/panel/tasks, mistgate.tasks.v1). They exist for the VPS edition; the Cloudflare edition has no caller, so this file
// is not in the Workers' wasm.

const (
	// ScheduledRunsKept is how many runs of one task the retention sweep keeps.
	ScheduledRunsKept = 50
	// ScheduledRunsKeep is how long a run is kept at most.
	ScheduledRunsKeep = 30 * 24 * time.Hour
	// ScheduledFailingStreak is the number of failed runs in a row that opens SCHEDULED_TASK_FAILING.
	ScheduledFailingStreak = 3
	// scheduledMessagesKept bounds the conversation of an assistant task: a user and an assistant message per run.
	scheduledMessagesKept = 2 * ScheduledRunsKept
)

// EnsureScheduledToken creates or refreshes the hidden identity of an admin's scheduled tasks (ScheduledTokenID): operator
// profile, no secret ever issued, named so the approval inbox and the audit read "Scheduled tasks".
func (s *Store) EnsureScheduledToken(ctx context.Context, adminID string, now time.Time) (APIToken, error) {
	if adminID == "" {
		return APIToken{}, errors.New("store: invalid scheduled task identity")
	}
	id := ScheduledTokenID(adminID)
	secretHash := make([]byte, 32)
	if _, err := rand.Read(secretHash); err != nil {
		return APIToken{}, err
	}
	const name = "Scheduled tasks"
	const expires = int64(253402300799) // like the assistant's: revoked with its admin, by code
	_, err := s.W.ExecContext(ctx, `INSERT INTO api_token
		(id, name, name_key, profile, secret_hash, hint, rate_per_min, created_by, created_at, expires_at, assistant_only)
		VALUES (?, ?, ?, ?, ?, '', 600, ?, ?, ?, 1)
		ON CONFLICT(id) DO UPDATE SET profile = excluded.profile, created_by = excluded.created_by,
			expires_at = excluded.expires_at, revoked_at = 0, revoked_by = ''
		WHERE api_token.assistant_only = 1`, id, name, strings.ToLower(name), ProfileOperator, secretHash, adminID, unix(now), expires)
	if err != nil {
		return APIToken{}, err
	}
	t, err := s.GetAssistantToken(ctx, id)
	if err != nil {
		return APIToken{}, err
	}
	if !t.AssistantOnly || t.CreatedBy != adminID || t.Profile != ProfileOperator {
		return APIToken{}, errors.New("store: scheduled task identity conflict")
	}
	return t, nil
}

// ErrScheduledTaskLimit is returned when the panel already holds the most tasks it allows.
var ErrScheduledTaskLimit = errors.New("store: too many scheduled tasks")

// ScheduledTask is one row of scheduled_task.
type ScheduledTask struct {
	ID             string
	Name           string
	Kind           string // "check" or "assistant"
	Enabled        bool
	ScheduleKind   string // "every", "daily" or "weekly"
	EveryMinutes   int
	MinuteOfDay    int
	Weekday        int
	Steps          string // JSON, check tasks
	PromptEnc      []byte // sealed, assistant tasks
	NotifyTelegram bool
	ConversationID string
	JitterS        int
	CreatedBy      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	NextRunAt      time.Time // zero: not scheduled (disabled)
	LeaseID        string
	LeaseUntil     time.Time
	LastManualAt   time.Time
	FailStreak     int
	FailReason     string
	Problem        bool
	ProblemSummary string
}

// Running reports whether a run holds the task's lease at now.
func (t ScheduledTask) Running(now time.Time) bool { return t.LeaseID != "" && t.LeaseUntil.After(now) }

// ScheduledTaskRun is one row of scheduled_task_run.
type ScheduledTaskRun struct {
	ID         string
	TaskID     string
	Trigger    string // "schedule", "manual" or "missed"
	Status     string // "running", "ok", "problem", "failed" or "skipped"
	Reason     string
	Summary    string
	Detail     string // JSON
	Actor      string
	StartedAt  time.Time
	FinishedAt time.Time
}

const scheduledTaskColumns = `id, name, kind, enabled, schedule_kind, every_minutes, minute_of_day, weekday, steps, prompt_enc,
	notify_telegram, conversation_id, jitter_s, created_by, created_at, updated_at, next_run_at, lease_id, lease_until,
	last_manual_at, fail_streak, fail_reason, problem, problem_summary`

func scanScheduledTask(row interface{ Scan(...any) error }) (ScheduledTask, error) {
	var t ScheduledTask
	var created, updated, next, leaseUntil, manual int64
	err := row.Scan(&t.ID, &t.Name, &t.Kind, &t.Enabled, &t.ScheduleKind, &t.EveryMinutes, &t.MinuteOfDay, &t.Weekday, &t.Steps, &t.PromptEnc,
		&t.NotifyTelegram, &t.ConversationID, &t.JitterS, &t.CreatedBy, &created, &updated, &next, &t.LeaseID, &leaseUntil,
		&manual, &t.FailStreak, &t.FailReason, &t.Problem, &t.ProblemSummary)
	if err != nil {
		return t, err
	}
	t.CreatedAt, t.UpdatedAt = fromUnix(created), fromUnix(updated)
	t.NextRunAt, t.LeaseUntil, t.LastManualAt = zeroUnix(next), zeroUnix(leaseUntil), zeroUnix(manual)
	return t, nil
}

// zeroUnix is fromUnix with 0 as the zero time.
func zeroUnix(s int64) time.Time {
	if s <= 0 {
		return time.Time{}
	}
	return fromUnix(s)
}

// unixOrZero is unix with the zero time as 0.
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return unix(t)
}

// nonNilBlob is b, or an empty blob: a nil slice is NULL to the driver, and the columns are NOT NULL.
func nonNilBlob(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

func scheduledBool(v bool) int {
	if v {
		return 1
	}
	return 0
}

// InsertScheduledTask stores a new task, unless the panel already holds max tasks (ErrScheduledTaskLimit).
func (s *Store) InsertScheduledTask(ctx context.Context, t ScheduledTask, max int) error {
	r, err := s.W.ExecContext(ctx, `INSERT INTO scheduled_task (id, name, kind, enabled, schedule_kind, every_minutes, minute_of_day, weekday,
		steps, prompt_enc, notify_telegram, conversation_id, jitter_s, created_by, created_at, updated_at, next_run_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ? WHERE (SELECT count(*) FROM scheduled_task) < ?`,
		t.ID, t.Name, t.Kind, scheduledBool(t.Enabled), t.ScheduleKind, t.EveryMinutes, t.MinuteOfDay, t.Weekday,
		t.Steps, nonNilBlob(t.PromptEnc), scheduledBool(t.NotifyTelegram), t.JitterS, t.CreatedBy, unix(t.CreatedAt), unix(t.UpdatedAt), unixOrZero(t.NextRunAt), max)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrScheduledTaskLimit
	}
	return nil
}

// UpdateScheduledTask replaces a task's definition and its next run. A new definition starts with a clean state: the failure
// count and the problem of the old one go (the alerts they held close), and the next run tells again. A run in progress keeps
// its lease. ErrNotFound for an unknown id.
func (s *Store) UpdateScheduledTask(ctx context.Context, t ScheduledTask) error {
	r, err := s.W.ExecContext(ctx, `UPDATE scheduled_task SET name = ?, enabled = ?, schedule_kind = ?, every_minutes = ?, minute_of_day = ?,
		weekday = ?, steps = ?, prompt_enc = ?, notify_telegram = ?, updated_at = ?, next_run_at = ?,
		fail_streak = 0, fail_reason = '', problem = 0, problem_summary = '' WHERE id = ?`,
		t.Name, scheduledBool(t.Enabled), t.ScheduleKind, t.EveryMinutes, t.MinuteOfDay, t.Weekday, t.Steps, nonNilBlob(t.PromptEnc),
		scheduledBool(t.NotifyTelegram), unix(t.UpdatedAt), unixOrZero(t.NextRunAt), t.ID)
	if err != nil {
		return err
	}
	if n, err := r.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ScheduledTask reads one task; ErrNotFound when there is none.
func (s *Store) ScheduledTask(ctx context.Context, id string) (ScheduledTask, error) {
	t, err := scanScheduledTask(s.R.QueryRowContext(ctx, `SELECT `+scheduledTaskColumns+` FROM scheduled_task WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// ScheduledTasks lists every task, oldest first.
func (s *Store) ScheduledTasks(ctx context.Context) ([]ScheduledTask, error) {
	return s.scheduledTasksWhere(ctx, `1 = 1 ORDER BY created_at, id`)
}

// DueScheduledTasks lists the enabled tasks whose time has come and that no run holds, the most overdue first.
func (s *Store) DueScheduledTasks(ctx context.Context, now time.Time, limit int) ([]ScheduledTask, error) {
	return s.scheduledTasksWhere(ctx, `enabled = 1 AND next_run_at > 0 AND next_run_at <= ? AND lease_until <= ? ORDER BY next_run_at, id LIMIT ?`,
		unix(now), unix(now), limit)
}

// ScheduledTaskAlerts lists the enabled tasks that hold an alert condition: a problem, or ScheduledFailingStreak failures in a row.
// It is what the health evaluator reads on every pass (the due index keeps it cheap).
func (s *Store) ScheduledTaskAlerts(ctx context.Context) ([]ScheduledTask, error) {
	return s.scheduledTasksWhere(ctx, `enabled = 1 AND (problem = 1 OR fail_streak >= ?) ORDER BY id`, ScheduledFailingStreak)
}

func (s *Store) scheduledTasksWhere(ctx context.Context, where string, args ...any) ([]ScheduledTask, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+scheduledTaskColumns+` FROM scheduled_task WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduledTask
	for rows.Next() {
		t, err := scanScheduledTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteScheduledTask deletes a task and its runs; false when there was none.
func (s *Store) DeleteScheduledTask(ctx context.Context, id string) (bool, error) {
	results, err := s.batch(ctx,
		Stmt{Query: `DELETE FROM scheduled_task_run WHERE task_id = ?`, Args: []any{id}},
		Stmt{Query: `DELETE FROM scheduled_task WHERE id = ?`, Args: []any{id}},
	)
	if err != nil {
		return false, err
	}
	return results[1].RowsAffected > 0, nil
}

// ClaimScheduledTask takes the task's lease for one run until `until`: false when another run holds it (one run of a task at a
// time), or, for a manual run (notBefore set), when the last manual run started after notBefore. The run row is written in the
// same batch, so a run exists exactly when its lease was taken.
func (s *Store) ClaimScheduledTask(ctx context.Context, run ScheduledTaskRun, leaseID string, now, until, notBefore time.Time) (bool, error) {
	manual := 0
	if run.Trigger == "manual" {
		manual = 1
	}
	results, err := s.batch(ctx,
		Stmt{Query: `UPDATE scheduled_task SET lease_id = ?, lease_until = ?, last_manual_at = CASE WHEN ? = 1 THEN ? ELSE last_manual_at END
			WHERE id = ? AND lease_until <= ? AND (? = 0 OR last_manual_at <= ?)`,
			Args: []any{leaseID, unix(until), manual, unix(now), run.TaskID, unix(now), manual, unixOrZero(notBefore)}},
		Stmt{Query: `INSERT INTO scheduled_task_run (id, task_id, trigger_kind, status, actor, started_at)
			SELECT ?, ?, ?, 'running', ?, ? WHERE EXISTS (SELECT 1 FROM scheduled_task WHERE id = ? AND lease_id = ?)`,
			Args: []any{run.ID, run.TaskID, run.Trigger, run.Actor, unix(run.StartedAt), run.TaskID, leaseID}},
	)
	if err != nil {
		return false, err
	}
	return results[0].RowsAffected > 0 && results[1].RowsAffected > 0, nil
}

// FinishScheduledRun records the outcome of a run and gives the lease back: the run's row, and on the task the next time, the
// failure count (a failed run adds one, ok and problem start it again, skipped leaves it, and so does a run the panel's stop
// interrupted) and the problem (set by a problem, cleared by ok; a failed or skipped run says nothing about it). A task deleted
// or given another lease meanwhile is left alone.
func (s *Store) FinishScheduledRun(ctx context.Context, run ScheduledTaskRun, leaseID string, next time.Time) error {
	failure := run.Status == "failed" && run.Reason != "interrupted"
	clean := run.Status == "ok" || run.Status == "problem"
	_, err := s.batch(ctx,
		Stmt{Query: `UPDATE scheduled_task_run SET status = ?, reason = ?, summary = ?, detail = ?, finished_at = ? WHERE id = ?`,
			Args: []any{run.Status, Clip(run.Reason, 64), Clip(run.Summary, 600), run.Detail, unix(run.FinishedAt), run.ID}},
		Stmt{Query: `UPDATE scheduled_task SET lease_id = '', lease_until = 0,
			next_run_at = CASE WHEN enabled = 1 THEN ? ELSE 0 END,
			fail_streak = CASE WHEN ? = 1 THEN fail_streak + 1 WHEN ? = 1 THEN 0 ELSE fail_streak END,
			fail_reason = CASE WHEN ? = 1 THEN ? WHEN ? = 1 THEN '' ELSE fail_reason END,
			problem = CASE ? WHEN 'problem' THEN 1 WHEN 'ok' THEN 0 ELSE problem END,
			problem_summary = CASE ? WHEN 'problem' THEN ? WHEN 'ok' THEN '' ELSE problem_summary END
			WHERE id = ? AND lease_id = ?`,
			Args: []any{unixOrZero(next), scheduledBool(failure), scheduledBool(clean), scheduledBool(failure), Clip(run.Reason, 64), scheduledBool(clean),
				run.Status, run.Status, Clip(run.Summary, 600), run.TaskID, leaseID}},
	)
	return err
}

// SetScheduledTaskNext moves a task's next run (a start that found it overdue, a schedule that changed with the time zone).
func (s *Store) SetScheduledTaskNext(ctx context.Context, id string, next time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE scheduled_task SET next_run_at = ? WHERE id = ? AND enabled = 1`, unixOrZero(next), id)
	return err
}

// SetScheduledTaskConversation remembers the conversation an assistant task writes into.
func (s *Store) SetScheduledTaskConversation(ctx context.Context, id, conversationID string) error {
	_, err := s.W.ExecContext(ctx, `UPDATE scheduled_task SET conversation_id = ? WHERE id = ?`, conversationID, id)
	return err
}

// RecoverScheduledRuns ends the runs a stopped panel left behind: they read as failed ("interrupted"), and every lease is given
// back. It runs once when the scheduler starts, before any run of its own; an interruption is not counted as a failure.
func (s *Store) RecoverScheduledRuns(ctx context.Context, now time.Time) (int64, error) {
	results, err := s.batch(ctx,
		Stmt{Query: `UPDATE scheduled_task_run SET status = 'failed', reason = 'interrupted', finished_at = ? WHERE status = 'running'`, Args: []any{unix(now)}},
		Stmt{Query: `UPDATE scheduled_task SET lease_id = '', lease_until = 0 WHERE lease_id <> '' OR lease_until <> 0`},
	)
	if err != nil {
		return 0, err
	}
	return results[0].RowsAffected, nil
}

const scheduledRunColumns = `id, task_id, trigger_kind, status, reason, summary, detail, actor, started_at, finished_at`

func scanScheduledRun(row interface{ Scan(...any) error }) (ScheduledTaskRun, error) {
	var r ScheduledTaskRun
	var started, finished int64
	if err := row.Scan(&r.ID, &r.TaskID, &r.Trigger, &r.Status, &r.Reason, &r.Summary, &r.Detail, &r.Actor, &started, &finished); err != nil {
		return r, err
	}
	r.StartedAt, r.FinishedAt = fromUnix(started), zeroUnix(finished)
	return r, nil
}

// ScheduledTaskRuns lists a task's runs, newest first.
func (s *Store) ScheduledTaskRuns(ctx context.Context, taskID string, limit int) ([]ScheduledTaskRun, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+scheduledRunColumns+` FROM scheduled_task_run WHERE task_id = ? ORDER BY started_at DESC, rowid DESC LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduledTaskRun
	for rows.Next() {
		r, err := scanScheduledRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ScheduledTaskRun reads one run; ErrNotFound when there is none.
func (s *Store) ScheduledTaskRun(ctx context.Context, id string) (ScheduledTaskRun, error) {
	r, err := scanScheduledRun(s.R.QueryRowContext(ctx, `SELECT `+scheduledRunColumns+` FROM scheduled_task_run WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// LastScheduledRuns is the newest run of every task that has one, by task id.
func (s *Store) LastScheduledRuns(ctx context.Context) (map[string]ScheduledTaskRun, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+scheduledRunColumns+` FROM (SELECT *, row_number() OVER (PARTITION BY task_id ORDER BY started_at DESC, rowid DESC) AS rn
		FROM scheduled_task_run) WHERE rn = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ScheduledTaskRun{}
	for rows.Next() {
		r, err := scanScheduledRun(rows)
		if err != nil {
			return nil, err
		}
		out[r.TaskID] = r
	}
	return out, rows.Err()
}

// PruneScheduledTaskRuns keeps the last ScheduledRunsKept runs of a task and none older than cutoff (a running one stays), drops
// the runs of tasks that are gone, and bounds the conversation of an assistant task the same way: its messages older than
// cutoff and beyond the newest two per kept run go, with the plan cards older than cutoff. It returns the runs deleted.
func (s *Store) PruneScheduledTaskRuns(ctx context.Context, cutoff time.Time) (int64, error) {
	const taskConversations = `SELECT conversation_id FROM scheduled_task WHERE conversation_id <> ''`
	results, err := s.batch(ctx,
		Stmt{Query: `DELETE FROM scheduled_task_run WHERE status <> 'running' AND (started_at < ? OR task_id NOT IN (SELECT id FROM scheduled_task))`, Args: []any{unix(cutoff)}},
		Stmt{Query: `DELETE FROM scheduled_task_run WHERE id IN (SELECT id FROM (SELECT id, row_number() OVER (PARTITION BY task_id ORDER BY started_at DESC, rowid DESC) AS rn
			FROM scheduled_task_run) WHERE rn > ?)`, Args: []any{ScheduledRunsKept}},
		Stmt{Query: `DELETE FROM assistant_message WHERE conversation_id IN (` + taskConversations + `) AND created_at < ?`, Args: []any{unix(cutoff)}},
		Stmt{Query: `DELETE FROM assistant_message WHERE id IN (SELECT id FROM (SELECT id, row_number() OVER (PARTITION BY conversation_id ORDER BY rowid DESC) AS rn
			FROM assistant_message WHERE conversation_id IN (` + taskConversations + `)) WHERE rn > ?)`, Args: []any{scheduledMessagesKept}},
		Stmt{Query: `DELETE FROM assistant_plan WHERE conversation_id IN (` + taskConversations + `) AND created_at < ?`, Args: []any{unix(cutoff)}},
	)
	if err != nil {
		return 0, err
	}
	return results[0].RowsAffected + results[1].RowsAffected, nil
}
