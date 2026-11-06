-- +goose Up
ALTER TABLE inbound ADD COLUMN direct_in_subscriptions INTEGER NOT NULL DEFAULT 1 CHECK (direct_in_subscriptions IN (0, 1));

-- +goose Down
ALTER TABLE inbound DROP COLUMN direct_in_subscriptions;
