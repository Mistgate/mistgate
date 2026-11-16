-- Reverse relay (the bridge): a relay can pass the traffic on in two ways, the entry dialing the exit (dnat, what every relay did
-- until now) or the exit dialing the entry (reverse). mode is the owner's choice ('auto' lets the panel choose), active_mode what
-- stands now, mode_changed_at when active_mode last changed (0 = never) and mode_reason why (the words of relay.proto
-- Relay.mode_reason). Every existing relay becomes 'auto' on 'dnat': it stays a DNAT relay until an agent pair that can do the
-- bridge needs it. The bridge state and the count of good direct checks live in relay.check_json (bridge, direct_streak).
-- node.bridge_port is the TCP port of the entry's site the bridge arrives at (0 = 443 on the node's own site) and
-- node.bridge_path the random path of it, chosen with the first reverse relay of the entry.

-- +goose Up
ALTER TABLE relay ADD COLUMN mode TEXT NOT NULL DEFAULT 'auto' CHECK (mode IN ('auto', 'dnat', 'reverse'));
ALTER TABLE relay ADD COLUMN active_mode TEXT NOT NULL DEFAULT 'dnat' CHECK (active_mode IN ('dnat', 'reverse'));
ALTER TABLE relay ADD COLUMN mode_changed_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE relay ADD COLUMN mode_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE node ADD COLUMN bridge_port INTEGER NOT NULL DEFAULT 0 CHECK (bridge_port BETWEEN 0 AND 65535);
ALTER TABLE node ADD COLUMN bridge_path TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE node DROP COLUMN bridge_path;
ALTER TABLE node DROP COLUMN bridge_port;
ALTER TABLE relay DROP COLUMN mode_reason;
ALTER TABLE relay DROP COLUMN mode_changed_at;
ALTER TABLE relay DROP COLUMN active_mode;
ALTER TABLE relay DROP COLUMN mode;
