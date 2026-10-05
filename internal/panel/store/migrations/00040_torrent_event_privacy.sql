-- A torrent_attempt event kept the client's address and the destination of the blocked flow. Events reach helpers, API
-- tokens and MCP, while client addresses are for the owner alone and the destination says where a person went: the
-- panel stores neither any more (fleet onEvent), and this removes them from the events already written. The user, the
-- inbound and the protocol stay.

-- +goose Up
UPDATE event SET params_json = json_remove(params_json, '$.client_ip', '$.destination')
 WHERE code = 'torrent_attempt'
   AND (json_extract(params_json, '$.client_ip') IS NOT NULL OR json_extract(params_json, '$.destination') IS NOT NULL);

-- +goose Down
-- The removed addresses are gone for good.
SELECT 1;
