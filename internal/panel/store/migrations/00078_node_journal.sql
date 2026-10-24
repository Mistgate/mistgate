-- The node journal (agent.proto JournalEntry, capability "journal/1"; design/connection-diagnostics.md section 5): the warnings
-- and errors of a node's agent and engines, one row per node, UTC hour and kind of record. key is the panel's hash of level,
-- source, message, inbound_id and the "code" attribute; a batch of the same hour adds its count and widens first_at..last_at,
-- so a storm of one error is one row an hour. level is agent.proto Severity (2 warning, 3 error). message and attrs_json are
-- cleaned on the node and again by the panel: no client address. Rows live 7 days (the health retention sweep). No foreign
-- keys, as in client_probe_report.

-- +goose Up
CREATE TABLE node_journal (
    node_id    TEXT    NOT NULL,
    hour_start INTEGER NOT NULL,
    key        TEXT    NOT NULL,
    level      INTEGER NOT NULL CHECK (level IN (2, 3)),
    source     TEXT    NOT NULL,
    message    TEXT    NOT NULL,
    inbound_id TEXT    NOT NULL DEFAULT '',
    attrs_json TEXT    NOT NULL DEFAULT '{}',
    count      INTEGER NOT NULL,
    first_at   INTEGER NOT NULL,
    last_at    INTEGER NOT NULL,
    PRIMARY KEY (node_id, hour_start, key)
) STRICT;
CREATE INDEX node_journal_hour ON node_journal (hour_start);

-- +goose Down
DROP TABLE node_journal;
