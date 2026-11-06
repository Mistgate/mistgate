-- +goose Up

ALTER TABLE assistant_settings ADD COLUMN panel_notes_enc BLOB NOT NULL DEFAULT X'';

CREATE TABLE assistant_budget_reservation (
    id            TEXT PRIMARY KEY,
    month         TEXT NOT NULL,
    admin_id      TEXT NOT NULL,
    provider      INTEGER NOT NULL,
    model         TEXT NOT NULL,
    estimate_usd  REAL NOT NULL CHECK (estimate_usd >= 0),
    state         TEXT NOT NULL DEFAULT 'reserved' CHECK (state IN ('reserved', 'settled')),
    created_at    INTEGER NOT NULL,
    settled_at    INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX assistant_budget_reservation_month ON assistant_budget_reservation(provider, month, state);

-- +goose Down

DROP TABLE assistant_budget_reservation;
ALTER TABLE assistant_settings DROP COLUMN panel_notes_enc;
