package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Queries of the API token model. Tables: migration 00016. Only the SHA-256 of a
// token secret is ever stored.

// Token profiles, the values of api_token.profile. The auth package maps them to roles.
const (
	ProfileReadonly = "readonly"
	ProfileOperator = "operator"
	ProfileAdmin    = "admin"
)

// MaxLiveTokens is the number of unrevoked, unexpired tokens the panel holds at most.
const MaxLiveTokens = 50

var (
	// ErrNameTaken: another unrevoked token already has this name (case insensitive).
	ErrNameTaken = errors.New("store: token name taken")
	// ErrTokenLimit: MaxLiveTokens unrevoked, unexpired tokens already exist.
	ErrTokenLimit = errors.New("store: too many tokens")
)

// APIToken is one api_token row. A zero time means "unset".
type APIToken struct {
	ID, Name, Profile, Hint  string
	RatePerMin               int
	CreatedBy, CreatedByName string // admin id; the display name is resolved on reads ("" when the admin is gone)
	RevokedBy                string
	CreatedAt, ExpiresAt     time.Time
	RevokedAt, LastUsedAt    time.Time
	LastUsedIP, LastUsedVia  string
}

// Revoked reports whether the token was revoked.
func (t APIToken) Revoked() bool { return !t.RevokedAt.IsZero() }

// Expired reports whether the token's lifetime is over at now.
func (t APIToken) Expired(now time.Time) bool { return !now.Before(t.ExpiresAt) }

const tokenColumns = `t.id, t.name, t.profile, t.hint, t.rate_per_min, t.created_by, COALESCE(a.display_name, ''),
	t.revoked_by, t.created_at, t.expires_at, t.revoked_at, t.last_used_at, t.last_used_ip, t.last_used_via`

const tokenFrom = ` FROM api_token t LEFT JOIN admin a ON a.id = t.created_by `

func scanToken(r rowScanner) (APIToken, error) {
	var t APIToken
	var created, expires, revoked, used int64
	err := r.Scan(&t.ID, &t.Name, &t.Profile, &t.Hint, &t.RatePerMin, &t.CreatedBy, &t.CreatedByName,
		&t.RevokedBy, &created, &expires, &revoked, &used, &t.LastUsedIP, &t.LastUsedVia)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	t.CreatedAt, t.ExpiresAt = fromUnix(created), fromUnix(expires)
	if revoked != 0 {
		t.RevokedAt = fromUnix(revoked)
	}
	if used != 0 {
		t.LastUsedAt = fromUnix(used)
	}
	return t, nil
}

// CreateAPIToken stores a new token whose secret hashes to secretHash. The name must be free among the
// unrevoked tokens (ErrNameTaken) and fewer than MaxLiveTokens live ones may exist (ErrTokenLimit); both checks
// and the insert are one transaction on the single writer connection. t.CreatedAt is "now" for the cap.
func (s *Store) CreateAPIToken(ctx context.Context, t APIToken, secretHash []byte) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var live int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM api_token WHERE revoked_at = 0 AND expires_at > ?`,
		unix(t.CreatedAt)).Scan(&live); err != nil {
		return err
	}
	if live >= MaxLiveTokens {
		return ErrTokenLimit
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO api_token (id, name, name_key, profile, secret_hash, hint, rate_per_min, created_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, strings.ToLower(t.Name), t.Profile, secretHash, t.Hint, t.RatePerMin, t.CreatedBy,
		unix(t.CreatedAt), unix(t.ExpiresAt))
	if accIsUnique(err) && strings.Contains(err.Error(), "name_key") {
		return ErrNameTaken
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// APITokenBySecretHash finds the token for SHA-256 of a presented secret (revoked and expired ones too: the
// caller says why it refuses). ErrNotFound if there is none.
func (s *Store) APITokenBySecretHash(ctx context.Context, h []byte) (APIToken, error) {
	return scanToken(s.R.QueryRowContext(ctx, `SELECT `+tokenColumns+tokenFrom+`WHERE t.secret_hash = ?`, h))
}

// GetAPIToken returns one token. ErrNotFound if there is none.
func (s *Store) GetAPIToken(ctx context.Context, id string) (APIToken, error) {
	return scanToken(s.R.QueryRowContext(ctx, `SELECT `+tokenColumns+tokenFrom+`WHERE t.id = ?`, id))
}

// ListAPITokens returns every token, newest first, revoked and expired ones included.
func (s *Store) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+tokenColumns+tokenFrom+`ORDER BY t.created_at DESC, t.rowid DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeAPIToken revokes a token and cancels its open plans in one transaction. It is idempotent: a second call
// changes nothing (the first revoker and time stay) and returns the token. ErrNotFound for an unknown id. The
// partial unique index on the name is freed by the revocation.
func (s *Store) RevokeAPIToken(ctx context.Context, id, by string, now time.Time) (APIToken, error) {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return APIToken{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE api_token SET revoked_at = ?, revoked_by = ? WHERE id = ? AND revoked_at = 0`,
		unix(now), by, id)
	if err != nil {
		return APIToken{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM api_token WHERE id = ?`, id).Scan(&exists); err != nil {
			return APIToken{}, err
		}
		if exists == 0 {
			return APIToken{}, ErrNotFound
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mcp_plan SET status = 'cancelled' WHERE token_id = ? AND status IN ('planned', 'awaiting', 'approved')`, id); err != nil {
		return APIToken{}, err
	}
	t, err := scanToken(tx.QueryRowContext(ctx, `SELECT `+tokenColumns+tokenFrom+`WHERE t.id = ?`, id))
	if err != nil {
		return APIToken{}, err
	}
	return t, tx.Commit()
}

// TouchAPIToken records a use of the token: when, from which address and over which channel ("api" or "mcp").
func (s *Store) TouchAPIToken(ctx context.Context, id string, now time.Time, ip, via string) error {
	if via != "api" && via != "mcp" {
		via = ""
	}
	_, err := s.W.ExecContext(ctx, `UPDATE api_token SET last_used_at = ?, last_used_ip = ?, last_used_via = ? WHERE id = ?`,
		unix(now), Clip(ip, 64), via, id)
	return err
}
