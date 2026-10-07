-- The start of the CURRENT episode of an alert. first_seen stays the first time it ever fired, because an alert that fires
-- again within an hour is re-opened in the same row; opened_at moves to the re-open, so a duration is measured from it.
-- +goose Up
ALTER TABLE health_alert ADD COLUMN opened_at INTEGER NOT NULL DEFAULT 0;
UPDATE health_alert SET opened_at = first_seen;

-- +goose Down
ALTER TABLE health_alert DROP COLUMN opened_at;
