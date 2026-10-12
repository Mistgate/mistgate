-- +goose Up
-- The latest TCP port check of a node, one row per port (design/vless.md section 7.4). A run replaces the node's rows.
CREATE TABLE node_tcp_check (
    node_id    TEXT    NOT NULL REFERENCES node(id) ON DELETE CASCADE,
    port       INTEGER NOT NULL,
    role       TEXT    NOT NULL,
    alpn       INTEGER NOT NULL DEFAULT 0,
    profile    TEXT    NOT NULL DEFAULT '',
    verdict    TEXT    NOT NULL,
    reason     TEXT    NOT NULL DEFAULT '',
    sender     TEXT    NOT NULL,
    checked_at INTEGER NOT NULL,
    PRIMARY KEY (node_id, port)
);

-- +goose Down
DROP TABLE node_tcp_check;
