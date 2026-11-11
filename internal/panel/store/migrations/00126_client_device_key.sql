-- key_hash is SHA-256 of the 32-byte device key; enrol_token_hash is SHA-256 of the subscription token used to enrol.
-- Device key hashes are never reused, including after a device is revoked.

-- +goose Up
ALTER TABLE device ADD COLUMN key_hash BLOB CHECK (key_hash IS NULL OR length(key_hash) = 32);
ALTER TABLE device ADD COLUMN enrol_token_hash BLOB CHECK (enrol_token_hash IS NULL OR length(enrol_token_hash) = 32);
CREATE UNIQUE INDEX device_key_hash ON device (key_hash) WHERE key_hash IS NOT NULL;

-- +goose Down
DROP INDEX device_key_hash;
ALTER TABLE device DROP COLUMN enrol_token_hash;
ALTER TABLE device DROP COLUMN key_hash;
