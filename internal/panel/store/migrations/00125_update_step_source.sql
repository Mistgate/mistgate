-- Persist the source reported by UpdateProgress so the Updates page can show source switches after a poll or restart.

-- +goose Up
ALTER TABLE update_step ADD COLUMN progress_source TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE update_step DROP COLUMN progress_source;
