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

// SetupTokenStatus reports whether setup is complete and the expiry of an unused,
// unexpired setup token, using one query.
func (s *Store) SetupTokenStatus(ctx context.Context, now time.Time) (adminExists bool, expiry time.Time, tokenExists bool, err error) {
	var adminCount int64
	var activeExpiry sql.NullInt64
	err = s.R.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM admin),
		       (SELECT expires_at FROM setup_token WHERE used_at IS NULL AND expires_at > ? ORDER BY expires_at DESC LIMIT 1)
	`, unix(now)).Scan(&adminCount, &activeExpiry)
	if err != nil {
		return false, time.Time{}, false, err
	}
	if activeExpiry.Valid {
		return adminCount > 0, fromUnix(activeExpiry.Int64), true, nil
	}
	return adminCount > 0, time.Time{}, false, nil
}

// PutSetupToken stores a new one-time setup token hash and drops older unused ones,
// so only the latest printed link works.
func (s *Store) PutSetupToken(ctx context.Context, hash []byte, expires time.Time) error {
	_, err := s.batch(ctx,
		Stmt{Query: `DELETE FROM setup_token WHERE used_at IS NULL`},
		Stmt{Query: `INSERT INTO setup_token (hash, expires_at) VALUES (?, ?)`, Args: []any{hash, unix(expires)}},
	)
	return err
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
	return s.createFirstAdmin(ctx, tokenHash, now, a, passkeyStmt(p, now))
}

// CreateFirstAdminPassword is CreateFirstAdmin for an admin whose first credential is a
// password + authenticator code instead of a passkey. A login name that is taken (it
// cannot be, before the first admin exists) is reported like any other insert error.
func (s *Store) CreateFirstAdminPassword(ctx context.Context, tokenHash []byte, now time.Time, a Admin, c PasswordCred) error {
	c.AdminID = a.ID
	return s.createFirstAdmin(ctx, tokenHash, now, a, Stmt{
		Query: `INSERT INTO admin_password (admin_id, login, hash, totp_secret, totp_step, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
		Args: []any{c.AdminID, c.Login, c.Hash, c.TOTPSecret, c.TOTPStep, unix(now)},
	})
}

func (s *Store) createFirstAdmin(ctx context.Context, tokenHash []byte, now time.Time, a Admin, insertCred Stmt) error {
	_, err := s.batch(ctx,
		guard(`EXISTS (SELECT 1 FROM setup_token WHERE hash = ? AND used_at IS NULL AND expires_at > ?)
			AND NOT EXISTS (SELECT 1 FROM admin)`, tokenHash, unix(now)),
		Stmt{Query: `UPDATE setup_token SET used_at = ? WHERE hash = ?`, Args: []any{unix(now), tokenHash}},
		Stmt{Query: `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES (?, ?, ?, ?, ?)`,
			Args: []any{a.ID, a.DisplayName, a.Role, a.UserHandle, unix(now)}},
		insertCred,
	)
	if errors.Is(err, errGuard) {
		return ErrSetupClosed
	}
	return err
}

// passkeyStmt is the INSERT of a passkey.
func passkeyStmt(p Passkey, now time.Time) Stmt {
	if p.AAGUID == nil {
		p.AAGUID = []byte{} // nil would be bound as NULL
	}
	return Stmt{Query: `INSERT INTO passkey (id, admin_id, credential_id, public_key, attestation_type, sign_count,
		                     aaguid, transports, flags, name, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		Args: []any{p.ID, p.AdminID, p.CredentialID, p.PublicKey, p.AttestationType, int64(p.SignCount),
			p.AAGUID, strings.Join(p.Transports, ","), int64(p.Flags), p.Name, unix(now)}}
}

// ErrLastMethod is returned when removing a credential would leave the admin unable to sign in.
var ErrLastMethod = errors.New("store: last sign-in method")

// AddPasskey stores another passkey for an existing admin.
func (s *Store) AddPasskey(ctx context.Context, p Passkey, now time.Time) error {
	st := passkeyStmt(p, now)
	_, err := s.W.ExecContext(ctx, st.Query, st.Args...)
	return err
}

// DeletePasskey removes one of the admin's passkeys. It returns ErrNotFound if the admin
// has no such passkey and ErrLastMethod if it is the only way the admin can sign in.
func (s *Store) DeletePasskey(ctx context.Context, adminID, id string) error {
	results, err := s.batch(ctx,
		Stmt{Query: `SELECT EXISTS (SELECT 1 FROM passkey WHERE id = ? AND admin_id = ?) AS found,
			(SELECT count(*) FROM passkey WHERE admin_id = ?) AS passkeys,
			EXISTS (SELECT 1 FROM admin_password WHERE admin_id = ?) AS has_password`,
			Args: []any{id, adminID, adminID, adminID}, Returning: true},
		Stmt{Query: `DELETE FROM passkey WHERE id = ? AND admin_id = ?
			AND ((SELECT count(*) FROM passkey WHERE admin_id = ?) > 1
				 OR EXISTS (SELECT 1 FROM admin_password WHERE admin_id = ?))`,
			Args: []any{id, adminID, adminID, adminID}},
	)
	if err != nil {
		return err
	}
	if results[1].RowsAffected == 1 {
		return nil
	}
	found, _ := results[0].Rows[0][0].(int64)
	count, _ := results[0].Rows[0][1].(int64)
	hasPassword, _ := results[0].Rows[0][2].(int64)
	if found == 0 {
		return ErrNotFound
	}
	if count == 1 && hasPassword == 0 {
		return ErrLastMethod
	}
	return ErrNotFound
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
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
