-- trusttunnel-refusals/1 (agent.proto LinkCounters tunnel_*): the tunnel requests a TrustTunnel engine refused after the credential
-- had authenticated, by reason: the egress forbids the destination, no IPv4 address, the connection failed (refused, no route, DNS,
-- other), the connect timed out, the request could not be served (deadlines, authority, ICMP, egress), the stream was not accepted
-- (connection closing, credential gone, connection limit). Event counters of the inbound row, never of a key. Rows of older
-- agents keep zeros, which the reads mark as unsupported for TrustTunnel.

-- +goose Up
ALTER TABLE link_hour ADD COLUMN tunnel_blocked INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tunnel_no_ipv4 INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tunnel_conn_refused INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tunnel_conn_unreachable INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tunnel_conn_dns INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tunnel_conn_other INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tunnel_timeout INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tunnel_other_deadlines INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tunnel_other_authority INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tunnel_other_icmp INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tunnel_other_dial INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tunnel_stream_closed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tunnel_stream_cred_gone INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tunnel_stream_conn_limit INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE link_hour DROP COLUMN tunnel_stream_conn_limit;
ALTER TABLE link_hour DROP COLUMN tunnel_stream_cred_gone;
ALTER TABLE link_hour DROP COLUMN tunnel_stream_closed;
ALTER TABLE link_hour DROP COLUMN tunnel_other_dial;
ALTER TABLE link_hour DROP COLUMN tunnel_other_icmp;
ALTER TABLE link_hour DROP COLUMN tunnel_other_authority;
ALTER TABLE link_hour DROP COLUMN tunnel_other_deadlines;
ALTER TABLE link_hour DROP COLUMN tunnel_timeout;
ALTER TABLE link_hour DROP COLUMN tunnel_conn_other;
ALTER TABLE link_hour DROP COLUMN tunnel_conn_dns;
ALTER TABLE link_hour DROP COLUMN tunnel_conn_unreachable;
ALTER TABLE link_hour DROP COLUMN tunnel_conn_refused;
ALTER TABLE link_hour DROP COLUMN tunnel_no_ipv4;
ALTER TABLE link_hour DROP COLUMN tunnel_blocked;
