-- The channel measure of a node (the owner's "measure the channel" action: 8 parallel streams against the widest other nodes): when
-- the last one started, which keeps the pause between two of them across a restart and counts one that failed, and the last that gave
-- a figure. The tests it ran are also kept as path_speed rows (7 days); this row is the answer the node page and the load page show.

-- +goose Up
CREATE TABLE node_channel_measure (
    node_id      TEXT PRIMARY KEY REFERENCES node(id) ON DELETE CASCADE,
    started_unix INTEGER NOT NULL,
    at_unix      INTEGER NOT NULL DEFAULT 0,
    result_json  TEXT    NOT NULL DEFAULT ''
);

-- +goose Down
DROP TABLE node_channel_measure;
