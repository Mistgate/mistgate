-- Server names include the saved profile label by default so users can tell protocols and WARP exits apart.

-- +goose Up
UPDATE setting SET v = json_set(v, '$.server_name_template', '{flag} {country} · {profile}')
 WHERE k = 'subscription_settings' AND json_valid(v) AND json_extract(v, '$.server_name_template') = '{flag} {country}';

-- +goose Down
-- Nothing records whether the old default came from this panel or the owner, so leave the name template as stored.
SELECT 1;
