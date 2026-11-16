package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// OwnProbe is an owner-managed measurement point. TokenHash and SecretHash are always SHA-256 digests; the source address
// used to find ASN is intentionally not part of this row.
type OwnProbe struct {
	ID, Name, City, Kind              string
	TokenHash, SecretHash             []byte
	TokenExpires                      time.Time
	State                             string
	ASN                               uint32
	Operator, PinnedOperator, Version string
	LastSeen, LastResult              time.Time
	ChecksDay                         int
	ChecksDayAt                       time.Time
	JobID, JobJSON                    string
	JobDue                            time.Time
	CreatedAt                         time.Time
	CreatedBy                         string
}

const ownProbeColumns = `id, name, city, kind, token_hash, token_expires, secret_hash, state, asn, operator,
	pinned_operator, version, last_seen, last_result, checks_day, checks_day_at, job_id, job_json, job_due, created_at, created_by`

func scanOwnProbe(row interface{ Scan(...any) error }) (OwnProbe, error) {
	var p OwnProbe
	var tokenExpires, asn, lastSeen, lastResult, checksDayAt, jobDue, createdAt int64
	err := row.Scan(&p.ID, &p.Name, &p.City, &p.Kind, &p.TokenHash, &tokenExpires, &p.SecretHash, &p.State, &asn,
		&p.Operator, &p.PinnedOperator, &p.Version, &lastSeen, &lastResult, &p.ChecksDay, &checksDayAt,
		&p.JobID, &p.JobJSON, &jobDue, &createdAt, &p.CreatedBy)
	if err != nil {
		return OwnProbe{}, err
	}
	p.ASN = uint32(max(asn, 0))
	p.TokenExpires, p.LastSeen, p.LastResult = probeTime(tokenExpires), probeTime(lastSeen), probeTime(lastResult)
	p.ChecksDayAt, p.JobDue, p.CreatedAt = probeTime(checksDayAt), probeTime(jobDue), probeTime(createdAt)
	return p, nil
}

func probeTime(unixAt int64) time.Time {
	if unixAt == 0 {
		return time.Time{}
	}
	return fromUnix(unixAt)
}

// CreateOwnProbe creates a pending probe with the supplied one-time token digest.
func (s *Store) CreateOwnProbe(ctx context.Context, p OwnProbe) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO own_probe
		(id, name, city, kind, token_hash, token_expires, state, job_id, job_json, job_due, created_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', '', '{}', ?, ?, ?)`,
		p.ID, p.Name, p.City, p.Kind, p.TokenHash, unix(p.TokenExpires), unix(p.CreatedAt), unix(p.CreatedAt), p.CreatedBy)
	return err
}

// OwnProbes lists the rows without exposing any token or secret plaintext.
func (s *Store) OwnProbes(ctx context.Context) ([]OwnProbe, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+ownProbeColumns+` FROM own_probe ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OwnProbe
	for rows.Next() {
		p, err := scanOwnProbe(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// OwnProbe reads one row by id.
func (s *Store) OwnProbe(ctx context.Context, id string) (OwnProbe, error) {
	p, err := scanOwnProbe(s.R.QueryRowContext(ctx, `SELECT `+ownProbeColumns+` FROM own_probe WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return OwnProbe{}, ErrNotFound
	}
	return p, err
}

// EnrollOwnProbe atomically consumes a live one-use token. Only the token digest is accepted and the returned row contains
// the generated secret digest, never the secret itself.
func (s *Store) EnrollOwnProbe(ctx context.Context, tokenHash, secretHash []byte, asn uint32, operator, pin, version string, now time.Time) (OwnProbe, error) {
	p, err := scanOwnProbe(s.W.QueryRowContext(ctx, `UPDATE own_probe SET token_hash = NULL, token_expires = 0, secret_hash = ?, state = 'active',
		asn = ?, operator = ?, pinned_operator = ?, version = ?, last_seen = ?
		WHERE token_hash = ? AND token_expires >= ? AND state = 'pending' RETURNING `+ownProbeColumns,
		secretHash, asn, operator, pin, version, unix(now), tokenHash, unix(now)))
	if errors.Is(err, sql.ErrNoRows) {
		return OwnProbe{}, ErrNotFound
	}
	return p, err
}

// OwnProbeBySecretHash authenticates a probe without reading or retaining its source address.
func (s *Store) OwnProbeBySecretHash(ctx context.Context, secretHash []byte) (OwnProbe, error) {
	p, err := scanOwnProbe(s.R.QueryRowContext(ctx, `SELECT `+ownProbeColumns+` FROM own_probe
		WHERE secret_hash = ? AND state IN ('active', 'network_changed')`, secretHash))
	if errors.Is(err, sql.ErrNoRows) {
		return OwnProbe{}, ErrNotFound
	}
	return p, err
}

// ObserveOwnProbeNetwork records only the derived ASN and operator, and the version the probe reported (empty keeps the last one).
// The first request from an operator other than the pinned one moves an active probe to network_changed and clears its job; a
// probe in that state stays there, whatever network it comes from next, until the owner confirms (ConfirmOwnProbeNetwork).
func (s *Store) ObserveOwnProbeNetwork(ctx context.Context, id string, secretHash []byte, asn uint32, operator, version string, now time.Time) (OwnProbe, error) {
	res, err := s.W.ExecContext(ctx, `UPDATE own_probe SET asn = ?1, operator = ?2, last_seen = ?3,
		version = CASE WHEN ?4 <> '' THEN ?4 ELSE version END,
		state = CASE WHEN state = 'active' AND pinned_operator <> ?2 THEN 'network_changed' ELSE state END,
		job_id = CASE WHEN state = 'active' AND pinned_operator <> ?2 THEN '' ELSE job_id END,
		job_json = CASE WHEN state = 'active' AND pinned_operator <> ?2 THEN '{}' ELSE job_json END,
		job_due = CASE WHEN state = 'active' AND pinned_operator <> ?2 THEN 0 ELSE job_due END
		WHERE id = ?5 AND secret_hash = ?6 AND state IN ('active', 'network_changed')`,
		asn, operator, unix(now), version, id, secretHash)
	if err != nil {
		return OwnProbe{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return OwnProbe{}, err
	}
	if n == 0 {
		return OwnProbe{}, ErrNotFound
	}
	return s.OwnProbe(ctx, id)
}

// ConfirmOwnProbeNetwork pins the most recently observed ASN and restores the probe to active.
func (s *Store) ConfirmOwnProbeNetwork(ctx context.Context, id, operator string) error {
	if operator == "" {
		operator = "AS0"
	}
	res, err := s.W.ExecContext(ctx, `UPDATE own_probe SET pinned_operator = ?, operator = ?,
		state = 'active', job_id = '', job_json = '{}', job_due = 0 WHERE id = ? AND state = 'network_changed'`, operator, operator, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// RevokeOwnProbe invalidates both credentials and clears any job immediately.
func (s *Store) RevokeOwnProbe(ctx context.Context, id string) error {
	res, err := s.W.ExecContext(ctx, `UPDATE own_probe SET state = 'revoked', token_hash = NULL, token_expires = 0,
		secret_hash = NULL, job_id = '', job_json = '{}', job_due = 0 WHERE id = ? AND state <> 'revoked'`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// SetOwnProbeJob replaces the outstanding target list. Addresses are trusted only because this list was built by the panel.
func (s *Store) SetOwnProbeJob(ctx context.Context, id, jobID, jobJSON string, due time.Time) error {
	res, err := s.W.ExecContext(ctx, `UPDATE own_probe SET job_id = ?, job_json = ?, job_due = ? WHERE id = ? AND state = 'active'`,
		jobID, jobJSON, unix(due), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// CompleteOwnProbeJob consumes one matching outstanding job and updates its counters. A replay or an older job cannot change
// the row. checks_day_at is the UTC day bucket's midnight.
func (s *Store) CompleteOwnProbeJob(ctx context.Context, id, jobID, version string, now time.Time, interval time.Duration, count int) error {
	day := now.UTC().Truncate(24 * time.Hour)
	due := now.Add(interval)
	res, err := s.W.ExecContext(ctx, `UPDATE own_probe SET last_result = ?, version = ?, checks_day =
		CASE WHEN checks_day_at < ? THEN MIN(600, ?) ELSE MIN(600, checks_day + ?) END,
		checks_day_at = ?, job_id = '', job_json = '{}', job_due = ?
		WHERE id = ? AND state = 'active' AND job_id = ?`,
		unix(now), version, unix(day), count, count, unix(day), unix(due), id, jobID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// DueOwnProbe marks an active probe for a check at the next sync.
func (s *Store) DueOwnProbe(ctx context.Context, id string, now time.Time) error {
	res, err := s.W.ExecContext(ctx, `UPDATE own_probe SET job_due = ?, job_id = '', job_json = '{}' WHERE id = ? AND state = 'active'`, unix(now), id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

// ClearOwnProbeJobs removes queued work while preserving enrollment and counters.
func (s *Store) ClearOwnProbeJobs(ctx context.Context) error {
	_, err := s.W.ExecContext(ctx, `UPDATE own_probe SET job_id = '', job_json = '{}', job_due = 0 WHERE state = 'active'`)
	return err
}
