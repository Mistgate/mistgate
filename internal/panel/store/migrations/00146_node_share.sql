-- Who a node is handed out to. A group receives every node that is not on its disabled list (00089). A node the owner holds
-- back has hold_new_groups = 1: it is on the disabled list of every group that has not been given it, and a group made later
-- starts with it on its list too (Access.CreateGroup). So the rule that decides what a group receives does not change, and
-- an existing node stays at 0: handed to every group, as before. share_changed_at is when the owner last set the node's
-- share (0 = never; its creation is then the time), and sharing is the initial share asked for in an SSH install, as JSON
-- ('' = decided when the node is made: the first node of the panel to everyone, the next ones to nobody).
-- +goose Up
ALTER TABLE node ADD COLUMN hold_new_groups INTEGER NOT NULL DEFAULT 0 CHECK (hold_new_groups IN (0, 1));
ALTER TABLE node ADD COLUMN share_changed_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_provision_job ADD COLUMN sharing TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE node_provision_job DROP COLUMN sharing;
ALTER TABLE node DROP COLUMN share_changed_at;
ALTER TABLE node DROP COLUMN hold_new_groups;
