-- traffic_bucket (per person, node, protocol and hour) had no index on the hour alone, so pruning it by age would scan the whole
-- table while holding the only writer. This index serves that prune (retention.traffic_bucket_days, default 400). node_traffic_hour
-- already has node_traffic_hour_hour.

-- +goose Up
CREATE INDEX IF NOT EXISTS traffic_bucket_hour ON traffic_bucket (hour_start);

-- +goose Down
DROP INDEX IF EXISTS traffic_bucket_hour;
