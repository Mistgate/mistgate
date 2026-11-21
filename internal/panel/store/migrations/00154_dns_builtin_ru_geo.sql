-- One new built-in: Russia, Russian sites direct by the community list. The same resolvers and suffixes as the stock Russian split
-- (dns_builtin_ru_split, left as it is), plus the country list "ru" (geosite category-ru and geoip ru, internal/panel/dns/geo.go)
-- that the apps download and refresh themselves. The list names the well-known Russian services, not every Russian site.
-- Listed right after the stock Russian split (10). Nobody is moved onto it (users, groups and the default keep their presets);
-- the owner picks it where it is wanted. If a custom preset already took the name, the built-in gets a suffix instead of failing
-- the migration. Description: Russian, newline, English; the other names live in the code, keyed by id.

-- +goose Up
INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, sort, created_at, updated_at)
SELECT 'dns_builtin_ru_geo',
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Россия: российские сайты напрямую (список сообщества)' COLLATE NOCASE) THEN 'Россия: российские сайты напрямую (список сообщества) (встроенный)' ELSE 'Россия: российские сайты напрямую (список сообщества)' END,
 'Известные российские сервисы напрямую по списку сообщества, не все сайты; приложение само скачивает и обновляет список' || char(10) || 'Known Russian services direct by a community list, not every site; the app downloads and updates the list itself',
 1, '[{"variant":"cloudflare/standard"},{"variant":"google/standard"}]',
 '[{"suffixes":[".ru",".su",".xn--p1ai","yandex.com","yandex.net","yastatic.net","vk.com","userapi.com","sberbank.com","vtb.com"],"geo":["ru"],"servers":[{"variant":"yandex/basic"}]}]',
 0, 1, 'plain', 15, strftime('%s', 'now'), strftime('%s', 'now');

-- +goose Down
-- Whoever used the preset falls back to the default, as when a preset is deleted.
UPDATE user SET dns_preset_id = NULL WHERE dns_preset_id = 'dns_builtin_ru_geo';
UPDATE user_group SET dns_preset_id = NULL WHERE dns_preset_id = 'dns_builtin_ru_geo';
DELETE FROM setting WHERE k = 'dns.default_preset' AND v = 'dns_builtin_ru_geo';
DELETE FROM node_dns_option WHERE preset_id = 'dns_builtin_ru_geo';
DELETE FROM user_node_dns WHERE preset_id = 'dns_builtin_ru_geo';
DELETE FROM link_dns_option WHERE preset_id = 'dns_builtin_ru_geo';
DELETE FROM user_link_dns WHERE preset_id = 'dns_builtin_ru_geo';
DELETE FROM dns_preset WHERE id = 'dns_builtin_ru_geo';
