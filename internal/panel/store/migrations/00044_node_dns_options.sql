-- DNS per node for the person (user page, "DNS for each server"). The owner offers presets on a node; a person who
-- opens the user page picks one of them per node. Where the format of a client allows a resolver per server (the
-- AmneziaWG keys and the AmneziaWG proxies of a Mihomo profile) the pick is carried there; Hysteria2 in Mihomo and in
-- Happ keep one DNS for the whole subscription, so the pick does not reach them.
--
-- Effective DNS of (user, node): the person's pick while the node still offers it, else the node's default, else the
-- rule that applied before (the user's own preset, the group's, the instance default).

-- +goose Up

-- What the owner offers on a node: position orders the list, one row at most is the default.
CREATE TABLE node_dns_option (
    node_id    TEXT    NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    preset_id  TEXT    NOT NULL REFERENCES dns_preset (id) ON DELETE CASCADE,
    position   INTEGER NOT NULL,
    is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
    PRIMARY KEY (node_id, preset_id)
) STRICT;
CREATE UNIQUE INDEX node_dns_option_default ON node_dns_option (node_id) WHERE is_default = 1;

-- What a person picked. updated_at is Unix MILLISECONDS (unlike the seconds elsewhere): it is compared with
-- device_credential.configs_at to tell whether a key was fetched after the pick, and a pick and a fetch within one
-- second must still be told apart.
CREATE TABLE user_node_dns (
    user_id    TEXT    NOT NULL REFERENCES user (id) ON DELETE CASCADE,
    node_id    TEXT    NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    preset_id  TEXT    NOT NULL REFERENCES dns_preset (id) ON DELETE CASCADE,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, node_id)
) STRICT;
CREATE INDEX user_node_dns_node ON user_node_dns (node_id);

-- When the device last fetched its configs (Unix milliseconds, 0 = not since it was created: the credential's own
-- created_at then stands in). A pick made after it means the key holds an older DNS.
ALTER TABLE device_credential ADD COLUMN configs_at INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE device_credential DROP COLUMN configs_at;
DROP TABLE user_node_dns;
DROP TABLE node_dns_option;
