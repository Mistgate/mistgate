-- The IPv6 entry of a node: an extra server "<node> · IPv6" in people's subscriptions that connects to an IPv6 address of the node
-- instead of its main address, for those whose network blocks the node's IPv4 address. ipv6_entry is that address ('' = off, the
-- default and what every node did before). ipv6_json is what the panel knows about it: the latest outside checks of the node's
-- IPv6 addresses (the address, when, by whom, a verdict per port), newest first. Later steps of the feature (changing the address)
-- keep their state in the same document, so they need no migration. Both columns are written by the panel only, with a
-- compare-and-swap on the pair, which works the same on SQLite and D1.

-- +goose Up
ALTER TABLE node ADD COLUMN ipv6_entry TEXT NOT NULL DEFAULT '';
ALTER TABLE node ADD COLUMN ipv6_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(ipv6_json));

-- +goose Down
ALTER TABLE node DROP COLUMN ipv6_json;
ALTER TABLE node DROP COLUMN ipv6_entry;
