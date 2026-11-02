-- The Telegram proxy credential is the person's own key (a device of kind 'key'), made on request, no longer one per link
-- device. A build that gave every link device an MTProto credential left them behind: revoke them, so that only a key goes to
-- the nodes. Nothing changes for a database that never held one.

-- +goose Up
UPDATE device_credential SET revoked_at = strftime('%s', 'now')
 WHERE protocol = 'mtproto' AND revoked_at IS NULL
   AND device_id IN (SELECT id FROM device WHERE kind = 'link');

-- +goose Down
SELECT 1;
