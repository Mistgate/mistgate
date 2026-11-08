-- +goose Up
ALTER TABLE assistant_provider ADD COLUMN custom_name TEXT NOT NULL DEFAULT '';
ALTER TABLE assistant_provider ADD COLUMN custom_url TEXT NOT NULL DEFAULT '';
ALTER TABLE assistant_provider ADD COLUMN allow_local INTEGER NOT NULL DEFAULT 0 CHECK (allow_local IN (0, 1));
ALTER TABLE assistant_provider ADD COLUMN input_usd_per_mtok REAL NOT NULL DEFAULT 0 CHECK (input_usd_per_mtok >= 0);
ALTER TABLE assistant_provider ADD COLUMN output_usd_per_mtok REAL NOT NULL DEFAULT 0 CHECK (output_usd_per_mtok >= 0);

-- +goose Down
DELETE FROM assistant_provider WHERE provider BETWEEN 5 AND 9;
UPDATE assistant_settings SET enabled=0, provider=0 WHERE provider BETWEEN 5 AND 9;
UPDATE assistant_settings SET fallback_provider=0 WHERE fallback_provider BETWEEN 5 AND 9;
ALTER TABLE assistant_provider DROP COLUMN output_usd_per_mtok;
ALTER TABLE assistant_provider DROP COLUMN input_usd_per_mtok;
ALTER TABLE assistant_provider DROP COLUMN allow_local;
ALTER TABLE assistant_provider DROP COLUMN custom_url;
ALTER TABLE assistant_provider DROP COLUMN custom_name;
