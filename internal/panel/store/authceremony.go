package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	ErrAuthCeremonyNotFound = errors.New("auth ceremony not found")
	ErrAuthCeremonyLimit    = errors.New("too many pending ceremonies")
)

// AuthCeremony is the persisted server-side half of an authentication ceremony.
// SessionData is JSON; transient secrets such as a setup TOTP seed are sealed by auth.
type AuthCeremony struct {
	ID              string
	Kind            string
	Source          string
	SessionData     string
	TokenHash       []byte
	AdminID         string
	AdminUserHandle []byte
	Name            string
	Login           string
	PasswordHash    string
	TOTPEncrypted   []byte
	Tries           int
	ExpiresAt       time.Time
}

const sessionBoundCeremonyKinds = `kind IN ('stepup', 'add-passkey', 'totp-enroll')`

// Keep this list in sync with the session-bound ceremony kinds in internal/panel/auth.
func sessionBoundCeremony(kind string) bool {
	switch kind {
	case "stepup", "add-passkey", "totp-enroll":
		return true
	default:
		return false
	}
}

// PutAuthCeremony prunes expired rows, keeps separate budgets for anonymous and session-bound ceremonies, and evicts
// the oldest login ceremony when that class reaches its total budget.
// Setup ceremonies require a setup token, so anonymous login churn cannot evict them.
func (s *Store) PutAuthCeremony(ctx context.Context, c AuthCeremony, now time.Time, maxPerSource, maxTotal int) error {
	if c.TokenHash == nil {
		c.TokenHash = []byte{}
	}
	if c.TOTPEncrypted == nil {
		c.TOTPEncrypted = []byte{}
	}
	if c.AdminUserHandle == nil {
		c.AdminUserHandle = []byte{}
	}
	classPredicate := `kind NOT IN ('stepup', 'add-passkey', 'totp-enroll')`
	if sessionBoundCeremony(c.Kind) {
		classPredicate = sessionBoundCeremonyKinds
	}
	stmts := []Stmt{{Query: `DELETE FROM auth_ceremony WHERE id IN (
		SELECT id FROM auth_ceremony WHERE expires_at <= ? ORDER BY expires_at LIMIT 100
	)`, Args: []any{now.Unix()}}}
	if !sessionBoundCeremony(c.Kind) {
		stmts = append(stmts, Stmt{Query: `DELETE FROM auth_ceremony
			WHERE id = (
				SELECT id FROM auth_ceremony
				WHERE expires_at > ? AND kind = 'login' AND ` + classPredicate + `
				ORDER BY rowid LIMIT 1
			)
			AND (SELECT count(*) FROM auth_ceremony WHERE expires_at > ? AND ` + classPredicate + `) >= ?
			AND (SELECT count(*) FROM auth_ceremony WHERE source_key = ? AND expires_at > ? AND ` + classPredicate + `) < ?`,
			Args: []any{now.Unix(), now.Unix(), maxTotal, c.Source, now.Unix(), maxPerSource}})
	}
	stmts = append(stmts, Stmt{Query: `
		INSERT INTO auth_ceremony (
			id, kind, source_key, session_data, token_hash, admin_id, admin_user_handle, name, login,
			password_hash, totp_enc, tries, expires_at
		)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE (SELECT count(*) FROM auth_ceremony WHERE source_key = ? AND expires_at > ? AND ` + classPredicate + `) < ?
		  AND (SELECT count(*) FROM auth_ceremony WHERE expires_at > ? AND ` + classPredicate + `) < ?`,
		Args: []any{c.ID, c.Kind, c.Source, c.SessionData, c.TokenHash, c.AdminID, c.AdminUserHandle, c.Name, c.Login,
			c.PasswordHash, c.TOTPEncrypted, c.Tries, c.ExpiresAt.Unix(),
			c.Source, now.Unix(), maxPerSource, now.Unix(), maxTotal},
	})
	results, err := s.batch(ctx, stmts...)
	if err != nil {
		return err
	}
	n := results[len(results)-1].RowsAffected
	if n == 0 {
		return ErrAuthCeremonyLimit
	}
	return nil
}

// GetAuthCeremony reads one live row without consuming it.
func (s *Store) GetAuthCeremony(ctx context.Context, id string, now time.Time) (AuthCeremony, error) {
	var c AuthCeremony
	var expires int64
	err := s.R.QueryRowContext(ctx, `
		SELECT id, kind, source_key, session_data, token_hash, admin_id, admin_user_handle,
		       name, login, password_hash, totp_enc, tries, expires_at
		FROM auth_ceremony
		WHERE id = ? AND expires_at > ?
	`, id, now.Unix()).Scan(&c.ID, &c.Kind, &c.Source, &c.SessionData, &c.TokenHash,
		&c.AdminID, &c.AdminUserHandle, &c.Name, &c.Login, &c.PasswordHash, &c.TOTPEncrypted, &c.Tries, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthCeremony{}, ErrAuthCeremonyNotFound
	}
	if err != nil {
		return AuthCeremony{}, err
	}
	c.ExpiresAt = time.Unix(expires, 0)
	return c, nil
}

// ConsumeAuthCeremony deletes a ceremony, returning whether this call won.
func (s *Store) ConsumeAuthCeremony(ctx context.Context, id string) (bool, error) {
	result, err := s.W.ExecContext(ctx, `DELETE FROM auth_ceremony WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// FailAuthCeremony advances one recoverable code failure and deletes the ceremony at the limit.
func (s *Store) FailAuthCeremony(ctx context.Context, id string, maxTries int) (bool, error) {
	results, err := s.batch(ctx,
		Stmt{Query: `UPDATE auth_ceremony SET tries = tries + 1 WHERE id = ? RETURNING tries`, Args: []any{id}, Returning: true},
		Stmt{Query: `DELETE FROM auth_ceremony WHERE id = ? AND tries >= ?`, Args: []any{id, maxTries}},
	)
	if err != nil {
		return false, err
	}
	return len(results[0].Rows) == 1, nil
}
