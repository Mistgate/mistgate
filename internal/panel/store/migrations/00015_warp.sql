-- WARP egress. One Cloudflare WARP account per node, stored encrypted.
-- Additive: a new table only. The registration constants (API version, user agent, client
-- version, TLS spec id) are data in setting(warp.api), not schema.

-- +goose Up

CREATE TABLE warp_account (                        -- at most one per node
    node_id           TEXT PRIMARY KEY REFERENCES node (id) ON DELETE CASCADE,
    source            TEXT    NOT NULL CHECK (source IN ('registered', 'imported')),
    -- vault, AAD = node_id: {"private_key","token","reg_id","license"}; token, reg_id and license are empty for an
    -- imported profile that came without wgcf-account.toml.
    secret_enc        BLOB    NOT NULL,
    peer_public_key   TEXT    NOT NULL,            -- always from the account, never a constant
    endpoint_v4       TEXT    NOT NULL,            -- IP literal, ":0" stripped
    endpoint_v6       TEXT    NOT NULL DEFAULT '',
    ports_json        TEXT    NOT NULL DEFAULT '[2408,500,1701,4500]' CHECK (json_valid(ports_json)),
    address_v4        TEXT    NOT NULL,            -- "172.16.0.2/32"
    address_v6        TEXT    NOT NULL DEFAULT '', -- new accounts may have none
    mtu               INTEGER NOT NULL DEFAULT 1280,
    client_id         TEXT    NOT NULL DEFAULT '', -- base64; its first 3 bytes are the "reserved" bytes
    use_reserved      INTEGER NOT NULL DEFAULT 0 CHECK (use_reserved IN (0, 1)),
    account_type      TEXT    NOT NULL DEFAULT '', -- free | plus, informational
    enabled           INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    tos_url           TEXT    NOT NULL DEFAULT '', -- what the owner was shown
    tos_accepted_by   TEXT    NOT NULL DEFAULT '', -- admin id; '' for an import
    tos_accepted_at   INTEGER NOT NULL DEFAULT 0,
    registered_with   TEXT    NOT NULL DEFAULT '', -- API version + user agent at registration (429 diagnosis)
    attention         TEXT    NOT NULL DEFAULT '', -- reason code when the owner has to decide (registration failed, revoked); '' = fine
    health_json       TEXT    NOT NULL DEFAULT '' CHECK (health_json = '' OR json_valid(health_json)),  -- last agent.v1.WarpHealth
    health_at         INTEGER NOT NULL DEFAULT 0,  -- when it was reported; 0 = never
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
) STRICT;

-- +goose Down

DROP TABLE warp_account;
