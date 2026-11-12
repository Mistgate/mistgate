-- OS updates of nodes started from the panel: one rollout (at most one active, like update_rollout) and one step per node.
-- A step follows one job on the node by its job id; everything the state machine needs to continue after a panel restart
-- is in these rows. Times are UTC Unix seconds, 0 = "not yet". No foreign key from the step to the node: a retired node
-- keeps its history.

-- +goose Up
CREATE TABLE os_rollout (
    id                TEXT PRIMARY KEY,                                   -- osr_...
    status            TEXT    NOT NULL CHECK (status IN ('running', 'paused', 'done', 'cancelled', 'failed')),
    mode              TEXT    NOT NULL CHECK (mode IN ('security', 'all', 'reboot_only')),
    reboot            TEXT    NOT NULL CHECK (reboot IN ('never', 'if_required', 'always')),
    batch_size        INTEGER NOT NULL DEFAULT 1 CHECK (batch_size BETWEEN 1 AND 5),
    window_start      INTEGER NOT NULL DEFAULT -1,                        -- minute of the day, -1 = no window
    window_end        INTEGER NOT NULL DEFAULT -1,
    tz_offset         INTEGER NOT NULL DEFAULT 0,                         -- the update time zone when it was created
    max_online        INTEGER NOT NULL DEFAULT 0,                         -- 0 = ignore
    pause_key         TEXT    NOT NULL DEFAULT '',
    pause_params_json TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(pause_params_json)),
    created_by        TEXT    NOT NULL DEFAULT '',
    created_at        INTEGER NOT NULL,
    finished_at       INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE UNIQUE INDEX os_rollout_active ON os_rollout ((1)) WHERE status IN ('running', 'paused');
CREATE INDEX os_rollout_created ON os_rollout (created_at);

CREATE TABLE os_step (
    rollout_id      TEXT    NOT NULL REFERENCES os_rollout (id) ON DELETE CASCADE,
    node_id         TEXT    NOT NULL,
    node_name       TEXT    NOT NULL,
    stage           INTEGER NOT NULL,
    state           TEXT    NOT NULL CHECK (state IN ('pending', 'sent', 'rebooting', 'gating', 'passed', 'failed', 'skipped')),
    job_id          TEXT    NOT NULL DEFAULT '',                          -- osj_...; a new attempt is a new id
    sends           INTEGER NOT NULL DEFAULT 0,                           -- times UpdateOs was sent under job_id (a lost answer is sent again)
    acked_at        INTEGER NOT NULL DEFAULT 0,                           -- the node answered started / running with this job id
    phase           TEXT    NOT NULL DEFAULT '',
    phase_at        INTEGER NOT NULL DEFAULT 0,
    boot_before     INTEGER NOT NULL DEFAULT 0,                           -- the node's boot time when the job was sent
    kernel_from     TEXT    NOT NULL DEFAULT '',
    kernel_to       TEXT    NOT NULL DEFAULT '',
    pre_failed_json TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(pre_failed_json)),  -- inbounds FAILED before the job
    sent_at         INTEGER NOT NULL DEFAULT 0,
    reboot_at       INTEGER NOT NULL DEFAULT 0,
    back_at         INTEGER NOT NULL DEFAULT 0,                           -- the check window opened (back after the reboot, or the job ended)
    finished_at     INTEGER NOT NULL DEFAULT 0,
    error_key       TEXT    NOT NULL DEFAULT '',
    params_json     TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(params_json)),
    PRIMARY KEY (rollout_id, node_id)
) STRICT;

-- +goose Down
DROP TABLE os_step;
DROP INDEX os_rollout_created;
DROP INDEX os_rollout_active;
DROP TABLE os_rollout;
