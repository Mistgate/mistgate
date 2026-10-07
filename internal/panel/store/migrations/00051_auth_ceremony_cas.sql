-- Pending authentication ceremonies are transient and may be dropped on upgrade.
-- +goose Up
DROP TABLE auth_ceremony;

CREATE TABLE auth_ceremony (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    source_key TEXT NOT NULL DEFAULT '',
    session_data TEXT NOT NULL CHECK (json_valid(session_data)),
    token_hash BLOB NOT NULL DEFAULT X'',
    admin_id TEXT NOT NULL DEFAULT '',
    admin_display_name TEXT NOT NULL DEFAULT '',
    admin_role TEXT NOT NULL DEFAULT '',
    admin_user_handle BLOB NOT NULL DEFAULT X'',
    name TEXT NOT NULL DEFAULT '',
    login TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL DEFAULT '',
    totp_enc BLOB NOT NULL DEFAULT X'',
    tries INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER NOT NULL
) STRICT;

CREATE INDEX auth_ceremony_expiry ON auth_ceremony (expires_at);
CREATE INDEX auth_ceremony_source_expiry ON auth_ceremony (source_key, expires_at);

-- +goose Down
DROP TABLE auth_ceremony;

CREATE TABLE auth_ceremony (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    source_key TEXT NOT NULL DEFAULT '',
    session_data TEXT NOT NULL CHECK (json_valid(session_data)),
    token_hash BLOB NOT NULL DEFAULT X'',
    admin_json TEXT NOT NULL CHECK (json_valid(admin_json)),
    name TEXT NOT NULL DEFAULT '',
    login TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL DEFAULT '',
    totp_enc BLOB NOT NULL DEFAULT X'',
    tries INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER NOT NULL
) STRICT;

CREATE INDEX auth_ceremony_expiry ON auth_ceremony (expires_at);
CREATE INDEX auth_ceremony_source_expiry ON auth_ceremony (source_key, expires_at);
