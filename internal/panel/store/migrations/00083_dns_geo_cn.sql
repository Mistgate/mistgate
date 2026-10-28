-- A split rule may name whole countries next to (or instead of) domain suffixes:
--   split: [{"suffixes": [], "geo": ["cn"], "servers": [{"variant": "alidns/standard"}]}]
-- The code is a list of the geosite and geoip data the clients carry (internal/panel/dns/geo.go). It is a new key of the JSON,
-- so no column changes and no stored preset is touched: a rule without "geo" reads as before.
--
-- One new built-in: China, Chinese sites direct. The main resolvers are Cloudflare and Google through the tunnel, the Chinese
-- sites go direct and resolve through AliDNS and DNSPod. Nobody is moved onto it (users, groups and the default keep their
-- presets); the owner picks it where it is wanted. If a custom preset already took the name, the built-in gets a suffix instead
-- of failing the migration. Description: Russian, newline, English; the Chinese and English names live in the code, keyed by id.

-- +goose Up
INSERT INTO dns_preset (id, name, description, builtin, servers, split, ipv4_only, split_direct, preferred_transport, sort, created_at, updated_at)
SELECT 'dns_builtin_cn_split',
 CASE WHEN EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Китай: китайские сайты напрямую' COLLATE NOCASE) THEN 'Китай: китайские сайты напрямую (встроенный)' ELSE 'Китай: китайские сайты напрямую' END,
 'Китайские сайты напрямую, остальное через VPN' || char(10) || 'Chinese sites direct, everything else through the VPN',
 1, '[{"variant":"cloudflare/standard"},{"variant":"google/standard"}]',
 '[{"suffixes":[],"geo":["cn"],"servers":[{"variant":"alidns/standard"},{"variant":"dnspod/standard"}]}]',
 0, 1, 'plain', 40, strftime('%s', 'now'), strftime('%s', 'now');

-- +goose Down
-- Whoever used the preset falls back to the default, as when a preset is deleted.
UPDATE user SET dns_preset_id = NULL WHERE dns_preset_id = 'dns_builtin_cn_split';
UPDATE user_group SET dns_preset_id = NULL WHERE dns_preset_id = 'dns_builtin_cn_split';
DELETE FROM setting WHERE k = 'dns.default_preset' AND v = 'dns_builtin_cn_split';
DELETE FROM node_dns_option WHERE preset_id = 'dns_builtin_cn_split';
DELETE FROM user_node_dns WHERE preset_id = 'dns_builtin_cn_split';
DELETE FROM link_dns_option WHERE preset_id = 'dns_builtin_cn_split';
DELETE FROM user_link_dns WHERE preset_id = 'dns_builtin_cn_split';
DELETE FROM dns_preset WHERE id = 'dns_builtin_cn_split';
