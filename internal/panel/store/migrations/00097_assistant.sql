-- Assistant state. User-visible text, provider transcripts, plan confirmations and provider keys are sealed by the
-- assistant service with record-specific AAD before they reach these tables.

-- +goose Up

ALTER TABLE api_token ADD COLUMN assistant_only INTEGER NOT NULL DEFAULT 0 CHECK (assistant_only IN (0, 1));
DROP INDEX api_token_live_name;
CREATE UNIQUE INDEX api_token_live_name ON api_token(name_key) WHERE revoked_at = 0 AND assistant_only = 0;

CREATE TABLE assistant_settings (
    id             INTEGER PRIMARY KEY CHECK (id = 1),
    enabled        INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    provider       INTEGER NOT NULL DEFAULT 0,
    effort         INTEGER NOT NULL DEFAULT 2,
    audience       INTEGER NOT NULL DEFAULT 1,
    panel_notes    TEXT NOT NULL DEFAULT '',
    retention_days INTEGER NOT NULL DEFAULT 90,
    updated_at     INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE assistant_provider (
    provider         INTEGER PRIMARY KEY,
    key_enc          BLOB NOT NULL DEFAULT X'',
    key_hint         TEXT NOT NULL DEFAULT '',
    added_at         INTEGER NOT NULL DEFAULT 0,
    model            TEXT NOT NULL DEFAULT '',
    budget_usd       REAL NOT NULL DEFAULT 20 CHECK (budget_usd >= 0),
    last_error_code  TEXT NOT NULL DEFAULT '',
    last_error_at    INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE assistant_conversation (
    id          TEXT PRIMARY KEY,
    admin_id    TEXT NOT NULL,
    title_enc   BLOB NOT NULL DEFAULT X'',
    summary_enc BLOB NOT NULL DEFAULT X'',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    segment     INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX assistant_conversation_admin ON assistant_conversation(admin_id, updated_at DESC, id DESC);

CREATE TABLE assistant_message (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    admin_id        TEXT NOT NULL,
    role            TEXT NOT NULL CHECK (role IN ('user', 'assistant', 'summary')),
    segment         INTEGER NOT NULL DEFAULT 0,
    body_enc        BLOB NOT NULL,
    created_at      INTEGER NOT NULL
) STRICT;
CREATE INDEX assistant_message_conversation ON assistant_message(conversation_id, created_at, id);
CREATE INDEX assistant_message_admin ON assistant_message(admin_id, created_at);

CREATE TABLE assistant_plan (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    admin_id        TEXT NOT NULL,
    token_id        TEXT NOT NULL REFERENCES api_token(id),
    tool            TEXT NOT NULL,
    confirm_enc     BLOB NOT NULL,
    card_enc        BLOB NOT NULL,
    inbox_only      INTEGER NOT NULL DEFAULT 0 CHECK (inbox_only IN (0, 1)),
    state           TEXT NOT NULL DEFAULT 'proposed',
    created_at      INTEGER NOT NULL,
    expires_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
) STRICT;
CREATE INDEX assistant_plan_conversation ON assistant_plan(conversation_id, admin_id, created_at);

CREATE TABLE assistant_memory (
    id              TEXT PRIMARY KEY,
    admin_id        TEXT NOT NULL,
    text_enc        BLOB NOT NULL,
    by_assistant    INTEGER NOT NULL DEFAULT 0 CHECK (by_assistant IN (0, 1)),
    conversation_id TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
) STRICT;
CREATE INDEX assistant_memory_admin ON assistant_memory(admin_id, created_at, id);

CREATE TABLE assistant_usage (
    month          TEXT NOT NULL,
    admin_id       TEXT NOT NULL,
    provider       INTEGER NOT NULL,
    input_tokens   INTEGER NOT NULL DEFAULT 0,
    output_tokens  INTEGER NOT NULL DEFAULT 0,
    usd            REAL NOT NULL DEFAULT 0,
    turns          INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(month, admin_id, provider)
) STRICT;

CREATE TABLE assistant_lock (
    conversation_id TEXT PRIMARY KEY,
    admin_id        TEXT NOT NULL,
    lease_id        TEXT NOT NULL,
    expires_at      INTEGER NOT NULL,
    stop_requested  INTEGER NOT NULL DEFAULT 0 CHECK (stop_requested IN (0, 1))
) STRICT;

CREATE TABLE assistant_rate (
    admin_id     TEXT NOT NULL,
    minute       INTEGER NOT NULL,
    turns        INTEGER NOT NULL,
    PRIMARY KEY(admin_id, minute)
) STRICT;

-- +goose Down

DROP TABLE assistant_rate;
DROP TABLE assistant_lock;
DROP TABLE assistant_usage;
DROP TABLE assistant_memory;
DROP TABLE assistant_plan;
DROP TABLE assistant_message;
DROP TABLE assistant_conversation;
DROP TABLE assistant_provider;
DROP TABLE assistant_settings;
DROP INDEX api_token_live_name;
ALTER TABLE api_token DROP COLUMN assistant_only;
CREATE UNIQUE INDEX api_token_live_name ON api_token(name_key) WHERE revoked_at = 0;
