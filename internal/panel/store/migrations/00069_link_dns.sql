-- One DNS for the apps that take the link (Happ, the Mihomo apps, Xray JSON and sing-box), picked by the person on their page
-- from the presets the owner offers for them (SubscriptionSettings.link_dns). These apps have one resolver for the whole
-- subscription, so it is one pick for all servers; the AmneziaWG keys keep their pick per server (user_node_dns).
--
-- DNS of a person's link apps: their pick while the owner still offers it, else their own preset (user.dns_preset_id, set by an
-- admin), else the group's, else the default of the offer, else the instance default.

-- +goose Up

-- What the owner offers for the link apps: position orders the list, one row at most is the default.
CREATE TABLE link_dns_option (
    preset_id  TEXT    NOT NULL PRIMARY KEY REFERENCES dns_preset (id) ON DELETE CASCADE,
    position   INTEGER NOT NULL,
    is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1))
) STRICT;
CREATE UNIQUE INDEX link_dns_option_default ON link_dns_option (is_default) WHERE is_default = 1;

-- What a person picked for their link apps. updated_at is Unix milliseconds, as in user_node_dns.
CREATE TABLE user_link_dns (
    user_id    TEXT    NOT NULL PRIMARY KEY REFERENCES user (id) ON DELETE CASCADE,
    preset_id  TEXT    NOT NULL REFERENCES dns_preset (id) ON DELETE CASCADE,
    updated_at INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE user_link_dns;
DROP TABLE link_dns_option;
