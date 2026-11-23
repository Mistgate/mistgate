-- block_max: the preset's apps drop the domains of the messenger Max (max.ru, oneme.ru) themselves, so that it cannot reach its servers
-- and report the VPN (Mihomo, Xray, sing-box, Happ; AmneziaWG keys block nothing). 0 = off, what every stored preset did before.
-- The two built-in Russian presets that send Russian sites direct get it on, so it starts working for everybody after the update;
-- the owner can switch it off in the preset, and a custom preset keeps 0. Written by the dns module only.

-- +goose Up
ALTER TABLE dns_preset ADD COLUMN block_max INTEGER NOT NULL DEFAULT 0 CHECK (block_max IN (0, 1));
UPDATE dns_preset SET block_max = 1 WHERE id IN ('dns_builtin_ru_split', 'dns_builtin_ru_geo');

-- +goose Down
ALTER TABLE dns_preset DROP COLUMN block_max;
