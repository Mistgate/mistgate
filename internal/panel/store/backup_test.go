package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestPanelBackupSettingsPersistenceAndRunStatus(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	defaults, err := s.PanelBackupSettings(ctx)
	if err != nil || defaults.Jurisdiction != "default" || defaults.IntervalHours != 24 || defaults.Enabled {
		t.Fatalf("default backup settings = %+v, %v", defaults, err)
	}

	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	saved := PanelBackupSettings{
		AccountID: "0123456789abcdef0123456789abcdef", Jurisdiction: "eu", Bucket: "panel-backups",
		AccessKeyID: "access-id", SecretAccessKey: []byte{0x01, 0x02, 0x03}, AgeRecipient: "age1recipient",
		Enabled: true, IntervalHours: 6, RetentionDays: 30, UpdatedAt: now,
	}
	if err := s.SavePanelBackupSettings(ctx, saved, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.PanelBackupSettings(ctx)
	if err != nil || !reflect.DeepEqual(got, saved) {
		t.Fatalf("saved backup settings = %+v, %v; want %+v", got, err, saved)
	}

	firstSuccess := now.Add(time.Hour)
	if err := s.RecordPanelBackupRun(ctx, firstSuccess, true, ""); err != nil {
		t.Fatal(err)
	}
	failedLater := firstSuccess.Add(2 * time.Hour)
	if err := s.RecordPanelBackupRun(ctx, failedLater, false, "backup_storage_failed"); err != nil {
		t.Fatal(err)
	}
	got, err = s.PanelBackupSettings(ctx)
	if err != nil || !got.LastSuccess.Equal(firstSuccess) || got.LastErrorCode != "backup_storage_failed" {
		t.Fatalf("run status = %+v, %v", got, err)
	}

	saved.RetentionDays = 0
	if err := s.SavePanelBackupSettings(ctx, saved, failedLater); err != nil {
		t.Fatal(err)
	}
	got, err = s.PanelBackupSettings(ctx)
	if err != nil || got.RetentionDays != 0 || !got.LastSuccess.Equal(firstSuccess) || got.LastErrorCode != "backup_storage_failed" {
		t.Fatalf("settings edit lost run status: %+v, %v", got, err)
	}
}

func TestSnapshotDatabaseIsPrivateAndRefusesOverwrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetSettings(ctx, map[string]string{"snapshot": "included"}); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "snapshot.db")
	if err := s.SnapshotDatabase(ctx, dst); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Errorf("snapshot permissions are too broad: %o", info.Mode().Perm())
	}
	if err := s.SnapshotDatabase(ctx, dst); !errors.Is(err, ErrConflict) {
		t.Errorf("second snapshot error = %v, want ErrConflict", err)
	}
	copy, err := Open(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	if value, err := copy.Setting(ctx, "snapshot"); err != nil || value != "included" {
		t.Errorf("snapshot value = %q, %v", value, err)
	}
}
