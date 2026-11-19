-- The TCP ports a node shares between services by the SNI of the ClientHello (agent.proto "SHARED TCP PORT"): the snapshot of the
-- counters the agent last reported, as JSON (an adminv1 SharedPorts), and when it was written. One row per node that shares a port; the
-- row goes when the node stops sharing. The panel writes it when the snapshot changes and at most every five minutes.

-- +goose Up
CREATE TABLE node_shared_ports (
    node_id    TEXT PRIMARY KEY REFERENCES node(id) ON DELETE CASCADE,
    at_unix    INTEGER NOT NULL,
    ports_json TEXT    NOT NULL
);

-- +goose Down
DROP TABLE node_shared_ports;
