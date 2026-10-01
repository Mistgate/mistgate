-- +goose Up
-- An explicit order for the built-in presets (before this they followed the row id, which put the Russia
-- preset added by 00008 last). Custom presets keep sort 0 and are listed after the built-ins, by name.
ALTER TABLE dns_preset ADD COLUMN sort INTEGER NOT NULL DEFAULT 0;

UPDATE dns_preset SET sort = CASE id
    WHEN 'dns_builtin_ru_split'   THEN 10
    WHEN 'dns_builtin_ru_proxied' THEN 20
    WHEN 'dns_builtin_standard'   THEN 30
    WHEN 'dns_builtin_adblock'    THEN 40
    WHEN 'dns_builtin_family'     THEN 50
    WHEN 'dns_builtin_quad9'      THEN 60
    WHEN 'dns_builtin_yandex'     THEN 70
    ELSE 0 END
WHERE builtin = 1;

-- One-line descriptions for the cards (the old ones were a paragraph; the editor still takes any length). Only a
-- description that is still the stock text is replaced: an admin's own words stay. Russian, newline, English.
UPDATE dns_preset SET description =
    'Российские сайты напрямую, остальное через VPN' || char(10) || 'Russian sites direct, everything else through the VPN'
WHERE id = 'dns_builtin_ru_split' AND description =
    'Сайты .ru, .su, .рф, а также государственные и банковские открываются напрямую, мимо VPN — для тех, кто находится в России. Остальное идёт через VPN: Cloudflare и Google.' || char(10) ||
    'Sites on .ru, .su, .рф and state and bank sites open directly, bypassing the VPN — for people inside Russia. Everything else goes through the VPN: Cloudflare and Google.';
UPDATE dns_preset SET description =
    'Всё через VPN, российские сайты — через российскую ноду' || char(10) || 'Everything through the VPN, Russian sites via a Russian node'
WHERE id = 'dns_builtin_ru_proxied' AND description =
    'Всё идёт через VPN, российские госсайты открываются через российскую ноду — для тех, кто за границей. Cloudflare и Google для всего.' || char(10) ||
    'Everything goes through the VPN; Russian state sites open through a Russian node — for people abroad. Cloudflare and Google for everything.';
UPDATE dns_preset SET description =
    'Cloudflare и Google для всего' || char(10) || 'Cloudflare and Google for everything'
WHERE id = 'dns_builtin_standard' AND description = 'Cloudflare и Google для всего.' || char(10) || 'Cloudflare and Google for everything.';
UPDATE dns_preset SET description =
    'AdGuard DNS: без рекламы и трекеров' || char(10) || 'AdGuard DNS: no ads, no trackers'
WHERE id = 'dns_builtin_adblock' AND description = 'AdGuard DNS: блокирует рекламу и трекеры.' || char(10) || 'AdGuard DNS: blocks ads and trackers.';
UPDATE dns_preset SET description =
    'AdGuard Family: без рекламы и сайтов для взрослых' || char(10) || 'AdGuard Family: no ads, no adult sites'
WHERE id = 'dns_builtin_family' AND description = 'AdGuard Family: блокирует рекламу, трекеры и сайты для взрослых.' || char(10) || 'AdGuard Family: blocks ads, trackers and adult sites.';
UPDATE dns_preset SET description =
    'Quad9: блокирует вредоносные и фишинговые домены' || char(10) || 'Quad9: blocks malware and phishing domains'
WHERE id = 'dns_builtin_quad9' AND description = 'Quad9: блокирует известные вредоносные и фишинговые домены.' || char(10) || 'Quad9: blocks known malware and phishing domains.';
UPDATE dns_preset SET description =
    'Яндекс DNS для всего: российские сайты надёжнее' || char(10) || 'Yandex DNS for everything: Russian sites are more reliable'
WHERE id = 'dns_builtin_yandex' AND description = 'Яндекс DNS для всего: российские сайты открываются надёжнее.' || char(10) || 'Yandex DNS for everything: Russian sites resolve more reliably.';

-- Names that say what is inside (the old ones, like "Standard", told nothing). Renamed only while the name is still the
-- stock one (an admin may have edited it) and only if the new name is free. The English names live in the admin UI,
-- keyed by the preset id; the stored name is Russian.
UPDATE dns_preset SET name = 'Россия: .ru напрямую', updated_at = strftime('%s', 'now')
WHERE id = 'dns_builtin_ru_split' AND name IN ('Россия-сплит (напрямую)', 'Россия-сплит')
  AND NOT EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Россия: .ru напрямую');
UPDATE dns_preset SET name = 'Россия: всё через VPN', updated_at = strftime('%s', 'now')
WHERE id = 'dns_builtin_ru_proxied' AND name IN ('Россия через VPN', 'Россия через VPN (встроенный)')
  AND NOT EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Россия: всё через VPN');
UPDATE dns_preset SET name = 'Cloudflare + Google', updated_at = strftime('%s', 'now')
WHERE id = 'dns_builtin_standard' AND name = 'Стандарт'
  AND NOT EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Cloudflare + Google');
UPDATE dns_preset SET name = 'AdGuard: без рекламы', updated_at = strftime('%s', 'now')
WHERE id = 'dns_builtin_adblock' AND name = 'Без рекламы'
  AND NOT EXISTS (SELECT 1 FROM dns_preset WHERE name = 'AdGuard: без рекламы');
UPDATE dns_preset SET name = 'AdGuard Family: без рекламы и 18+', updated_at = strftime('%s', 'now')
WHERE id = 'dns_builtin_family' AND name = 'Семейный'
  AND NOT EXISTS (SELECT 1 FROM dns_preset WHERE name = 'AdGuard Family: без рекламы и 18+');
UPDATE dns_preset SET name = 'Quad9: защита от вредоносных', updated_at = strftime('%s', 'now')
WHERE id = 'dns_builtin_quad9' AND name = 'Защита от вредоносных'
  AND NOT EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Quad9: защита от вредоносных');
UPDATE dns_preset SET name = 'Яндекс DNS', updated_at = strftime('%s', 'now')
WHERE id = 'dns_builtin_yandex' AND name = 'Яндекс'
  AND NOT EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Яндекс DNS');

-- +goose Down
-- Only the names and the column go back; the shorter descriptions stay (they are still true).
UPDATE dns_preset SET name = 'Россия-сплит (напрямую)' WHERE id = 'dns_builtin_ru_split' AND name = 'Россия: .ru напрямую'
  AND NOT EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Россия-сплит (напрямую)');
UPDATE dns_preset SET name = 'Россия через VPN' WHERE id = 'dns_builtin_ru_proxied' AND name = 'Россия: всё через VPN'
  AND NOT EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Россия через VPN');
UPDATE dns_preset SET name = 'Россия через VPN (встроенный)' WHERE id = 'dns_builtin_ru_proxied' AND name = 'Россия: всё через VPN';
UPDATE dns_preset SET name = 'Стандарт' WHERE id = 'dns_builtin_standard' AND name = 'Cloudflare + Google'
  AND NOT EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Стандарт');
UPDATE dns_preset SET name = 'Без рекламы' WHERE id = 'dns_builtin_adblock' AND name = 'AdGuard: без рекламы'
  AND NOT EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Без рекламы');
UPDATE dns_preset SET name = 'Семейный' WHERE id = 'dns_builtin_family' AND name = 'AdGuard Family: без рекламы и 18+'
  AND NOT EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Семейный');
UPDATE dns_preset SET name = 'Защита от вредоносных' WHERE id = 'dns_builtin_quad9' AND name = 'Quad9: защита от вредоносных'
  AND NOT EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Защита от вредоносных');
UPDATE dns_preset SET name = 'Яндекс' WHERE id = 'dns_builtin_yandex' AND name = 'Яндекс DNS'
  AND NOT EXISTS (SELECT 1 FROM dns_preset WHERE name = 'Яндекс');
ALTER TABLE dns_preset DROP COLUMN sort;
