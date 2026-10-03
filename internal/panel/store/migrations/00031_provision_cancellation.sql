-- Add explicit states for requested and completed cancellation of durable SSH installs.

-- +goose Up
DROP INDEX node_provision_event_job;
DROP INDEX node_provision_job_queue;
ALTER TABLE node_provision_event RENAME TO node_provision_event_old;
ALTER TABLE node_provision_job RENAME TO node_provision_job_old;

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
    state               TEXT    NOT NULL CHECK (state IN ('queued', 'running', 'cancel_requested', 'cancelled', 'completed', 'failed')),
    phase               TEXT    NOT NULL,
    error_code          TEXT    NOT NULL DEFAULT '',
    created_by          TEXT    NOT NULL,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
) STRICT;

INSERT INTO node_provision_job
SELECT id, node_id, name, address, country_code, location, provider, ssh_host, ssh_port,
       host_fingerprint, secret, state, phase, error_code, created_by, created_at, updated_at
FROM node_provision_job_old;

CREATE INDEX node_provision_job_queue ON node_provision_job (state, created_at);

CREATE TABLE node_provision_event (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id      TEXT    NOT NULL REFERENCES node_provision_job (id) ON DELETE CASCADE,
    phase       TEXT    NOT NULL,
    code        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL
) STRICT;

INSERT INTO node_provision_event SELECT id, job_id, phase, code, created_at FROM node_provision_event_old;
CREATE INDEX node_provision_event_job ON node_provision_event (job_id, id);

DROP TABLE node_provision_event_old;
DROP TABLE node_provision_job_old;

-- +goose Down
DROP INDEX node_provision_event_job;
DROP INDEX node_provision_job_queue;
ALTER TABLE node_provision_event RENAME TO node_provision_event_new;
ALTER TABLE node_provision_job RENAME TO node_provision_job_new;

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

INSERT INTO node_provision_job
SELECT id, node_id, name, address, country_code, location, provider, ssh_host, ssh_port,
       host_fingerprint, secret,
       CASE WHEN state IN ('cancel_requested', 'cancelled') THEN 'failed' ELSE state END,
       CASE WHEN state IN ('cancel_requested', 'cancelled') THEN 'failed' ELSE phase END,
       CASE WHEN state IN ('cancel_requested', 'cancelled') THEN 'cancelled' ELSE error_code END,
       created_by, created_at, updated_at
FROM node_provision_job_new;

CREATE INDEX node_provision_job_queue ON node_provision_job (state, created_at);

CREATE TABLE node_provision_event (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id      TEXT    NOT NULL REFERENCES node_provision_job (id) ON DELETE CASCADE,
    phase       TEXT    NOT NULL,
    code        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL
) STRICT;

INSERT INTO node_provision_event SELECT id, job_id, phase, code, created_at FROM node_provision_event_new;
CREATE INDEX node_provision_event_job ON node_provision_event (job_id, id);

DROP TABLE node_provision_event_new;
DROP TABLE node_provision_job_new;
