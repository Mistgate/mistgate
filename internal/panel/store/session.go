package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Session is a signed-in browser session. Only the SHA-256 of the cookie token is stored.
type Session struct {
	TokenHash  []byte
	AdminID    string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	IP         string
	UserAgent  string
	StepUpAt   time.Time // last proof of a sign-in factor in this session (signing in counts)
}

// CreateSession inserts a session. Signing in is itself a proof of a factor, so StepUpAt starts at CreatedAt.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.W.ExecContext(ctx, `
		INSERT INTO session (token_hash, admin_id, created_at, last_seen_at, expires_at, ip, user_agent, stepup_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.TokenHash, sess.AdminID, unix(sess.CreatedAt), unix(sess.LastSeenAt), unix(sess.ExpiresAt),
		sess.IP, sess.UserAgent, unix(sess.CreatedAt))
	return err
}

// CreatePasswordSession inserts a password + code sign-in session only while the checked credential is current and the
// login is not locked. The session insert and successful-sign-in failure reset share one atomic batch. The reset can
// also clear reservations for wrong checks already inside argon2. Only the handful admitted under this login's limit
// can finish uncounted. They remain counted against the per-source limit, and every successful sign-in still requires
// its second factor.
func (s *Store) CreatePasswordSession(ctx context.Context, sess Session, checked PasswordCred) error {
	results, err := s.batch(ctx,
		Stmt{Query: `
		INSERT INTO session (token_hash, admin_id, created_at, last_seen_at, expires_at, ip, user_agent, stepup_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM admin_password WHERE admin_id = ? AND hash = ? AND totp_secret = ?)
		  AND NOT EXISTS (SELECT 1 FROM login_failure WHERE login = ? AND locked_until > ?)`,
			Args: []any{sess.TokenHash, sess.AdminID, unix(sess.CreatedAt), unix(sess.LastSeenAt), unix(sess.ExpiresAt),
				sess.IP, sess.UserAgent, unix(sess.CreatedAt), checked.AdminID, checked.Hash, checked.TOTPSecret,
				checked.Login, unix(sess.CreatedAt)}},
		Stmt{Query: `DELETE FROM login_failure
			WHERE login = ?
			  AND NOT EXISTS (SELECT 1 FROM login_failure WHERE login = ? AND locked_until > ?)
			  AND EXISTS (SELECT 1 FROM admin_password WHERE admin_id = ? AND login = ? AND hash = ? AND totp_secret = ?)
			  AND EXISTS (SELECT 1 FROM session WHERE token_hash = ? AND admin_id = ?)`,
			Args: []any{checked.Login, checked.Login, unix(sess.CreatedAt), checked.AdminID, checked.Login, checked.Hash, checked.TOTPSecret,
				sess.TokenHash, sess.AdminID}},
	)
	if err != nil {
		return err
	}
	if results[0].RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// SessionStepUp records that the session proved a sign-in factor at now.
func (s *Store) SessionStepUp(ctx context.Context, tokenHash []byte, now time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE session SET stepup_at = ? WHERE token_hash = ?`, unix(now), tokenHash)
	return err
}

// SessionWithAdmin returns the session for a token hash together with its admin.
func (s *Store) SessionWithAdmin(ctx context.Context, tokenHash []byte) (Session, Admin, error) {
	var sess Session
	var a Admin
	var created, seen, expires, adminCreated, stepUp int64
	err := s.R.QueryRowContext(ctx, `
		SELECT s.token_hash, s.admin_id, s.created_at, s.last_seen_at, s.expires_at, s.ip, s.user_agent, s.stepup_at,
		       a.id, a.display_name, a.role, a.user_handle, a.created_at
		FROM session s JOIN admin a ON a.id = s.admin_id WHERE s.token_hash = ?`, tokenHash).
		Scan(&sess.TokenHash, &sess.AdminID, &created, &seen, &expires, &sess.IP, &sess.UserAgent, &stepUp,
			&a.ID, &a.DisplayName, &a.Role, &a.UserHandle, &adminCreated)
	if errors.Is(err, sql.ErrNoRows) {
		return sess, a, ErrNotFound
	}
	sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt, sess.StepUpAt = fromUnix(created), fromUnix(seen), fromUnix(expires), fromUnix(stepUp)
	a.CreatedAt = fromUnix(adminCreated)
	return sess, a, err
}

// TouchSession bumps last_seen_at.
func (s *Store) TouchSession(ctx context.Context, tokenHash []byte, now time.Time) error {
	_, err := s.W.ExecContext(ctx, `UPDATE session SET last_seen_at = ? WHERE token_hash = ?`, unix(now), tokenHash)
	return err
}

// DeleteSession removes a session (logout or expiry).
func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM session WHERE token_hash = ?`, tokenHash)
	return err
}

// SessionsByAdmin lists an admin's sessions, most recently used first. Expired ones are
// included until PurgeSessions removes them; callers filter by the clock they trust.
func (s *Store) SessionsByAdmin(ctx context.Context, adminID string) ([]Session, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT token_hash, admin_id, created_at, last_seen_at, expires_at, ip, user_agent
		FROM session WHERE admin_id = ? ORDER BY last_seen_at DESC, created_at DESC`, adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var sess Session
		var created, seen, expires int64
		if err := rows.Scan(&sess.TokenHash, &sess.AdminID, &created, &seen, &expires, &sess.IP, &sess.UserAgent); err != nil {
			return nil, err
		}
		sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt = fromUnix(created), fromUnix(seen), fromUnix(expires)
		out = append(out, sess)
	}
	return out, rows.Err()
}

// DeleteAdminSession ends one of the admin's sessions; it reports whether there was one.
func (s *Store) DeleteAdminSession(ctx context.Context, adminID string, tokenHash []byte) (bool, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM session WHERE admin_id = ? AND token_hash = ?`, adminID, tokenHash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteOtherSessions ends every session of the admin except keep, returning how many.
func (s *Store) DeleteOtherSessions(ctx context.Context, adminID string, keep []byte) (int, error) {
	if keep == nil {
		keep = []byte{} // nil would bind as NULL, and "<> NULL" matches nothing
	}
	res, err := s.W.ExecContext(ctx, `DELETE FROM session WHERE admin_id = ? AND token_hash <> ?`, adminID, keep)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// PurgeSessions deletes sessions past their absolute expiry or idle for longer than idle.
func (s *Store) PurgeSessions(ctx context.Context, now time.Time, idle time.Duration) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM session WHERE expires_at <= ? OR last_seen_at <= ?`,
		unix(now), unix(now.Add(-idle)))
	return err
}
