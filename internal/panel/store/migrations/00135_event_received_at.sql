-- Keep node-event quotas based on when the panel accepted the event, not the node's clock.

-- +goose Up
ALTER TABLE event ADD COLUMN received_at INTEGER NOT NULL DEFAULT 0;
CREATE INDEX event_agent_received ON event (node_id, received_at) WHERE source = 'agent' AND node_id IS NOT NULL;
-- One marker per node, class (params.level) and UTC day: the day's budget of that class of agent events ran out.
CREATE UNIQUE INDEX event_node_daily_limit ON event (node_id, received_at / 86400, json_extract(params_json, '$.level'))
    WHERE source = 'panel' AND code = 'node_event_daily_limit' AND node_id IS NOT NULL;

-- +goose Down
DROP INDEX event_node_daily_limit;
DROP INDEX event_agent_received;
ALTER TABLE event DROP COLUMN received_at;
