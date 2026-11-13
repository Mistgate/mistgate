-- The newest session a node saw on a device, of any protocol (0 = none seen). A link device's last_seen_at is its last
-- subscription fetch, so the idle rule (a device unused for N days stops filling a place) reads this too: an app that
-- tunnels every day but never refetches its subscription is in use.

-- +goose Up
ALTER TABLE device ADD COLUMN last_connected_at INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE device DROP COLUMN last_connected_at;
