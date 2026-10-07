package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// NodeProvisionJob is durable metadata for one owner-requested SSH installation.
// Secret is ciphertext produced by vault.Vault and is never returned by RPC methods.
type NodeProvisionJob struct {
	ID, NodeID, Name, Address, CountryCode, Location, Provider string
	SSHHost, HostFingerprint                                   string
	SSHPort                                                    uint16
	Secret                                                     []byte
	State, Phase, ErrorCode, CreatedBy                         string
	CreatedAt, UpdatedAt                                       time.Time
}

// NodeProvisionEvent is a redacted, stable-code progress event.
type NodeProvisionEvent struct {
	ID          int64
	Phase, Code string
	CreatedAt   time.Time
}

// NodeServerAccess contains public connection metadata and vault ciphertext. Callers must never return
// Password or PendingPassword from an API response. NodeName is the node's current name and NodeRetired its state
// (both read from node); PasswordGenerated says the panel generated the saved password.
type NodeServerAccess struct {
	NodeID, NodeName, SSHHost, SSHUser, HostFingerprint string
	SSHPort                                             uint16
	Password, PendingPassword                           []byte
	ConfiguredAt                                        time.Time
	PasswordGenerated, NodeRetired                      bool
}

const nodeProvisionJobCols = `id, node_id, name, address, country_code, location, provider,
	ssh_host, ssh_port, host_fingerprint, secret, state, phase, error_code, created_by, created_at, updated_at`

func scanNodeProvisionJob(row rowScanner) (NodeProvisionJob, error) {
	var job NodeProvisionJob
	var port int
	var created, updated int64
	err := row.Scan(&job.ID, &job.NodeID, &job.Name, &job.Address, &job.CountryCode, &job.Location, &job.Provider,
		&job.SSHHost, &port, &job.HostFingerprint, &job.Secret, &job.State, &job.Phase, &job.ErrorCode,
		&job.CreatedBy, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeProvisionJob{}, ErrNotFound
	}
	if err != nil {
		return NodeProvisionJob{}, err
	}
	job.SSHPort = uint16(port)
	job.CreatedAt, job.UpdatedAt = fromUnix(created), fromUnix(updated)
	return job, nil
}

// CreateNodeProvisionJob persists an owner-confirmed SSH installation request.
func (s *Store) CreateNodeProvisionJob(ctx context.Context, job NodeProvisionJob) error {
	return s.createNodeProvisionJob(ctx, job, "created")
}

func (s *Store) createNodeProvisionJob(ctx context.Context, job NodeProvisionJob, eventCode string) error {
	stmts := []Stmt{{Query: `INSERT INTO node_provision_job (
		id, node_id, name, address, country_code, location, provider, ssh_host, ssh_port,
		host_fingerprint, secret, state, phase, error_code, created_by, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?)`,
		Args: []any{job.ID, job.NodeID, job.Name, job.Address, job.CountryCode, job.Location, job.Provider,
			job.SSHHost, int64(job.SSHPort), job.HostFingerprint, job.Secret, "queued", "queued", job.CreatedBy,
			unix(job.CreatedAt), unix(job.UpdatedAt)}}}
	if eventCode != "" {
		stmts = append(stmts, Stmt{Query: `INSERT INTO node_provision_event (job_id, phase, code, created_at) VALUES (?, 'queued', ?, ?)`,
			Args: []any{job.ID, eventCode, unix(job.CreatedAt)}})
	}
	_, err := s.batch(ctx, stmts...)
	if fleetIsUnique(err) {
		return ErrConflict
	}
	return err
}

// NodeProvisionJob returns one job without unsealing its secret.
func (s *Store) NodeProvisionJob(ctx context.Context, id string) (NodeProvisionJob, error) {
	return scanNodeProvisionJob(s.R.QueryRowContext(ctx,
		`SELECT `+nodeProvisionJobCols+` FROM node_provision_job WHERE id = ?`, id))
}

// NodeProvisionJobState reads only the durable worker state, without loading the encrypted credential blob.
func (s *Store) NodeProvisionJobState(ctx context.Context, id string) (string, error) {
	var state string
	err := s.R.QueryRowContext(ctx, `SELECT state FROM node_provision_job WHERE id = ?`, id).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return state, err
}

// NodeProvisionJobs returns the most recently changed jobs, newest first.
func (s *Store) NodeProvisionJobs(ctx context.Context, limit int) ([]NodeProvisionJob, error) {
	if limit < 1 || limit > 100 {
		limit = 100
	}
	rows, err := s.R.QueryContext(ctx,
		`SELECT `+nodeProvisionJobCols+` FROM node_provision_job ORDER BY updated_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]NodeProvisionJob, 0)
	for rows.Next() {
		job, err := scanNodeProvisionJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

// RequeueNodeProvisionJobs restores interrupted work after a panel restart.
func (s *Store) RequeueNodeProvisionJobs(ctx context.Context, now time.Time) error {
	_, err := s.batch(ctx,
		Stmt{Query: `INSERT INTO node_provision_event (job_id, phase, code, created_at)
			SELECT id, 'queued', 'resumed_after_restart', ? FROM node_provision_job WHERE state = 'running'`, Args: []any{unix(now)}},
		Stmt{Query: `UPDATE node_provision_job SET state = 'queued', phase = 'queued', updated_at = ? WHERE state = 'running'`, Args: []any{unix(now)}},
		Stmt{Query: `INSERT INTO node_provision_event (job_id, phase, code, created_at)
			SELECT id, 'cancelled', 'remote_outcome_unknown', ? FROM node_provision_job WHERE state = 'cancel_requested'`, Args: []any{unix(now)}},
		Stmt{Query: `UPDATE node_provision_job
			SET state = 'cancelled', phase = 'cancelled', error_code = 'remote_outcome_unknown', secret = X'', updated_at = ?
			WHERE state = 'cancel_requested'`, Args: []any{unix(now)}},
	)
	return err
}

// RequestCancelNodeProvisionJob atomically stops queued work or requests cancellation
// of the active worker. The returned state is either cancelled or cancel_requested.
func (s *Store) RequestCancelNodeProvisionJob(ctx context.Context, id string, now time.Time) (state string, changed bool, err error) {
	results, err := s.batch(ctx,
		Stmt{Query: `SELECT state FROM node_provision_job WHERE id = ?`, Args: []any{id}, Returning: true},
		Stmt{Query: `INSERT INTO node_provision_event (job_id, phase, code, created_at)
			SELECT id, CASE WHEN state = 'queued' THEN 'cancelled' ELSE 'cancelling' END,
				CASE WHEN state = 'queued' THEN 'cancelled_before_start' ELSE 'cancel_requested' END, ?
			FROM node_provision_job WHERE id = ? AND state IN ('queued', 'running')`, Args: []any{unix(now), id}},
		Stmt{Query: `UPDATE node_provision_job SET
			state = CASE WHEN state = 'queued' THEN 'cancelled' ELSE 'cancel_requested' END,
			phase = CASE WHEN state = 'queued' THEN 'cancelled' ELSE 'cancelling' END,
			error_code = CASE WHEN state = 'queued' THEN 'cancelled_before_start' ELSE 'cancel_requested' END,
			secret = CASE WHEN state = 'queued' THEN X'' ELSE secret END, updated_at = ?
			WHERE id = ? AND state IN ('queued', 'running') RETURNING state`,
			Args: []any{unix(now), id}, Returning: true},
	)
	if err != nil {
		return "", false, err
	}
	if len(results[0].Rows) == 0 {
		return "", false, ErrNotFound
	}
	current, _ := results[0].Rows[0][0].(string)
	switch current {
	case "queued", "running":
		if len(results[2].Rows) != 1 {
			return "", false, ErrConflict
		}
		state, _ = results[2].Rows[0][0].(string)
		return state, true, nil
	case "cancel_requested", "cancelled":
		return current, false, nil
	default:
		return "", false, ErrConflict
	}
}

// FinishCancelledNodeProvisionJob clears credentials after an active worker stops.
// The remote host may already have received some installation commands.
func (s *Store) FinishCancelledNodeProvisionJob(ctx context.Context, id string, now time.Time) (bool, error) {
	results, err := s.batch(ctx,
		Stmt{Query: `INSERT INTO node_provision_event (job_id, phase, code, created_at)
			SELECT id, 'cancelled', 'remote_outcome_unknown', ? FROM node_provision_job
			WHERE id = ? AND state = 'cancel_requested'`, Args: []any{unix(now), id}},
		Stmt{Query: `UPDATE node_provision_job
			SET state = 'cancelled', phase = 'cancelled', error_code = 'remote_outcome_unknown', secret = X'', updated_at = ?
			WHERE id = ? AND state = 'cancel_requested'`, Args: []any{unix(now), id}},
	)
	if err != nil {
		return false, err
	}
	return results[1].RowsAffected == 1, nil
}

// ClaimNodeProvisionJob atomically claims the oldest queued job for the single panel worker.
func (s *Store) ClaimNodeProvisionJob(ctx context.Context, now time.Time) (NodeProvisionJob, bool, error) {
	queued := `SELECT id FROM node_provision_job WHERE state = 'queued' ORDER BY created_at, id LIMIT 1`
	results, err := s.batch(ctx,
		Stmt{Query: `INSERT INTO node_provision_event (job_id, phase, code, created_at)
			SELECT id, 'connecting', 'started', ? FROM node_provision_job
			WHERE id = (` + queued + `) AND state = 'queued'`, Args: []any{unix(now)}},
		Stmt{Query: `UPDATE node_provision_job SET state = 'running', phase = 'connecting', error_code = '', updated_at = ?
			WHERE id = (` + queued + `) AND state = 'queued' RETURNING ` + nodeProvisionJobCols,
			Args: []any{unix(now)}, Returning: true},
	)
	if err != nil {
		return NodeProvisionJob{}, false, err
	}
	if len(results[1].Rows) == 0 {
		return NodeProvisionJob{}, false, nil
	}
	job, err := scanNodeProvisionJob(batchRow(results[1].Rows[0]))
	if err != nil {
		return NodeProvisionJob{}, false, err
	}
	return job, true, nil
}

// UpdateRunningNodeProvisionJob only changes a job that is still running, so it
// cannot undo a concurrent owner cancellation or overwrite a terminal result.
func (s *Store) UpdateRunningNodeProvisionJob(ctx context.Context, id, state, phase, errorCode string, secret []byte, now time.Time) error {
	return s.updateNodeProvisionJobFromState(ctx, id, "running", state, phase, errorCode, secret, "", now)
}

// UpdateRunningNodeProvisionJobWithEvent updates an active job and its redacted
// journal atomically, without reverting a cancel_requested state.
func (s *Store) UpdateRunningNodeProvisionJobWithEvent(ctx context.Context, id, state, phase, errorCode string, secret []byte, eventCode string, now time.Time) error {
	return s.updateNodeProvisionJobFromState(ctx, id, "running", state, phase, errorCode, secret, eventCode, now)
}

// RetryNodeProvisionJob requeues a failed or cancelled job with a freshly sealed credential. It changes the job only
// while it is still in the state the caller read (from): a double submit cannot requeue a job a worker has already
// claimed (ErrConflict). ErrNameTaken: a live node or another active install took the name meanwhile.
func (s *Store) RetryNodeProvisionJob(ctx context.Context, id, from string, secret []byte, now time.Time) error {
	if from != "failed" && from != "cancelled" {
		return ErrConflict
	}
	err := s.updateNodeProvisionJobFromState(ctx, id, from, "queued", "queued", "", secret, "retry_requested", now)
	if fleetIsUnique(err) {
		return ErrNameTaken
	}
	return err
}

func (s *Store) updateNodeProvisionJobFromState(ctx context.Context, id, expected, state, phase, errorCode string, secret []byte, eventCode string, now time.Time) error {
	stmts := make([]Stmt, 0, 2)
	if eventCode != "" {
		stmts = append(stmts, Stmt{Query: `INSERT INTO node_provision_event (job_id, phase, code, created_at)
			SELECT id, ?, ?, ? FROM node_provision_job WHERE id = ? AND state = ?`,
			Args: []any{phase, eventCode, unix(now), id, expected}})
	}
	stmts = append(stmts, Stmt{Query: `UPDATE node_provision_job
		SET state = ?, phase = ?, error_code = ?, secret = ?, updated_at = ? WHERE id = ? AND state = ?`,
		Args: []any{state, phase, errorCode, secret, unix(now), id, expected}})
	results, err := s.batch(ctx, stmts...)
	if err != nil {
		return err
	}
	if results[len(results)-1].RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

// CompleteNodeProvisionJob clears the temporary job secret and retains the verified SSH credential
// in the encrypted access table in the same transaction as the completion event.
func (s *Store) CompleteNodeProvisionJob(ctx context.Context, id string, access NodeServerAccess, now time.Time) error {
	_, err := s.batch(ctx,
		guard(`EXISTS (SELECT 1 FROM node_provision_job WHERE id = ? AND state = 'running')`, id),
		Stmt{Query: `UPDATE node_provision_job
			SET state = 'completed', phase = 'completed', error_code = '', secret = X'', updated_at = ? WHERE id = ?`,
			Args: []any{unix(now), id}},
		// node_name is kept only because the column is NOT NULL: readers take the node's current name from node.
		Stmt{Query: `INSERT INTO node_server_access (
		node_id, node_name, ssh_host, ssh_port, ssh_username, host_fingerprint, password, pending_password, configured_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)
	ON CONFLICT(node_id) DO UPDATE SET node_name=excluded.node_name, ssh_host=excluded.ssh_host,
		ssh_port=excluded.ssh_port, ssh_username=excluded.ssh_username,
		host_fingerprint=excluded.host_fingerprint, password=excluded.password,
		pending_password=NULL, configured_at=excluded.configured_at, password_generated=0`,
			Args: []any{access.NodeID, access.NodeName, access.SSHHost, int64(access.SSHPort), access.SSHUser,
				access.HostFingerprint, access.Password, unix(now)}},
		Stmt{Query: `INSERT INTO node_provision_event (job_id, phase, code, created_at)
			VALUES (?, 'completed', 'agent_connected', ?)`, Args: []any{id, unix(now)}},
	)
	if errors.Is(err, errGuard) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return nil
}

// nodeServerAccessFrom joins the node: its name changes with a rename, and a retired node's access is marked.
const nodeServerAccessFrom = `SELECT a.node_id, n.name, a.ssh_host, a.ssh_port, a.ssh_username, a.host_fingerprint,
	a.password, a.pending_password, a.configured_at, a.password_generated, n.state = 'retired'
	FROM node_server_access a JOIN node n ON n.id = a.node_id `

func scanNodeServerAccess(row rowScanner) (NodeServerAccess, error) {
	var out NodeServerAccess
	var port int
	var configured int64
	err := row.Scan(&out.NodeID, &out.NodeName, &out.SSHHost, &port, &out.SSHUser, &out.HostFingerprint,
		&out.Password, &out.PendingPassword, &configured, &out.PasswordGenerated, &out.NodeRetired)
	if errors.Is(err, sql.ErrNoRows) {
		return NodeServerAccess{}, ErrNotFound
	}
	if err != nil {
		return NodeServerAccess{}, err
	}
	out.SSHPort = uint16(port)
	out.ConfiguredAt = fromUnix(configured)
	return out, nil
}

// NodeServerAccess returns one node's encrypted SSH credential and public metadata.
func (s *Store) NodeServerAccess(ctx context.Context, nodeID string) (NodeServerAccess, error) {
	return scanNodeServerAccess(s.R.QueryRowContext(ctx, nodeServerAccessFrom+`WHERE a.node_id = ?`, nodeID))
}

// NodeServerAccesses lists access metadata in stable name order, live nodes before retired ones of the same name.
func (s *Store) NodeServerAccesses(ctx context.Context) ([]NodeServerAccess, error) {
	rows, err := s.R.QueryContext(ctx, nodeServerAccessFrom+`ORDER BY n.name COLLATE NOCASE, n.state = 'retired', a.node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]NodeServerAccess, 0)
	for rows.Next() {
		v, err := scanNodeServerAccess(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SetPendingNodeServerPassword journals an encrypted replacement before it is sent to the server. A generated one marks
// the access as holding a panel-generated password at once: from here on the server may already use it.
func (s *Store) SetPendingNodeServerPassword(ctx context.Context, nodeID string, encrypted []byte, generated bool, now time.Time) error {
	result, err := s.W.ExecContext(ctx, `UPDATE node_server_access SET pending_password = ?, configured_at = ?,
		password_generated = CASE WHEN ? THEN 1 ELSE password_generated END WHERE node_id = ?`, encrypted, unix(now), generated, nodeID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrNotFound
	}
	return nil
}

// CommitPendingNodeServerPassword promotes the verified node-bound ciphertext and removes its recovery copy. ownerChosen
// says the owner typed the promoted password (it clears the generated mark); a recovered pending value leaves it.
func (s *Store) CommitPendingNodeServerPassword(ctx context.Context, nodeID string, encryptedCurrent []byte, ownerChosen bool, now time.Time) error {
	result, err := s.W.ExecContext(ctx, `UPDATE node_server_access SET password = ?, pending_password = NULL, configured_at = ?,
		password_generated = CASE WHEN ? THEN 0 ELSE password_generated END
		WHERE node_id = ? AND pending_password IS NOT NULL`, encryptedCurrent, unix(now), ownerChosen, nodeID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrNotFound
	}
	return nil
}

// ClearPendingNodeServerPassword discards a replacement that could not be applied while the old login still works.
func (s *Store) ClearPendingNodeServerPassword(ctx context.Context, nodeID string) error {
	result, err := s.W.ExecContext(ctx, `UPDATE node_server_access SET pending_password = NULL WHERE node_id = ?`, nodeID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrNotFound
	}
	return nil
}

// ForgetNodeServerAccess deletes the saved access of a retired node, its sealed password included: the owner's explicit
// decision, since retiring keeps it. ErrNotFound: no saved access; ErrConflict: the node is not retired.
func (s *Store) ForgetNodeServerAccess(ctx context.Context, nodeID string) error {
	result, err := s.W.ExecContext(ctx, `DELETE FROM node_server_access WHERE node_id = ?
		AND EXISTS (SELECT 1 FROM node WHERE id = node_server_access.node_id AND state = 'retired')`, nodeID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n == 1 {
		return nil
	}
	if _, err := s.NodeServerAccess(ctx, nodeID); err != nil {
		return err
	}
	return ErrConflict
}

// NodeProvisionEvents returns events after the given cursor and the final cursor.
func (s *Store) NodeProvisionEvents(ctx context.Context, jobID string, after int64, limit int) ([]NodeProvisionEvent, int64, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := s.R.QueryContext(ctx, `SELECT id, phase, code, created_at
		FROM node_provision_event WHERE job_id = ? AND id > ? ORDER BY id LIMIT ?`, jobID, after, limit)
	if err != nil {
		return nil, after, err
	}
	defer rows.Close()
	out := make([]NodeProvisionEvent, 0)
	next := after
	for rows.Next() {
		var event NodeProvisionEvent
		var created int64
		if err := rows.Scan(&event.ID, &event.Phase, &event.Code, &created); err != nil {
			return nil, after, err
		}
		event.CreatedAt = fromUnix(created)
		out = append(out, event)
		next = event.ID
	}
	return out, next, rows.Err()
}
