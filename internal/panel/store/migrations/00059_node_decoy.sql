-- Existing nodes keep the site they serve today: the built-in page. New nodes choose their panel default at enrollment.

-- +goose Up
ALTER TABLE node ADD COLUMN decoy_mode TEXT NOT NULL DEFAULT 'builtin'
    CHECK (decoy_mode IN ('builtin', 'panel', 'own'));
ALTER TABLE node ADD COLUMN decoy_bundle_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE node ADD COLUMN decoy_agent_mode TEXT NOT NULL DEFAULT '';
ALTER TABLE node ADD COLUMN decoy_agent_hash TEXT NOT NULL DEFAULT '';

-- Bundles are normalized ZIPs addressed by the SHA-256 of their sorted file paths and contents.
CREATE TABLE node_decoy_bundle (
    sha256  TEXT PRIMARY KEY,
    content BLOB NOT NULL CHECK (length(content) <= 2621440)
);

-- +goose Down
DROP TABLE node_decoy_bundle;
ALTER TABLE node DROP COLUMN decoy_agent_hash;
ALTER TABLE node DROP COLUMN decoy_agent_mode;
ALTER TABLE node DROP COLUMN decoy_bundle_hash;
ALTER TABLE node DROP COLUMN decoy_mode;
