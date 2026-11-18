-- Extra groups of a person. user.group_id stays the person's main group: it decides the DNS preset, the Mihomo template, the
-- response headers and the colour. The groups listed here only add access: profile P on node N comes to the person from a group
-- (the main one or an extra one) that holds P and gets N, the same group for both. A person's extra group is never their main
-- group (Access.UpdateUser and Access.DeleteGroup, the two writers of user.group_id, keep that in the batch that writes it).
-- No row to backfill: an empty table is exactly the access everyone had.
-- +goose Up
CREATE TABLE user_extra_group (
    user_id  TEXT NOT NULL REFERENCES user (id)       ON DELETE CASCADE,
    group_id TEXT NOT NULL REFERENCES user_group (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, group_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX user_extra_group_by_group ON user_extra_group (group_id);

-- +goose Down
DROP TABLE user_extra_group;
