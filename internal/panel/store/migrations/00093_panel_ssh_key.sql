-- One panel-wide ed25519 key and the verified SSH login methods for node server access.
-- The private key is sealed by the panel vault with AAD "panel-ssh-key".
-- An empty node_server_access.password (X'') means no password is saved.
-- +goose Up
CREATE TABLE panel_ssh_key (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    private_key BLOB    NOT NULL,
    public_key  TEXT    NOT NULL,
    fingerprint TEXT    NOT NULL,
    created_at  INTEGER NOT NULL
) STRICT;
ALTER TABLE node_server_access ADD COLUMN panel_key_fp TEXT NOT NULL DEFAULT '';
ALTER TABLE node_server_access ADD COLUMN password_login INTEGER NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE node_server_access DROP COLUMN password_login;
ALTER TABLE node_server_access DROP COLUMN panel_key_fp;
DROP TABLE panel_ssh_key;
