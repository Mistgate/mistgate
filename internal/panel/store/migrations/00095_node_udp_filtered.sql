-- A paired UDP port check found that source-side filtering makes further confirmation senders harmful.

-- +goose Up
ALTER TABLE node ADD COLUMN udp_filtered_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node ADD COLUMN udp_filtered_sender TEXT NOT NULL DEFAULT '';
ALTER TABLE node ADD COLUMN udp_filtered_checked_at INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE node DROP COLUMN udp_filtered_checked_at;
ALTER TABLE node DROP COLUMN udp_filtered_sender;
ALTER TABLE node DROP COLUMN udp_filtered_at;
