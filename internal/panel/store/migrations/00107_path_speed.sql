-- Bounded throughput tests between two fleet nodes (the MCP tool path_speed): the target serves 8 to 32 MB, the sender measures. The
-- result is JSON (megabits per second, connect time, time to the first byte, duration); a caller sees node names only, never an
-- address. Kept 7 days; the panel VPS prunes it (the Cloudflare edition has no such test). A table of its own: path_probe's kind
-- check cannot be widened without rebuilding it on both SQLite and D1.

-- +goose Up
CREATE TABLE path_speed (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    from_node_id TEXT    NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    to_node_id   TEXT    NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    direction    TEXT    NOT NULL CHECK (direction IN ('down', 'up')),
    at_unix      INTEGER NOT NULL,
    result_json  TEXT    NOT NULL
);
CREATE INDEX path_speed_at ON path_speed (at_unix);
CREATE INDEX path_speed_pair ON path_speed (from_node_id, to_node_id, at_unix);

-- +goose Down
DROP INDEX path_speed_pair;
DROP INDEX path_speed_at;
DROP TABLE path_speed;
