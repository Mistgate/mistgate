-- How many times a worker has claimed an install job. A job interrupted by a panel restart is requeued until its third
-- claim; the next interruption fails it with job_interrupted instead of looping. A retry starts the count again.
-- +goose Up
ALTER TABLE node_provision_job ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE node_provision_job DROP COLUMN attempt;
