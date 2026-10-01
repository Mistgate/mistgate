-- API tokens (scripts and MCP) and the changes an agent asked for through MCP.
-- Additive: two new tables. Only the hash of a token secret is stored.

-- +goose Up

CREATE TABLE api_token (
    id            TEXT PRIMARY KEY,                       -- tok_...
    name          TEXT NOT NULL,
    name_key      TEXT NOT NULL,                          -- lower(name): uniqueness among unrevoked tokens
    profile       TEXT NOT NULL CHECK (profile IN ('readonly', 'operator', 'admin')),
    secret_hash   BLOB NOT NULL UNIQUE,                   -- SHA-256 of the secret
    hint          TEXT NOT NULL,                          -- last 4 characters of the secret
    rate_per_min  INTEGER NOT NULL CHECK (rate_per_min BETWEEN 1 AND 600),
    created_by    TEXT NOT NULL,                          -- admin id (no FK: the row outlives the admin)
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    revoked_at    INTEGER NOT NULL DEFAULT 0,
    revoked_by    TEXT NOT NULL DEFAULT '',
    last_used_at  INTEGER NOT NULL DEFAULT 0,
    last_used_ip  TEXT NOT NULL DEFAULT '',
    last_used_via TEXT NOT NULL DEFAULT '' CHECK (last_used_via IN ('', 'api', 'mcp'))
) STRICT;
CREATE UNIQUE INDEX api_token_live_name ON api_token(name_key) WHERE revoked_at = 0;

-- One row per plan; the safe ones (needs_approval = 0) are kept too, for the audit trail, until the sweep deletes them.
CREATE TABLE mcp_plan (
    id             TEXT PRIMARY KEY,                      -- pln_...
    token_id       TEXT NOT NULL REFERENCES api_token(id) ON DELETE CASCADE,
    tool           TEXT NOT NULL,                         -- "rollout_start" (no _plan suffix)
    params_json    TEXT NOT NULL,                         -- canonical JSON of the validated arguments, no secrets
    params_hash    BLOB NOT NULL,                         -- SHA-256 of params_json
    confirm_hash   BLOB NOT NULL UNIQUE,                  -- SHA-256 of the confirm token
    facts_json     TEXT NOT NULL,                         -- [{"key","value","untrusted"}]
    summary        TEXT NOT NULL,                         -- the English paragraph the agent got
    danger         TEXT NOT NULL DEFAULT '[]',            -- JSON array: step_up | fleet | bulk
    needs_approval INTEGER NOT NULL CHECK (needs_approval IN (0, 1)),
    reason         TEXT NOT NULL DEFAULT '',              -- the agent's own words, untrusted, <= 300 chars
    inner_ref      TEXT NOT NULL DEFAULT '',              -- node_fix: the ApplyFix plan_id (never shown to the agent)
    status         TEXT NOT NULL CHECK (status IN
                     ('planned', 'awaiting', 'approved', 'rejected', 'expired', 'applying', 'applied', 'failed', 'cancelled')),
    created_at     INTEGER NOT NULL,
    expires_at     INTEGER NOT NULL,                      -- created_at + 600
    decided_by     TEXT NOT NULL DEFAULT '',              -- admin id of the approver or rejecter
    decided_at     INTEGER NOT NULL DEFAULT 0,
    applied_at     INTEGER NOT NULL DEFAULT 0,            -- while 'applying': when the apply began; after: when it finished
    result         TEXT NOT NULL DEFAULT '',
    error          TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX mcp_plan_status ON mcp_plan(status, expires_at);
CREATE INDEX mcp_plan_token  ON mcp_plan(token_id, created_at);

-- +goose Down

DROP INDEX mcp_plan_token;
DROP INDEX mcp_plan_status;
DROP TABLE mcp_plan;
DROP INDEX api_token_live_name;
DROP TABLE api_token;
