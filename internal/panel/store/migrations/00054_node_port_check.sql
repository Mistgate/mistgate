-- +goose Up
CREATE TABLE node_port_check (
    node_id    TEXT    NOT NULL REFERENCES node(id) ON DELETE CASCADE,
    port       INTEGER NOT NULL,
    address    TEXT    NOT NULL,
    sent       INTEGER NOT NULL,
    got        INTEGER NOT NULL,
    verdict    TEXT    NOT NULL,
    sender     TEXT    NOT NULL,
    checked_at INTEGER NOT NULL,
    bad_at     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, port)
);

-- +goose Down
DROP TABLE node_port_check;
