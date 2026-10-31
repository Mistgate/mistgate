-- block_ads: the preset's apps drop the domains of the community list category-ads-all themselves (Mihomo, Xray, sing-box, Happ).
-- 0 = off, the default and what every stored preset did before, so no preset changes behaviour. Written by the dns module only.

-- +goose Up
ALTER TABLE dns_preset ADD COLUMN block_ads INTEGER NOT NULL DEFAULT 0 CHECK (block_ads IN (0, 1));

-- +goose Down
ALTER TABLE dns_preset DROP COLUMN block_ads;
