-- AmneziaWG. Devices that hold their own keys, one credential per (device, profile), IPAM for the tunnel
-- addresses, per-inbound plugin key material (the AWG server key pair), the profile epoch behind "needs a new
-- key" and the AWG backend setting of a node.
--
-- Device rows of AWG devices: device.hwid_hash is NOT NULL, set to sha256("awg:" || device id). NULL is the
-- implicit per-user subscription device (index device_implicit allows exactly one), so an explicit AWG device
-- cannot use it; a synthetic hash also keeps every "hwid_hash IS NULL" lookup of the implicit device from ever
-- matching an AWG device, and never equals the hash of a real HWID header.
--
-- Additive on a live database: every new column has a default that keeps the old rows valid (hysteria2 stays
-- with profile_id NULL and behaves exactly as before). 00012 and 00013 are the newest before it.

-- +goose Up

-- One AWG credential per (device, profile); hysteria2 keeps profile_id NULL and its (device, protocol) uniqueness.
ALTER TABLE device_credential ADD COLUMN profile_id TEXT REFERENCES profile (id) ON DELETE CASCADE;
-- The profile critical_epoch the device last RECEIVED a config for; stale = config_epoch < profile.critical_epoch.
ALTER TABLE device_credential ADD COLUMN config_epoch INTEGER NOT NULL DEFAULT 0;
DROP INDEX device_credential_live;
CREATE UNIQUE INDEX device_credential_live ON device_credential (device_id, protocol, COALESCE(profile_id, ''))
    WHERE revoked_at IS NULL;
-- Stale counts and "all live credentials of a profile" (impact of a critical change, delete cascade).
CREATE INDEX device_credential_profile ON device_credential (profile_id) WHERE revoked_at IS NULL AND profile_id IS NOT NULL;
-- NOTE for the desired-state query: it must join credentials to inbounds by profile as well
-- ((c.profile_id IS NULL OR c.profile_id = inbound.profile_id)), or an AWG credential would go to every AWG inbound.

-- Bumped by every x-critical change of the profile (and by "change the server key"): it is what makes the devices
-- of the profile stale.
ALTER TABLE profile ADD COLUMN critical_epoch INTEGER NOT NULL DEFAULT 0;

-- Plugin-neutral material of one inbound. AWG: the server private key (vault, AAD = inbound id) and the public
-- key. NULL / '{}' for hysteria2. awg_health_json is the last agent.v1.AwgHealth of the inbound, as reported.
ALTER TABLE inbound ADD COLUMN plugin_state_enc   BLOB;
ALTER TABLE inbound ADD COLUMN plugin_public_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(plugin_public_json));
ALTER TABLE inbound ADD COLUMN awg_health_json    TEXT NOT NULL DEFAULT '' CHECK (awg_health_json = '' OR json_valid(awg_health_json));
ALTER TABLE inbound ADD COLUMN awg_health_at      INTEGER NOT NULL DEFAULT 0;   -- when it was reported; 0 = never

-- IPAM and the public key of one AWG credential. idx: 2..(subnet size - 2); address = subnet base + idx, the node
-- is .1. A released idx stays in quarantine for 24 h (released_at > 0): it is not handed out again while an old
-- client may still use it. A rotation releases the old row and inserts a new one with the same idx, so uniqueness is
-- only among the live rows.
CREATE TABLE awg_peer (
    credential_id TEXT PRIMARY KEY REFERENCES device_credential (id) ON DELETE CASCADE,
    profile_id    TEXT    NOT NULL REFERENCES profile (id) ON DELETE CASCADE,
    idx           INTEGER NOT NULL CHECK (idx >= 2),
    public_key    TEXT    NOT NULL,
    released_at   INTEGER NOT NULL DEFAULT 0,      -- 0 = live; > 0 = start of the quarantine
    created_at    INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX awg_peer_live_idx ON awg_peer (profile_id, idx) WHERE released_at = 0;
CREATE UNIQUE INDEX awg_peer_pub      ON awg_peer (public_key) WHERE released_at = 0;
-- The allocator looks at the recent releases of a profile.
CREATE INDEX awg_peer_released ON awg_peer (profile_id, released_at) WHERE released_at > 0;

-- "auto": the kernel module when it is already loaded, else userspace. Sent to agents that list "awg/1".
ALTER TABLE node ADD COLUMN awg_backend TEXT NOT NULL DEFAULT 'auto' CHECK (awg_backend IN ('auto', 'kernel', 'userspace'));

-- +goose Down

ALTER TABLE node DROP COLUMN awg_backend;

DROP TABLE awg_peer;

ALTER TABLE inbound DROP COLUMN awg_health_at;
ALTER TABLE inbound DROP COLUMN awg_health_json;
ALTER TABLE inbound DROP COLUMN plugin_public_json;
ALTER TABLE inbound DROP COLUMN plugin_state_enc;

ALTER TABLE profile DROP COLUMN critical_epoch;

-- The old schema cannot hold AWG credentials (one live credential per device and protocol): remove the devices
-- that only existed for AWG, then the remaining AWG credentials (the credential rows of a removed device go with it).
DELETE FROM device
 WHERE id IN (SELECT device_id FROM device_credential WHERE profile_id IS NOT NULL)
   AND id NOT IN (SELECT device_id FROM device_credential WHERE profile_id IS NULL);
DELETE FROM device_credential WHERE profile_id IS NOT NULL;

-- SQLite cannot drop a column that carries a foreign key: rebuild the table as 00002 made it.
DROP INDEX device_credential_profile;
DROP INDEX device_credential_live;
CREATE TABLE device_credential_old (
    id                  TEXT PRIMARY KEY,
    device_id           TEXT    NOT NULL REFERENCES device (id) ON DELETE CASCADE,
    user_id             TEXT    NOT NULL REFERENCES user (id)   ON DELETE CASCADE,
    protocol            TEXT    NOT NULL,
    secret_enc          BLOB    NOT NULL,
    data_json           TEXT    NOT NULL CHECK (json_valid(data_json)),
    created_at          INTEGER NOT NULL,
    revoked_at          INTEGER
) STRICT;
INSERT INTO device_credential_old (id, device_id, user_id, protocol, secret_enc, data_json, created_at, revoked_at)
    SELECT id, device_id, user_id, protocol, secret_enc, data_json, created_at, revoked_at FROM device_credential;
DROP TABLE device_credential;
ALTER TABLE device_credential_old RENAME TO device_credential;
CREATE UNIQUE INDEX device_credential_live ON device_credential (device_id, protocol) WHERE revoked_at IS NULL;
CREATE INDEX device_credential_user ON device_credential (user_id) WHERE revoked_at IS NULL;
CREATE INDEX device_credential_protocol ON device_credential (protocol) WHERE revoked_at IS NULL;
