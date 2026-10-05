-- Saved server access outlives its node (retiring keeps it until the owner forgets it), and the panel can say whether
-- it generated the saved password itself (an MCP rotation), which only the panel then knows. Rows saved before this
-- migration count as owner-chosen.

-- +goose Up
ALTER TABLE node_server_access ADD COLUMN password_generated INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE node_server_access DROP COLUMN password_generated;
