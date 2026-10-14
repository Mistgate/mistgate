-- The country check (health module): what probes inside a country saw when they pinged a node and opened TCP connections
-- to it. One row per node and round. verdict is ok | tcp_blocked | blocked | unknown. detail_json holds, per probe, the
-- network name, ASN and city, ICMP and TCP results with their round trips and whether the probe counted; it never holds an
-- address of a probe. Rows live 7 days (the health retention sweep). No foreign key on node_id: a retired node's history is
-- still readable until it is swept.

-- +goose Up
CREATE TABLE country_check (
    node_id     TEXT    NOT NULL,
    at          INTEGER NOT NULL,
    country     TEXT    NOT NULL,
    verdict     TEXT    NOT NULL,
    detail_json TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(detail_json)),
    PRIMARY KEY (node_id, at)
) STRICT;
-- The retention sweep and "the latest round".
CREATE INDEX country_check_at ON country_check (at);

-- +goose Down
DROP TABLE country_check;
