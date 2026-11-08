-- Persist the latest agent update progress with the rollout step so edge isolates
-- and panel restarts can render the same state.

-- +goose Up
ALTER TABLE update_step ADD COLUMN progress_phase TEXT NOT NULL DEFAULT '';
ALTER TABLE update_step ADD COLUMN progress_rank INTEGER NOT NULL DEFAULT -1;
ALTER TABLE update_step ADD COLUMN progress_downloaded INTEGER NOT NULL DEFAULT 0;
ALTER TABLE update_step ADD COLUMN progress_total INTEGER NOT NULL DEFAULT 0;
ALTER TABLE update_step ADD COLUMN progress_speed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE update_step ADD COLUMN progress_updated_unix INTEGER NOT NULL DEFAULT 0;
ALTER TABLE update_step ADD COLUMN progress_error TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE update_step DROP COLUMN progress_error;
ALTER TABLE update_step DROP COLUMN progress_updated_unix;
ALTER TABLE update_step DROP COLUMN progress_speed;
ALTER TABLE update_step DROP COLUMN progress_total;
ALTER TABLE update_step DROP COLUMN progress_downloaded;
ALTER TABLE update_step DROP COLUMN progress_rank;
ALTER TABLE update_step DROP COLUMN progress_phase;
