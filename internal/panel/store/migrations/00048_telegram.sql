-- Telegram alerts. One bot per panel (the owner pastes its token, sealed by the vault; a row exists only while a bot is
-- set), and a chat per admin who linked one. A chat belongs to a bot: replacing or clearing the bot deletes every link
-- (the code does it in the same transaction). One chat belongs to one admin.

-- +goose Up
CREATE TABLE telegram_bot (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    token       BLOB NOT NULL,          -- vault ciphertext, record "telegram_bot.token"; never returned by an RPC
    bot_id      INTEGER NOT NULL,       -- from getMe: tells a new bot from the same one pasted again
    username    TEXT NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;

CREATE TABLE telegram_link (
    admin_id   TEXT PRIMARY KEY REFERENCES admin(id) ON DELETE CASCADE,
    chat_id    INTEGER NOT NULL UNIQUE,
    enabled    INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    linked_at  INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE telegram_link;
DROP TABLE telegram_bot;
