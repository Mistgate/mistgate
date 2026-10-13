-- device.kind says what a device is, so no query has to guess it from hwid_hash. 'key' is an AmneziaVPN key made by
-- AddAWGDevice (it carries a synthetic hwid_hash); 'link' is everything that takes the subscription link: the implicit
-- shared device (hwid_hash IS NULL) and, later, devices that report a real HWID. Until now a non-NULL hwid_hash always
-- meant a key, so the backfill marks exactly those rows.

-- +goose Up
ALTER TABLE device ADD COLUMN kind TEXT NOT NULL DEFAULT 'link' CHECK (kind IN ('link', 'key'));
UPDATE device SET kind = 'key' WHERE hwid_hash IS NOT NULL;

-- +goose Down
ALTER TABLE device DROP COLUMN kind;
