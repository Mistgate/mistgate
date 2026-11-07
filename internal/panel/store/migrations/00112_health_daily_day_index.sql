-- Health history is read and pruned by day, but its primary key starts with inbound_id.
-- The day index keeps those operations from scanning every inbound on D1.

-- +goose Up
CREATE INDEX health_check_daily_day_inbound ON health_check_daily (day, inbound_id);

-- +goose Down
DROP INDEX health_check_daily_day_inbound;
