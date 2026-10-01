-- +goose Up
-- Password + authenticator-code sign-in (the passkey fallback chosen at setup).
CREATE TABLE admin_password (
    admin_id    TEXT PRIMARY KEY REFERENCES admin(id) ON DELETE CASCADE,
    login       TEXT NOT NULL UNIQUE,        -- lower case
    hash        TEXT NOT NULL,               -- argon2id, PHC string
    totp_secret BLOB NOT NULL,               -- sealed with the master key, AAD = admin id
    totp_step   INTEGER NOT NULL DEFAULT 0,  -- last accepted 30 s step: a code works once
    created_at  INTEGER NOT NULL
) STRICT;

-- Failed password sign-ins per login name, whether or not the account exists, so a
-- lockout looks the same for both.
CREATE TABLE login_failure (
    login        TEXT PRIMARY KEY,
    failures     INTEGER NOT NULL,
    locked_until INTEGER NOT NULL DEFAULT 0,
    last_at      INTEGER NOT NULL
) STRICT;

ALTER TABLE audit ADD COLUMN source TEXT NOT NULL DEFAULT 'panel' CHECK (source IN ('panel', 'bot', 'mcp', 'api'));
ALTER TABLE audit ADD COLUMN ip TEXT NOT NULL DEFAULT '';
CREATE INDEX audit_source ON audit(source, id);

-- +goose Down
DROP INDEX audit_source;
ALTER TABLE audit DROP COLUMN ip;
ALTER TABLE audit DROP COLUMN source;
DROP TABLE login_failure;
DROP TABLE admin_password;
