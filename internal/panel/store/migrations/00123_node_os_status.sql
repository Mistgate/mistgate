-- One newest read-only operating-system snapshot per node.

-- +goose Up
CREATE TABLE node_os_status (
    node_id TEXT PRIMARY KEY NOT NULL REFERENCES node(id) ON DELETE CASCADE,
    status_json TEXT NOT NULL CHECK (json_valid(status_json)),
    measured_at INTEGER NOT NULL,
    received_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE node_os_status;
