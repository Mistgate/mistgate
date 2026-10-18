-- Retain the node/protocol relationship for delayed reliable stats from an inbound that was deleted.
-- The node foreign key removes these tombstones when the node itself is forgotten.

-- +goose Up
CREATE TABLE node_inbound_stats_tombstone (
    node_id    TEXT NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    inbound_id TEXT NOT NULL,
    protocol   TEXT NOT NULL,
    PRIMARY KEY (node_id, inbound_id)
) STRICT, WITHOUT ROWID;

-- +goose Down
DROP TABLE node_inbound_stats_tombstone;
