-- link_hour.probe_pkts_sent and probe_pkts_lost, on the row of an inbound itself (cred_id ''): the packets the node sent to the
-- panel's own health check (its system credential) and declared lost, the way back of the check. The check's key has no row of
-- its own (00077: its handshakes go to probe_handshakes). 0 for other rows and before this migration.

-- +goose Up
ALTER TABLE link_hour ADD COLUMN probe_pkts_sent INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN probe_pkts_lost INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE link_hour DROP COLUMN probe_pkts_lost;
ALTER TABLE link_hour DROP COLUMN probe_pkts_sent;
