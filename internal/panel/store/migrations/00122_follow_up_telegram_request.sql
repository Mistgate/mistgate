-- Follow-up notification requests now come from the person's message rather than a model argument.
-- Older pending rows cannot prove that the person asked for a Telegram notification, so clear those flags once.

-- +goose Up
UPDATE assistant_follow_up SET notify_telegram = 0 WHERE notify_telegram <> 0;

-- +goose Down
-- This cleanup cannot be reversed because older rows did not record whether the person asked for the message.
SELECT 1;
