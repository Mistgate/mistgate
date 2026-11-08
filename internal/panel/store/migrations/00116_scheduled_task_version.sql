-- The task version prevents stale task-update plans from overwriting newer definitions.

-- +goose Up
ALTER TABLE scheduled_task ADD COLUMN version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0);
