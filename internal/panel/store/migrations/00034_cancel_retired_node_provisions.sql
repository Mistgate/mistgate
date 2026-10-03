-- Retiring a node must also release any in-flight SSH job name reservation.
-- The old worker may have changed the host already, so keep an explicit warning.
-- +goose Up
INSERT INTO node_provision_event (job_id, phase, code, created_at)
SELECT j.id, 'cancelled', 'remote_outcome_unknown', COALESCE(n.retired_at, j.updated_at)
FROM node_provision_job AS j
JOIN node AS n ON n.id = j.node_id
WHERE n.state = 'retired'
  AND j.state IN ('queued', 'running', 'cancel_requested');

UPDATE node_provision_job
SET state = 'cancelled',
    phase = 'cancelled',
    error_code = 'remote_outcome_unknown',
    secret = X'',
    updated_at = COALESCE(
        (SELECT retired_at FROM node WHERE node.id = node_provision_job.node_id),
        updated_at
    )
WHERE state IN ('queued', 'running', 'cancel_requested')
  AND EXISTS (
      SELECT 1 FROM node
      WHERE node.id = node_provision_job.node_id
        AND node.state = 'retired'
  );

-- +goose Down
-- This cleanup is irreversible: cancelled remote work and cleared credentials cannot be restored.
SELECT 1;
