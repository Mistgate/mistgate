-- +goose Up
-- When the session last proved a sign-in factor (passkey or authenticator code): the clock of step-up
-- re-authentication. Set to the creation time at sign-in.
ALTER TABLE session ADD COLUMN stepup_at INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE session DROP COLUMN stepup_at;
