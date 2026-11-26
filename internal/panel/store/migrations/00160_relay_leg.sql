-- The address family of a relay's leg between the entry and the exit (relay.proto Relay.leg): the entry may reach the exit's
-- server over IPv4 (what every relay did until now) or over IPv6, while people still connect to the entry over IPv4. 'auto'
-- lets the panel try the family that works and remember it in active_leg ('4' or '6'); leg_tried_at is when the panel last
-- moved to the other family by itself, so a dead leg is retried at a sane pace, not on every check.
-- The address itself is not stored: the panel takes the exit's IPv6 entry when it is on, else the doctor's stable address, and
-- the leg check proves that this entry reaches it.

-- +goose Up
ALTER TABLE relay ADD COLUMN leg TEXT NOT NULL DEFAULT 'auto' CHECK (leg IN ('auto', 'ipv4', 'ipv6'));
ALTER TABLE relay ADD COLUMN active_leg TEXT NOT NULL DEFAULT '4' CHECK (active_leg IN ('4', '6'));
ALTER TABLE relay ADD COLUMN leg_tried_at INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE relay DROP COLUMN leg_tried_at;
ALTER TABLE relay DROP COLUMN active_leg;
ALTER TABLE relay DROP COLUMN leg;
