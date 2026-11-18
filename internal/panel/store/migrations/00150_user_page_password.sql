-- The page password of a person (package pagepass) is computed, never stored: from the subscription token and a small number, its
-- generation. "Change" adds one to it (a new password, the same link; the cookies of the old one stop working), generation 0 is the
-- password every person had before this migration. page_open is "the password was removed": the person's page opens on the link alone.
-- Neither column holds a password or its hash. A new link starts the generation again at 0 and leaves page_open as it was.

-- +goose Up
ALTER TABLE user ADD COLUMN page_pass_gen INTEGER NOT NULL DEFAULT 0
    CHECK (page_pass_gen >= 0);
ALTER TABLE user ADD COLUMN page_open INTEGER NOT NULL DEFAULT 0
    CHECK (page_open IN (0, 1));

-- +goose Down
ALTER TABLE user DROP COLUMN page_open;
ALTER TABLE user DROP COLUMN page_pass_gen;
