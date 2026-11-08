-- Keep path_speed start reservations for the rolling daily limits, including tests that fail before a result is stored.

-- +goose Up
CREATE TABLE path_speed_start (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    actor_id     TEXT    NOT NULL,
    from_node_id TEXT    NOT NULL,
    to_node_id   TEXT    NOT NULL,
    at_unix      INTEGER NOT NULL
);
CREATE INDEX path_speed_start_at ON path_speed_start (at_unix);
CREATE INDEX path_speed_start_actor_at ON path_speed_start (actor_id, at_unix);
CREATE INDEX path_speed_start_from_at ON path_speed_start (from_node_id, at_unix);
CREATE INDEX path_speed_start_to_at ON path_speed_start (to_node_id, at_unix);

-- +goose Down
DROP INDEX path_speed_start_to_at;
DROP INDEX path_speed_start_from_at;
DROP INDEX path_speed_start_actor_at;
DROP INDEX path_speed_start_at;
DROP TABLE path_speed_start;
