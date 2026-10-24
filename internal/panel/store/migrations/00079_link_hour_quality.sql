-- links/2 (agent.proto LinkCounters from client_closes on; design/connection-diagnostics-gaps.md B1, B2, C): how Hysteria2
-- connections ended and how they went, taken from the QUIC connection itself. Event counters: client_closes, freezes (an
-- idle timeout after the node kept sending: a frozen path), quiet (an idle timeout with nothing to send: the client went
-- away). Volume counters: accepted and hellos_foreign (inbound rows), rtt_ms_sum and rtt_samples (an average RTT), pkts_sent
-- and pkts_lost (a loss rate). Rows of older agents keep zeros, which the reads mark as unsupported for them.

-- +goose Up
ALTER TABLE link_hour ADD COLUMN client_closes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN freezes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN quiet INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN accepted INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN hellos_foreign INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN rtt_ms_sum INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN rtt_samples INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN pkts_sent INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN pkts_lost INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE link_hour DROP COLUMN pkts_lost;
ALTER TABLE link_hour DROP COLUMN pkts_sent;
ALTER TABLE link_hour DROP COLUMN rtt_samples;
ALTER TABLE link_hour DROP COLUMN rtt_ms_sum;
ALTER TABLE link_hour DROP COLUMN hellos_foreign;
ALTER TABLE link_hour DROP COLUMN accepted;
ALTER TABLE link_hour DROP COLUMN quiet;
ALTER TABLE link_hour DROP COLUMN freezes;
ALTER TABLE link_hour DROP COLUMN client_closes;
