-- device.blocked_at: the owner revoked the device and also refused its HWID. A blocked row stays (the prune keeps it), and
-- while one with the hash exists no device of that HWID is born again (linkDeviceCreateGuard); unblocking clears the column.
-- Only a link device that reported a HWID is ever blocked, so the hash a block is looked up by is the HWID hash.
--
-- event_device_limit serves the alert of step 4, which looks at the device_limit_reached events of the last day.

-- +goose Up
ALTER TABLE device ADD COLUMN blocked_at INTEGER;
CREATE INDEX event_device_limit ON event (ts) WHERE code = 'device_limit_reached';

-- +goose Down
DROP INDEX event_device_limit;
ALTER TABLE device DROP COLUMN blocked_at;
