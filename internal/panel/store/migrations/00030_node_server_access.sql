-- Encrypted SSH access for nodes installed by the panel. Passwords are ciphertext from the panel vault.

-- +goose Up
CREATE TABLE node_server_access (
    node_id             TEXT    PRIMARY KEY REFERENCES node (id) ON DELETE CASCADE,
    node_name           TEXT    NOT NULL,
    ssh_host            TEXT    NOT NULL,
    ssh_port            INTEGER NOT NULL CHECK (ssh_port BETWEEN 1 AND 65535),
    ssh_username        TEXT    NOT NULL,
    host_fingerprint    TEXT    NOT NULL,
    password            BLOB    NOT NULL,
    pending_password    BLOB,
    configured_at       INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE node_server_access;
