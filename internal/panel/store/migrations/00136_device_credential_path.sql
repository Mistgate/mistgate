-- A credential path separates a relay peer from the device's direct peer: '' is the direct key, 'relay:<entry node id>'
-- the key a device uses through that entry node. Nothing issues a path key yet; every existing row is direct.

-- +goose Up
ALTER TABLE device_credential ADD COLUMN path TEXT NOT NULL DEFAULT '' CHECK (path = '' OR path GLOB 'relay:*');
DROP INDEX device_credential_live;
CREATE UNIQUE INDEX device_credential_live ON device_credential (device_id, protocol, COALESCE(profile_id, ''), path)
    WHERE revoked_at IS NULL;
-- The sweep of orphan path keys and a rotation look for the live path keys only: no row while none is issued.
CREATE INDEX device_credential_path ON device_credential (device_id) WHERE path <> '' AND revoked_at IS NULL;

-- +goose Down
-- The old schema holds one live key per device and profile: the path keys and their tunnel addresses go, the direct keys stay.
DELETE FROM awg_peer WHERE credential_id IN (SELECT id FROM device_credential WHERE path <> '');
DELETE FROM device_credential WHERE path <> '';
DROP INDEX device_credential_path;
DROP INDEX device_credential_live;
CREATE UNIQUE INDEX device_credential_live ON device_credential (device_id, protocol, COALESCE(profile_id, ''))
    WHERE revoked_at IS NULL;
ALTER TABLE device_credential DROP COLUMN path;
