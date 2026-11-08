-- Remember the latest accepted debug-capture chunk so a reconnect can resend it without storing the lines twice.

-- +goose Up
ALTER TABLE debug_capture ADD COLUMN last_chunk_seq INTEGER NOT NULL DEFAULT -1;

-- +goose Down
ALTER TABLE debug_capture DROP COLUMN last_chunk_seq;
