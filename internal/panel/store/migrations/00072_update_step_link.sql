-- Recorded update commands and per-step download credentials for the edge fleet.
-- Additive: existing rollout steps remain empty and keep their current state.

-- +goose Up
ALTER TABLE update_step ADD COLUMN request_id TEXT NOT NULL DEFAULT '';
ALTER TABLE update_step ADD COLUMN request_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE update_step ADD COLUMN answer_json TEXT NOT NULL DEFAULT '';
ALTER TABLE update_step ADD COLUMN download_sha TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE update_step DROP COLUMN download_sha;
ALTER TABLE update_step DROP COLUMN answer_json;
ALTER TABLE update_step DROP COLUMN request_at;
ALTER TABLE update_step DROP COLUMN request_id;
