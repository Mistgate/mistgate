-- The automatic preparation of the AmneziaWG kernel module (agent.proto "AWG AND WARP"): what the
-- panel last learned about a build it asked a node to run. JSON {state, since, want, code, reason}; '' = nothing was
-- ever asked. state is running | done | failed; want says the admin picked "kernel" and the panel switches the node's
-- awg_backend to it when the node reports the module built and loaded (awg_backend itself never becomes "kernel"
-- before that, so a failed or interrupted build cannot leave the node's AmneziaWG profiles without a backend).
-- Additive: one column with a default, nothing existing changes.

-- +goose Up
ALTER TABLE node ADD COLUMN awg_prepare_json TEXT NOT NULL DEFAULT '' CHECK (awg_prepare_json = '' OR json_valid(awg_prepare_json));

-- +goose Down
ALTER TABLE node DROP COLUMN awg_prepare_json;
