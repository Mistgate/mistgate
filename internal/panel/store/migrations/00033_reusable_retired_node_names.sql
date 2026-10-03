-- Retired nodes and finished SSH jobs keep their history, but no longer reserve a live node name.
-- Rebuild these two tables because SQLite cannot drop an inline UNIQUE constraint.
-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;

CREATE TABLE node_new (
    id                  TEXT PRIMARY KEY,
    name                TEXT    NOT NULL COLLATE NOCASE,
    address             TEXT    NOT NULL,
    country_code        TEXT    NOT NULL DEFAULT '',
    location            TEXT    NOT NULL DEFAULT '',
    provider            TEXT    NOT NULL DEFAULT '',
    notes               TEXT    NOT NULL DEFAULT '',
    dns_resolvers       TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(dns_resolvers)),
    liveness_timeout_s  INTEGER NOT NULL DEFAULT 90  CHECK (liveness_timeout_s BETWEEN 15 AND 3600),
    apply_timeout_s     INTEGER NOT NULL DEFAULT 120 CHECK (apply_timeout_s BETWEEN 10 AND 3600),
    dial_timeout_s      INTEGER NOT NULL DEFAULT 15  CHECK (dial_timeout_s BETWEEN 5 AND 120),
    state               TEXT    NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'active', 'retired')),
    cert_serial         TEXT,
    agent_version       TEXT    NOT NULL DEFAULT '',
    api_version         INTEGER NOT NULL DEFAULT 0,
    boot_at             INTEGER NOT NULL DEFAULT 0,
    last_seen_at        INTEGER NOT NULL DEFAULT 0,
    last_connected_at   INTEGER NOT NULL DEFAULT 0,
    last_disconnected_at INTEGER NOT NULL DEFAULT 0,
    agent_instance_id   TEXT    NOT NULL DEFAULT '',
    last_seq            INTEGER NOT NULL DEFAULT 0,
    desired_revision    INTEGER NOT NULL DEFAULT 0,
    desired_hash        TEXT    NOT NULL DEFAULT '',
    applied_revision    INTEGER NOT NULL DEFAULT 0,
    applied_hash        TEXT    NOT NULL DEFAULT '',
    created_at          INTEGER NOT NULL,
    retired_at          INTEGER,
    agent_built         INTEGER NOT NULL DEFAULT 0,
    agent_caps          TEXT    NOT NULL DEFAULT '',
    last_update_json    TEXT    NOT NULL DEFAULT '' CHECK (last_update_json = '' OR json_valid(last_update_json)),
    awg_backend         TEXT    NOT NULL DEFAULT 'auto' CHECK (awg_backend IN ('auto', 'kernel', 'userspace')),
    awg_prepare_json    TEXT    NOT NULL DEFAULT '' CHECK (awg_prepare_json = '' OR json_valid(awg_prepare_json))
) STRICT;

INSERT INTO node_new (
    id, name, address, country_code, location, provider, notes, dns_resolvers,
    liveness_timeout_s, apply_timeout_s, dial_timeout_s, state, cert_serial,
    agent_version, api_version, boot_at, last_seen_at, last_connected_at, last_disconnected_at,
    agent_instance_id, last_seq, desired_revision, desired_hash, applied_revision, applied_hash,
    created_at, retired_at, agent_built, agent_caps, last_update_json, awg_backend, awg_prepare_json
)
SELECT
    id, name, address, country_code, location, provider, notes, dns_resolvers,
    liveness_timeout_s, apply_timeout_s, dial_timeout_s, state, cert_serial,
    agent_version, api_version, boot_at, last_seen_at, last_connected_at, last_disconnected_at,
    agent_instance_id, last_seq, desired_revision, desired_hash, applied_revision, applied_hash,
    created_at, retired_at, agent_built, agent_caps, last_update_json, awg_backend, awg_prepare_json
FROM node;

DROP TABLE node;
ALTER TABLE node_new RENAME TO node;
CREATE INDEX node_state ON node (state);
CREATE UNIQUE INDEX node_live_name ON node (name COLLATE NOCASE) WHERE state <> 'retired';

CREATE TABLE node_provision_job_new (
    id                  TEXT    PRIMARY KEY,
    node_id             TEXT    NOT NULL UNIQUE,
    name                TEXT    NOT NULL,
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

INSERT INTO node_provision_job_new (
    id, node_id, name, address, country_code, location, provider, ssh_host, ssh_port,
    host_fingerprint, secret, state, phase, error_code, created_by, created_at, updated_at
)
SELECT
    id, node_id, name, address, country_code, location, provider, ssh_host, ssh_port,
    host_fingerprint, secret, state, phase, error_code, created_by, created_at, updated_at
FROM node_provision_job;

DROP TABLE node_provision_job;
ALTER TABLE node_provision_job_new RENAME TO node_provision_job;
CREATE INDEX node_provision_job_queue ON node_provision_job (state, created_at);
CREATE UNIQUE INDEX node_provision_job_active_name ON node_provision_job (name COLLATE NOCASE)
    WHERE state IN ('queued', 'running', 'cancel_requested');

-- A live node and an in-flight SSH install share one name namespace. These triggers
-- keep the cross-table reservation atomic while allowing retired/finished history.
-- +goose StatementBegin
CREATE TRIGGER node_name_job_insert
BEFORE INSERT ON node
WHEN NEW.state <> 'retired'
BEGIN
    SELECT RAISE(ABORT, 'UNIQUE constraint failed: node.name')
    WHERE EXISTS (
        SELECT 1 FROM node_provision_job
        WHERE name = NEW.name COLLATE NOCASE
          AND node_id <> NEW.id
          AND state IN ('queued', 'running', 'cancel_requested')
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER node_name_job_update
BEFORE UPDATE OF name, state ON node
WHEN NEW.state <> 'retired'
BEGIN
    SELECT RAISE(ABORT, 'UNIQUE constraint failed: node.name')
    WHERE EXISTS (
        SELECT 1 FROM node_provision_job
        WHERE name = NEW.name COLLATE NOCASE
          AND node_id <> NEW.id
          AND state IN ('queued', 'running', 'cancel_requested')
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER job_name_node_insert
BEFORE INSERT ON node_provision_job
WHEN NEW.state IN ('queued', 'running', 'cancel_requested')
BEGIN
    SELECT RAISE(ABORT, 'UNIQUE constraint failed: node_provision_job.name')
    WHERE EXISTS (
        SELECT 1 FROM node
        WHERE name = NEW.name COLLATE NOCASE AND state <> 'retired' AND id <> NEW.node_id
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER job_name_node_update
BEFORE UPDATE OF name, state ON node_provision_job
WHEN NEW.state IN ('queued', 'running', 'cancel_requested')
BEGIN
    SELECT RAISE(ABORT, 'UNIQUE constraint failed: node_provision_job.name')
    WHERE EXISTS (
        SELECT 1 FROM node
        WHERE name = NEW.name COLLATE NOCASE AND state <> 'retired' AND id <> NEW.node_id
    );
END;
-- +goose StatementEnd

COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;
BEGIN IMMEDIATE;

DROP TRIGGER job_name_node_update;
DROP TRIGGER job_name_node_insert;
DROP TRIGGER node_name_job_update;
DROP TRIGGER node_name_job_insert;

CREATE TABLE node_old (
    id                  TEXT PRIMARY KEY,
    name                TEXT    NOT NULL COLLATE NOCASE UNIQUE,
    address             TEXT    NOT NULL,
    country_code        TEXT    NOT NULL DEFAULT '',
    location            TEXT    NOT NULL DEFAULT '',
    provider            TEXT    NOT NULL DEFAULT '',
    notes               TEXT    NOT NULL DEFAULT '',
    dns_resolvers       TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(dns_resolvers)),
    liveness_timeout_s  INTEGER NOT NULL DEFAULT 90  CHECK (liveness_timeout_s BETWEEN 15 AND 3600),
    apply_timeout_s     INTEGER NOT NULL DEFAULT 120 CHECK (apply_timeout_s BETWEEN 10 AND 3600),
    dial_timeout_s      INTEGER NOT NULL DEFAULT 15  CHECK (dial_timeout_s BETWEEN 5 AND 120),
    state               TEXT    NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'active', 'retired')),
    cert_serial         TEXT,
    agent_version       TEXT    NOT NULL DEFAULT '',
    api_version         INTEGER NOT NULL DEFAULT 0,
    boot_at             INTEGER NOT NULL DEFAULT 0,
    last_seen_at        INTEGER NOT NULL DEFAULT 0,
    last_connected_at   INTEGER NOT NULL DEFAULT 0,
    last_disconnected_at INTEGER NOT NULL DEFAULT 0,
    agent_instance_id   TEXT    NOT NULL DEFAULT '',
    last_seq            INTEGER NOT NULL DEFAULT 0,
    desired_revision    INTEGER NOT NULL DEFAULT 0,
    desired_hash        TEXT    NOT NULL DEFAULT '',
    applied_revision    INTEGER NOT NULL DEFAULT 0,
    applied_hash        TEXT    NOT NULL DEFAULT '',
    created_at          INTEGER NOT NULL,
    retired_at          INTEGER,
    agent_built         INTEGER NOT NULL DEFAULT 0,
    agent_caps          TEXT    NOT NULL DEFAULT '',
    last_update_json    TEXT    NOT NULL DEFAULT '' CHECK (last_update_json = '' OR json_valid(last_update_json)),
    awg_backend         TEXT    NOT NULL DEFAULT 'auto' CHECK (awg_backend IN ('auto', 'kernel', 'userspace')),
    awg_prepare_json    TEXT    NOT NULL DEFAULT '' CHECK (awg_prepare_json = '' OR json_valid(awg_prepare_json))
) STRICT;

INSERT INTO node_old SELECT * FROM node;
DROP TABLE node;
ALTER TABLE node_old RENAME TO node;
CREATE INDEX node_state ON node (state);

CREATE TABLE node_provision_job_old (
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

INSERT INTO node_provision_job_old SELECT * FROM node_provision_job;
DROP TABLE node_provision_job;
ALTER TABLE node_provision_job_old RENAME TO node_provision_job;
CREATE INDEX node_provision_job_queue ON node_provision_job (state, created_at);

COMMIT;
PRAGMA foreign_keys = ON;
