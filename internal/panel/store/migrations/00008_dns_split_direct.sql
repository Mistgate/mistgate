-- +goose Up
-- Per-preset "split goes direct" (Happ can only send a split resolver's domains direct, bypassing the VPN).
-- Default 0: a preset with a split that does not set it keeps everything inside the tunnel.
ALTER TABLE dns_preset ADD COLUMN split_direct INTEGER NOT NULL DEFAULT 0 CHECK (split_direct IN (0, 1));

-- The Russia split was always delivered direct; say so. The name and description are renamed only while the
-- name is still the stock one (an admin may have edited the built-in) and only if the new name is free.
UPDATE dns_preset SET split_direct = 1 WHERE id = 'dns_builtin_ru_split';
UPDATE dns_preset SET
    name = 'Россия-сплит (напрямую)',
    description = 'Сайты .ru, .su, .рф, а также государственные и банковские открываются напрямую, мимо VPN — для тех, кто находится в России. Остальное идёт через VPN: Cloudflare и Google.' || char(10) ||
                  'Sites on .ru, .su, .рф and state and bank sites open directly, bypassing the VPN — for people inside Russia. Everything else goes through the VPN: Cloudflare and Google.',
    updated_at = strftime('%s', 'now')
WHERE id = 'dns_builtin_ru_split' AND name = 'Россия-сплит'
  AND NOT EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Россия-сплит (напрямую)' COLLATE NOCASE);

-- The other Russia preset: no split, everything through the VPN. If a custom preset already took the name,
-- the built-in gets a suffix instead of failing the migration.
INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, created_at, updated_at)
SELECT 'dns_builtin_ru_proxied',
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Россия через VPN' COLLATE NOCASE)
      THEN 'Россия через VPN (встроенный)' ELSE 'Россия через VPN' END,
 'Всё идёт через VPN, российские госсайты открываются через российскую ноду — для тех, кто за границей. Cloudflare и Google для всего.' || char(10) ||
 'Everything goes through the VPN; Russian state sites open through a Russian node — for people abroad. Cloudflare and Google for everything.',
 1,
 '[{"kind":"plain","address":"1.1.1.1"},{"kind":"plain","address":"8.8.8.8"}]',
 '[]', 0, 0, strftime('%s', 'now'), strftime('%s', 'now');

-- +goose Down
UPDATE user SET dns_preset_id = NULL WHERE dns_preset_id = 'dns_builtin_ru_proxied';
UPDATE user_group SET dns_preset_id = NULL WHERE dns_preset_id = 'dns_builtin_ru_proxied';
DELETE FROM setting WHERE k = 'dns.default_preset' AND v = 'dns_builtin_ru_proxied';
DELETE FROM dns_preset WHERE id = 'dns_builtin_ru_proxied';
UPDATE dns_preset SET
    name = 'Россия-сплит',
    description = 'Российские домены (.ru, .su, .рф, госуслуги, банки) спрашивают Яндекс, остальное — Cloudflare и Google. Подходит по умолчанию.' || char(10) ||
                  'Russian domains (.ru, .su, .рф, state services, banks) go to Yandex DNS, everything else to Cloudflare and Google. The default.'
WHERE id = 'dns_builtin_ru_split' AND name = 'Россия-сплит (напрямую)'
  AND NOT EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Россия-сплит' COLLATE NOCASE);
ALTER TABLE dns_preset DROP COLUMN split_direct;
