-- A cross-isolate lease for scheduled work in the Cloudflare edition.

-- +goose Up
CREATE TABLE job_lease (
    name       TEXT PRIMARY KEY,
    holder     TEXT NOT NULL,
    expires_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE job_lease;
