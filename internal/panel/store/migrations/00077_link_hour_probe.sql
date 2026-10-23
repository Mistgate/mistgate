-- The panel's own synthetic checks dial every inbound with the health probe credential (health_probe_cred), so the handshake totals
-- of the inbound row (hellos, handshakes) hold them too. The probe's key rows are not stored; their connects and rejects, one per
-- authenticated handshake, go to the inbound row here and are taken off its hellos and handshakes when they are read. Rows written
-- before this column keep the checks in their totals until the 48-hour retention drops them.

-- +goose Up
ALTER TABLE link_hour ADD COLUMN probe_handshakes INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE link_hour DROP COLUMN probe_handshakes;
