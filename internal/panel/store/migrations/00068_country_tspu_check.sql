-- The country check, third source: the node agent's own TSPU probe (internal/tspuprobe, agent command CountryProbe). A node
-- inside the country (the vantage) tests whether TCP SYNs to node_id survive the national DPI. One row per probed node, vantage
-- and run. verdict is the node as a whole: 'blocked' (every probed port was), 'open' (none was), 'partial' (some were) or
-- 'unknown' (no port got a verdict). dpi_hop is the hop in front of the DPI box on the path from the vantage, control_ok says
-- the control target got past it (a run where it did not is no verdict about any node and stores no rows). detail_json is one
-- entry per port: [{"port":443,"verdict":"blocked"}]. No address of a probe or of a hop is stored. Rows live 7 days (the health
-- retention sweep). No foreign key on node_id or vantage, as in country_check.

-- +goose Up
CREATE TABLE country_tspu_check (
    node_id     TEXT    NOT NULL,
    vantage     TEXT    NOT NULL,
    at          INTEGER NOT NULL,
    verdict     TEXT    NOT NULL,
    dpi_hop     INTEGER NOT NULL DEFAULT 0,
    control_ok  INTEGER NOT NULL DEFAULT 0 CHECK (control_ok IN (0, 1)),
    detail_json TEXT    NOT NULL DEFAULT '[]',
    PRIMARY KEY (node_id, vantage, at)
) STRICT;
CREATE INDEX country_tspu_check_at ON country_tspu_check (at);

-- +goose Down
DROP TABLE country_tspu_check;
