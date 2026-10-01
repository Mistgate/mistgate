-- Health: alerts, the latest doctor report of each node, synthetic check results and the hidden
-- per-inbound credential the panel's own probe client uses.
--
-- Additive only: new tables and one new index on the existing event table (for its retention sweep); no
-- existing row is rewritten, so the migration applies to a live database. ids keep the 00002 conventions
-- (TEXT, vault AAD = the row's own id); times are UTC Unix seconds, 0 = "not yet" in NOT NULL columns.

-- +goose Up

-- The system credential of the synthetic checker. One per inbound, created the first time the desired state of
-- the inbound is built and removed with the inbound. It is NOT a device_credential: no user, no device, so it
-- cannot show up in any user/device list, subscription or quota, and the stats ingest skips its traffic (it
-- resolves credentials through device_credential only).
CREATE TABLE health_probe_cred (
    inbound_id          TEXT PRIMARY KEY REFERENCES inbound (id) ON DELETE CASCADE,
    cred_id             TEXT    NOT NULL UNIQUE,                -- crd_... = Credential.cred_id on the wire
    secret_enc          BLOB    NOT NULL,                       -- vault, AAD = cred_id; what the probe client presents
    data_json           TEXT    NOT NULL CHECK (json_valid(data_json)),  -- verifier shipped to the node (hy2: {"auth_sha256":...})
    created_at          INTEGER NOT NULL
) STRICT;

-- Alerts. No foreign key on node_id: a retired node's history stays readable. node_id '' = fleet-wide or
-- user alert. At most one ACTIVE alert per (kind, node, subject); a resolved row that fires again within an hour
-- is re-opened instead of duplicated (id and first_seen stay).
CREATE TABLE health_alert (
    id                  TEXT PRIMARY KEY,                       -- alt_...
    kind                TEXT    NOT NULL,                       -- node_down | host_blip | no_traffic | check_failed | doctor_warn | ...
    severity            INTEGER NOT NULL CHECK (severity BETWEEN 1 AND 3),  -- 1 info, 2 warning, 3 critical
    node_id             TEXT    NOT NULL DEFAULT '',
    subject             TEXT    NOT NULL DEFAULT '',
    params_json         TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(params_json)),
    title_key           TEXT    NOT NULL,
    why_key             TEXT    NOT NULL DEFAULT '',
    first_seen          INTEGER NOT NULL,
    last_seen           INTEGER NOT NULL,
    resolved_at         INTEGER NOT NULL DEFAULT 0,             -- 0 = active
    resolution          TEXT    NOT NULL DEFAULT '',            -- cleared | fix_applied | node_retired | node_returned | superseded
    muted_until         INTEGER NOT NULL DEFAULT 0,
    created_at          INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX health_alert_active ON health_alert (kind, node_id, subject) WHERE resolved_at = 0;
-- History list and the retention sweep.
CREATE INDEX health_alert_resolved ON health_alert (resolved_at) WHERE resolved_at > 0;

-- The latest doctor state of each node: a full report replaces the node's rows, a partial one upserts.
CREATE TABLE doctor_result (
    node_id             TEXT    NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    check_id            TEXT    NOT NULL,
    status              INTEGER NOT NULL CHECK (status BETWEEN 1 AND 4),  -- agent.proto DoctorStatus
    title_key           TEXT    NOT NULL DEFAULT '',
    detail              TEXT    NOT NULL DEFAULT '',
    params_json         TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(params_json)),
    fix_id              TEXT    NOT NULL DEFAULT '',
    measured_unix       INTEGER NOT NULL DEFAULT 0,
    received_unix       INTEGER NOT NULL,
    PRIMARY KEY (node_id, check_id)
) STRICT, WITHOUT ROWID;

-- Synthetic check rounds, kept 25 h (status: 1 ok, 2 degraded, 3 failed; skips are not stored).
CREATE TABLE health_check_sample (
    inbound_id          TEXT    NOT NULL REFERENCES inbound (id) ON DELETE CASCADE,
    at                  INTEGER NOT NULL,
    status              INTEGER NOT NULL CHECK (status BETWEEN 1 AND 3),
    latency_ms          INTEGER NOT NULL DEFAULT 0,
    exit_ip             TEXT    NOT NULL DEFAULT '',
    exit_country        TEXT    NOT NULL DEFAULT '',
    error_code          TEXT    NOT NULL DEFAULT '',
    error_detail        TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (inbound_id, at)
) STRICT, WITHOUT ROWID;
CREATE INDEX health_check_sample_at ON health_check_sample (at);

-- One row per inbound and finished UTC day, written from the raw rounds before they are pruned; kept 90 days.
CREATE TABLE health_check_daily (
    inbound_id          TEXT    NOT NULL REFERENCES inbound (id) ON DELETE CASCADE,
    day                 INTEGER NOT NULL CHECK (day % 86400 = 0),
    ok                  INTEGER NOT NULL DEFAULT 0,
    failed              INTEGER NOT NULL DEFAULT 0,
    degraded            INTEGER NOT NULL DEFAULT 0,
    p50_ms              INTEGER NOT NULL DEFAULT 0,
    p95_ms              INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (inbound_id, day)
) STRICT, WITHOUT ROWID;

-- Event retention (00002: 90 days for info, 400 for warning and error) walks events by severity and time.
CREATE INDEX event_retention ON event (severity, ts);

-- +goose Down
DROP INDEX event_retention;
DROP TABLE health_check_daily;
DROP TABLE health_check_sample;
DROP TABLE doctor_result;
DROP TABLE health_alert;
DROP TABLE health_probe_cred;
