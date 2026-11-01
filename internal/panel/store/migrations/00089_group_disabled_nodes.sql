-- Disable selected nodes for every user in a group. An empty set leaves all current and future nodes available.
-- +goose Up
CREATE TABLE user_group_disabled_node (
    group_id TEXT NOT NULL REFERENCES user_group (id) ON DELETE CASCADE,
    node_id  TEXT NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    PRIMARY KEY (group_id, node_id)
) STRICT, WITHOUT ROWID;

-- +goose Down
DROP TABLE user_group_disabled_node;
