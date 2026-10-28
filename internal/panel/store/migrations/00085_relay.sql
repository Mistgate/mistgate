-- Relays: a port of an entry node that passes the encrypted traffic, unchanged, to a server (an inbound) on another node
-- (the exit). The entry keeps no keys; accounting and limits stay on the exit. A relay is created switched off and is
-- switched on only by the gate of the panel; state, last_error, to_ip and applied_at are what the entry node's agent
-- reported for the forward (ok -> active, else failed), check_json is the latest check of the path (see the relay module).
-- network is the network of the exit's server ("tcp" for VLESS, "udp" for Hysteria2 and AmneziaWG; it is fixed by the
-- protocol): with it the database itself refuses two relays on the same port of one entry. A relay goes with its exit
-- server (the foreign key); the panel deletes the relays of a retired node itself.

-- +goose Up
CREATE TABLE relay (
    id          TEXT    PRIMARY KEY,
    inbound_id  TEXT    NOT NULL REFERENCES inbound (id) ON DELETE CASCADE,
    via_node_id TEXT    NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    network     TEXT    NOT NULL CHECK (network IN ('tcp', 'udp')),
    port        INTEGER NOT NULL CHECK (port = 443 OR port BETWEEN 1024 AND 65535),
    enabled     INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    state       TEXT    NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'active', 'failed')),
    last_error  TEXT    NOT NULL DEFAULT '',
    to_ip       TEXT    NOT NULL DEFAULT '',
    applied_at  INTEGER NOT NULL DEFAULT 0,
    check_json  TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(check_json)),
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE (inbound_id, via_node_id),
    UNIQUE (via_node_id, network, port)
) STRICT;
CREATE INDEX relay_via ON relay (via_node_id);

-- +goose Down
DROP TABLE relay;
