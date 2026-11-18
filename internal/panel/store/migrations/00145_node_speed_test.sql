-- When the panel made a node measure its speed (the bandwidth measurement and the leg speed test). The capacity report leaves
-- the traffic peak of those hours out: the measurement fills the link by design, and read as people's traffic it called an
-- idle node overloaded. Rows older than the report's window are removed when a new one is written.

-- +goose Up
CREATE TABLE node_speed_test (
    node_id TEXT NOT NULL REFERENCES node(id) ON DELETE CASCADE,
    at      INTEGER NOT NULL,
    PRIMARY KEY (node_id, at)
);

-- +goose Down
DROP TABLE node_speed_test;
