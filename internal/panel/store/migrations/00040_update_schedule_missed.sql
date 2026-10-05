-- A node-update schedule that could not start within two hours of its time is marked missed (Unix seconds; 0 = not
-- missed) and never starts by itself; saving the schedule again clears it.
-- The update time zone default became UTC. An existing installation (one with an admin) keeps entering schedules in
-- UTC+3, the old default, unless it chose an offset already.
-- +goose Up
ALTER TABLE node_update_schedule ADD COLUMN missed_at INTEGER NOT NULL DEFAULT 0;
INSERT OR IGNORE INTO setting (k, v)
    SELECT 'update_schedule_timezone_offset_minutes', '180' WHERE EXISTS (SELECT 1 FROM admin);

-- +goose Down
ALTER TABLE node_update_schedule DROP COLUMN missed_at;
