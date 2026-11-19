-- The last scan of the neighbours of a node (the ProbeService's NodeSniScan, the MCP tool node_neighbor_sites): the sites of its /24
-- that can serve as a REALITY target, as JSON. One row per node, written only by a scan that finished; it shows the list again
-- without a new scan and holds the pause of 30 minutes between two scans.

-- +goose Up
CREATE TABLE node_sni_scan (
    node_id     TEXT    PRIMARY KEY REFERENCES node (id) ON DELETE CASCADE,
    at_unix     INTEGER NOT NULL,
    result_json TEXT    NOT NULL
);

-- +goose Down
DROP TABLE node_sni_scan;
