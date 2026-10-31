-- One new built-in: No ads on the device. Cloudflare and Google with block_ads on, so the apps drop the domains of the
-- community list category-ads-all themselves (Mihomo, Xray, sing-box, Happ; AmneziaWG keys block nothing). Listed with
-- the no-ads presets, right after AdGuard (200). Nobody is moved onto it (users, groups and the default keep their
-- presets); the owner picks it where it is wanted. If a custom preset already took the name, the built-in gets a suffix
-- instead of failing the migration. Description: Russian, newline, English; the Chinese and English names live in the
-- code, keyed by id.

-- +goose Up
INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, block_ads, sort, created_at, updated_at)
SELECT 'dns_builtin_ads_device',
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Без рекламы на устройстве' COLLATE NOCASE) THEN 'Без рекламы на устройстве (встроенный)' ELSE 'Без рекламы на устройстве' END,
 'Приложение само режет рекламу и трекеры по списку category-ads-all; DNS — Cloudflare и Google' || char(10) || 'The app drops ads and trackers by the category-ads-all list; Cloudflare and Google for DNS',
 1, '[{"variant":"cloudflare/standard"},{"variant":"google/standard"}]', '[]', 0, 0, 'plain', 1, 210, strftime('%s', 'now'), strftime('%s', 'now');

-- +goose Down
-- Whoever used the preset falls back to the default, as when a preset is deleted.
UPDATE user SET dns_preset_id = NULL WHERE dns_preset_id = 'dns_builtin_ads_device';
UPDATE user_group SET dns_preset_id = NULL WHERE dns_preset_id = 'dns_builtin_ads_device';
DELETE FROM setting WHERE k = 'dns.default_preset' AND v = 'dns_builtin_ads_device';
DELETE FROM node_dns_option WHERE preset_id = 'dns_builtin_ads_device';
DELETE FROM user_node_dns WHERE preset_id = 'dns_builtin_ads_device';
DELETE FROM link_dns_option WHERE preset_id = 'dns_builtin_ads_device';
DELETE FROM user_link_dns WHERE preset_id = 'dns_builtin_ads_device';
DELETE FROM dns_preset WHERE id = 'dns_builtin_ads_device';
