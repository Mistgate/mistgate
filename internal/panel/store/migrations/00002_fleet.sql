-- Fleet migration: nodes, agents, profiles, users, devices, credentials, traffic accounting, events.
--
--
-- Conventions:
--   * ids: random 128-bit base32 lowercase with a kind prefix (nod_, prf_, inb_, grp_, usr_, dev_, crd_,
--     enr_, cas_); generated in Go, TEXT PRIMARY KEY, no AUTOINCREMENT.
--     Exception: event.id is an INTEGER AUTOINCREMENT, because it is an append-only log whose id is the
--     pagination cursor and ordering key.
--   * times: UTC Unix seconds, INTEGER. 0 means "never / not yet" for *_at columns that are NOT NULL;
--     NULL where absence is a real state (expires_at: NULL = never expires).
--   * secrets: stored as vault ciphertext BLOB (XChaCha20-Poly1305), AAD = the row id (for
--     secrets_enc / key_enc / secret_enc / sub_token_enc the row id is the id of THAT row).
--   * STRICT tables (SQLite >= 3.37; modernc.org/sqlite ships newer), booleans are INTEGER 0/1 with CHECK.
--   * Depends on 00001 only through admin ids stored as plain TEXT (created_by), without a foreign key, so
--     this file does not care how 00001 names its admin table.
--   * PRAGMA foreign_keys=ON is assumed (one writer + read pool).
--
-- Hot paths and the indexes that serve them are noted next to each table. Budget: the performance target
-- (5000 users, 50 nodes; subscription p95 < 50 ms; new user applied on all nodes < 5 s).

-- +goose Up

-- ---------------------------------------------------------------------------------------------------
-- PKI for the panel <-> agent link

-- The panel CA. One active row; old rows stay so previously issued certificates keep verifying until
-- they expire. The agent listener's own server certificate is issued from this CA at startup and kept
-- in memory (no table).
CREATE TABLE panel_ca (
    id                  TEXT PRIMARY KEY,                       -- cas_...
    cert_pem            TEXT    NOT NULL,
    fingerprint_sha256  TEXT    NOT NULL,                       -- hex of the DER certificate; what install commands carry
    key_enc             BLOB    NOT NULL,                       -- vault, AAD = id (PKCS#8 ECDSA P-256)
    not_before          INTEGER NOT NULL,
    not_after           INTEGER NOT NULL,
    active              INTEGER NOT NULL DEFAULT 1 CHECK (active IN (0, 1)),
    created_at          INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX panel_ca_one_active ON panel_ca (active) WHERE active = 1;

-- ---------------------------------------------------------------------------------------------------
-- Nodes

CREATE TABLE node (
    id                  TEXT PRIMARY KEY,                       -- nod_...
    name                TEXT    NOT NULL COLLATE NOCASE UNIQUE, -- "de1"
    address             TEXT    NOT NULL,                       -- public host/IP used in subscriptions
    country_code        TEXT    NOT NULL DEFAULT '',
    location            TEXT    NOT NULL DEFAULT '',
    provider            TEXT    NOT NULL DEFAULT '',
    notes               TEXT    NOT NULL DEFAULT '',
    dns_resolvers       TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(dns_resolvers)),  -- JSON array; [] = agent default
    -- per-node timeouts (agent.proto "LIVENESS AND TIMEOUTS")
    liveness_timeout_s  INTEGER NOT NULL DEFAULT 90  CHECK (liveness_timeout_s  BETWEEN 15 AND 3600),
    apply_timeout_s     INTEGER NOT NULL DEFAULT 120 CHECK (apply_timeout_s     BETWEEN 10 AND 3600),
    dial_timeout_s      INTEGER NOT NULL DEFAULT 15  CHECK (dial_timeout_s      BETWEEN 5  AND 120),
    -- lifecycle. "Status" shown in the UI (online/down/blip/...) is DERIVED from state + the fields
    -- below + the in-memory stream registry; it is not stored.
    state               TEXT    NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'active', 'retired')),
    cert_serial         TEXT,                                   -- current certificate (node_cert.serial)
    -- what the agent told us
    agent_version       TEXT    NOT NULL DEFAULT '',
    api_version         INTEGER NOT NULL DEFAULT 0,
    boot_at             INTEGER NOT NULL DEFAULT 0,             -- Hello.facts.boot_unix; changed = real reboot, same = blip
    last_seen_at        INTEGER NOT NULL DEFAULT 0,             -- last message of any kind (liveness)
    last_connected_at   INTEGER NOT NULL DEFAULT 0,
    last_disconnected_at INTEGER NOT NULL DEFAULT 0,
    -- reliable-message dedup (agent.proto "RELIABLE MESSAGES"): updated in the SAME transaction as the
    -- counters the message contributes to
    agent_instance_id   TEXT    NOT NULL DEFAULT '',
    last_seq            INTEGER NOT NULL DEFAULT 0,
    -- desired-state bookkeeping (survives panel restarts so revisions stay monotonic)
    desired_revision    INTEGER NOT NULL DEFAULT 0,
    desired_hash        TEXT    NOT NULL DEFAULT '',
    applied_revision    INTEGER NOT NULL DEFAULT 0,             -- last ApplyResult.revision
    applied_hash        TEXT    NOT NULL DEFAULT '',            -- last ApplyResult.state_hash (agent-observed)
    created_at          INTEGER NOT NULL,
    retired_at          INTEGER
) STRICT;
-- Access computation walks nodes by state; the list is tiny, so no more indexes.
CREATE INDEX node_state ON node (state);

-- Host facts reported in Hello.
CREATE TABLE node_facts (
    node_id             TEXT PRIMARY KEY REFERENCES node (id) ON DELETE CASCADE,
    hostname            TEXT    NOT NULL DEFAULT '',
    os                  TEXT    NOT NULL DEFAULT '',
    kernel              TEXT    NOT NULL DEFAULT '',
    arch                TEXT    NOT NULL DEFAULT '',
    cpu_count           INTEGER NOT NULL DEFAULT 0,
    ram_total_bytes     INTEGER NOT NULL DEFAULT 0,
    disk_total_bytes    INTEGER NOT NULL DEFAULT 0,
    virt                TEXT    NOT NULL DEFAULT '',
    has_ipv6            INTEGER NOT NULL DEFAULT 0 CHECK (has_ipv6 IN (0, 1)),
    engines_json        TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(engines_json)),  -- [{protocol, version}]
    extra_json          TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(extra_json)),
    updated_at          INTEGER NOT NULL
) STRICT;

-- One-time enrollment tokens. Only the SHA-256 of the token is stored.
CREATE TABLE enrollment_token (
    id                  TEXT PRIMARY KEY,                       -- enr_...
    node_id             TEXT    NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    token_hash          BLOB    NOT NULL UNIQUE,                -- sha256(token); lookup key on Enroll
    created_by          TEXT    NOT NULL,                       -- admin id, no FK (see header)
    created_at          INTEGER NOT NULL,
    expires_at          INTEGER NOT NULL,
    used_at             INTEGER,                                -- NULL = unused
    csr_key_hash        BLOB,                                   -- sha256 of the CSR public key of the first use (idempotent retry window)
    issued_serial       TEXT                                    -- node_cert.serial handed out
) STRICT;
CREATE INDEX enrollment_token_node ON enrollment_token (node_id);

-- Every certificate the CA issued to a node. Revocation is checked at TLS handshake by serial.
CREATE TABLE node_cert (
    serial              TEXT PRIMARY KEY,                       -- hex
    node_id             TEXT    NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    ca_id               TEXT    NOT NULL REFERENCES panel_ca (id),
    pem                 TEXT    NOT NULL,                       -- public material only
    not_before          INTEGER NOT NULL,
    not_after           INTEGER NOT NULL,
    issued_at           INTEGER NOT NULL,
    revoked_at          INTEGER,
    revoke_reason       TEXT    NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX node_cert_node ON node_cert (node_id, not_after);
-- Handshake check "is this serial revoked" is a primary-key lookup; no extra index.

-- ---------------------------------------------------------------------------------------------------
-- Profiles, inbounds, groups

CREATE TABLE profile (
    id                  TEXT PRIMARY KEY,                       -- prf_...
    protocol            TEXT    NOT NULL,                       -- plugin id: "hysteria2"
    name                TEXT    NOT NULL COLLATE NOCASE UNIQUE,
    settings_json       TEXT    NOT NULL CHECK (json_valid(settings_json)),  -- validated by the plugin; NO secrets inside
    secrets_enc         BLOB,                                   -- vault, AAD = id; JSON object {json-pointer: value} of x-secret fields
    version             INTEGER NOT NULL DEFAULT 1,             -- bumped on every change; optimistic concurrency
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
) STRICT;

-- profile x node. Effective port/SNI = overrides, else profile settings, else node address.
CREATE TABLE inbound (
    id                  TEXT PRIMARY KEY,                       -- inb_...
    profile_id          TEXT    NOT NULL REFERENCES profile (id) ON DELETE RESTRICT,
    node_id             TEXT    NOT NULL REFERENCES node (id)    ON DELETE RESTRICT,
    port_override       INTEGER CHECK (port_override BETWEEN 1 AND 65535),
    tls_server_name_override TEXT NOT NULL DEFAULT '',
    enabled             INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    spec_version        INTEGER NOT NULL DEFAULT 1,             -- goes into InboundSpec.spec_version; bumped with profile.version and overrides
    state               TEXT    NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'active', 'failed', 'disabled')),
    last_error          TEXT    NOT NULL DEFAULT '',
    applied_spec_hash   TEXT    NOT NULL DEFAULT '',            -- InboundResult.spec_hash
    cert_pin_sha256     TEXT    NOT NULL DEFAULT '',            -- InboundResult, for pinSHA256 in subscriptions
    cert_not_after      INTEGER NOT NULL DEFAULT 0,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    UNIQUE (profile_id, node_id)
) STRICT;
-- Desired-state build for one node, and "inbounds of a node" on the node page.
CREATE INDEX inbound_node ON inbound (node_id);

CREATE TABLE user_group (
    id                  TEXT PRIMARY KEY,                       -- grp_...
    name                TEXT    NOT NULL COLLATE NOCASE UNIQUE,
    created_at          INTEGER NOT NULL
) STRICT;

CREATE TABLE user_group_profile (
    group_id            TEXT NOT NULL REFERENCES user_group (id) ON DELETE CASCADE,
    profile_id          TEXT NOT NULL REFERENCES profile (id)    ON DELETE CASCADE,
    PRIMARY KEY (group_id, profile_id)
) STRICT, WITHOUT ROWID;
-- "Which groups contain this profile" (profile user_count, impact of a profile change).
CREATE INDEX user_group_profile_profile ON user_group_profile (profile_id);

-- ---------------------------------------------------------------------------------------------------
-- Users, devices, credentials

CREATE TABLE user (
    id                  TEXT PRIMARY KEY,                       -- usr_...
    name                TEXT    NOT NULL COLLATE NOCASE UNIQUE,
    group_id            TEXT    NOT NULL REFERENCES user_group (id) ON DELETE RESTRICT,
    disabled            INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1)),   -- admin switch
    -- Derived, maintained by the access module (on usage ingest, on edits, and by a minute timer for
    -- expiry). Precedence: disabled > expired > limited > active. Stored so that filters are index scans.
    status              TEXT    NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'expired', 'limited')),
    app_happ            INTEGER NOT NULL DEFAULT 1 CHECK (app_happ    IN (0, 1)),
    app_amnezia         INTEGER NOT NULL DEFAULT 1 CHECK (app_amnezia IN (0, 1)),
    all_nodes           INTEGER NOT NULL DEFAULT 1 CHECK (all_nodes IN (0, 1)),  -- 1 = automatic incl. future nodes; 0 = see user_node
    quota_bytes         INTEGER NOT NULL DEFAULT 0 CHECK (quota_bytes >= 0),     -- 0 = unlimited
    quota_reset         TEXT    NOT NULL DEFAULT 'month' CHECK (quota_reset IN ('none', 'day', 'week', 'month', 'rolling_month')),
    period_start        INTEGER NOT NULL,                       -- start of the current quota period
    used_bytes          INTEGER NOT NULL DEFAULT 0,             -- up+down in the current period; incremented in the stats transaction
    expires_at          INTEGER,                                -- NULL = never
    device_limit        INTEGER NOT NULL DEFAULT 5 CHECK (device_limit BETWEEN 1 AND 100),
    speed_limit_bps     INTEGER NOT NULL DEFAULT 0 CHECK (speed_limit_bps >= 0), -- 0 = none (experimental)
    -- Subscription token: lookup by hash, re-display by decrypting. Rotation replaces both.
    sub_token_hash      BLOB    NOT NULL UNIQUE,                -- sha256(token)
    sub_token_enc       BLOB    NOT NULL,                       -- vault, AAD = id
    last_seen_at        INTEGER NOT NULL DEFAULT 0,
    last_node_id        TEXT    REFERENCES node (id) ON DELETE SET NULL,
    created_at          INTEGER NOT NULL,
    CHECK (app_happ = 1 OR app_amnezia = 1)                     -- "at least one app"
) STRICT;
-- Users list: keyset pagination by name (unique, NOCASE, so the UNIQUE index on name is the keyset index;
-- id is only a tiebreak in the API contract) with an optional group filter.
CREATE INDEX user_by_group ON user (group_id, name COLLATE NOCASE);
-- "Expiring <= 7 d" and the expiry timer.
CREATE INDEX user_expires ON user (expires_at) WHERE expires_at IS NOT NULL;
-- "Over quota" and the quota check; only quota'd users are interesting.
CREATE INDEX user_quota ON user (used_bytes) WHERE quota_bytes > 0;
CREATE INDEX user_status ON user (status);

-- Explicit node selection; rows matter only while user.all_nodes = 0.
CREATE TABLE user_node (
    user_id             TEXT NOT NULL REFERENCES user (id) ON DELETE CASCADE,
    node_id             TEXT NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, node_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX user_node_node ON user_node (node_id);

CREATE TABLE device (
    id                  TEXT PRIMARY KEY,                       -- dev_...
    user_id             TEXT    NOT NULL REFERENCES user (id) ON DELETE CASCADE,
    -- sha256 of the client HWID header (Happ etc.). NULL = the implicit per-user device used
    -- for subscription fetches without a HWID (one shared token, counted as 1 device).
    hwid_hash           BLOB,
    platform            TEXT    NOT NULL DEFAULT '',
    model               TEXT    NOT NULL DEFAULT '',
    os_version          TEXT    NOT NULL DEFAULT '',
    first_seen_at       INTEGER NOT NULL,
    last_seen_at        INTEGER NOT NULL,
    created_at          INTEGER NOT NULL,
    revoked_at          INTEGER                                 -- soft delete: stats of a revoked device must still resolve
) STRICT;
CREATE UNIQUE INDEX device_hwid ON device (user_id, hwid_hash) WHERE hwid_hash IS NOT NULL AND revoked_at IS NULL;
CREATE UNIQUE INDEX device_implicit ON device (user_id) WHERE hwid_hash IS NULL AND revoked_at IS NULL;
CREATE INDEX device_user ON device (user_id);

-- One per device x protocol (the same Hysteria2 token works on every Hysteria2 inbound of every node).
CREATE TABLE device_credential (
    id                  TEXT PRIMARY KEY,                       -- crd_...  (= Credential.cred_id on the wire)
    device_id           TEXT    NOT NULL REFERENCES device (id) ON DELETE CASCADE,
    user_id             TEXT    NOT NULL REFERENCES user (id)   ON DELETE CASCADE,  -- denormalized for the access query
    protocol            TEXT    NOT NULL,
    secret_enc          BLOB    NOT NULL,                       -- vault, AAD = id; what the client presents (hy2 token)
    data_json           TEXT    NOT NULL CHECK (json_valid(data_json)),  -- verifier sent to nodes (hy2: {"auth_sha256":...})
    created_at          INTEGER NOT NULL,
    revoked_at          INTEGER                                 -- soft delete
) STRICT;
CREATE UNIQUE INDEX device_credential_live ON device_credential (device_id, protocol) WHERE revoked_at IS NULL;
-- Desired-state build: all live credentials of a protocol, joined to users by id.
CREATE INDEX device_credential_user ON device_credential (user_id) WHERE revoked_at IS NULL;
CREATE INDEX device_credential_protocol ON device_credential (protocol) WHERE revoked_at IS NULL;

-- ---------------------------------------------------------------------------------------------------
-- Traffic accounting

-- user x node x protocol x hour. Upsert: ON CONFLICT DO UPDATE SET bytes_up = bytes_up + excluded.bytes_up ...
-- Rows exist only for active pairs. Retention: 400 days (pruned daily). Size estimate: 5000 users x ~3
-- active nodes x 24 h x 400 d at 50 B is a worst-case few GB; realistic (users online a few hours) is 10x less.
CREATE TABLE traffic_bucket (
    user_id             TEXT    NOT NULL REFERENCES user (id) ON DELETE CASCADE,
    node_id             TEXT    NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    protocol            TEXT    NOT NULL,
    hour_start          INTEGER NOT NULL CHECK (hour_start % 3600 = 0),
    bytes_up            INTEGER NOT NULL DEFAULT 0,             -- client -> internet
    bytes_down          INTEGER NOT NULL DEFAULT 0,             -- internet -> client
    PRIMARY KEY (user_id, node_id, protocol, hour_start)
) STRICT, WITHOUT ROWID;
-- User detail (daily chart, per-node split) uses the primary key prefix (user_id, ..).
-- Node page "top today" and node bytes today: scan one node over a few hours.
CREATE INDEX traffic_bucket_node_hour ON traffic_bucket (node_id, hour_start);

-- Fleet rollup, written in the same transaction as traffic_bucket. Serves the Overview traffic chart,
-- the per-node 24 h bars, the node's traffic today, AND the online chart (peak_users), so no scan of
-- traffic_bucket is ever needed for the Overview (168 h x protocols x nodes rows at most).
CREATE TABLE node_traffic_hour (
    node_id             TEXT    NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    protocol            TEXT    NOT NULL,
    hour_start          INTEGER NOT NULL CHECK (hour_start % 3600 = 0),
    bytes_up            INTEGER NOT NULL DEFAULT 0,
    bytes_down          INTEGER NOT NULL DEFAULT 0,
    peak_users          INTEGER NOT NULL DEFAULT 0,             -- max distinct users online in any snapshot of the hour
    peak_devices        INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, protocol, hour_start)
) STRICT, WITHOUT ROWID;
CREATE INDEX node_traffic_hour_hour ON node_traffic_hour (hour_start);

-- ---------------------------------------------------------------------------------------------------
-- Events (panel-generated and agent-reported). Append-only; retention 90 days for severity info, 400 for
-- warning/error. No foreign keys on purpose: events outlive the things they talk about.

CREATE TABLE event (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    ts                  INTEGER NOT NULL,
    severity            INTEGER NOT NULL CHECK (severity BETWEEN 1 AND 3),  -- 1 info, 2 warning, 3 error
    code                TEXT    NOT NULL,
    source              TEXT    NOT NULL CHECK (source IN ('agent', 'panel', 'admin')),
    node_id             TEXT,
    user_id             TEXT,
    device_id           TEXT,
    inbound_id          TEXT,
    params_json         TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(params_json)),
    -- agent events: belt-and-braces dedup on top of node.last_seq
    src_instance        TEXT,
    src_seq             INTEGER
) STRICT;
CREATE INDEX event_node ON event (node_id, id DESC) WHERE node_id IS NOT NULL;
CREATE INDEX event_user ON event (user_id, id DESC) WHERE user_id IS NOT NULL;
CREATE INDEX event_severity ON event (severity, id DESC) WHERE severity >= 2;
CREATE UNIQUE INDEX event_agent_dedup ON event (node_id, src_instance, src_seq) WHERE src_seq IS NOT NULL;

-- +goose Down

DROP TABLE event;
DROP TABLE node_traffic_hour;
DROP TABLE traffic_bucket;
DROP TABLE device_credential;
DROP TABLE device;
DROP TABLE user_node;
DROP TABLE user;
DROP TABLE user_group_profile;
DROP TABLE user_group;
DROP TABLE inbound;
DROP TABLE profile;
DROP TABLE node_cert;
DROP TABLE enrollment_token;
DROP TABLE node_facts;
DROP TABLE node;
DROP TABLE panel_ca;
