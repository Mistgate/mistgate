-- One-time, short-lived token offers for recovering an existing admin login.

-- +goose Up
CREATE TABLE recovery_token (
    hash       BLOB PRIMARY KEY,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER
) STRICT, WITHOUT ROWID;

-- +goose Down
DROP TABLE recovery_token;
