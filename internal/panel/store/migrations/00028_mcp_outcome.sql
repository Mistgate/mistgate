-- +goose Up
-- What came of an applied or failed MCP plan as a code the admin UI words itself, with its values as a JSON object of
-- strings (integrations.proto Approval.outcome_code / outcome_params). '' for plans made before codes existed.
ALTER TABLE mcp_plan ADD COLUMN outcome_code TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_plan ADD COLUMN outcome_params TEXT NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE mcp_plan DROP COLUMN outcome_params;
ALTER TABLE mcp_plan DROP COLUMN outcome_code;
