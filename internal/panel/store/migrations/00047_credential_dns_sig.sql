-- The DNS a key was issued with, per node: what a key carries is one resolver pair per node, and the page calls a key
-- stale when the DNS that applies to the person on a node is no longer that pair, whatever changed it (the person's
-- pick or its removal, what the owner offers or defaults to, a preset that was edited or deleted). dns_sig is a JSON
-- object, node id -> the pair the key's config holds ("1.1.1.1,8.8.8.8"); it is written when the person's device
-- fetches its configs (and when the key is made or rotated), never when an admin only looks. '' = nothing recorded
-- (a key from before this): such a key is not called stale, so an upgrade raises no false alarm.
--
-- It replaces configs_at (00044), the time of the last fetch that a person's pick was compared with: a pick that was
-- removed, or a change on the owner's side, left nothing to compare.

-- +goose Up
ALTER TABLE device_credential ADD COLUMN dns_sig TEXT NOT NULL DEFAULT '';
ALTER TABLE device_credential DROP COLUMN configs_at;

-- +goose Down
ALTER TABLE device_credential ADD COLUMN configs_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE device_credential DROP COLUMN dns_sig;
