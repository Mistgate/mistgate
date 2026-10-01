-- +goose Up
-- DNS v2: a preset server may point at a provider of the built-in catalog (internal/panel/dns/providers.go)
-- instead of a bare address, and a preset has one preferred transport (plain / DoT / DoH).
--   servers: [{"variant": "cloudflare/standard"}, {"kind": "plain", "address": "9.9.9.9"}]   (catalog or custom, mixed)
--
-- Additive: one column with a default, the stock built-ins now point at the catalog, more built-ins. Rows that an
-- admin edited are left alone, and so is every custom preset; users and groups keep their preset ids. The plain
-- addresses a variant stands for are the ones the stock rows had, so what clients receive does not change: every
-- preset starts on the plain transport.
ALTER TABLE dns_preset ADD COLUMN preferred_transport TEXT NOT NULL DEFAULT 'plain' CHECK (preferred_transport IN ('plain', 'dot', 'doh'));

-- The stock built-ins reference the catalog. Only while servers / split are still the stock text.
UPDATE dns_preset SET servers = '[{"variant":"cloudflare/standard"},{"variant":"google/standard"}]' WHERE id = 'dns_builtin_ru_split' AND servers = '[{"kind":"plain","address":"1.1.1.1"},{"kind":"plain","address":"8.8.8.8"}]';
UPDATE dns_preset SET servers = '[{"variant":"cloudflare/standard"},{"variant":"google/standard"}]' WHERE id = 'dns_builtin_ru_proxied' AND servers = '[{"kind":"plain","address":"1.1.1.1"},{"kind":"plain","address":"8.8.8.8"}]';
UPDATE dns_preset SET servers = '[{"variant":"cloudflare/standard"},{"variant":"google/standard"}]' WHERE id = 'dns_builtin_standard' AND servers = '[{"kind":"plain","address":"1.1.1.1"},{"kind":"plain","address":"8.8.8.8"}]';
UPDATE dns_preset SET servers = '[{"variant":"adguard/default"}]' WHERE id = 'dns_builtin_adblock' AND servers = '[{"kind":"plain","address":"94.140.14.14"},{"kind":"plain","address":"94.140.15.15"},{"kind":"doh","address":"https://dns.adguard-dns.com/dns-query"}]';
UPDATE dns_preset SET servers = '[{"variant":"adguard/family"}]' WHERE id = 'dns_builtin_family' AND servers = '[{"kind":"plain","address":"94.140.14.15"},{"kind":"plain","address":"94.140.15.16"}]';
UPDATE dns_preset SET servers = '[{"variant":"quad9/standard"}]' WHERE id = 'dns_builtin_quad9' AND servers = '[{"kind":"plain","address":"9.9.9.9"},{"kind":"plain","address":"149.112.112.112"}]';
UPDATE dns_preset SET servers = '[{"variant":"yandex/basic"}]' WHERE id = 'dns_builtin_yandex' AND servers = '[{"kind":"plain","address":"77.88.8.8"},{"kind":"plain","address":"77.88.8.1"}]';
UPDATE dns_preset SET split = '[{"suffixes":[".ru",".su",".xn--p1ai","yandex.com","yandex.net","yastatic.net","vk.com","userapi.com","sberbank.com","vtb.com"],"servers":[{"variant":"yandex/basic"}]}]' WHERE id = 'dns_builtin_ru_split' AND split = '[{"suffixes":[".ru",".su",".xn--p1ai","yandex.com","yandex.net","yastatic.net","vk.com","userapi.com","sberbank.com","vtb.com"],"servers":[{"kind":"plain","address":"77.88.8.8"},{"kind":"plain","address":"77.88.8.1"}]}]';

-- Order by category (the UI also filters by it): Russia, regular, no ads, family, security. Room between the numbers.
UPDATE dns_preset SET sort = CASE id
    WHEN 'dns_builtin_ru_split' THEN 10
    WHEN 'dns_builtin_ru_proxied' THEN 20
    WHEN 'dns_builtin_yandex' THEN 30
    WHEN 'dns_builtin_standard' THEN 100
    WHEN 'dns_builtin_adblock' THEN 200
    WHEN 'dns_builtin_family' THEN 300
    WHEN 'dns_builtin_quad9' THEN 400
    ELSE sort END
WHERE builtin = 1;

-- New built-ins, one per provider variant that is not covered yet. If a custom preset already took the name, the
-- built-in gets a suffix instead of failing the migration. Description: Russian, newline, English. The English
-- names live in the admin UI keyed by id. (Mullvad is in the catalog but has no preset: its public encrypted DNS
-- shuts down on 2026-11-02.)
INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, sort, created_at, updated_at)
SELECT 'dns_builtin_cloudflare',
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Cloudflare: без фильтров' COLLATE NOCASE) THEN 'Cloudflare: без фильтров (встроенный)' ELSE 'Cloudflare: без фильтров' END,
 'Cloudflare 1.1.1.1: быстрый DNS без фильтров' || char(10) || 'Cloudflare 1.1.1.1: fast DNS, no filtering',
 1, '[{"variant":"cloudflare/standard"}]', '[]', 0, 0, 'plain', 110, strftime('%s', 'now'), strftime('%s', 'now');

INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, sort, created_at, updated_at)
SELECT 'dns_builtin_google',
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Google: без фильтров' COLLATE NOCASE) THEN 'Google: без фильтров (встроенный)' ELSE 'Google: без фильтров' END,
 'Google Public DNS: без фильтров' || char(10) || 'Google Public DNS: no filtering',
 1, '[{"variant":"google/standard"}]', '[]', 0, 0, 'plain', 120, strftime('%s', 'now'), strftime('%s', 'now');

INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, sort, created_at, updated_at)
SELECT 'dns_builtin_dnssb',
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset WHERE name = 'DNS.SB: без фильтров' COLLATE NOCASE) THEN 'DNS.SB: без фильтров (встроенный)' ELSE 'DNS.SB: без фильтров' END,
 'DNS.SB: без логов и без фильтров' || char(10) || 'DNS.SB: no logs, no filtering',
 1, '[{"variant":"dnssb/standard"}]', '[]', 0, 0, 'plain', 130, strftime('%s', 'now'), strftime('%s', 'now');

INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, sort, created_at, updated_at)
SELECT 'dns_builtin_cloudflare_family',
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Cloudflare Family: без вредоносных и 18+' COLLATE NOCASE) THEN 'Cloudflare Family: без вредоносных и 18+ (встроенный)' ELSE 'Cloudflare Family: без вредоносных и 18+' END,
 'Cloudflare Family: без вредоносных сайтов и контента 18+' || char(10) || 'Cloudflare Family: no malware, no 18+',
 1, '[{"variant":"cloudflare/family"}]', '[]', 0, 0, 'plain', 310, strftime('%s', 'now'), strftime('%s', 'now');

INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, sort, created_at, updated_at)
SELECT 'dns_builtin_yandex_family',
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Яндекс семейный: без 18+' COLLATE NOCASE) THEN 'Яндекс семейный: без 18+ (встроенный)' ELSE 'Яндекс семейный: без 18+' END,
 'Яндекс DNS, семейный режим: без контента 18+' || char(10) || 'Yandex DNS, family mode: no 18+ content',
 1, '[{"variant":"yandex/family"}]', '[]', 0, 0, 'plain', 320, strftime('%s', 'now'), strftime('%s', 'now');

INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, sort, created_at, updated_at)
SELECT 'dns_builtin_opendns_family',
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset WHERE name = 'OpenDNS FamilyShield: без 18+' COLLATE NOCASE) THEN 'OpenDNS FamilyShield: без 18+ (встроенный)' ELSE 'OpenDNS FamilyShield: без 18+' END,
 'OpenDNS FamilyShield: без контента 18+' || char(10) || 'OpenDNS FamilyShield: no 18+ content',
 1, '[{"variant":"opendns/familyshield"}]', '[]', 0, 0, 'plain', 330, strftime('%s', 'now'), strftime('%s', 'now');

INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, sort, created_at, updated_at)
SELECT 'dns_builtin_cloudflare_security',
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Cloudflare Security: защита от вредоносных' COLLATE NOCASE) THEN 'Cloudflare Security: защита от вредоносных (встроенный)' ELSE 'Cloudflare Security: защита от вредоносных' END,
 'Cloudflare Security: блокирует вредоносные домены' || char(10) || 'Cloudflare Security: blocks malware domains',
 1, '[{"variant":"cloudflare/security"}]', '[]', 0, 0, 'plain', 410, strftime('%s', 'now'), strftime('%s', 'now');

INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, sort, created_at, updated_at)
SELECT 'dns_builtin_yandex_safe',
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Яндекс безопасный: защита от вредоносных' COLLATE NOCASE) THEN 'Яндекс безопасный: защита от вредоносных (встроенный)' ELSE 'Яндекс безопасный: защита от вредоносных' END,
 'Яндекс DNS, безопасный режим: без вредоносных и мошеннических сайтов' || char(10) || 'Yandex DNS, safe mode: no malware, no fraud sites',
 1, '[{"variant":"yandex/safe"}]', '[]', 0, 0, 'plain', 420, strftime('%s', 'now'), strftime('%s', 'now');

-- +goose Down
-- Users and groups that use the added built-ins fall back to the default, as when a preset is deleted.
UPDATE user SET dns_preset_id = NULL WHERE dns_preset_id IN ('dns_builtin_cloudflare', 'dns_builtin_google', 'dns_builtin_dnssb', 'dns_builtin_cloudflare_family', 'dns_builtin_yandex_family', 'dns_builtin_opendns_family', 'dns_builtin_cloudflare_security', 'dns_builtin_yandex_safe');
UPDATE user_group SET dns_preset_id = NULL WHERE dns_preset_id IN ('dns_builtin_cloudflare', 'dns_builtin_google', 'dns_builtin_dnssb', 'dns_builtin_cloudflare_family', 'dns_builtin_yandex_family', 'dns_builtin_opendns_family', 'dns_builtin_cloudflare_security', 'dns_builtin_yandex_safe');
DELETE FROM setting WHERE k = 'dns.default_preset' AND v IN ('dns_builtin_cloudflare', 'dns_builtin_google', 'dns_builtin_dnssb', 'dns_builtin_cloudflare_family', 'dns_builtin_yandex_family', 'dns_builtin_opendns_family', 'dns_builtin_cloudflare_security', 'dns_builtin_yandex_safe');
DELETE FROM dns_preset WHERE id IN ('dns_builtin_cloudflare', 'dns_builtin_google', 'dns_builtin_dnssb', 'dns_builtin_cloudflare_family', 'dns_builtin_yandex_family', 'dns_builtin_opendns_family', 'dns_builtin_cloudflare_security', 'dns_builtin_yandex_safe');

-- The stock built-ins go back to bare addresses. A custom preset that uses a catalog variant is not converted:
-- the older code cannot read it, so delete or edit such presets before rolling back.
UPDATE dns_preset SET servers = '[{"kind":"plain","address":"1.1.1.1"},{"kind":"plain","address":"8.8.8.8"}]' WHERE id = 'dns_builtin_ru_split' AND servers = '[{"variant":"cloudflare/standard"},{"variant":"google/standard"}]';
UPDATE dns_preset SET servers = '[{"kind":"plain","address":"1.1.1.1"},{"kind":"plain","address":"8.8.8.8"}]' WHERE id = 'dns_builtin_ru_proxied' AND servers = '[{"variant":"cloudflare/standard"},{"variant":"google/standard"}]';
UPDATE dns_preset SET servers = '[{"kind":"plain","address":"1.1.1.1"},{"kind":"plain","address":"8.8.8.8"}]' WHERE id = 'dns_builtin_standard' AND servers = '[{"variant":"cloudflare/standard"},{"variant":"google/standard"}]';
UPDATE dns_preset SET servers = '[{"kind":"plain","address":"94.140.14.14"},{"kind":"plain","address":"94.140.15.15"},{"kind":"doh","address":"https://dns.adguard-dns.com/dns-query"}]' WHERE id = 'dns_builtin_adblock' AND servers = '[{"variant":"adguard/default"}]';
UPDATE dns_preset SET servers = '[{"kind":"plain","address":"94.140.14.15"},{"kind":"plain","address":"94.140.15.16"}]' WHERE id = 'dns_builtin_family' AND servers = '[{"variant":"adguard/family"}]';
UPDATE dns_preset SET servers = '[{"kind":"plain","address":"9.9.9.9"},{"kind":"plain","address":"149.112.112.112"}]' WHERE id = 'dns_builtin_quad9' AND servers = '[{"variant":"quad9/standard"}]';
UPDATE dns_preset SET servers = '[{"kind":"plain","address":"77.88.8.8"},{"kind":"plain","address":"77.88.8.1"}]' WHERE id = 'dns_builtin_yandex' AND servers = '[{"variant":"yandex/basic"}]';
UPDATE dns_preset SET split = '[{"suffixes":[".ru",".su",".xn--p1ai","yandex.com","yandex.net","yastatic.net","vk.com","userapi.com","sberbank.com","vtb.com"],"servers":[{"kind":"plain","address":"77.88.8.8"},{"kind":"plain","address":"77.88.8.1"}]}]' WHERE id = 'dns_builtin_ru_split' AND split = '[{"suffixes":[".ru",".su",".xn--p1ai","yandex.com","yandex.net","yastatic.net","vk.com","userapi.com","sberbank.com","vtb.com"],"servers":[{"variant":"yandex/basic"}]}]';
UPDATE dns_preset SET sort = CASE id
    WHEN 'dns_builtin_ru_split' THEN 10
    WHEN 'dns_builtin_ru_proxied' THEN 20
    WHEN 'dns_builtin_yandex' THEN 70
    WHEN 'dns_builtin_standard' THEN 30
    WHEN 'dns_builtin_adblock' THEN 40
    WHEN 'dns_builtin_family' THEN 50
    WHEN 'dns_builtin_quad9' THEN 60
    ELSE sort END
WHERE builtin = 1;
ALTER TABLE dns_preset DROP COLUMN preferred_transport;
