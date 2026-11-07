-- TCP SYN TTL walks from one fleet node to another (the MCP tool node_trace): the hops as JSON (ttl, rtt, whether a router
-- answered; never a router address). The last 20 traces of each pair are kept, so the next one can be compared with the previous.

-- +goose Up
CREATE TABLE node_trace (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    from_node_id TEXT    NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    to_node_id   TEXT    NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    at_unix      INTEGER NOT NULL,
    result_json  TEXT    NOT NULL
);
CREATE INDEX node_trace_pair ON node_trace (from_node_id, to_node_id, at_unix);

-- +goose Down
DROP INDEX node_trace_pair;
DROP TABLE node_trace;
