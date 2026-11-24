-- The route of an AmneziaWG key: '' = everything goes through the VPN (every key before this), 'ru-direct' = the key's AllowedIPs leave
-- the big Russian services out (awg.RouteRUDirect). It is a property of the device, so a rotation keeps it. The version of the list a
-- key holds is in device_credential.dns_sig under "_ru" (access.markDNSStale compares it), not here.

-- +goose Up
ALTER TABLE device ADD COLUMN awg_route TEXT NOT NULL DEFAULT '' CHECK (awg_route IN ('', 'ru-direct'));

-- +goose Down
ALTER TABLE device DROP COLUMN awg_route;
