-- The panel stores only hourly ASN counters and an optional short-lived per-user total. Addresses and operator names never enter
-- these tables. The node's latest loaded table digest is status metadata from Hello / StatsBatch, not connection data.

-- +goose Up
ALTER TABLE node ADD COLUMN networks_db_sha256 TEXT NOT NULL DEFAULT '';

CREATE TABLE network_hour (
    node_id    TEXT    NOT NULL,
    hour_start INTEGER NOT NULL,
    inbound_id TEXT    NOT NULL,
    asn        INTEGER NOT NULL,
    attempts   INTEGER NOT NULL,
    unfinished INTEGER NOT NULL,
    connects   INTEGER NOT NULL,
    broken     INTEGER NOT NULL,
    people     INTEGER NOT NULL,
    PRIMARY KEY (node_id, hour_start, inbound_id, asn)
) STRICT;
CREATE INDEX network_hour_hour_start ON network_hour (hour_start);

CREATE TABLE user_network (
    user_id   TEXT    NOT NULL,
    asn       INTEGER NOT NULL,
    connects  INTEGER NOT NULL,
    last_hour INTEGER NOT NULL,
    PRIMARY KEY (user_id, asn)
) STRICT;
CREATE INDEX user_network_last_hour ON user_network (last_hour);

-- +goose Down
DROP TABLE user_network;
DROP TABLE network_hour;
ALTER TABLE node DROP COLUMN networks_db_sha256;
