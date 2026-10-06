-- batch_guard never holds a row. A batch that must stop when a condition fails inserts here only then, and the
-- CHECK aborts the whole batch: D1 has no interactive transaction to roll back by hand.

-- +goose Up
CREATE TABLE batch_guard (
    failed INTEGER NOT NULL,
    CONSTRAINT batch_guard CHECK (failed = 0)
);

-- +goose Down
DROP TABLE batch_guard;
