-- Per-node switch "IPv6 for clients". On (the default, and what every node did before): clients may leave the node
-- over IPv6 too. Off: the node refuses client IPv6 that would leave through its own uplink, so apps fall back to IPv4
-- at once. For a provider whose IPv6 range is geolocated in another country this keeps people in the node's country.

-- +goose Up
ALTER TABLE node ADD COLUMN client_ipv6 INTEGER NOT NULL DEFAULT 1
    CHECK (client_ipv6 IN (0, 1));

-- +goose Down
ALTER TABLE node DROP COLUMN client_ipv6;
