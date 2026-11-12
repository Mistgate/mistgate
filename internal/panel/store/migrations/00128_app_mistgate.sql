-- The Mistgate app switch of a person. Stored as "off" so that a row nobody touched (every person before this migration, every
-- insert that does not name it) keeps what it had: the app works for a person whose subscription link is on. The app rides on
-- that link, so it is allowed only while app_happ is on as well.

-- +goose Up
ALTER TABLE user ADD COLUMN app_mistgate_off INTEGER NOT NULL DEFAULT 0
    CHECK (app_mistgate_off IN (0, 1));

-- +goose Down
ALTER TABLE user DROP COLUMN app_mistgate_off;
