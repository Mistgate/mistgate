-- A VLESS profile says what REALITY shows to anyone who is not a client: the node's own site ("self", the default for new
-- profiles) or another site ("site", reality.target). Profiles made before the field existed have a target and no kind, and
-- they have always meant another site: write that down, so the editor shows the right segment and a save never turns them
-- into "self" (design/vless.md section 7.5). What a node is sent does not change.

-- +goose Up
UPDATE profile
   SET settings_json = json_set(settings_json, '$.reality.target_kind', 'site')
 WHERE protocol = 'vless'
   AND json_type(settings_json, '$.reality.target') = 'text'
   AND trim(json_extract(settings_json, '$.reality.target')) <> ''
   AND json_type(settings_json, '$.reality.target_kind') IS NULL;

-- +goose Down
-- An older panel refuses a field it does not know: take the kind out of every VLESS profile (a "self" profile has no
-- target for that panel to read, and stays unusable until it is given one).
UPDATE profile
   SET settings_json = json_remove(settings_json, '$.reality.target_kind')
 WHERE protocol = 'vless'
   AND json_type(settings_json, '$.reality.target_kind') IS NOT NULL;
