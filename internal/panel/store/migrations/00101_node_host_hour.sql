-- Host load per node and hour, rolled up on ingest from HostMetrics (agent.proto). The panel kept only the latest sample in
-- node_live. Percentages are stored times 100 and the sums with the sample count, so a mean is sum / samples. Kept 90 days.

-- +goose Up
CREATE TABLE node_host_hour (
    node_id          TEXT    NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    hour_start       INTEGER NOT NULL CHECK (hour_start % 3600 = 0),
    samples          INTEGER NOT NULL DEFAULT 0,
    cpu_sum_x100     INTEGER NOT NULL DEFAULT 0,
    cpu_max_x100     INTEGER NOT NULL DEFAULT 0,
    softirq_max_x100 INTEGER NOT NULL DEFAULT 0,
    load1_max_x100   INTEGER NOT NULL DEFAULT 0,
    ram_used_max     INTEGER NOT NULL DEFAULT 0,
    ram_total        INTEGER NOT NULL DEFAULT 0,
    rx_bps_sum       INTEGER NOT NULL DEFAULT 0,
    rx_bps_max       INTEGER NOT NULL DEFAULT 0,
    tx_bps_sum       INTEGER NOT NULL DEFAULT 0,
    tx_bps_max       INTEGER NOT NULL DEFAULT 0,
    sessions_max     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, hour_start)
) STRICT, WITHOUT ROWID;
CREATE INDEX node_host_hour_hour ON node_host_hour (hour_start);

-- +goose Down
DROP INDEX node_host_hour_hour;
DROP TABLE node_host_hour;
