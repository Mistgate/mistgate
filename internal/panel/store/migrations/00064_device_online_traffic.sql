-- device.used_bytes: everything the device carried, all time (IngestStats adds the per-credential deltas of each batch to the
-- device the credential belongs to). traffic_bucket stays per person.
--
-- node_live.devices: the devices with a session on the node right now, device id -> the start of their newest session, written
-- by the same statement as node_live.users. The node says which credential a session uses, and a credential belongs to
-- one device, so this is exact for every protocol.

-- +goose Up
ALTER TABLE device ADD COLUMN used_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_live ADD COLUMN devices TEXT NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE node_live DROP COLUMN devices;
ALTER TABLE device DROP COLUMN used_bytes;
