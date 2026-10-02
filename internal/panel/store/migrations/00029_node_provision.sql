-- Durable SSH node-install jobs. SSH credentials are sealed by the panel vault before
-- they enter this table; the event stream stores stable codes, never command output.

-- +goose Up
CREATE TABLE node_provision_job (
    id                  TEXT    PRIMARY KEY,
    node_id             TEXT    NOT NULL UNIQUE,
    name                TEXT    NOT NULL UNIQUE,
    address             TEXT    NOT NULL,
    country_code        TEXT    NOT NULL DEFAULT '',
    location            TEXT    NOT NULL DEFAULT '',
    provider            TEXT    NOT NULL DEFAULT '',
    ssh_host            TEXT    NOT NULL,
    ssh_port            INTEGER NOT NULL CHECK (ssh_port BETWEEN 1 AND 65535),
    host_fingerprint    TEXT    NOT NULL,
    secret              BLOB    NOT NULL,
    state               TEXT    NOT NULL CHECK (state IN ('queued', 'running', 'completed', 'failed')),
    phase               TEXT    NOT NULL,
    error_code          TEXT    NOT NULL DEFAULT '',
    created_by          TEXT    NOT NULL,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
) STRICT;

CREATE INDEX node_provision_job_queue ON node_provision_job (state, created_at);

CREATE TABLE node_provision_event (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id      TEXT    NOT NULL REFERENCES node_provision_job (id) ON DELETE CASCADE,
    phase       TEXT    NOT NULL,
    code        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX node_provision_event_job ON node_provision_event (job_id, id);

-- +goose Down
DROP TABLE node_provision_event;
DROP TABLE node_provision_job;
