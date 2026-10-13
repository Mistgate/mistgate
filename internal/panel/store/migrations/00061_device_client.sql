-- device.client is the app that last fetched the subscription for the device: the first token of its User-Agent
-- ("Happ/3.4.2", "mihomo/1.19.31"). With a device per HWID the admin needs it next to the platform and the model to
-- tell two phones apart, and for the shared place it names the app that came last.

-- +goose Up
ALTER TABLE device ADD COLUMN client TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE device DROP COLUMN client;
