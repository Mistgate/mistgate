-- Idle devices stop filling a place after thirty days without a connection or subscription fetch.

-- +goose Up
INSERT INTO setting (k, v) VALUES ('device_idle_after_days', '30') ON CONFLICT(k) DO NOTHING;

-- +goose Down
DELETE FROM setting WHERE k = 'device_idle_after_days';
