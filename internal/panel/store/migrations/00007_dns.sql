-- +goose Up
-- DNS presets. A preset is a named resolver policy assigned to a user, to a group, or to the
-- whole instance. servers / split are JSON so a preset stays one row:
--   servers: [{"kind": "plain" | "doh" | "dot", "address": "..."}]   (main resolvers, in order of preference)
--   split:   [{"suffixes": [".ru", ...], "servers": [{"kind": ..., "address": ...}]}]
CREATE TABLE dns_preset (
    id          TEXT PRIMARY KEY,                       -- dns_<random>, or dns_builtin_<slug> for the built-ins
    name        TEXT    NOT NULL COLLATE NOCASE UNIQUE,
    description TEXT    NOT NULL DEFAULT '',
    builtin     INTEGER NOT NULL DEFAULT 0 CHECK (builtin IN (0, 1)),
    servers     TEXT    NOT NULL,
    split       TEXT    NOT NULL DEFAULT '[]',
    ipv4_only   INTEGER NOT NULL DEFAULT 0 CHECK (ipv4_only IN (0, 1)),
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;

-- NULL = inherit (user -> group -> instance default -> the built-in default). Deliberately no FOREIGN KEY:
-- SQLite cannot drop such a column again, and the delete in the store clears the references itself, in
-- the same transaction.
ALTER TABLE user_group ADD COLUMN dns_preset_id TEXT;
ALTER TABLE user ADD COLUMN dns_preset_id TEXT;

-- Built-ins: stable ids, editable, never deleted. The description is Russian, then a newline, then English.
INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, created_at, updated_at) VALUES
('dns_builtin_ru_split', 'Россия-сплит',
 'Российские домены (.ru, .su, .рф, госуслуги, банки) спрашивают Яндекс, остальное — Cloudflare и Google. Подходит по умолчанию.' || char(10) ||
 'Russian domains (.ru, .su, .рф, state services, banks) go to Yandex DNS, everything else to Cloudflare and Google. The default.',
 1,
 '[{"kind":"plain","address":"1.1.1.1"},{"kind":"plain","address":"8.8.8.8"}]',
 '[{"suffixes":[".ru",".su",".xn--p1ai","yandex.com","yandex.net","yastatic.net","vk.com","userapi.com","sberbank.com","vtb.com"],"servers":[{"kind":"plain","address":"77.88.8.8"},{"kind":"plain","address":"77.88.8.1"}]}]',
 0, strftime('%s', 'now'), strftime('%s', 'now')),
('dns_builtin_standard', 'Стандарт',
 'Cloudflare и Google для всего.' || char(10) || 'Cloudflare and Google for everything.',
 1,
 '[{"kind":"plain","address":"1.1.1.1"},{"kind":"plain","address":"8.8.8.8"}]',
 '[]', 0, strftime('%s', 'now'), strftime('%s', 'now')),
('dns_builtin_adblock', 'Без рекламы',
 'AdGuard DNS: блокирует рекламу и трекеры.' || char(10) || 'AdGuard DNS: blocks ads and trackers.',
 1,
 '[{"kind":"plain","address":"94.140.14.14"},{"kind":"plain","address":"94.140.15.15"},{"kind":"doh","address":"https://dns.adguard-dns.com/dns-query"}]',
 '[]', 0, strftime('%s', 'now'), strftime('%s', 'now')),
('dns_builtin_family', 'Семейный',
 'AdGuard Family: блокирует рекламу, трекеры и сайты для взрослых.' || char(10) || 'AdGuard Family: blocks ads, trackers and adult sites.',
 1,
 '[{"kind":"plain","address":"94.140.14.15"},{"kind":"plain","address":"94.140.15.16"}]',
 '[]', 0, strftime('%s', 'now'), strftime('%s', 'now')),
('dns_builtin_quad9', 'Защита от вредоносных',
 'Quad9: блокирует известные вредоносные и фишинговые домены.' || char(10) || 'Quad9: blocks known malware and phishing domains.',
 1,
 '[{"kind":"plain","address":"9.9.9.9"},{"kind":"plain","address":"149.112.112.112"}]',
 '[]', 0, strftime('%s', 'now'), strftime('%s', 'now')),
('dns_builtin_yandex', 'Яндекс',
 'Яндекс DNS для всего: российские сайты открываются надёжнее.' || char(10) || 'Yandex DNS for everything: Russian sites resolve more reliably.',
 1,
 '[{"kind":"plain","address":"77.88.8.8"},{"kind":"plain","address":"77.88.8.1"}]',
 '[]', 0, strftime('%s', 'now'), strftime('%s', 'now'));

-- +goose Down
ALTER TABLE user DROP COLUMN dns_preset_id;
ALTER TABLE user_group DROP COLUMN dns_preset_id;
DROP TABLE dns_preset;
