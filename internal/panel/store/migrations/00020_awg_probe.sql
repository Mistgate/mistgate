-- The synthetic checker's client for AmneziaWG: the system credential of an AWG inbound is a
-- peer with a tunnel address, and the address comes from the same IPAM as the devices'.
-- awg_idx is the peer index (address = subnet base + idx) the credential holds: 0 = none (hysteria2, which has no
-- address). The allocator of awg_peer counts these rows too, so a device never gets the address of a probe. There is no
-- quarantine: the row goes with its inbound (ON DELETE CASCADE) and nothing outside the panel ever held the address.
-- Additive: one column with a default, the existing rows (hysteria2) stay valid.

-- +goose Up
ALTER TABLE health_probe_cred ADD COLUMN awg_idx INTEGER NOT NULL DEFAULT 0 CHECK (awg_idx = 0 OR awg_idx >= 2);

-- +goose Down
-- An AWG credential without its index would hold an address the allocator no longer sees: drop it, the checker makes a
-- new one after the next Up.
DELETE FROM health_probe_cred WHERE awg_idx > 0;
ALTER TABLE health_probe_cred DROP COLUMN awg_idx;
