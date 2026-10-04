-- Optional per-node network capacity used to calculate user-facing live utilization.
-- Zero means unknown and keeps the percentage hidden.
-- +goose Up
ALTER TABLE node ADD COLUMN bandwidth_mbps INTEGER NOT NULL DEFAULT 0
    CHECK (bandwidth_mbps BETWEEN 0 AND 1000000);

-- +goose Down
ALTER TABLE node DROP COLUMN bandwidth_mbps;
