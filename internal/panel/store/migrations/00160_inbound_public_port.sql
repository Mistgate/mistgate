-- +goose Up
ALTER TABLE inbound ADD COLUMN public_port INTEGER NOT NULL DEFAULT 0 CHECK (public_port BETWEEN 0 AND 65535);

-- +goose Down
ALTER TABLE inbound DROP COLUMN public_port;
