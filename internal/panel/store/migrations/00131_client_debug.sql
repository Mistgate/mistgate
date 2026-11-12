-- Logs people send for support (client_debug.go). One row per request of the owner and per bundle a person sent on their own: the row
-- is the bundle's header, its lines are debug_capture_line rows (00109) under the same id, which starts with "dbr_" and so never
-- meets a node capture's. A request asks one device with a key (state open, until expires_at); the bundle that answers it, or a
-- bundle sent unasked (unasked = 1, actor '', no request), is received. A person who refuses leaves declined, the owner who
-- withdraws cancelled; an open row past expires_at reads as expired and is replaced by the next request. At most one open request
-- per device. closed_at is when the row stopped being open (receipt time for a bundle). No foreign keys, as in debug_capture: the
-- retention sweep removes rows whose device or person is gone, and rows (with their lines) older than 7 days.
-- records, bytes, first_ms and last_ms describe the lines (the time range of the bundle); dropped_kv is how many attributes the
-- panel did not keep; app is the first token of the sender's User-Agent, cleaned.

-- +goose Up
CREATE TABLE client_debug (
    id           TEXT    PRIMARY KEY,
    user_id      TEXT    NOT NULL,
    device_id    TEXT    NOT NULL,
    actor        TEXT    NOT NULL DEFAULT '',
    unasked      INTEGER NOT NULL DEFAULT 0 CHECK (unasked IN (0, 1)),
    note         TEXT    NOT NULL DEFAULT '',
    level        TEXT    NOT NULL DEFAULT 'info' CHECK (level IN ('info', 'debug')),
    requested_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    state        TEXT    NOT NULL CHECK (state IN ('open', 'received', 'declined', 'cancelled', 'expired')),
    closed_at    INTEGER NOT NULL DEFAULT 0,
    records      INTEGER NOT NULL DEFAULT 0,
    bytes        INTEGER NOT NULL DEFAULT 0,
    dropped_kv   INTEGER NOT NULL DEFAULT 0,
    first_ms     INTEGER NOT NULL DEFAULT 0,
    last_ms      INTEGER NOT NULL DEFAULT 0,
    app          TEXT    NOT NULL DEFAULT '',
    read_at      INTEGER NOT NULL DEFAULT 0,
    read_by      TEXT    NOT NULL DEFAULT ''
) STRICT;
CREATE UNIQUE INDEX client_debug_open ON client_debug (device_id) WHERE state = 'open';
CREATE INDEX client_debug_device ON client_debug (device_id, state, closed_at);
CREATE INDEX client_debug_user ON client_debug (user_id, requested_at);
CREATE INDEX client_debug_requested ON client_debug (requested_at);

-- +goose Down
DROP INDEX client_debug_requested;
DROP INDEX client_debug_user;
DROP INDEX client_debug_device;
DROP INDEX client_debug_open;
DROP TABLE client_debug;
