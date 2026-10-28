-- The country check gets an address family and a wider primary key. family is 4 or 6: the IPv4 measurement of a node
-- and its IPv6 one are separate rows, so a node's rows of one second no longer replace each other. country joins the key
-- for the same reason (several countries at once). Every existing row is IPv4. The table has no foreign keys, so the
-- rebuild needs no PRAGMA and no explicit transaction and runs the same on SQLite and D1.

-- +goose Up
CREATE TABLE country_check_new (
    node_id     TEXT    NOT NULL,
    country     TEXT    NOT NULL,
    family      INTEGER NOT NULL DEFAULT 4 CHECK (family IN (4, 6)),
    at          INTEGER NOT NULL,
    verdict     TEXT    NOT NULL,
    detail_json TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(detail_json)),
    PRIMARY KEY (node_id, country, family, at)
) STRICT;

INSERT INTO country_check_new (node_id, country, family, at, verdict, detail_json)
SELECT node_id, country, 4, at, verdict, detail_json FROM country_check;

DROP TABLE country_check;
ALTER TABLE country_check_new RENAME TO country_check;
-- The retention sweep and "the latest round".
CREATE INDEX country_check_at ON country_check (at);

-- +goose Down
CREATE TABLE country_check_old (
    node_id     TEXT    NOT NULL,
    at          INTEGER NOT NULL,
    country     TEXT    NOT NULL,
    verdict     TEXT    NOT NULL,
    detail_json TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(detail_json)),
    PRIMARY KEY (node_id, at)
) STRICT;

INSERT OR REPLACE INTO country_check_old (node_id, at, country, verdict, detail_json)
SELECT node_id, at, country, verdict, detail_json FROM country_check WHERE family = 4 ORDER BY at, country;

DROP TABLE country_check;
ALTER TABLE country_check_old RENAME TO country_check;
CREATE INDEX country_check_at ON country_check (at);
