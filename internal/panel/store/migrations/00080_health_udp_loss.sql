-- health_check_sample.udp_sent and udp_lost: the packets the panel's own QUIC client (the Hysteria2 check) sent and
-- declared lost during the round, for the UDP-loss notice and for MCP. 0 for the other protocols and for rows written
-- before this migration.

-- +goose Up
ALTER TABLE health_check_sample ADD COLUMN udp_sent INTEGER NOT NULL DEFAULT 0;
ALTER TABLE health_check_sample ADD COLUMN udp_lost INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE health_check_sample DROP COLUMN udp_lost;
ALTER TABLE health_check_sample DROP COLUMN udp_sent;
