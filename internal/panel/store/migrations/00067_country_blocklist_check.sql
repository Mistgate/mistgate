-- The country check, second source: public block lists, checked on the panel (internal/panel/blocklist). One row per node and
-- check: whether the node's IPv4 address is in the RKN registry (antifilter's ipsum and community lists) and the matching
-- subnet, and which CDN or hosting provider the address belongs to (123jjck/cdn-ip-ranges, context only). lists_at is the date
-- of the newest list used. A node's address is never sent anywhere; the lists themselves are not stored. Rows live 7 days (the
-- health retention sweep). No foreign key on node_id, as in country_check.

-- +goose Up
CREATE TABLE country_blocklist_check (
    node_id      TEXT    NOT NULL,
    at           INTEGER NOT NULL,
    in_rkn       INTEGER NOT NULL DEFAULT 0 CHECK (in_rkn IN (0, 1)),
    rkn_subnet   TEXT    NOT NULL DEFAULT '',
    listed_by    TEXT    NOT NULL DEFAULT '',
    cdn_provider TEXT    NOT NULL DEFAULT '',
    cdn_region   TEXT    NOT NULL DEFAULT '',
    lists_at     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, at)
) STRICT;
CREATE INDEX country_blocklist_check_at ON country_blocklist_check (at);

-- +goose Down
DROP TABLE country_blocklist_check;
