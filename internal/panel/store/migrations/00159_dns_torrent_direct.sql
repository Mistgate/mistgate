-- torrent_direct: the preset's apps send the traffic of torrent clients direct, past the VPN (internal/panel/dns/torrent.go). 0 = off, what
-- every stored preset did before and what the built-in ones keep: the person's real address is seen in a swarm, so the owner switches
-- it on where it is wanted. Written by the dns module only.

-- +goose Up
ALTER TABLE dns_preset ADD COLUMN torrent_direct INTEGER NOT NULL DEFAULT 0 CHECK (torrent_direct IN (0, 1));

-- +goose Down
ALTER TABLE dns_preset DROP COLUMN torrent_direct;
