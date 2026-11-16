-- A home or mobile network that the owner chose to observe the fleet from. Addresses are deliberately absent: only the
-- ASN and the operator label found from the request's source address live past that request.

-- +goose Up
CREATE TABLE own_probe (
    id                TEXT PRIMARY KEY CHECK (id GLOB 'prb_*'),
    name              TEXT NOT NULL,
    city              TEXT NOT NULL,
    kind              TEXT NOT NULL CHECK (kind IN ('home', 'mobile', 'dc')),
    token_hash        BLOB,
    token_expires     INTEGER NOT NULL DEFAULT 0,
    secret_hash       BLOB,
    state             TEXT NOT NULL CHECK (state IN ('pending', 'active', 'network_changed', 'revoked')),
    asn               INTEGER NOT NULL DEFAULT 0,
    operator          TEXT NOT NULL DEFAULT '',
    pinned_operator   TEXT NOT NULL DEFAULT '',
    version           TEXT NOT NULL DEFAULT '',
    last_seen         INTEGER NOT NULL DEFAULT 0,
    last_result       INTEGER NOT NULL DEFAULT 0,
    checks_day        INTEGER NOT NULL DEFAULT 0 CHECK (checks_day >= 0),
    checks_day_at     INTEGER NOT NULL DEFAULT 0,
    job_id            TEXT NOT NULL DEFAULT '',
    job_json          TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(job_json)),
    job_due           INTEGER NOT NULL DEFAULT 0,
    created_at        INTEGER NOT NULL,
    created_by        TEXT NOT NULL,
    CHECK (token_hash IS NULL OR (typeof(token_hash) = 'blob' AND length(token_hash) = 32)),
    CHECK (secret_hash IS NULL OR (typeof(secret_hash) = 'blob' AND length(secret_hash) = 32))
) STRICT;
CREATE UNIQUE INDEX own_probe_token_hash ON own_probe (token_hash) WHERE token_hash IS NOT NULL;
CREATE UNIQUE INDEX own_probe_secret_hash ON own_probe (secret_hash) WHERE secret_hash IS NOT NULL;
CREATE INDEX own_probe_state_seen ON own_probe (state, last_seen);

-- +goose Down
DROP TABLE own_probe;
