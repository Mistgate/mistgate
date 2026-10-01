package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// PasswordCred is an admin's password + authenticator-code credential.
type PasswordCred struct {
	AdminID    string
	Login      string // lower case
	Hash       string // argon2id PHC string
	TOTPSecret []byte // sealed by the caller (vault); the store never sees the plain secret
	TOTPStep   int64  // last accepted 30 s step
}

// PasswordByLogin returns the credential for a (lower-case) login name, or ErrNotFound.
func (s *Store) PasswordByLogin(ctx context.Context, login string) (PasswordCred, error) {
	var c PasswordCred
	err := s.R.QueryRowContext(ctx,
		`SELECT admin_id, login, hash, totp_secret, totp_step FROM admin_password WHERE login = ?`, login).
		Scan(&c.AdminID, &c.Login, &c.Hash, &c.TOTPSecret, &c.TOTPStep)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// PasswordByAdmin returns the password credential of an admin, or ErrNotFound.
func (s *Store) PasswordByAdmin(ctx context.Context, adminID string) (PasswordCred, error) {
	var c PasswordCred
	err := s.R.QueryRowContext(ctx,
		`SELECT admin_id, login, hash, totp_secret, totp_step FROM admin_password WHERE admin_id = ?`, adminID).
		Scan(&c.AdminID, &c.Login, &c.Hash, &c.TOTPSecret, &c.TOTPStep)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// AdminHasPassword reports whether the admin can sign in with a password.
func (s *Store) AdminHasPassword(ctx context.Context, adminID string) (bool, error) {
	var ok bool
	err := s.R.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM admin_password WHERE admin_id = ?)`, adminID).Scan(&ok)
	return ok, err
}

// AnyPassword reports whether any admin can sign in with a password (the sign-in page
// offers the password form only then).
func (s *Store) AnyPassword(ctx context.Context) (bool, error) {
	var ok bool
	err := s.R.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM admin_password)`).Scan(&ok)
	return ok, err
}

// ErrLoginTaken: another admin signs in with this login.
var ErrLoginTaken = errors.New("store: login taken")

// SetPasswordHash replaces the password of an admin's password login; ErrNotFound if the admin has none.
func (s *Store) SetPasswordHash(ctx context.Context, adminID, hash string) error {
	res, err := s.W.ExecContext(ctx, `UPDATE admin_password SET hash = ? WHERE admin_id = ?`, hash, adminID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTOTPSecret replaces the authenticator secret (sealed by the caller) of an admin's password login. The last
// accepted step never goes back, so a code that was used once stays used. ErrNotFound if the admin has none.
func (s *Store) SetTOTPSecret(ctx context.Context, adminID string, sealed []byte, step int64) error {
	res, err := s.W.ExecContext(ctx, `UPDATE admin_password SET totp_secret = ?, totp_step = max(totp_step, ?) WHERE admin_id = ?`, sealed, step, adminID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddPassword gives an admin who has none a password login (c.Login lower case, c.Hash and the sealed c.TOTPSecret).
// ErrLoginTaken when another admin has the login, ErrAccessExists when this admin already has a password login,
// ErrNotFound for an unknown admin.
func (s *Store) AddPassword(ctx context.Context, c PasswordCred, now time.Time) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertPassword(ctx, tx, c, now); err != nil {
		return err
	}
	return tx.Commit()
}

// insertPassword is the checked INSERT of AddPassword and ResetPasswordLogin.
func insertPassword(ctx context.Context, tx *sql.Tx, c PasswordCred, now time.Time) error {
	var admin, has, taken bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM admin WHERE id = ?), EXISTS (SELECT 1 FROM admin_password WHERE admin_id = ?),
		EXISTS (SELECT 1 FROM admin_password WHERE login = ?)`, c.AdminID, c.AdminID, c.Login).Scan(&admin, &has, &taken); err != nil {
		return err
	}
	switch {
	case !admin:
		return ErrNotFound
	case has:
		return ErrAccessExists
	case taken:
		return ErrLoginTaken
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO admin_password (admin_id, login, hash, totp_secret, totp_step, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		c.AdminID, c.Login, c.Hash, c.TOTPSecret, c.TOTPStep, unix(now))
	return err
}

// AdminLogin is an admin with its password login, for the operator's command line.
type AdminLogin struct {
	Admin
	Login    string // "" = no password login
	Passkeys int
}

// AdminLogins lists every admin, oldest first, with its password login and how many passkeys it has.
func (s *Store) AdminLogins(ctx context.Context) ([]AdminLogin, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT a.id, a.display_name, a.role, a.created_at, COALESCE(p.login, ''), (SELECT count(*) FROM passkey k WHERE k.admin_id = a.id)
		FROM admin a LEFT JOIN admin_password p ON p.admin_id = a.id ORDER BY a.created_at, a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminLogin
	for rows.Next() {
		var a AdminLogin
		var created int64
		if err := rows.Scan(&a.ID, &a.DisplayName, &a.Role, &created, &a.Login, &a.Passkeys); err != nil {
			return nil, err
		}
		a.CreatedAt = fromUnix(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ResetPasswordLogin is the operator's way back in (mistgate auth reset-login), in one transaction: c becomes the
// admin's password login (a new password and authenticator secret for its existing login, or a new login when create
// is set), the lockouts named in clearLocks are lifted, every session of the admin ends, and the audit row is written.
// The last accepted authenticator step of an existing login never goes back. It returns how many sessions ended.
func (s *Store) ResetPasswordLogin(ctx context.Context, c PasswordCred, create bool, clearLocks []string, now time.Time, audit AuditEntry) (int, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if create {
		if err := insertPassword(ctx, tx, c, now); err != nil {
			return 0, err
		}
	} else {
		res, err := tx.ExecContext(ctx, `UPDATE admin_password SET hash = ?, totp_secret = ?, totp_step = max(totp_step, ?) WHERE admin_id = ? AND login = ?`,
			c.Hash, c.TOTPSecret, c.TOTPStep, c.AdminID, c.Login)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return 0, ErrNotFound
		}
	}
	for _, k := range clearLocks {
		if _, err := tx.ExecContext(ctx, `DELETE FROM login_failure WHERE login = ?`, k); err != nil {
			return 0, err
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM session WHERE admin_id = ?`, c.AdminID)
	if err != nil {
		return 0, err
	}
	ended, _ := res.RowsAffected()
	if audit.Params == "" {
		audit.Params = "{}"
	}
	if audit.Source == "" {
		audit.Source = AuditPanel
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit (ts, actor, action, params, result, source, ip) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		unix(now), audit.Actor, audit.Action, audit.Params, audit.Result, audit.Source, audit.IP); err != nil {
		return 0, err
	}
	return int(ended), tx.Commit()
}

// AdvanceTOTPStep records step as used. It returns false if step is not newer than the
// last accepted one, which is how a code is made single-use (also across racing requests).
func (s *Store) AdvanceTOTPStep(ctx context.Context, adminID string, step int64) (bool, error) {
	res, err := s.W.ExecContext(ctx,
		`UPDATE admin_password SET totp_step = ? WHERE admin_id = ? AND totp_step < ?`, step, adminID, step)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// LoginFailure is the effective lockout state of a login name.
type LoginFailure struct {
	Failures    int
	LockedUntil time.Time // zero when not locked
}

// Locked reports whether the login is locked at now.
func (f LoginFailure) Locked(now time.Time) bool { return f.LockedUntil.After(now) }

// effective drops state that has aged out: a finished lockout and failures older than window.
func effective(failures int, lockedUntil, lastAt int64, now time.Time, window time.Duration) LoginFailure {
	if lockedUntil != 0 && lockedUntil <= unix(now) {
		return LoginFailure{}
	}
	if lockedUntil == 0 && fromUnix(lastAt).Add(window).Before(now) {
		return LoginFailure{}
	}
	f := LoginFailure{Failures: failures}
	if lockedUntil != 0 {
		f.LockedUntil = fromUnix(lockedUntil)
	}
	return f
}

// LoginFailures returns the lockout state of login (zero value if it has none).
func (s *Store) LoginFailures(ctx context.Context, login string, now time.Time, window time.Duration) (LoginFailure, error) {
	var failures int
	var locked, last int64
	err := s.R.QueryRowContext(ctx, `SELECT failures, locked_until, last_at FROM login_failure WHERE login = ?`, login).
		Scan(&failures, &locked, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return LoginFailure{}, nil
	}
	if err != nil {
		return LoginFailure{}, err
	}
	return effective(failures, locked, last, now, window), nil
}

// RecordLoginFailure counts one failed sign-in for login. Reaching maxFailures failures within
// window locks the login for lock. It also drops stale rows, so the table cannot grow
// without bound from names an attacker makes up.
func (s *Store) RecordLoginFailure(ctx context.Context, login string, now time.Time, maxFailures int, window, lock time.Duration) (LoginFailure, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return LoginFailure{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM login_failure WHERE last_at < ? AND locked_until < ?`,
		unix(now.Add(-window)), unix(now)); err != nil {
		return LoginFailure{}, err
	}
	var failures int
	var locked, last int64
	err = tx.QueryRowContext(ctx, `SELECT failures, locked_until, last_at FROM login_failure WHERE login = ?`, login).
		Scan(&failures, &locked, &last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return LoginFailure{}, err
	}
	f := effective(failures, locked, last, now, window)
	f.Failures++
	lockedUntil := int64(0)
	if f.Failures >= maxFailures {
		f.LockedUntil = now.Add(lock)
		lockedUntil = unix(f.LockedUntil)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO login_failure (login, failures, locked_until, last_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(login) DO UPDATE SET failures = excluded.failures, locked_until = excluded.locked_until, last_at = excluded.last_at`,
		login, f.Failures, lockedUntil, unix(now)); err != nil {
		return LoginFailure{}, err
	}
	return f, tx.Commit()
}

// ClearLoginFailures forgets the failures of login after a successful sign-in.
func (s *Store) ClearLoginFailures(ctx context.Context, login string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM login_failure WHERE login = ?`, login)
	return err
}
