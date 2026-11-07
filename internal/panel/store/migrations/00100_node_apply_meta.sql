-- When the node last reported what it runs, and since when the panel has seen it differ from what was sent. node.applied_* hold
-- the revision and hash; this row holds the when and the how, for the "desired versus applied" read (GetNodeRuntime).

-- +goose Up
CREATE TABLE node_apply_meta (
    node_id     TEXT    PRIMARY KEY REFERENCES node (id) ON DELETE CASCADE,
    applied_at  INTEGER NOT NULL DEFAULT 0,   -- the last ApplyResult (or in-sync Hello) of the node
    status      TEXT    NOT NULL DEFAULT '',  -- applied | partial | in_sync (a Hello that matched, nothing was sent)
    duration_ms INTEGER NOT NULL DEFAULT 0,
    drift_since INTEGER NOT NULL DEFAULT 0    -- 0 = no drift; else when the node first applied a state other than the sent one
) STRICT;

-- +goose Down
DROP TABLE node_apply_meta;
