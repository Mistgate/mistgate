-- Five new built-ins for people inside other countries, in the manner of China (00083): the main resolver is Cloudflare through the tunnel,
-- the country's sites go direct and resolve through Google (split_direct on). Iran goes by the community list "ir" (geosite category-ir
-- and geoip ir, internal/panel/dns/geo.go); Turkey, Kazakhstan, Belarus and Uzbekistan go by the domain endings of the country
-- (.tr; .kz and .қаз; .by and .бел; .uz), the Cyrillic ones as punycode. The main and the split resolver differ on purpose: the direct
-- rule of Xray JSON picks the resolvers by address, and the same address on both sides would send the main queries direct too.
-- Google as the local resolver is not checked from inside these countries (the panel checked Yandex for Russia and AliDNS and DNSPod
-- for China only): the docs say so. Nobody is moved onto them (users, groups and the default keep their presets). If a custom
-- preset already took a name, the built-in gets a suffix instead of failing the migration. Description: Russian, newline, English;
-- the other names live in the code, keyed by id. Listed after China (40).

-- +goose Up
INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, sort, created_at, updated_at)
SELECT 'dns_builtin_ir_split',
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Иран: иранские сайты напрямую' COLLATE NOCASE) THEN 'Иран: иранские сайты напрямую (встроенный)' ELSE 'Иран: иранские сайты напрямую' END,
 'Иранские сайты напрямую по списку сообщества, остальное через VPN; приложение само скачивает и обновляет список' || char(10) || 'Iranian sites direct by a community list, everything else through the VPN; the app downloads and updates the list itself',
 1, '[{"variant":"cloudflare/standard"}]',
 '[{"suffixes":[],"geo":["ir"],"servers":[{"variant":"google/standard"}]}]',
 0, 1, 'plain', 41, strftime('%s', 'now'), strftime('%s', 'now');

INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, sort, created_at, updated_at)
SELECT 'dns_builtin_tr_split',
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Турция: .tr напрямую' COLLATE NOCASE) THEN 'Турция: .tr напрямую (встроенный)' ELSE 'Турция: .tr напрямую' END,
 'Сайты на .tr напрямую, остальное через VPN' || char(10) || 'Sites on .tr direct, everything else through the VPN',
 1, '[{"variant":"cloudflare/standard"}]',
 '[{"suffixes":[".tr"],"servers":[{"variant":"google/standard"}]}]',
 0, 1, 'plain', 42, strftime('%s', 'now'), strftime('%s', 'now');

INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, sort, created_at, updated_at)
SELECT 'dns_builtin_kz_split',
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Казахстан: .kz напрямую' COLLATE NOCASE) THEN 'Казахстан: .kz напрямую (встроенный)' ELSE 'Казахстан: .kz напрямую' END,
 'Сайты на .kz и .қаз напрямую, остальное через VPN' || char(10) || 'Sites on .kz and .қаз direct, everything else through the VPN',
 1, '[{"variant":"cloudflare/standard"}]',
 '[{"suffixes":[".kz",".xn--80ao21a"],"servers":[{"variant":"google/standard"}]}]',
 0, 1, 'plain', 43, strftime('%s', 'now'), strftime('%s', 'now');

INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, sort, created_at, updated_at)
SELECT 'dns_builtin_by_split',
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Беларусь: .by напрямую' COLLATE NOCASE) THEN 'Беларусь: .by напрямую (встроенный)' ELSE 'Беларусь: .by напрямую' END,
 'Сайты на .by и .бел напрямую, остальное через VPN' || char(10) || 'Sites on .by and .бел direct, everything else through the VPN',
 1, '[{"variant":"cloudflare/standard"}]',
 '[{"suffixes":[".by",".xn--90ais"],"servers":[{"variant":"google/standard"}]}]',
 0, 1, 'plain', 44, strftime('%s', 'now'), strftime('%s', 'now');

INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, sort, created_at, updated_at)
SELECT 'dns_builtin_uz_split',
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Узбекистан: .uz напрямую' COLLATE NOCASE) THEN 'Узбекистан: .uz напрямую (встроенный)' ELSE 'Узбекистан: .uz напрямую' END,
 'Сайты на .uz напрямую, остальное через VPN' || char(10) || 'Sites on .uz direct, everything else through the VPN',
 1, '[{"variant":"cloudflare/standard"}]',
 '[{"suffixes":[".uz"],"servers":[{"variant":"google/standard"}]}]',
 0, 1, 'plain', 45, strftime('%s', 'now'), strftime('%s', 'now');

-- +goose Down
-- Whoever used the presets falls back to the default, as when a preset is deleted.
UPDATE user SET dns_preset_id = NULL WHERE dns_preset_id IN ('dns_builtin_ir_split', 'dns_builtin_tr_split', 'dns_builtin_kz_split', 'dns_builtin_by_split', 'dns_builtin_uz_split');
UPDATE user_group SET dns_preset_id = NULL WHERE dns_preset_id IN ('dns_builtin_ir_split', 'dns_builtin_tr_split', 'dns_builtin_kz_split', 'dns_builtin_by_split', 'dns_builtin_uz_split');
DELETE FROM setting WHERE k = 'dns.default_preset' AND v IN ('dns_builtin_ir_split', 'dns_builtin_tr_split', 'dns_builtin_kz_split', 'dns_builtin_by_split', 'dns_builtin_uz_split');
DELETE FROM node_dns_option WHERE preset_id IN ('dns_builtin_ir_split', 'dns_builtin_tr_split', 'dns_builtin_kz_split', 'dns_builtin_by_split', 'dns_builtin_uz_split');
DELETE FROM user_node_dns WHERE preset_id IN ('dns_builtin_ir_split', 'dns_builtin_tr_split', 'dns_builtin_kz_split', 'dns_builtin_by_split', 'dns_builtin_uz_split');
DELETE FROM link_dns_option WHERE preset_id IN ('dns_builtin_ir_split', 'dns_builtin_tr_split', 'dns_builtin_kz_split', 'dns_builtin_by_split', 'dns_builtin_uz_split');
DELETE FROM user_link_dns WHERE preset_id IN ('dns_builtin_ir_split', 'dns_builtin_tr_split', 'dns_builtin_kz_split', 'dns_builtin_by_split', 'dns_builtin_uz_split');
DELETE FROM dns_preset WHERE id IN ('dns_builtin_ir_split', 'dns_builtin_tr_split', 'dns_builtin_kz_split', 'dns_builtin_by_split', 'dns_builtin_uz_split');
