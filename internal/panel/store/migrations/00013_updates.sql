-- Node-agent updates: what the agent said about its build in Hello, and the staged rollouts of the node agent.
--
-- Additive only: three columns on node (defaults keep every existing row valid) and two new tables, so the
-- migration applies to a live database (00012 is the newest before it; the AWG migration takes 00014).
-- ids keep the 00002 conventions; times are UTC Unix seconds, 0 = "not yet" in NOT NULL columns.

-- +goose Up

-- What the agent said in Hello. Offline nodes keep their last known update state.
ALTER TABLE node ADD COLUMN agent_built      INTEGER NOT NULL DEFAULT 0;   -- Hello.built (0: an agent that predates the field)
ALTER TABLE node ADD COLUMN agent_caps       TEXT    NOT NULL DEFAULT '';  -- Hello.capabilities, space separated
ALTER TABLE node ADD COLUMN last_update_json TEXT    NOT NULL DEFAULT '' CHECK (last_update_json = '' OR json_valid(last_update_json));

-- One rollout of a signed bundle. The manifest and its signature are copied in verbatim: a bundle that changes on
-- disk later does not change a rollout that is running.
CREATE TABLE update_rollout (
    id                TEXT PRIMARY KEY,                                   -- rol_...
    status            TEXT    NOT NULL CHECK (status IN ('running', 'paused', 'done', 'cancelled', 'failed')),
    to_version        TEXT    NOT NULL,
    to_built          INTEGER NOT NULL,
    manifest          BLOB    NOT NULL,                                   -- the signed bytes
    signature         BLOB    NOT NULL,
    batch_size        INTEGER NOT NULL CHECK (batch_size BETWEEN 1 AND 10),
    pause_key         TEXT    NOT NULL DEFAULT '',
    pause_params_json TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(pause_params_json)),
    created_by        TEXT    NOT NULL DEFAULT '',                        -- admin id
    created_at        INTEGER NOT NULL,
    finished_at       INTEGER NOT NULL DEFAULT 0
) STRICT;
-- At most one active rollout, enforced by the database.
CREATE UNIQUE INDEX update_rollout_active ON update_rollout ((1)) WHERE status IN ('running', 'paused');
CREATE INDEX update_rollout_created ON update_rollout (created_at);

-- One node's turn in a rollout.
CREATE TABLE update_step (
    rollout_id        TEXT    NOT NULL REFERENCES update_rollout (id) ON DELETE CASCADE,
    node_id           TEXT    NOT NULL,                                   -- no FK: a retired node keeps its history
    node_name         TEXT    NOT NULL,
    stage             INTEGER NOT NULL,                                   -- 0 = canary
    state             TEXT    NOT NULL CHECK (state IN ('pending', 'sent', 'gating', 'passed', 'failed', 'rolled_back', 'skipped')),
    from_version      TEXT    NOT NULL DEFAULT '',
    from_built        INTEGER NOT NULL DEFAULT 0,
    pre_failed_json   TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(pre_failed_json)),  -- inbounds FAILED before the update
    sent_at           INTEGER NOT NULL DEFAULT 0,
    acked_at          INTEGER NOT NULL DEFAULT 0,                         -- the agent answered UpdateAgent with ok
    reconnected_at    INTEGER NOT NULL DEFAULT 0,
    finished_at       INTEGER NOT NULL DEFAULT 0,
    error_key         TEXT    NOT NULL DEFAULT '',
    params_json       TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(params_json)),
    PRIMARY KEY (rollout_id, node_id)
) STRICT;

-- +goose Down
DROP TABLE update_step;
DROP INDEX update_rollout_created;
DROP INDEX update_rollout_active;
DROP TABLE update_rollout;
ALTER TABLE node DROP COLUMN last_update_json;
ALTER TABLE node DROP COLUMN agent_caps;
ALTER TABLE node DROP COLUMN agent_built;
