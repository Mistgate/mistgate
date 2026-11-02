-- The Telegram app toggle. The user table's original CHECK still requires at
-- least one of Happ and Amnezia, so Telegram is additive in the first version:
-- a user turns it on next to one of the existing apps.

-- +goose Up
ALTER TABLE user ADD COLUMN app_telegram INTEGER NOT NULL DEFAULT 0
    CHECK (app_telegram IN (0, 1));

-- +goose Down
ALTER TABLE user DROP COLUMN app_telegram;
