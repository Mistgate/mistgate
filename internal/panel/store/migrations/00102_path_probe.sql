-- On-demand probes between two fleet nodes (the MCP tool path_probe): udp, tcp or tls from one node to another's ports, with the
-- result as JSON (per port: sent, got, verdict, milliseconds; the MTU sweep). A caller sees node names only, never an
-- address. Kept 7 days; the panel VPS prunes it (the Cloudflare edition has no such probe).

-- +goose Up
CREATE TABLE path_probe (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    from_node_id TEXT    NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    to_node_id   TEXT    NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    kind         TEXT    NOT NULL CHECK (kind IN ('udp', 'tcp', 'tls')),
    at_unix      INTEGER NOT NULL,
    result_json  TEXT    NOT NULL
);
CREATE INDEX path_probe_at ON path_probe (at_unix);
CREATE INDEX path_probe_pair ON path_probe (from_node_id, to_node_id, at_unix);

-- +goose Down
DROP INDEX path_probe_pair;
DROP INDEX path_probe_at;
DROP TABLE path_probe;
