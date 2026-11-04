-- A path that delivers some ports whole and drops the others completely is a filter (a hoster's DDoS protection that
-- lets only UDP 443 through, say), not a lossy line: open_ports keeps the ports the sender did get through.

-- +goose Up
ALTER TABLE udp_path_loss ADD COLUMN open_ports TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE udp_path_loss DROP COLUMN open_ports;
