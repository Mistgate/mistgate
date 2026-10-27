-- Results from external country-reachability sources. Each row is one node and source at one time; detail_json contains
-- source-neutral vantages and never stores a probe or node address. Rows live 7 days (the health retention sweep).

-- +goose Up
CREATE TABLE country_ext_check (
    node_id     TEXT    NOT NULL,
    source      TEXT    NOT NULL,
    at          INTEGER NOT NULL,
    verdict     TEXT    NOT NULL,
    detail_json TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(detail_json)),
    PRIMARY KEY (node_id, source, at)
) STRICT;
CREATE INDEX country_ext_check_at ON country_ext_check (at);

-- +goose Down
DROP TABLE country_ext_check;
