-- The library of decoy sites. A site is a named set of files with versions; a node in mode 'own' points at a site
-- (node.decoy_site_id) and keeps decoy_bundle_hash equal to the site's current hash, so the agent link and the agents are
-- unchanged. The files themselves stay in node_decoy_bundle, addressed by SHA-256; a bundle is deleted when no version and no
-- node holds it. At most 5 versions of a site are kept and 100 sites are held (both enforced by the store, not here).
--
-- state: 'draft' cannot be put anywhere, 'ready' can. source: 'upload' | 'assistant' | 'mcp'; source_label names who made it.
-- A version's files_json, checks_json, title and sizes are worked out from the bundle when it is saved. A version made by this
-- migration has checks_json = '' until the store reads its bundle (store.BackfillDecoyVersions, run when the panel opens).
-- The panel's own site is the setting decoy_panel_source: 'builtin', 'dir' or 'site:<id>'.

-- +goose Up
CREATE TABLE decoy_site (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL COLLATE NOCASE CHECK (length(name) BETWEEN 1 AND 60),
    note         TEXT NOT NULL DEFAULT '' CHECK (length(note) <= 200),
    source       TEXT NOT NULL CHECK (source IN ('upload', 'assistant', 'mcp')),
    source_label TEXT NOT NULL DEFAULT '',
    state        TEXT NOT NULL CHECK (state IN ('draft', 'ready')),
    bundle_hash  TEXT NOT NULL,
    created_at   INTEGER NOT NULL,
    created_by   TEXT NOT NULL DEFAULT '',
    updated_at   INTEGER NOT NULL,
    updated_by   TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX decoy_site_name ON decoy_site (name);

CREATE TABLE decoy_site_version (
    site_id     TEXT NOT NULL,
    seq         INTEGER NOT NULL,
    bundle_hash TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    created_by  TEXT NOT NULL DEFAULT '',
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    file_count  INTEGER NOT NULL DEFAULT 0,
    files_json  TEXT NOT NULL DEFAULT '',
    title       TEXT NOT NULL DEFAULT '',
    checks_json TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (site_id, seq)
);
CREATE INDEX decoy_site_version_hash ON decoy_site_version (bundle_hash);

ALTER TABLE node ADD COLUMN decoy_site_id TEXT NOT NULL DEFAULT '';

-- Every bundle a node holds becomes a ready site named after the node (a second node of the same name gets " (2)"); nodes that
-- hold the same bundle share one site. A node that is not in mode 'own' but still holds a bundle keeps it too, as a site, so the
-- bundle is not lost: switching it back to 'own' works as before.
INSERT INTO decoy_site (id, name, note, source, source_label, state, bundle_hash, created_at, created_by, updated_at, updated_by)
SELECT 'dsi_' || lower(hex(randomblob(16))),
       CASE WHEN rn = 1 THEN substr(name, 1, 60) ELSE substr(name, 1, 50) || ' (' || rn || ')' END,
       '', 'upload', '', 'ready', hash,
       CAST(strftime('%s', 'now') AS INTEGER), '', CAST(strftime('%s', 'now') AS INTEGER), ''
FROM (
    SELECT r.hash AS hash, n.name AS name,
           row_number() OVER (PARTITION BY lower(substr(n.name, 1, 60)) ORDER BY n.id) AS rn
    FROM (SELECT decoy_bundle_hash AS hash, min(id) AS node_id FROM node WHERE decoy_bundle_hash <> '' GROUP BY decoy_bundle_hash) r
    JOIN node n ON n.id = r.node_id
);

INSERT INTO decoy_site_version (site_id, seq, bundle_hash, created_at)
SELECT id, 1, bundle_hash, created_at FROM decoy_site;

UPDATE node SET decoy_site_id = (SELECT s.id FROM decoy_site s WHERE s.bundle_hash = node.decoy_bundle_hash)
WHERE decoy_bundle_hash <> '';

-- +goose Down
ALTER TABLE node DROP COLUMN decoy_site_id;
DROP TABLE decoy_site_version;
DROP TABLE decoy_site;
