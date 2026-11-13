-- Whether the node ever opened a sealed link session (1) or not (0). A node that did is not accepted again on a plain
-- one: a party between the agent and the panel cannot turn its sealed link back into a readable one. Re-enrolling the node
-- clears it, for an agent rolled back to a build that cannot seal.

-- +goose Up
ALTER TABLE node ADD COLUMN link_sealed INTEGER NOT NULL DEFAULT 0 CHECK (link_sealed IN (0, 1));

-- +goose Down
ALTER TABLE node DROP COLUMN link_sealed;
