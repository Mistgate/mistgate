-- The AWG server key pair outlives the inbound. Device keys and tunnel addresses are per (device, profile) and
-- survive an inbound removal; the server key is per inbound, so removing a profile from a node and adding it back
-- used to make a NEW server key and silently break every config already imported for that node. DeleteInbound now
-- parks the key here, CreateInbound of the same (profile, node) takes it back and deletes the row.
--
-- state_enc: the vault blob of the private key, AAD = "awg_retained_key:<profile id>:<node id>" (not the old inbound
-- id, which is gone); public_json: the plugin's public part as it was on the inbound. Deleting the profile or the
-- node drops the row (a retired node drops it explicitly: its row stays). Additive: a new table, nothing existing changes.

-- +goose Up
CREATE TABLE awg_retained_key (
    profile_id  TEXT    NOT NULL REFERENCES profile (id) ON DELETE CASCADE,
    node_id     TEXT    NOT NULL REFERENCES node (id)    ON DELETE CASCADE,
    state_enc   BLOB    NOT NULL,
    public_json TEXT    NOT NULL CHECK (json_valid(public_json)),
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (profile_id, node_id)
) STRICT;

-- +goose Down
DROP TABLE awg_retained_key;
