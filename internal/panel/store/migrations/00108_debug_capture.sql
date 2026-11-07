-- On-demand debug captures of one node (agent.proto "DEBUG CAPTURE", capability "debug/1"; OpsService.StartDebugCapture). One row per
-- capture: who asked, for which node, level and sources, until when, and how it ended. state is running until the node ships its
-- records (done); a running capture whose time passed long ago reads as expired. A start the node refuses leaves no row. The rows
-- and their lines (00109) live 24 hours and at most a few per node: the retention sweep prunes them on the VPS edition only (the
-- Cloudflare edition cannot start one, so its cron makes no call for this). sources is comma separated, empty for every source.
-- records and bytes are what the lines hold now, the bound the panel enforces on intake. No foreign keys, as in node_journal.

-- +goose Up
CREATE TABLE debug_capture (
    id         TEXT    PRIMARY KEY,
    node_id    TEXT    NOT NULL,
    actor      TEXT    NOT NULL,
    level      TEXT    NOT NULL CHECK (level IN ('debug', 'info')),
    sources    TEXT    NOT NULL DEFAULT '',
    duration_s INTEGER NOT NULL,
    started_at INTEGER NOT NULL,
    ends_at    INTEGER NOT NULL,
    state      TEXT    NOT NULL CHECK (state IN ('running', 'done')),
    end_reason TEXT    NOT NULL DEFAULT '',
    records    INTEGER NOT NULL DEFAULT 0,
    bytes      INTEGER NOT NULL DEFAULT 0,
    dropped    INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX debug_capture_node ON debug_capture (node_id, started_at);
CREATE INDEX debug_capture_started ON debug_capture (started_at);

-- +goose Down
DROP INDEX debug_capture_started;
DROP INDEX debug_capture_node;
DROP TABLE debug_capture;
