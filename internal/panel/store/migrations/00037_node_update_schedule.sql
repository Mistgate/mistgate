-- Per-node signed-agent update schedules. Times are absolute UTC Unix seconds;
-- the offset records how the owner entered and sees the wall-clock time.
-- +goose Up
CREATE TABLE node_update_schedule (
    node_id                    TEXT PRIMARY KEY REFERENCES node (id) ON DELETE CASCADE,
    to_version                 TEXT    NOT NULL,
    to_built                   INTEGER NOT NULL,
    scheduled_at               INTEGER NOT NULL,
    timezone_offset_minutes    INTEGER NOT NULL CHECK (timezone_offset_minutes BETWEEN -720 AND 840),
    created_by                 TEXT    NOT NULL DEFAULT '',
    created_at                 INTEGER NOT NULL
) STRICT;

CREATE INDEX node_update_schedule_due ON node_update_schedule (scheduled_at, node_id);

-- +goose Down
DROP INDEX node_update_schedule_due;
DROP TABLE node_update_schedule;
