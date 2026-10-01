-- Server names in subscription apps say the country ("🇩🇪 Germany"), not the node ("🇩🇪 de1"): the default template is now
-- "{flag} {country}" (subsettings.DefaultNameTemplate). An install that saved its settings kept the old default
-- "{flag} {node}" in the stored document; that untouched old default moves to the new one, a
-- template anyone edited stays. Users see the new names at their next subscription refresh.

-- +goose Up
UPDATE setting SET v = json_set(v, '$.server_name_template', '{flag} {country}')
 WHERE k = 'subscription_settings' AND json_valid(v) AND json_extract(v, '$.server_name_template') = '{flag} {node}';

-- +goose Down
-- Nothing records whether "{flag} {country}" came from Up or from the owner, so the data stays as it is.
SELECT 1;
