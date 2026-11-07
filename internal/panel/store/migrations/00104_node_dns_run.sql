-- Lookup timing of a node's resolver per run of the doctor's resolver check (the MCP tool node_dns_check): median and 95th
-- percentile in milliseconds, how many control domains failed and how many were asked. The last 24 runs of a node are kept.

-- +goose Up
CREATE TABLE node_dns_run (
    node_id TEXT    NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    at_unix INTEGER NOT NULL,
    p50_ms  INTEGER NOT NULL,
    p95_ms  INTEGER NOT NULL,
    failed  INTEGER NOT NULL,
    domains INTEGER NOT NULL,
    PRIMARY KEY (node_id, at_unix)
) WITHOUT ROWID;

-- +goose Down
DROP TABLE node_dns_run;
