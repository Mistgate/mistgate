-- +goose Up
CREATE TABLE admin (
    id           TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    role         TEXT NOT NULL CHECK (role IN ('owner', 'helper', 'readonly')),
    user_handle  BLOB NOT NULL UNIQUE,      -- WebAuthn user.id, random, never shown
    created_at   INTEGER NOT NULL
) STRICT;

CREATE TABLE passkey (
    id               TEXT PRIMARY KEY,
    admin_id         TEXT NOT NULL REFERENCES admin(id) ON DELETE CASCADE,
    credential_id    BLOB NOT NULL UNIQUE,
    public_key       BLOB NOT NULL,          -- COSE key
    attestation_type TEXT NOT NULL DEFAULT '',
    sign_count       INTEGER NOT NULL DEFAULT 0,
    aaguid           BLOB NOT NULL DEFAULT x'',
    transports       TEXT NOT NULL DEFAULT '',   -- comma separated
    flags            INTEGER NOT NULL DEFAULT 0, -- raw authenticator data flags byte (UP/UV/BE/BS)
    name             TEXT NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL,
    last_used_at     INTEGER
) STRICT;
CREATE INDEX passkey_admin ON passkey(admin_id);

CREATE TABLE session (
    token_hash   BLOB PRIMARY KEY,           -- SHA-256 of the cookie value
    admin_id     TEXT NOT NULL REFERENCES admin(id) ON DELETE CASCADE,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,           -- absolute lifetime
    ip           TEXT NOT NULL DEFAULT '',
    user_agent   TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX session_admin ON session(admin_id);

CREATE TABLE setup_token (
    hash       BLOB PRIMARY KEY,             -- SHA-256 of the one-time token
    expires_at INTEGER NOT NULL,
    used_at    INTEGER
) STRICT;

CREATE TABLE audit (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    ts     INTEGER NOT NULL,
    actor  TEXT NOT NULL,                    -- admin id or 'anonymous'
    action TEXT NOT NULL,
    params TEXT NOT NULL DEFAULT '{}',       -- JSON, never contains secrets
    result TEXT NOT NULL
) STRICT;
CREATE INDEX audit_ts ON audit(ts);

CREATE TABLE setting (
    k TEXT PRIMARY KEY,
    v TEXT NOT NULL
) STRICT;

-- +goose Down
DROP TABLE setting;
DROP TABLE audit;
DROP TABLE setup_token;
DROP TABLE session;
DROP TABLE passkey;
DROP TABLE admin;
