-- health_check_sample.connect_ms and response_ms split the old latency_ms (the cold time of the whole round) in two:
-- connect_ms is the time from the start of the dial until the first connection through the tunnel is open (the tunnel
-- handshake plus the TCP connect), response_ms is one more generate_204 request on that open HTTPS connection (what a
-- person feels while the tunnel is up). 0 = not measured: rows written before this migration, and rounds whose second
-- request did not reuse the connection. latency_ms keeps its meaning, so history stays comparable.

-- +goose Up
ALTER TABLE health_check_sample ADD COLUMN connect_ms INTEGER NOT NULL DEFAULT 0;
ALTER TABLE health_check_sample ADD COLUMN response_ms INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE health_check_sample DROP COLUMN response_ms;
ALTER TABLE health_check_sample DROP COLUMN connect_ms;
