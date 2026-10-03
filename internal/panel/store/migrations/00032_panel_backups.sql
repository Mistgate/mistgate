-- Owner-configured encrypted backups to the owner's R2 bucket.

-- +goose Up
CREATE TABLE panel_backup_settings (
    id                  INTEGER PRIMARY KEY CHECK (id = 1),
    account_id          TEXT    NOT NULL DEFAULT '',
    jurisdiction        TEXT    NOT NULL DEFAULT 'default',
    bucket              TEXT    NOT NULL DEFAULT '',
    access_key_id       TEXT    NOT NULL DEFAULT '',
    secret_access_key   BLOB    NOT NULL DEFAULT X'',
    age_recipient       TEXT    NOT NULL DEFAULT '',
    enabled             INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    interval_hours      INTEGER NOT NULL DEFAULT 24 CHECK (interval_hours BETWEEN 1 AND 168),
    retention_days      INTEGER NOT NULL DEFAULT 0 CHECK (retention_days = 0 OR retention_days BETWEEN 7 AND 3650),
    last_success        INTEGER NOT NULL DEFAULT 0,
    last_error_code     TEXT    NOT NULL DEFAULT '',
    updated_at          INTEGER NOT NULL DEFAULT 0
) STRICT;

-- +goose Down
DROP TABLE panel_backup_settings;
