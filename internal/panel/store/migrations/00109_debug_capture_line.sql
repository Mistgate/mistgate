-- The records of a debug capture (00108), cleaned on the node and again by the panel on intake. seq counts from 1 in the order the
-- node sent them; level is 0 debug, 1 info, 2 warn, 3 error; attrs_json holds only the allow-listed attribute keys. Pruned with the
-- capture (24 hours), by the same VPS-only sweep.

-- +goose Up
CREATE TABLE debug_capture_line (
    capture_id TEXT    NOT NULL,
    seq        INTEGER NOT NULL,
    at_ms      INTEGER NOT NULL,
    level      INTEGER NOT NULL CHECK (level BETWEEN 0 AND 3),
    source     TEXT    NOT NULL,
    message    TEXT    NOT NULL,
    attrs_json TEXT    NOT NULL DEFAULT '{}',
    PRIMARY KEY (capture_id, seq)
) STRICT;

-- +goose Down
DROP TABLE debug_capture_line;
