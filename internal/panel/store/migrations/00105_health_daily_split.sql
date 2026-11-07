-- health_check_daily keeps the percentiles of the two parts of a round as well as of its cold time (latency_ms, 00012):
-- connect_ms (the time to open the tunnel) and response_ms (one request on the open connection, what a person feels), and
-- jitter_ms, the standard deviation of response_ms over the day, in milliseconds. 0 = not measured, which is also what a
-- day rolled up before this migration holds.

-- +goose Up
ALTER TABLE health_check_daily ADD COLUMN connect_p50_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE health_check_daily ADD COLUMN connect_p95_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE health_check_daily ADD COLUMN response_p50_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE health_check_daily ADD COLUMN response_p95_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE health_check_daily ADD COLUMN jitter_ms INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE health_check_daily DROP COLUMN jitter_ms;
ALTER TABLE health_check_daily DROP COLUMN response_p95_ms;
ALTER TABLE health_check_daily DROP COLUMN response_p50_ms;
ALTER TABLE health_check_daily DROP COLUMN connect_p95_ms;
ALTER TABLE health_check_daily DROP COLUMN connect_p50_ms;
