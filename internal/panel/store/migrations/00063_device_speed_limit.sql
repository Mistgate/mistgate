-- user.device_speed_limit_bps is the speed limit of ONE device of the person: no single device goes faster than this.
-- 0 = no separate device limit. user.speed_limit_bps keeps its meaning (the limit of the person, shared by all their
-- devices on a node). The panel sends the first to the nodes as Credential.rate_limit_bps and the second as
-- Credential.user_rate_limit_bps.

-- +goose Up
ALTER TABLE user ADD COLUMN device_speed_limit_bps INTEGER NOT NULL DEFAULT 0 CHECK (device_speed_limit_bps >= 0);

-- +goose Down
ALTER TABLE user DROP COLUMN device_speed_limit_bps;
