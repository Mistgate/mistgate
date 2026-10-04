-- A separate public-facing name for the personal subscription page.
-- +goose Up
ALTER TABLE user ADD COLUMN subscription_name TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE user DROP COLUMN subscription_name;
