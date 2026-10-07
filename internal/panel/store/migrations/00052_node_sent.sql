-- +goose Up
CREATE TABLE node_sent (
    node_id TEXT PRIMARY KEY REFERENCES node(id) ON DELETE CASCADE,
    digest TEXT NOT NULL
);

-- +goose Down
DROP TABLE node_sent;
