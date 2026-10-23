-- Connection counters (agent.proto LinkCounters, capability "links/1"; design/connection-diagnostics.md section 4): what a node saw of
-- connection attempts, summed per hour. One row per node, UTC hour, inbound and credential; cred_id '' is the inbound itself (what
-- cannot be put on a key: unknown keys, the handshake totals). user_id is the owner of the credential, resolved from
-- device_credential when the row is written ('' on the inbound row), so a person's page reads by one index. A batch of the same hour
-- adds to the row. Counts only: no address, no time of a single connection. Rows live 48 hours (the health retention sweep). No
-- foreign keys, as in client_probe_report.

-- +goose Up
CREATE TABLE link_hour (
    node_id      TEXT    NOT NULL,
    hour_start   INTEGER NOT NULL,
    inbound_id   TEXT    NOT NULL,
    cred_id      TEXT    NOT NULL,
    user_id      TEXT    NOT NULL DEFAULT '',
    connects     INTEGER NOT NULL DEFAULT 0,
    rejects      INTEGER NOT NULL DEFAULT 0,
    drops        INTEGER NOT NULL DEFAULT 0,
    stalls       INTEGER NOT NULL DEFAULT 0,
    online_s     INTEGER NOT NULL DEFAULT 0,
    unknown_keys INTEGER NOT NULL DEFAULT 0,
    hellos       INTEGER NOT NULL DEFAULT 0,
    handshakes   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, hour_start, inbound_id, cred_id)
) STRICT;
CREATE INDEX link_hour_user ON link_hour (user_id, hour_start) WHERE user_id <> '';
CREATE INDEX link_hour_hour ON link_hour (hour_start);

-- +goose Down
DROP TABLE link_hour;
