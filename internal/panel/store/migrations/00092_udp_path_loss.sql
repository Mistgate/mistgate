-- The node verdict is the result after comparing two senders. Keep each sample and path attribution for the latest run.
-- A path-loss row also demotes its sender for a day so one weak path cannot dominate future checks.

-- +goose Up
ALTER TABLE node_port_check ADD COLUMN sender_verdict TEXT NOT NULL DEFAULT '';
ALTER TABLE node_port_check ADD COLUMN confirm_sender TEXT NOT NULL DEFAULT '';
ALTER TABLE node_port_check ADD COLUMN confirm_sent INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_port_check ADD COLUMN confirm_got INTEGER NOT NULL DEFAULT 0;
ALTER TABLE node_port_check ADD COLUMN confirm_verdict TEXT NOT NULL DEFAULT '';
ALTER TABLE node_port_check ADD COLUMN blame TEXT NOT NULL DEFAULT '' CHECK (blame IN ('', 'node', 'path'));

CREATE TABLE udp_path_loss (
    sender_id       TEXT    NOT NULL REFERENCES node(id) ON DELETE CASCADE,
    target_id       TEXT    NOT NULL REFERENCES node(id) ON DELETE CASCADE,
    ports           TEXT    NOT NULL,
    sent            INTEGER NOT NULL,
    got             INTEGER NOT NULL,
    other_sender_id TEXT    NOT NULL REFERENCES node(id) ON DELETE CASCADE,
    other_got       INTEGER NOT NULL,
    checked_at      INTEGER NOT NULL,
    PRIMARY KEY (sender_id, target_id)
);
CREATE INDEX udp_path_loss_checked_at ON udp_path_loss(checked_at);

-- +goose Down
DROP INDEX udp_path_loss_checked_at;
DROP TABLE udp_path_loss;
ALTER TABLE node_port_check DROP COLUMN blame;
ALTER TABLE node_port_check DROP COLUMN confirm_verdict;
ALTER TABLE node_port_check DROP COLUMN confirm_got;
ALTER TABLE node_port_check DROP COLUMN confirm_sent;
ALTER TABLE node_port_check DROP COLUMN confirm_sender;
ALTER TABLE node_port_check DROP COLUMN sender_verdict;
