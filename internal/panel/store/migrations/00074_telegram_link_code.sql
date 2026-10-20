-- Telegram chat-link codes are short-lived capabilities. Store only their hash so they survive a VPS restart and can be
-- consumed atomically by either edition.

-- +goose Up
CREATE TABLE telegram_link_code (
    hash       TEXT PRIMARY KEY CHECK (length(hash) = 64),
    admin_id   TEXT NOT NULL UNIQUE REFERENCES admin(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL
) STRICT;

CREATE INDEX telegram_link_code_expiry ON telegram_link_code(expires_at);

-- +goose Down
DROP TABLE telegram_link_code;
