-- The public label of a node: the short text the owner chooses to show people in the Mistgate app next to a server ("Fast",
-- "Home route"). Empty = none. It is shown instead of nothing, never instead of the country and city, and it is not the node's
-- name, which people never see.

-- +goose Up
ALTER TABLE node ADD COLUMN public_label TEXT NOT NULL DEFAULT ''
    CHECK (length(public_label) <= 40);

-- +goose Down
ALTER TABLE node DROP COLUMN public_label;
