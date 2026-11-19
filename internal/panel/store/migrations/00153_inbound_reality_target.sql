-- The REALITY target of one inbound: "host:port" of a site this node shows instead of the profile's target (empty = the profile's own).
-- The neighbour of a node is a property of the node, while the profile's target is one for every node the profile stands on.

-- +goose Up
ALTER TABLE inbound ADD COLUMN reality_target_override TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE inbound DROP COLUMN reality_target_override;
