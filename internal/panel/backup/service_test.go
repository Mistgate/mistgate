package backup

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// newTestService is a backup service with valid, enabled settings whose R2 is the fake.
func newTestService(t *testing.T) (*Service, *fakeS3, *age.HybridIdentity) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, databaseName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key, err := vault.LoadKey(dataDir, true)
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{Store: st, Vault: v, DataDir: dataDir, MasterKey: key})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SavePanelBackupSettings(ctx, store.PanelBackupSettings{
		AccountID: strings.Repeat("a", 32), Jurisdiction: "default", Bucket: "bucket", AccessKeyID: "key",
		SecretAccessKey: v.Seal([]byte("secret"), secretRecordID), AgeRecipient: identity.Recipient().String(),
		Enabled: true, IntervalHours: 24,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	api := &fakeS3{}
	s.r2Factory = func(context.Context, store.PanelBackupSettings) (s3API, error) { return api, nil }
	return s, api, identity
}

// A failing backup is not retried every minute: each failure doubles the wait, and new settings end it.
func TestScheduledBackupBacksOffAfterFailures(t *testing.T) {
	s, _, _ := newTestService(t)
	calls := 0
	s.r2Factory = func(context.Context, store.PanelBackupSettings) (s3API, error) {
		calls++
		return nil, errBackupStorageFailed
	}
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	ctx := context.Background()
	for i, step := range []struct {
		after time.Duration
		calls int
	}{
		{0, 1},               // the first attempt fails: 2 minutes until the next
		{time.Minute, 1},     // the scheduler ticks every minute
		{time.Minute, 2},     // 2 minutes on, it fails again: 4 minutes
		{3 * time.Minute, 2}, //
		{time.Minute, 3},     // 4 minutes on
	} {
		now = now.Add(step.after)
		s.runDue(ctx)
		if calls != step.calls {
			t.Fatalf("step %d: %d attempts, want %d", i, calls, step.calls)
		}
	}
	s.backoff(false, 0) // what saving the settings does
	if s.runDue(ctx); calls != 4 {
		t.Errorf("new settings did not get an attempt at once: %d attempts", calls)
	}
	if d := retryDelay(30, 24*time.Hour); d != 24*time.Hour {
		t.Errorf("the backoff passed the interval: %v", d)
	}
}

// A backup works in the data directory, not in the temporary directory (a tmpfs counted against the panel's memory),
// and holds no copy of the data there: the snapshot and the archive only. A work directory a crash left is removed, and
// none of them ends up in the archive.
func TestBackupStagesInTheDataDirectoryWithoutCopies(t *testing.T) {
	sysTmp := filepath.Join(t.TempDir(), "tmp")
	if err := os.Mkdir(sysTmp, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(env, sysTmp)
	}
	s, api, identity := newTestService(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(s.dataDir, "big.bin"), bytes.Repeat([]byte("x"), 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	crashed := filepath.Join(s.dataDir, stagePrefix+"crashed")
	if err := os.Mkdir(crashed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crashed, "snapshot.db"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	var staged []string
	api.onPut = func() {
		if entries, _ := os.ReadDir(sysTmp); len(entries) != 0 {
			t.Errorf("the backup wrote to the temporary directory: %v", entries)
		}
		filepath.WalkDir(s.dataDir, func(p string, d fs.DirEntry, err error) error {
			if rel, _ := filepath.Rel(s.dataDir, p); err == nil && !d.IsDir() && strings.HasPrefix(rel, stagePrefix) {
				staged = append(staged, filepath.ToSlash(rel[strings.IndexAny(rel, `/\`)+1:]))
			}
			return nil
		})
	}
	if _, _, err := s.createBackup(ctx); err != nil {
		t.Fatal(err)
	}
	slices.Sort(staged)
	if !slices.Equal(staged, []string{"mistgate-backup.tar.gz.age", "snapshot.db"}) {
		t.Errorf("work directory during the upload = %v, want the archive and the snapshot only", staged)
	}
	if entries, _ := os.ReadDir(s.dataDir); slices.ContainsFunc(entries, func(e os.DirEntry) bool { return strings.HasPrefix(e.Name(), stagePrefix) }) {
		t.Errorf("a work directory was left in the data directory: %v", entries)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := RestoreEncryptedArchive(ctx, bytes.NewReader(api.lastPut), []age.Identity{identity}, restored); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(restored, "big.bin")); err != nil || len(b) != 1<<20 {
		t.Errorf("big.bin was not restored: %d bytes, %v", len(b), err)
	}
	if entries, _ := os.ReadDir(restored); slices.ContainsFunc(entries, func(e os.DirEntry) bool { return strings.HasPrefix(e.Name(), stagePrefix) }) {
		t.Errorf("a work directory is in the archive: %v", entries)
	}
}
