package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Errors of the enrollment path.
var (
	// ErrEnrollToken: the token is unknown, expired or already used (and not a valid replay).
	ErrEnrollToken = errors.New("store: enrollment token unknown, expired or used")
	// ErrNodeRetired: the node exists but was retired.
	ErrNodeRetired = errors.New("store: node retired")
	// ErrConflict: a unique constraint was violated (node name).
	ErrConflict = errors.New("store: conflict")
)

// enrollReplayWindow is how long after first use the same token + same CSR key returns the same
// certificate (agent.proto EnrollmentService.Enroll).
const enrollReplayWindow = 10 * time.Minute

// fleetTime converts Unix seconds to a time, 0 to the zero time.
func fleetTime(s int64) time.Time {
	if s == 0 {
		return time.Time{}
	}
	return fromUnix(s)
}

// fleetUnix converts a time to Unix seconds, the zero time to 0.
func fleetUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return unix(t)
}

// CARow is one panel_ca row.
type CARow struct {
	ID, CertPEM, Fingerprint string
	KeyEnc                   []byte // vault, AAD = ID
	NotBefore, NotAfter      time.Time
	Active                   bool
}

// CAs returns every CA, the active one first.
func (s *Store) CAs(ctx context.Context) ([]CARow, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT id, cert_pem, fingerprint_sha256, key_enc, not_before, not_after, active
		FROM panel_ca ORDER BY active DESC, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CARow
	for rows.Next() {
		var c CARow
		var nb, na int64
		if err := rows.Scan(&c.ID, &c.CertPEM, &c.Fingerprint, &c.KeyEnc, &nb, &na, &c.Active); err != nil {
			return nil, err
		}
		c.NotBefore, c.NotAfter = fromUnix(nb), fromUnix(na)
		out = append(out, c)
	}
	return out, rows.Err()
}

// InsertCA stores a new active CA. It fails if another CA is already active.
func (s *Store) InsertCA(ctx context.Context, c CARow, now time.Time) error {
	_, err := s.W.ExecContext(ctx, `
		INSERT INTO panel_ca (id, cert_pem, fingerprint_sha256, key_enc, not_before, not_after, active, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?)`,
		c.ID, c.CertPEM, c.Fingerprint, c.KeyEnc, unix(c.NotBefore), unix(c.NotAfter), unix(now))
	return err
}

// CertRow is one node_cert row (public material only).
type CertRow struct {
	Serial, NodeID, CAID, PEM string
	NotBefore, NotAfter       time.Time
	IssuedAt                  time.Time
}

// LinkCertificates returns the node certificates that mTLS would still accept at now, including a
// previous certificate inside the renewal grace period. Retired nodes have no acceptable certificates.
func (s *Store) LinkCertificates(ctx context.Context, nodeID string, now time.Time) ([]CertRow, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT c.serial, c.node_id, c.ca_id, c.pem, c.not_before, c.not_after, c.issued_at
		FROM node_cert c JOIN node n ON n.id = c.node_id
		WHERE c.node_id = ? AND n.state <> 'retired' AND c.not_before <= ? AND c.not_after > ?
		  AND (c.revoked_at IS NULL OR c.revoked_at > ?)
		ORDER BY c.issued_at DESC`, nodeID, unix(now), unix(now), unix(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CertRow
	for rows.Next() {
		var c CertRow
		var nb, na, issued int64
		if err := rows.Scan(&c.Serial, &c.NodeID, &c.CAID, &c.PEM, &nb, &na, &issued); err != nil {
			return nil, err
		}
		c.NotBefore, c.NotAfter, c.IssuedAt = fromUnix(nb), fromUnix(na), fromUnix(issued)
		out = append(out, c)
	}
	return out, rows.Err()
}

func insertCert(ctx context.Context, tx *sql.Tx, c CertRow) error {
	stmt := insertCertStmt(c)
	_, err := tx.ExecContext(ctx, stmt.Query, stmt.Args...)
	return err
}

func insertCertStmt(c CertRow) Stmt {
	return Stmt{Query: `
		INSERT INTO node_cert (serial, node_id, ca_id, pem, not_before, not_after, issued_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		Args: []any{c.Serial, c.NodeID, c.CAID, c.PEM, unix(c.NotBefore), unix(c.NotAfter), unix(c.IssuedAt)}}
}

// RenewCert records a certificate issued by Renew and makes it the node's current one. The node's other
// certificates are scheduled for revocation grace after now (a lost response must not lock the agent out,
// a stolen key must not live out its 30 days): revoked_at is set in the future and CertStatus honours it
// from then on. A certificate that is already scheduled keeps its earlier time, so renewing again with
// the old certificate does not extend it.
func (s *Store) RenewCert(ctx context.Context, c CertRow, now time.Time, grace time.Duration) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertCert(ctx, tx, c); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE node_cert SET revoked_at = ?, revoke_reason = 'renewed' WHERE node_id = ? AND serial <> ? AND revoked_at IS NULL`,
		unix(now.Add(grace)), c.NodeID, c.Serial); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE node SET cert_serial = ? WHERE id = ?`, c.Serial, c.NodeID); err != nil {
		return err
	}
	return tx.Commit()
}

// CertState is what the panel knows about an issued certificate.
type CertState struct {
	NodeID, NodeState string
	NotAfter          time.Time
	Revoked           bool // revoked at or before the asked time (a scheduled revocation is not yet one)
}

// CertStatus looks up an issued certificate by serial for the TLS handshake, per-RPC checks and the
// periodic check of a running stream. ErrNotFound if the panel never issued it.
func (s *Store) CertStatus(ctx context.Context, serial string, now time.Time) (CertState, error) {
	var c CertState
	var notAfter int64
	err := s.R.QueryRowContext(ctx, `
		SELECT c.node_id, n.state, c.not_after, c.revoked_at IS NOT NULL AND c.revoked_at <= ?
		FROM node_cert c JOIN node n ON n.id = c.node_id WHERE c.serial = ?`, unix(now), serial).
		Scan(&c.NodeID, &c.NodeState, &notAfter, &c.Revoked)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	c.NotAfter = fromUnix(notAfter)
	return c, err
}

// CreateEnrollment issues an enrollment token. With n != nil it also creates the (pending) node in the
// same batch (ErrConflict when the name is taken); otherwise nodeID must be an existing,
// non-retired node (re-enrollment). Older unused tokens of the node stop working.
// The returned node is the current row.
func (s *Store) CreateEnrollment(ctx context.Context, n *NodeRow, nodeID string, tokenHash []byte, createdBy string, now, expires time.Time) (NodeRow, error) {
	stmts := make([]Stmt, 0, 5)
	if n != nil {
		nodeID = n.ID
		stmts = append(stmts, Stmt{Query: `
			INSERT INTO node (id, name, address, country_code, location, provider, state, created_at)
			VALUES (?, ?, ?, ?, ?, ?, 'pending', ?)`,
			Args: []any{n.ID, n.Name, n.Address, n.CountryCode, n.Location, n.Provider, unix(now)}})
	}
	stmts = append(stmts,
		guard(`EXISTS (SELECT 1 FROM node WHERE id = ? AND state <> 'retired')`, nodeID),
		Stmt{Query: `UPDATE enrollment_token SET expires_at = ? WHERE node_id = ? AND used_at IS NULL AND expires_at > ?`,
			Args: []any{unix(now), nodeID, unix(now)}},
		Stmt{Query: `
		INSERT INTO enrollment_token (id, node_id, token_hash, created_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
			Args: []any{NewID("enr_"), nodeID, tokenHash, createdBy, unix(now), unix(expires)}},
		Stmt{Query: `SELECT ` + nodeCols + ` FROM node WHERE id = ?`, Args: []any{nodeID}, Returning: true},
	)
	results, err := s.batch(ctx, stmts...)
	if errors.Is(err, errGuard) {
		cur, readErr := s.Node(ctx, nodeID)
		if errors.Is(readErr, ErrNotFound) {
			return NodeRow{}, ErrNotFound
		}
		if readErr != nil {
			return NodeRow{}, readErr
		}
		if cur.State == "retired" {
			return NodeRow{}, ErrNodeRetired
		}
		return NodeRow{}, ErrConflict
	}
	if err != nil {
		if n != nil && fleetIsUnique(err) && (strings.Contains(err.Error(), "node.name") || strings.Contains(err.Error(), "node.id")) {
			return NodeRow{}, ErrConflict
		}
		return NodeRow{}, err
	}
	return scanNode(batchRow(results[len(results)-1].Rows[0]))
}

// PendingEnrollmentExpiry returns when the newest usable enrollment token of the node expires; the zero
// time if there is none.
func (s *Store) PendingEnrollmentExpiry(ctx context.Context, nodeID string, now time.Time) (time.Time, error) {
	var exp sql.NullInt64
	err := s.R.QueryRowContext(ctx,
		`SELECT max(expires_at) FROM enrollment_token WHERE node_id = ? AND used_at IS NULL AND expires_at > ?`,
		nodeID, unix(now)).Scan(&exp)
	return fleetTime(exp.Int64), err
}

// EnrollResult is what a successful Enroll returns.
type EnrollResult struct {
	NodeID string
	Cert   CertRow
	Replay bool // the same certificate as before, returned for a retried request
}

// Enroll consumes a one-time token. keyHash is sha256 of the CSR public key; issue signs a certificate
// for the node after the reads and before the guarded batch (only for a first use). Errors: ErrEnrollToken,
// ErrNodeRetired. A repeat with the same token and key within 10 minutes of first use returns the
// stored certificate. Every other certificate of the node is revoked (re-enrollment).
func (s *Store) Enroll(ctx context.Context, tokenHash, keyHash []byte, now time.Time, issue func(nodeID string) (CertRow, error)) (EnrollResult, error) {
	type enrollSnapshot struct {
		id, nodeID string
		expires    int64
		used       sql.NullInt64
		csr        []byte
		serial     sql.NullString
		nodeState  string
		tokenFound bool
		nodeFound  bool
		cert       CertRow
		certFound  bool
	}
	readSnapshot := func() (enrollSnapshot, error) {
		var snap enrollSnapshot
		var certNB, certNA, certIssued int64
		r := reads{}
		r.add(func(rows [][]any) error {
			if len(rows) == 0 {
				return nil
			}
			if len(rows) != 1 {
				return errors.New("store: enrollment token batch returned multiple rows")
			}
			snap.tokenFound = true
			return batchRow(rows[0]).Scan(&snap.id, &snap.nodeID, &snap.expires, &snap.used, &snap.csr, &snap.serial)
		}, `SELECT id, node_id, expires_at, used_at, csr_key_hash, issued_serial FROM enrollment_token WHERE token_hash = ?`, tokenHash)
		r.add(func(rows [][]any) error {
			if len(rows) == 0 {
				return nil
			}
			snap.nodeFound = true
			return batchRow(rows[0]).Scan(&snap.nodeState)
		}, `SELECT state FROM node WHERE id = (SELECT node_id FROM enrollment_token WHERE token_hash = ?)`, tokenHash)
		r.add(func(rows [][]any) error {
			if len(rows) == 0 {
				return nil
			}
			snap.certFound = true
			if err := batchRow(rows[0]).Scan(&snap.cert.Serial, &snap.cert.NodeID, &snap.cert.CAID, &snap.cert.PEM, &certNB, &certNA, &certIssued); err != nil {
				return err
			}
			snap.cert.NotBefore, snap.cert.NotAfter, snap.cert.IssuedAt = fromUnix(certNB), fromUnix(certNA), fromUnix(certIssued)
			return nil
		}, `SELECT serial, node_id, ca_id, pem, not_before, not_after, issued_at FROM node_cert
			WHERE serial = (SELECT issued_serial FROM enrollment_token WHERE token_hash = ?) AND revoked_at IS NULL`, tokenHash)
		return snap, r.run(ctx, s)
	}
	checkSnapshot := func(snap enrollSnapshot) (EnrollResult, bool, error) {
		if !snap.tokenFound {
			return EnrollResult{}, false, ErrEnrollToken
		}
		if snap.used.Valid && now.Unix()-snap.used.Int64 > int64(enrollReplayWindow/time.Second) {
			return EnrollResult{}, false, ErrEnrollToken
		}
		if !snap.used.Valid && snap.expires <= now.Unix() {
			return EnrollResult{}, false, ErrEnrollToken
		}
		if !snap.nodeFound {
			return EnrollResult{}, false, ErrNotFound
		}
		if snap.nodeState == "retired" {
			return EnrollResult{}, false, ErrNodeRetired
		}
		if snap.used.Valid {
			if !snap.serial.Valid || !bytes.Equal(snap.csr, keyHash) || now.Unix()-snap.used.Int64 > int64(enrollReplayWindow/time.Second) || !snap.certFound {
				return EnrollResult{}, false, ErrEnrollToken
			}
			return EnrollResult{NodeID: snap.nodeID, Cert: snap.cert, Replay: true}, true, nil
		}
		return EnrollResult{}, false, nil
	}
	snap, err := readSnapshot()
	if err != nil {
		return EnrollResult{}, err
	}
	if result, replay, checkErr := checkSnapshot(snap); checkErr != nil {
		return EnrollResult{}, checkErr
	} else if replay {
		return result, nil
	}
	c, err := issue(snap.nodeID)
	if err != nil {
		return EnrollResult{}, err
	}
	_, err = s.batch(ctx,
		guard(`EXISTS (SELECT 1 FROM enrollment_token t JOIN node n ON n.id = t.node_id
			WHERE t.id = ? AND t.used_at IS NULL AND t.expires_at > ? AND n.state <> 'retired')`, snap.id, now.Unix()),
		Stmt{Query: `UPDATE node_cert SET revoked_at = ?, revoke_reason = 're-enrolled' WHERE node_id = ? AND (revoked_at IS NULL OR revoked_at > ?)`,
			Args: []any{unix(now), snap.nodeID, unix(now)}},
		insertCertStmt(c),
		Stmt{Query: `UPDATE enrollment_token SET used_at = ?, csr_key_hash = ?, issued_serial = ? WHERE id = ?`, Args: []any{unix(now), keyHash, c.Serial, snap.id}},
		Stmt{Query: `UPDATE node SET cert_serial = ? WHERE id = ?`, Args: []any{c.Serial, snap.nodeID}},
	)
	if errors.Is(err, errGuard) {
		snap, err = readSnapshot()
		if err != nil {
			return EnrollResult{}, err
		}
		if result, replay, checkErr := checkSnapshot(snap); checkErr != nil {
			return EnrollResult{}, checkErr
		} else if replay {
			return result, nil
		}
		return EnrollResult{}, ErrEnrollToken
	}
	if err != nil {
		return EnrollResult{}, err
	}
	return EnrollResult{NodeID: snap.nodeID, Cert: c}, nil
}
