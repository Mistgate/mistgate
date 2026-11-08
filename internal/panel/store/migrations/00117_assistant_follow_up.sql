-- Follow-ups of the assistant (proto/mistgate/admin/v1/assistant.proto, FOLLOW-UPS): a one-time continuation of a
-- conversation that the model set with its local tool follow_up, run by the panel itself at its time through the same turn
-- as a chat. The VPS edition runs them, with the scheduled tasks' scheduler (internal/panel/tasks); the Cloudflare edition has
-- the tables (one migration set) but nothing writes them.
--
-- assistant_follow_up is one follow-up that has not run yet: its conversation and admin, the model's note sealed with the
-- panel vault (associated data "assistant-follow-up:<id>", like the assistant's other text), and when it is due. A follow-up
-- tied to a plan of the conversation (plan_id) waits with due_at = 0 until that plan is applied, then is due delay_s after the
-- apply; a plan that ends otherwise takes the follow-up with it. lease_id / lease_until mark the run in progress (one run of a
-- follow-up, once); attempts counts runs a stop of the panel interrupted. The row goes when its run ends: the conversation
-- keeps what came of it (the answer, or a note with the reason). No secret is ever stored: the note is scrubbed first.
--
-- assistant_conversation.unread is set when a follow-up answered (or left a note) and cleared when the admin opens the
-- conversation.

-- +goose Up
CREATE TABLE assistant_follow_up (
    id              TEXT    PRIMARY KEY,
    conversation_id TEXT    NOT NULL,
    admin_id        TEXT    NOT NULL,
    note_enc        BLOB    NOT NULL,
    plan_id         TEXT    NOT NULL DEFAULT '',
    delay_s         INTEGER NOT NULL DEFAULT 0,
    notify_telegram INTEGER NOT NULL DEFAULT 0 CHECK (notify_telegram IN (0, 1)),
    created_at      INTEGER NOT NULL,
    due_at          INTEGER NOT NULL DEFAULT 0,
    lease_id        TEXT    NOT NULL DEFAULT '',
    lease_until     INTEGER NOT NULL DEFAULT 0,
    attempts        INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX assistant_follow_up_due ON assistant_follow_up (due_at);
CREATE INDEX assistant_follow_up_conversation ON assistant_follow_up (conversation_id, due_at);
CREATE INDEX assistant_follow_up_admin ON assistant_follow_up (admin_id);

ALTER TABLE assistant_conversation ADD COLUMN unread INTEGER NOT NULL DEFAULT 0 CHECK (unread IN (0, 1));

-- +goose Down
ALTER TABLE assistant_conversation DROP COLUMN unread;
DROP INDEX assistant_follow_up_admin;
DROP INDEX assistant_follow_up_conversation;
DROP INDEX assistant_follow_up_due;
DROP TABLE assistant_follow_up;
