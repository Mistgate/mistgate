-- Per-node best-effort blocking of recognized BitTorrent traffic; off for existing nodes.
-- +goose Up
ALTER TABLE node ADD COLUMN torrent_blocker_enabled INTEGER NOT NULL DEFAULT 0
    CHECK (torrent_blocker_enabled IN (0, 1));

-- +goose Down
ALTER TABLE node DROP COLUMN torrent_blocker_enabled;
