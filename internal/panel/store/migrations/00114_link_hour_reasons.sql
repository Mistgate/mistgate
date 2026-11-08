-- links/3 (agent.proto LinkCounters from hs_unfinished on; design/adr/0017-agent-operable-fleet.md): why handshakes fail,
-- where the engine can see it. Volume counters of the inbound rows: hs_unfinished (the node answered a ClientHello and the
-- handshake never finished), tls_alerts_sent, tls_alerts_recv and tls_cert_refused (Hysteria2, VLESS tls), reality_fallback
-- and auth_failed (VLESS REALITY). Volume counters of the key rows: initiations, responses and responses_unmatched
-- (AmneziaWG, userspace backend). Event counters: tcp_stalls and tcp_stalls_16k (VLESS). Rows of older agents keep zeros,
-- which the reads mark as unsupported for them.

-- +goose Up
ALTER TABLE link_hour ADD COLUMN hs_unfinished INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tls_alerts_sent INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tls_alerts_recv INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tls_cert_refused INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN reality_fallback INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN auth_failed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tcp_stalls INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN tcp_stalls_16k INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN initiations INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN responses INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN responses_unmatched INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE link_hour DROP COLUMN responses_unmatched;
ALTER TABLE link_hour DROP COLUMN responses;
ALTER TABLE link_hour DROP COLUMN initiations;
ALTER TABLE link_hour DROP COLUMN tcp_stalls_16k;
ALTER TABLE link_hour DROP COLUMN tcp_stalls;
ALTER TABLE link_hour DROP COLUMN auth_failed;
ALTER TABLE link_hour DROP COLUMN reality_fallback;
ALTER TABLE link_hour DROP COLUMN tls_cert_refused;
ALTER TABLE link_hour DROP COLUMN tls_alerts_recv;
ALTER TABLE link_hour DROP COLUMN tls_alerts_sent;
ALTER TABLE link_hour DROP COLUMN hs_unfinished;
