package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// TakeLease atomically takes name for holder until now+ttl, if no current lease exists or it has expired.
func (s *Store) TakeLease(ctx context.Context, name, holder string, ttl time.Duration, now time.Time) (bool, error) {
	if name == "" || holder == "" || ttl <= 0 {
		return false, errors.New("store: lease name, holder, and positive ttl are required")
	}

	expires := now.UTC().Add(ttl)
	expiresAt := expires.Unix()
	if expires.Nanosecond() != 0 {
		expiresAt++ // store whole seconds without shortening the lease's deadline
	}
	var got string
	err := s.W.QueryRowContext(ctx, `
		INSERT INTO job_lease (name, holder, expires_at) VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET holder = excluded.holder, expires_at = excluded.expires_at
		WHERE job_lease.expires_at <= ?
		RETURNING holder`, name, holder, expiresAt, now.UTC().Unix()).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return got == holder, nil
}

// ReleaseLease deletes name only if holder still owns it.
func (s *Store) ReleaseLease(ctx context.Context, name, holder string) error {
	_, err := s.W.ExecContext(ctx, `DELETE FROM job_lease WHERE name = ? AND holder = ?`, name, holder)
	return err
}
