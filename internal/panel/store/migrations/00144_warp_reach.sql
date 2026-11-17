-- WARP reach (agent.proto "WARP REACH"): what the sites of the owner's list (setting warp.reach_domains) showed through a node's
-- WARP the last time the node checked: reach_json is {"colo", "domains": [{"domain", "verdict", "status", "ms", "streak"}]},
-- streak counting the checks in a row that said "blocked" (the Health alert warp_reach opens at 2). reach_at is when the panel
-- stored it (0 = never). Nothing to backfill: the first check fills them.

-- +goose Up
ALTER TABLE warp_account ADD COLUMN reach_json TEXT NOT NULL DEFAULT '';
ALTER TABLE warp_account ADD COLUMN reach_at INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE warp_account DROP COLUMN reach_at;
ALTER TABLE warp_account DROP COLUMN reach_json;
