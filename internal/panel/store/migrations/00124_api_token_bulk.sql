-- +goose Up
ALTER TABLE api_token ADD COLUMN allow_bulk_user_changes INTEGER NOT NULL DEFAULT 0 CHECK (allow_bulk_user_changes IN (0, 1));

-- +goose Down
ALTER TABLE api_token DROP COLUMN allow_bulk_user_changes;
