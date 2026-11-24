-- Seven built-in presets are retired: Yandex DNS, the unfiltered Cloudflare, Google and DNS.SB, Cloudflare Family, Yandex family and
-- OpenDNS FamilyShield (they were one catalog server each, and a person picks any catalog server in a custom preset anyway).
-- One that is assigned anywhere (a user, a group, the instance default, a node's or the link's offer, a person's pick) or that the
-- owner changed (against the stock text of 00007/00009/00011: name, description, servers, split and the flags; updated_at is no
-- measure, 00009 moves it) stays as an ordinary own preset: builtin = 0, id and every reference as they were, so nobody notices.
-- Every other one is deleted. New installations never get them back: this migration deletes them right after 00011 made them.
-- The Russian, Chinese and the other built-ins are not touched.

-- +goose Up
WITH stock(id, name, description, servers) AS (VALUES
  ('dns_builtin_yandex', 'Яндекс DNS', 'Яндекс DNS для всего: российские сайты надёжнее' || char(10) || 'Yandex DNS for everything: Russian sites are more reliable', '[{"variant":"yandex/basic"}]'),
  ('dns_builtin_cloudflare', 'Cloudflare: без фильтров', 'Cloudflare 1.1.1.1: быстрый DNS без фильтров' || char(10) || 'Cloudflare 1.1.1.1: fast DNS, no filtering', '[{"variant":"cloudflare/standard"}]'),
  ('dns_builtin_google', 'Google: без фильтров', 'Google Public DNS: без фильтров' || char(10) || 'Google Public DNS: no filtering', '[{"variant":"google/standard"}]'),
  ('dns_builtin_dnssb', 'DNS.SB: без фильтров', 'DNS.SB: без логов и без фильтров' || char(10) || 'DNS.SB: no logs, no filtering', '[{"variant":"dnssb/standard"}]'),
  ('dns_builtin_cloudflare_family', 'Cloudflare Family: без вредоносных и 18+', 'Cloudflare Family: без вредоносных сайтов и контента 18+' || char(10) || 'Cloudflare Family: no malware, no 18+', '[{"variant":"cloudflare/family"}]'),
  ('dns_builtin_yandex_family', 'Яндекс семейный: без 18+', 'Яндекс DNS, семейный режим: без контента 18+' || char(10) || 'Yandex DNS, family mode: no 18+ content', '[{"variant":"yandex/family"}]'),
  ('dns_builtin_opendns_family', 'OpenDNS FamilyShield: без 18+', 'OpenDNS FamilyShield: без контента 18+' || char(10) || 'OpenDNS FamilyShield: no 18+ content', '[{"variant":"opendns/familyshield"}]')
)
UPDATE dns_preset SET builtin = 0
WHERE builtin = 1 AND id IN (SELECT id FROM stock) AND (
     id IN (SELECT dns_preset_id FROM user WHERE dns_preset_id IS NOT NULL)
  OR id IN (SELECT dns_preset_id FROM user_group WHERE dns_preset_id IS NOT NULL)
  OR id IN (SELECT v FROM setting WHERE k = 'dns.default_preset')
  OR id IN (SELECT preset_id FROM node_dns_option)
  OR id IN (SELECT preset_id FROM user_node_dns)
  OR id IN (SELECT preset_id FROM link_dns_option)
  OR id IN (SELECT preset_id FROM user_link_dns)
  OR split <> '[]' OR ipv4_only <> 0 OR split_direct <> 0 OR preferred_transport <> 'plain' OR block_ads <> 0 OR block_max <> 0
  OR NOT EXISTS (SELECT 1 FROM stock s WHERE s.id = dns_preset.id AND s.name = dns_preset.name AND s.description = dns_preset.description AND s.servers = dns_preset.servers)
);
DELETE FROM dns_preset WHERE builtin = 1 AND id IN ('dns_builtin_yandex', 'dns_builtin_cloudflare', 'dns_builtin_google', 'dns_builtin_dnssb', 'dns_builtin_cloudflare_family', 'dns_builtin_yandex_family', 'dns_builtin_opendns_family');

-- +goose Down
-- The ones that stayed become built-in again; the ones that were deleted come back as 00011 made them. A custom preset that took the
-- name meanwhile makes the returning one carry the suffix, as in 00011.
UPDATE dns_preset SET builtin = 1 WHERE id IN ('dns_builtin_yandex', 'dns_builtin_cloudflare', 'dns_builtin_google', 'dns_builtin_dnssb', 'dns_builtin_cloudflare_family', 'dns_builtin_yandex_family', 'dns_builtin_opendns_family');
WITH stock(id, name, description, servers, sort) AS (VALUES
  ('dns_builtin_yandex', 'Яндекс DNS', 'Яндекс DNS для всего: российские сайты надёжнее' || char(10) || 'Yandex DNS for everything: Russian sites are more reliable', '[{"variant":"yandex/basic"}]', 30),
  ('dns_builtin_cloudflare', 'Cloudflare: без фильтров', 'Cloudflare 1.1.1.1: быстрый DNS без фильтров' || char(10) || 'Cloudflare 1.1.1.1: fast DNS, no filtering', '[{"variant":"cloudflare/standard"}]', 110),
  ('dns_builtin_google', 'Google: без фильтров', 'Google Public DNS: без фильтров' || char(10) || 'Google Public DNS: no filtering', '[{"variant":"google/standard"}]', 120),
  ('dns_builtin_dnssb', 'DNS.SB: без фильтров', 'DNS.SB: без логов и без фильтров' || char(10) || 'DNS.SB: no logs, no filtering', '[{"variant":"dnssb/standard"}]', 130),
  ('dns_builtin_cloudflare_family', 'Cloudflare Family: без вредоносных и 18+', 'Cloudflare Family: без вредоносных сайтов и контента 18+' || char(10) || 'Cloudflare Family: no malware, no 18+', '[{"variant":"cloudflare/family"}]', 310),
  ('dns_builtin_yandex_family', 'Яндекс семейный: без 18+', 'Яндекс DNS, семейный режим: без контента 18+' || char(10) || 'Yandex DNS, family mode: no 18+ content', '[{"variant":"yandex/family"}]', 320),
  ('dns_builtin_opendns_family', 'OpenDNS FamilyShield: без 18+', 'OpenDNS FamilyShield: без контента 18+' || char(10) || 'OpenDNS FamilyShield: no 18+ content', '[{"variant":"opendns/familyshield"}]', 330)
)
INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, sort, created_at, updated_at)
SELECT s.id,
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset p WHERE p.name = s.name COLLATE NOCASE) THEN s.name || ' (встроенный)' ELSE s.name END,
 s.description, 1, s.servers, '[]', 0, 0, 'plain', s.sort, strftime('%s', 'now'), strftime('%s', 'now')
FROM stock s WHERE NOT EXISTS (SELECT 1 FROM dns_preset p WHERE p.id = s.id);
