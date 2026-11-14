-- A credential path 'ipv6' is the key a device uses for the "· IPv6" server of a node: one key on two endpoints of the same server
-- makes the server's peer endpoint jump between them. The CHECK of the path column has to allow it, and SQLite cannot change a
-- CHECK in place; the column is swapped in place of a table rebuild so that every row, the awg_peer rows that reference the
-- credentials and their cascades stay untouched. Nothing else changes: '' is the direct key, 'relay:<entry node id>' a relay key.

-- +goose Up
DROP INDEX device_credential_path;
DROP INDEX device_credential_live;
ALTER TABLE device_credential RENAME COLUMN path TO old_path;
ALTER TABLE device_credential ADD COLUMN path TEXT NOT NULL DEFAULT '' CHECK (path = '' OR path = 'ipv6' OR path GLOB 'relay:*');
UPDATE device_credential SET path = old_path;
ALTER TABLE device_credential DROP COLUMN old_path;
CREATE UNIQUE INDEX device_credential_live ON device_credential (device_id, protocol, COALESCE(profile_id, ''), path)
    WHERE revoked_at IS NULL;
CREATE INDEX device_credential_path ON device_credential (device_id) WHERE path <> '' AND revoked_at IS NULL;

-- +goose Down
-- A schema without the IPv6 path cannot keep those credentials; relay paths and direct keys still fit its CHECK.
DELETE FROM awg_peer WHERE credential_id IN (SELECT id FROM device_credential WHERE path = 'ipv6');
DELETE FROM device_credential WHERE path = 'ipv6';
DROP INDEX device_credential_path;
DROP INDEX device_credential_live;
ALTER TABLE device_credential RENAME COLUMN path TO old_path;
ALTER TABLE device_credential ADD COLUMN path TEXT NOT NULL DEFAULT '' CHECK (path = '' OR path GLOB 'relay:*');
UPDATE device_credential SET path = old_path;
ALTER TABLE device_credential DROP COLUMN old_path;
CREATE UNIQUE INDEX device_credential_live ON device_credential (device_id, protocol, COALESCE(profile_id, ''), path)
    WHERE revoked_at IS NULL;
CREATE INDEX device_credential_path ON device_credential (device_id) WHERE path <> '' AND revoked_at IS NULL;
