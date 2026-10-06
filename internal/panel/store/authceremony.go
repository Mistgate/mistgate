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
	ID            string
	Kind          string
	Source        string
	SessionData   string
	TokenHash     []byte
	AdminJSON     string
	Name          string
	Login         string
	PasswordHash  string
	TOTPEncrypted []byte
	Tries         int
	ExpiresAt     time.Time
}

// PutAuthCeremony prunes up to 100 expired rows, then inserts only while both caps allow it.
func (s *Store) PutAuthCeremony(ctx context.Context, c AuthCeremony, now time.Time, maxPerSource, maxTotal int) error {
	if c.TokenHash == nil {
		c.TokenHash = []byte{}
	}
	if c.TOTPEncrypted == nil {
		c.TOTPEncrypted = []byte{}
	}
	if _, err := s.W.ExecContext(ctx, `DELETE FROM auth_ceremony WHERE id IN (
		SELECT id FROM auth_ceremony WHERE expires_at <= ? ORDER BY expires_at LIMIT 100
	)`, now.UnixMilli()); err != nil {
		return err
	}
	result, err := s.W.ExecContext(ctx, `
		INSERT INTO auth_ceremony (
			id, kind, source_key, session_data, token_hash, admin_json, name, login,
			password_hash, totp_enc, tries, expires_at
		)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE (SELECT count(*) FROM auth_ceremony WHERE source_key = ? AND expires_at > ?) < ?
		  AND (SELECT count(*) FROM auth_ceremony WHERE expires_at > ?) < ?`,
		c.ID, c.Kind, c.Source, c.SessionData, c.TokenHash, c.AdminJSON, c.Name, c.Login,
		c.PasswordHash, c.TOTPEncrypted, c.Tries, c.ExpiresAt.UnixMilli(),
		c.Source, now.UnixMilli(), maxPerSource, now.UnixMilli(), maxTotal,
	)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAuthCeremonyLimit
	}
	return nil
}

// TakeAuthCeremony atomically removes one live row (single use: of two racing finishes one gets it). The caller checks the
// kind; a finish with the wrong kind still consumes the ceremony.
func (s *Store) TakeAuthCeremony(ctx context.Context, id string, now time.Time) (AuthCeremony, error) {
	var c AuthCeremony
	var expires int64
	err := s.W.QueryRowContext(ctx, `
		DELETE FROM auth_ceremony
		WHERE id = ? AND expires_at > ?
		RETURNING id, kind, source_key, session_data, token_hash, admin_json, name, login,
		          password_hash, totp_enc, tries, expires_at`,
		id, now.UnixMilli(),
	).Scan(&c.ID, &c.Kind, &c.Source, &c.SessionData, &c.TokenHash, &c.AdminJSON, &c.Name, &c.Login,
		&c.PasswordHash, &c.TOTPEncrypted, &c.Tries, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return AuthCeremony{}, ErrAuthCeremonyNotFound
	}
	if err != nil {
		return AuthCeremony{}, err
	}
	c.ExpiresAt = time.UnixMilli(expires)
	return c, nil
}

// RestoreAuthCeremony restores a ceremony with its original expiry after a recoverable typo.
func (s *Store) RestoreAuthCeremony(ctx context.Context, c AuthCeremony) error {
	if c.TokenHash == nil {
		c.TokenHash = []byte{}
	}
	if c.TOTPEncrypted == nil {
		c.TOTPEncrypted = []byte{}
	}
	_, err := s.W.ExecContext(ctx, `
		INSERT INTO auth_ceremony (
			id, kind, source_key, session_data, token_hash, admin_json, name, login,
			password_hash, totp_enc, tries, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		c.ID, c.Kind, c.Source, c.SessionData, c.TokenHash, c.AdminJSON, c.Name, c.Login,
		c.PasswordHash, c.TOTPEncrypted, c.Tries, c.ExpiresAt.UnixMilli(),
	)
	return err
}
