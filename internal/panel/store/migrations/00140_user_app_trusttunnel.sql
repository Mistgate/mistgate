-- The TrustTunnel app is independent of the subscription-link apps. Its per-person tt:// key is a counted key device.

-- +goose Up
ALTER TABLE user ADD COLUMN app_trusttunnel INTEGER NOT NULL DEFAULT 0
    CHECK (app_trusttunnel IN (0, 1));

-- +goose Down
ALTER TABLE user DROP COLUMN app_trusttunnel;
