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
// Password or PendingPassword from an API response.
type NodeServerAccess struct {
	NodeID, NodeName, SSHHost, SSHUser, HostFingerprint string
	SSHPort                                             uint16
	Password, PendingPassword                           []byte
	ConfiguredAt                                        time.Time
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
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO node_provision_job (
		id, node_id, name, address, country_code, location, provider, ssh_host, ssh_port,
		host_fingerprint, secret, state, phase, error_code, created_by, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?)`,
		job.ID, job.NodeID, job.Name, job.Address, job.CountryCode, job.Location, job.Provider,
		job.SSHHost, job.SSHPort, job.HostFingerprint, job.Secret, "queued", "queued", job.CreatedBy,
		unix(job.CreatedAt), unix(job.UpdatedAt))
	if err != nil {
		if fleetIsUnique(err) {
			return ErrConflict
		}
		return err
	}
	if eventCode != "" {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO node_provision_event (job_id, phase, code, created_at) VALUES (?, 'queued', ?, ?)`,
			job.ID, eventCode, unix(job.CreatedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// NodeProvisionJob returns one job without unsealing its secret.
func (s *Store) NodeProvisionJob(ctx context.Context, id string) (NodeProvisionJob, error) {
	return scanNodeProvisionJob(s.R.QueryRowContext(ctx,
		`SELECT `+nodeProvisionJobCols+` FROM node_provision_job WHERE id = ?`, id))
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
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM node_provision_job WHERE state = 'running'`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE node_provision_job SET state = 'queued', phase = 'queued', updated_at = ? WHERE id = ? AND state = 'running'`, unix(now), id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO node_provision_event (job_id, phase, code, created_at) VALUES (?, 'queued', 'resumed_after_restart', ?)`, id, unix(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ClaimNodeProvisionJob atomically claims the oldest queued job for the single panel worker.
func (s *Store) ClaimNodeProvisionJob(ctx context.Context, now time.Time) (NodeProvisionJob, bool, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return NodeProvisionJob{}, false, err
	}
	defer tx.Rollback()
	job, err := scanNodeProvisionJob(tx.QueryRowContext(ctx,
		`SELECT `+nodeProvisionJobCols+` FROM node_provision_job WHERE state = 'queued' ORDER BY created_at, id LIMIT 1`))
	if errors.Is(err, ErrNotFound) {
		return NodeProvisionJob{}, false, nil
	}
	if err != nil {
		return NodeProvisionJob{}, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE node_provision_job SET state = 'running', phase = 'connecting', error_code = '', updated_at = ? WHERE id = ? AND state = 'queued'`, unix(now), job.ID)
	if err != nil {
		return NodeProvisionJob{}, false, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return NodeProvisionJob{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO node_provision_event (job_id, phase, code, created_at) VALUES (?, 'connecting', 'started', ?)`, job.ID, unix(now)); err != nil {
		return NodeProvisionJob{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return NodeProvisionJob{}, false, err
	}
	job.State, job.Phase, job.ErrorCode, job.UpdatedAt = "running", "connecting", "", now.UTC()
	return job, true, nil
}

// UpdateNodeProvisionJob changes a job without adding a journal entry.
func (s *Store) UpdateNodeProvisionJob(ctx context.Context, id, state, phase, errorCode string, secret []byte, now time.Time) error {
	return s.updateNodeProvisionJob(ctx, id, state, phase, errorCode, secret, "", now)
}

// UpdateNodeProvisionJobWithEvent changes the job and appends its journal event atomically.
func (s *Store) UpdateNodeProvisionJobWithEvent(ctx context.Context, id, state, phase, errorCode string, secret []byte, eventCode string, now time.Time) error {
	return s.updateNodeProvisionJob(ctx, id, state, phase, errorCode, secret, eventCode, now)
}

func (s *Store) updateNodeProvisionJob(ctx context.Context, id, state, phase, errorCode string, secret []byte, eventCode string, now time.Time) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE node_provision_job
		SET state = ?, phase = ?, error_code = ?, secret = ?, updated_at = ? WHERE id = ?`,
		state, phase, errorCode, secret, unix(now), id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrNotFound
	}
	if eventCode != "" {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO node_provision_event (job_id, phase, code, created_at) VALUES (?, ?, ?, ?)`,
			id, phase, eventCode, unix(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// CompleteNodeProvisionJob clears the temporary job secret and retains the verified SSH credential
// in the encrypted access table in the same transaction as the completion event.
func (s *Store) CompleteNodeProvisionJob(ctx context.Context, id string, access NodeServerAccess, now time.Time) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE node_provision_job
		SET state = 'completed', phase = 'completed', error_code = '', secret = X'', updated_at = ?
		WHERE id = ? AND state = 'running'`, unix(now), id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrNotFound
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO node_server_access (
		node_id, node_name, ssh_host, ssh_port, ssh_username, host_fingerprint, password, pending_password, configured_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)
	ON CONFLICT(node_id) DO UPDATE SET node_name=excluded.node_name, ssh_host=excluded.ssh_host,
		ssh_port=excluded.ssh_port, ssh_username=excluded.ssh_username,
		host_fingerprint=excluded.host_fingerprint, password=excluded.password,
		pending_password=NULL, configured_at=excluded.configured_at`,
		access.NodeID, access.NodeName, access.SSHHost, access.SSHPort, access.SSHUser,
		access.HostFingerprint, access.Password, unix(now))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO node_provision_event (job_id, phase, code, created_at) VALUES (?, 'completed', 'agent_connected', ?)`, id, unix(now)); err != nil {
		return err
	}
	return tx.Commit()
}

const nodeServerAccessCols = `node_id, node_name, ssh_host, ssh_port, ssh_username, host_fingerprint,
	password, pending_password, configured_at`

func scanNodeServerAccess(row rowScanner) (NodeServerAccess, error) {
	var out NodeServerAccess
	var port int
	var configured int64
	err := row.Scan(&out.NodeID, &out.NodeName, &out.SSHHost, &port, &out.SSHUser, &out.HostFingerprint,
		&out.Password, &out.PendingPassword, &configured)
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
	return scanNodeServerAccess(s.R.QueryRowContext(ctx,
		`SELECT `+nodeServerAccessCols+` FROM node_server_access WHERE node_id = ?`, nodeID))
}

// NodeServerAccesses lists access metadata in stable name order.
func (s *Store) NodeServerAccesses(ctx context.Context) ([]NodeServerAccess, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+nodeServerAccessCols+` FROM node_server_access ORDER BY node_name, node_id`)
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

// SetPendingNodeServerPassword journals an encrypted replacement before it is sent to the server.
func (s *Store) SetPendingNodeServerPassword(ctx context.Context, nodeID string, encrypted []byte, now time.Time) error {
	result, err := s.W.ExecContext(ctx, `UPDATE node_server_access SET pending_password = ?, configured_at = ? WHERE node_id = ?`, encrypted, unix(now), nodeID)
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

// CommitPendingNodeServerPassword promotes the verified node-bound ciphertext and removes its recovery copy.
func (s *Store) CommitPendingNodeServerPassword(ctx context.Context, nodeID string, encryptedCurrent []byte, now time.Time) error {
	result, err := s.W.ExecContext(ctx, `UPDATE node_server_access SET password = ?, pending_password = NULL, configured_at = ?
		WHERE node_id = ? AND pending_password IS NOT NULL`, encryptedCurrent, unix(now), nodeID)
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
