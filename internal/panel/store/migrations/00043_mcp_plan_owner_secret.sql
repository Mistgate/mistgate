-- The SSH password the owner enters when approving an MCP node_install plan, sealed by the panel vault to the plan.
-- The agent never sees it: the plan's apply hands it to the install job once (and clears it), and the sweep clears it
-- from any plan that can no longer be applied.

-- +goose Up
ALTER TABLE mcp_plan ADD COLUMN owner_secret BLOB;

-- +goose Down
ALTER TABLE mcp_plan DROP COLUMN owner_secret;
