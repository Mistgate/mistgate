-- Keep the effective command wait chosen when an update step was sent.
-- A later settings change or panel restart must not change the deadline of an
-- update that is already in progress.

-- +goose Up
ALTER TABLE update_step ADD COLUMN answer_wait_seconds INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE update_step DROP COLUMN answer_wait_seconds;
