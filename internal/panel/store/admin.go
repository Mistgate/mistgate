package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Roles stored in admin.role.
const (
	RoleOwner    = "owner"
	RoleHelper   = "helper"
	RoleReadonly = "readonly"
)

// Admin is a panel administrator.
type Admin struct {
	ID          string
	DisplayName string
	Role        string
	UserHandle  []byte // WebAuthn user.id
	CreatedAt   time.Time
}

// Passkey is a stored WebAuthn credential.
type Passkey struct {
	ID              string
	AdminID         string
	CredentialID    []byte
	PublicKey       []byte
	AttestationType string
	SignCount       uint32
	AAGUID          []byte
	Transports      []string
	Flags           byte // raw authenticator data flags
	Name            string
	CreatedAt       time.Time
	LastUsedAt      time.Time // zero if never used to sign in
}

// AdminCount returns the number of admins.
func (s *Store) AdminCount(ctx context.Context) (int, error) {
	var n int
	err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM admin`).Scan(&n)
	return n, err
}

// PutSetupToken stores a new one-time setup token hash and drops older unused ones,
// so only the latest printed link works.
func (s *Store) PutSetupToken(ctx context.Context, hash []byte, expires time.Time) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM setup_token WHERE used_at IS NULL`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO setup_token (hash, expires_at) VALUES (?, ?)`, hash, unix(expires)); err != nil {
		return err
	}
	return tx.Commit()
}

// CheckSetupToken returns ErrSetupClosed unless hash is an unused, unexpired setup
// token and no admin exists yet. It does not consume the token.
func (s *Store) CheckSetupToken(ctx context.Context, hash []byte, now time.Time) error {
	var ok bool
	err := s.R.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM setup_token WHERE hash = ? AND used_at IS NULL AND expires_at > ?)
		   AND NOT EXISTS (SELECT 1 FROM admin)`, hash, unix(now)).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrSetupClosed
	}
	return nil
}

// CreateFirstAdmin atomically consumes the setup token and inserts the first admin
// and its passkey. It returns ErrSetupClosed if the token is not valid any more or an
// admin already exists, so a token can never create two admins.
func (s *Store) CreateFirstAdmin(ctx context.Context, tokenHash []byte, now time.Time, a Admin, p Passkey) error {
	return s.createFirstAdmin(ctx, tokenHash, now, a, func(tx *sql.Tx) error { return insertPasskey(ctx, tx, p, now) })
}

// CreateFirstAdminPassword is CreateFirstAdmin for an admin whose first credential is a
// password + authenticator code instead of a passkey. A login name that is taken (it
// cannot be, before the first admin exists) is reported like any other insert error.
func (s *Store) CreateFirstAdminPassword(ctx context.Context, tokenHash []byte, now time.Time, a Admin, c PasswordCred) error {
	return s.createFirstAdmin(ctx, tokenHash, now, a, func(tx *sql.Tx) error {
		c.AdminID = a.ID
		_, err := tx.ExecContext(ctx,
			`INSERT INTO admin_password (admin_id, login, hash, totp_secret, totp_step, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			c.AdminID, c.Login, c.Hash, c.TOTPSecret, c.TOTPStep, unix(now))
		return err
	})
}

func (s *Store) createFirstAdmin(ctx context.Context, tokenHash []byte, now time.Time, a Admin, insertCred func(*sql.Tx) error) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM admin)`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrSetupClosed
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE setup_token SET used_at = ? WHERE hash = ? AND used_at IS NULL AND expires_at > ?`,
		unix(now), tokenHash, unix(now))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrSetupClosed
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES (?, ?, ?, ?, ?)`,
		a.ID, a.DisplayName, a.Role, a.UserHandle, unix(now)); err != nil {
		return err
	}
	if err := insertCred(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ErrLastMethod is returned when removing a credential would leave the admin unable to sign in.
var ErrLastMethod = errors.New("store: last sign-in method")

// AddPasskey stores another passkey for an existing admin.
func (s *Store) AddPasskey(ctx context.Context, p Passkey, now time.Time) error {
	return insertPasskey(ctx, s.W, p, now)
}

// DeletePasskey removes one of the admin's passkeys. It returns ErrNotFound if the admin
// has no such passkey and ErrLastMethod if it is the only way the admin can sign in.
func (s *Store) DeletePasskey(ctx context.Context, adminID, id string) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	var found bool
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*), coalesce(sum(id = ?), 0) FROM passkey WHERE admin_id = ?`, id, adminID).Scan(&n, &found); err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	if n == 1 {
		var hasPw bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM admin_password WHERE admin_id = ?)`, adminID).Scan(&hasPw); err != nil {
			return err
		}
		if !hasPw {
			return ErrLastMethod
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM passkey WHERE id = ? AND admin_id = ?`, id, adminID); err != nil {
		return err
	}
	return tx.Commit()
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertPasskey(ctx context.Context, x execer, p Passkey, now time.Time) error {
	if p.AAGUID == nil {
		p.AAGUID = []byte{} // nil would be bound as NULL
	}
	_, err := x.ExecContext(ctx, `
		INSERT INTO passkey (id, admin_id, credential_id, public_key, attestation_type, sign_count,
		                     aaguid, transports, flags, name, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.AdminID, p.CredentialID, p.PublicKey, p.AttestationType, int64(p.SignCount),
		p.AAGUID, strings.Join(p.Transports, ","), int64(p.Flags), p.Name, unix(now))
	return err
}

// Admin returns an admin by id.
func (s *Store) Admin(ctx context.Context, id string) (Admin, error) {
	var a Admin
	var created int64
	err := s.R.QueryRowContext(ctx,
		`SELECT id, display_name, role, user_handle, created_at FROM admin WHERE id = ?`, id).
		Scan(&a.ID, &a.DisplayName, &a.Role, &a.UserHandle, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	a.CreatedAt = fromUnix(created)
	return a, err
}

const passkeyCols = `id, admin_id, credential_id, public_key, attestation_type, sign_count, aaguid, transports, flags, name, created_at, last_used_at`

func scanPasskey(sc interface{ Scan(...any) error }) (Passkey, error) {
	var p Passkey
	var sign, flags, created int64
	var lastUsed sql.NullInt64
	var transports string
	if err := sc.Scan(&p.ID, &p.AdminID, &p.CredentialID, &p.PublicKey, &p.AttestationType,
		&sign, &p.AAGUID, &transports, &flags, &p.Name, &created, &lastUsed); err != nil {
		return p, err
	}
	p.SignCount = uint32(sign)
	p.Flags = byte(flags)
	p.CreatedAt = fromUnix(created)
	if lastUsed.Valid {
		p.LastUsedAt = fromUnix(lastUsed.Int64)
	}
	if transports != "" {
		p.Transports = strings.Split(transports, ",")
	}
	return p, nil
}

// PasskeyByCredentialID returns the passkey with the given WebAuthn credential id.
func (s *Store) PasskeyByCredentialID(ctx context.Context, credID []byte) (Passkey, error) {
	p, err := scanPasskey(s.R.QueryRowContext(ctx,
		`SELECT `+passkeyCols+` FROM passkey WHERE credential_id = ?`, credID))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// PasskeysByAdmin lists an admin's passkeys.
func (s *Store) PasskeysByAdmin(ctx context.Context, adminID string) ([]Passkey, error) {
	rows, err := s.R.QueryContext(ctx,
		`SELECT `+passkeyCols+` FROM passkey WHERE admin_id = ? ORDER BY created_at`, adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Passkey
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// TouchPasskey records a successful sign-in: new signature counter, flags (backup
// state may change) and last-used time.
func (s *Store) TouchPasskey(ctx context.Context, credID []byte, signCount uint32, flags byte, now time.Time) error {
	_, err := s.W.ExecContext(ctx,
		`UPDATE passkey SET sign_count = ?, flags = ?, last_used_at = ? WHERE credential_id = ?`,
		int64(signCount), int64(flags), unix(now), credID)
	return err
}
