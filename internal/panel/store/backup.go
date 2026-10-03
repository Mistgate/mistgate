package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// PanelBackupSettings contains the saved R2 configuration. SecretAccessKey is
// vault ciphertext and must never be returned by an admin RPC.
type PanelBackupSettings struct {
	AccountID, Jurisdiction, Bucket, AccessKeyID, AgeRecipient string
	SecretAccessKey                                            []byte
	Enabled                                                    bool
	IntervalHours, RetentionDays                               int
	LastSuccess                                                time.Time
	LastErrorCode                                              string
	UpdatedAt                                                  time.Time
}

func (s *Store) PanelBackupSettings(ctx context.Context) (PanelBackupSettings, error) {
	var v PanelBackupSettings
	var enabled int
	var lastSuccess, updated int64
	err := s.R.QueryRowContext(ctx, `SELECT account_id, jurisdiction, bucket, access_key_id, secret_access_key,
		age_recipient, enabled, interval_hours, retention_days, last_success, last_error_code, updated_at
		FROM panel_backup_settings WHERE id = 1`).Scan(
		&v.AccountID, &v.Jurisdiction, &v.Bucket, &v.AccessKeyID, &v.SecretAccessKey,
		&v.AgeRecipient, &enabled, &v.IntervalHours, &v.RetentionDays, &lastSuccess, &v.LastErrorCode, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return PanelBackupSettings{Jurisdiction: "default", IntervalHours: 24}, nil
	}
	if err != nil {
		return PanelBackupSettings{}, err
	}
	v.Enabled = enabled != 0
	if lastSuccess != 0 {
		v.LastSuccess = fromUnix(lastSuccess)
	}
	if updated != 0 {
		v.UpdatedAt = fromUnix(updated)
	}
	return v, nil
}

// SavePanelBackupSettings replaces owner-editable settings while retaining the
// last successful run and status.
func (s *Store) SavePanelBackupSettings(ctx context.Context, v PanelBackupSettings, now time.Time) error {
	_, err := s.W.ExecContext(ctx, `INSERT INTO panel_backup_settings
		(id, account_id, jurisdiction, bucket, access_key_id, secret_access_key, age_recipient, enabled,
		 interval_hours, retention_days, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET account_id = excluded.account_id, jurisdiction = excluded.jurisdiction,
			bucket = excluded.bucket, access_key_id = excluded.access_key_id,
			secret_access_key = excluded.secret_access_key, age_recipient = excluded.age_recipient,
			enabled = excluded.enabled, interval_hours = excluded.interval_hours,
			retention_days = excluded.retention_days, updated_at = excluded.updated_at`,
		v.AccountID, v.Jurisdiction, v.Bucket, v.AccessKeyID, v.SecretAccessKey, v.AgeRecipient,
		v.Enabled, v.IntervalHours, v.RetentionDays, unix(now))
	return err
}

func (s *Store) RecordPanelBackupRun(ctx context.Context, now time.Time, succeeded bool, errorCode string) error {
	success := int64(0)
	if succeeded {
		success = unix(now)
	}
	result, err := s.W.ExecContext(ctx, `UPDATE panel_backup_settings SET last_success = CASE WHEN ? = 0 THEN last_success ELSE ? END,
		last_error_code = ?, updated_at = ? WHERE id = 1`, success, success, errorCode, unix(now))
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrNotFound
	}
	return nil
}
