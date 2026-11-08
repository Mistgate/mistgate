-- Scheduled tasks (mistgate.tasks.v1, design/adr/0017-agent-operable-fleet.md section 5): the owner's periodic checks of the
-- fleet. The VPS edition runs them; the Cloudflare edition has the tables (one migration set) but nothing writes them.
--
-- scheduled_task is one task. kind 'check' keeps its steps (tools, arguments, conditions) as JSON in steps; kind 'assistant'
-- keeps its prompt sealed with the panel vault (associated data "scheduled-task-prompt:<id>"), like the assistant's notes. The
-- schedule is schedule_kind with every_minutes, or minute_of_day (and weekday) in the panel's time zone; jitter_s is the task's
-- own delay after its time, fixed when it is created. next_run_at is when it runs next (0: disabled); lease_until marks a run
-- in progress (one run of a task at a time). fail_streak counts failed runs in a row; problem is set by a run that found a
-- problem and cleared by a clean one. Both are what the health evaluator reads for the SCHEDULED_* alerts.
--
-- scheduled_task_run is one run: its trigger, status, reason code and a one-line summary, and detail (JSON: the step results
-- of a check, the provider and cost of an assistant run; tool results are already scrubbed). The retention sweep keeps the
-- last 50 runs of a task, at most 30 days. No secret is ever stored in either table: names, arguments and prompts are refused
-- when they look like one.
--
-- An assistant task writes its answers into a conversation of its own: assistant_conversation.task_id names the task. The
-- assistant's fallback provider (assistant_settings.fallback_provider) is where a scheduled run goes when the main provider
-- keeps failing; 0 = none.

-- +goose Up
CREATE TABLE scheduled_task (
    id              TEXT    PRIMARY KEY,
    name            TEXT    NOT NULL,
    kind            TEXT    NOT NULL CHECK (kind IN ('check', 'assistant')),
    enabled         INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    schedule_kind   TEXT    NOT NULL CHECK (schedule_kind IN ('every', 'daily', 'weekly')),
    every_minutes   INTEGER NOT NULL DEFAULT 0,
    minute_of_day   INTEGER NOT NULL DEFAULT 0,
    weekday         INTEGER NOT NULL DEFAULT 0,
    steps           TEXT    NOT NULL DEFAULT '[]',
    prompt_enc      BLOB    NOT NULL DEFAULT X'',
    notify_telegram INTEGER NOT NULL DEFAULT 0 CHECK (notify_telegram IN (0, 1)),
    conversation_id TEXT    NOT NULL DEFAULT '',
    jitter_s        INTEGER NOT NULL DEFAULT 0,
    created_by      TEXT    NOT NULL,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    next_run_at     INTEGER NOT NULL DEFAULT 0,
    lease_id        TEXT    NOT NULL DEFAULT '',
    lease_until     INTEGER NOT NULL DEFAULT 0,
    last_manual_at  INTEGER NOT NULL DEFAULT 0,
    fail_streak     INTEGER NOT NULL DEFAULT 0,
    fail_reason     TEXT    NOT NULL DEFAULT '',
    problem         INTEGER NOT NULL DEFAULT 0 CHECK (problem IN (0, 1)),
    problem_summary TEXT    NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX scheduled_task_due ON scheduled_task (enabled, next_run_at);

CREATE TABLE scheduled_task_run (
    id          TEXT    PRIMARY KEY,
    task_id     TEXT    NOT NULL,
    trigger_kind TEXT   NOT NULL CHECK (trigger_kind IN ('schedule', 'manual', 'missed')),
    status      TEXT    NOT NULL CHECK (status IN ('running', 'ok', 'problem', 'failed', 'skipped')),
    reason      TEXT    NOT NULL DEFAULT '',
    summary     TEXT    NOT NULL DEFAULT '',
    detail      TEXT    NOT NULL DEFAULT '{}',
    actor       TEXT    NOT NULL DEFAULT '',
    started_at  INTEGER NOT NULL,
    finished_at INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX scheduled_task_run_task ON scheduled_task_run (task_id, started_at);
CREATE INDEX scheduled_task_run_started ON scheduled_task_run (started_at);

ALTER TABLE assistant_settings ADD COLUMN fallback_provider INTEGER NOT NULL DEFAULT 0;
ALTER TABLE assistant_conversation ADD COLUMN task_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE assistant_conversation DROP COLUMN task_id;
ALTER TABLE assistant_settings DROP COLUMN fallback_provider;
DROP INDEX scheduled_task_run_started;
DROP INDEX scheduled_task_run_task;
DROP TABLE scheduled_task_run;
DROP INDEX scheduled_task_due;
DROP TABLE scheduled_task;
