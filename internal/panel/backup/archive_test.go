//go:build !js

package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

func TestEncryptedArchiveRoundTripIncludesCommittedWAL(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, databaseName)
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key, err := vault.LoadKey(dataDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `PRAGMA wal_autocheckpoint = 0`); err != nil {
		t.Fatal(err)
	}
	const marker = "panel-data-secret-that-must-not-appear-in-the-archive"
	if err := st.SetSettings(ctx, map[string]string{"backup_fixture": marker}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dbPath + "-wal"); err != nil || info.Size() <= 32 {
		t.Fatalf("expected committed data to remain in WAL, stat = %+v, err = %v", info, err)
	}
	if err := os.Mkdir(filepath.Join(dataDir, "credentials"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "credentials", "remote.txt"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}

	identity, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	snapshot := filepath.Join(workDir, "snapshot.db")
	if err := st.SnapshotDatabase(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if err := CreateEncryptedArchive(ctx, dataDir, snapshot, key, "test-version", identity.Recipient().String(), time.Now(), &encrypted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted.Bytes(), []byte(marker)) || bytes.Contains(encrypted.Bytes(), key) {
		t.Fatal("archive contains panel plaintext")
	}

	restored := filepath.Join(t.TempDir(), "restored")
	if err := RestoreEncryptedArchive(ctx, bytes.NewReader(encrypted.Bytes()), []age.Identity{identity}, restored); err != nil {
		t.Fatal(err)
	}
	restoredStore, err := store.Open(ctx, filepath.Join(restored, databaseName))
	if err != nil {
		t.Fatal(err)
	}
	got, err := restoredStore.Setting(ctx, "backup_fixture")
	if err != nil || got != marker {
		t.Errorf("restored setting = %q, %v; want %q", got, err, marker)
	}
	if err := restoredStore.Close(); err != nil {
		t.Fatal(err)
	}
	gotFile, err := os.ReadFile(filepath.Join(restored, "credentials", "remote.txt"))
	if err != nil || string(gotFile) != marker {
		t.Errorf("restored file = %q, %v; want %q", gotFile, err, marker)
	}
	restoredKey, err := os.ReadFile(filepath.Join(restored, masterKeyName))
	if err != nil || !bytes.Equal(restoredKey, key) {
		t.Errorf("restored master key does not match the source")
	}
}

func TestEncryptedArchiveLimitWriterRejectsTheFirstByteOverLimit(t *testing.T) {
	var output bytes.Buffer
	writer := &encryptedArchiveLimitWriter{dst: &output, remaining: 5}
	if n, err := writer.Write([]byte("1234")); n != 4 || err != nil {
		t.Fatalf("first write = %d, %v", n, err)
	}
	if n, err := writer.Write([]byte("56")); n != 0 || !errors.Is(err, errArchiveTooLarge) {
		t.Fatalf("over-limit write = %d, %v", n, err)
	}
	if got := output.String(); got != "1234" {
		t.Fatalf("writer emitted %q beyond the limit", got)
	}
}

func TestScryptArchiveRoundTripAndWrongPassphrase(t *testing.T) {
	const passphrase = "correct horse battery staple"
	archive, wantKey, marker := makeScryptTestArchive(t, passphrase)
	ctx := context.Background()

	wrongPassphrase := "a wrong phrase that must not be reported"
	wrong, err := age.NewScryptIdentity(wrongPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	wrongTarget := filepath.Join(t.TempDir(), "wrong")
	if err := RestoreEncryptedArchive(ctx, bytes.NewReader(archive), []age.Identity{wrong}, wrongTarget); err == nil || strings.Contains(err.Error(), wrongPassphrase) {
		t.Fatalf("wrong passphrase restore error = %v", err)
	}
	if _, err := os.Lstat(wrongTarget); !os.IsNotExist(err) {
		t.Fatalf("wrong passphrase left a destination behind: %v", err)
	}

	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := RestoreEncryptedArchive(ctx, bytes.NewReader(archive), []age.Identity{identity}, restored); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(restored, "fixture.txt"))
	if err != nil || string(got) != marker {
		t.Errorf("restored fixture = %q, %v; want %q", got, err, marker)
	}
	gotKey, err := os.ReadFile(filepath.Join(restored, masterKeyName))
	if err != nil || !bytes.Equal(gotKey, wantKey) {
		t.Errorf("restored master key does not match source: %v", err)
	}
	st, err := store.Open(ctx, filepath.Join(restored, databaseName))
	if err != nil {
		t.Fatal(err)
	}
	gotSetting, err := st.Setting(ctx, "scrypt_fixture")
	closeErr := st.Close()
	if err != nil || gotSetting != marker || closeErr != nil {
		t.Errorf("restored setting = %q, read %v, close %v; want %q", gotSetting, err, closeErr, marker)
	}
}

func makeScryptTestArchive(t *testing.T, passphrase string) ([]byte, []byte, string) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, databaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key, err := vault.LoadKey(dataDir, true)
	if err != nil {
		t.Fatal(err)
	}
	const marker = "scrypt-backup-fixture"
	if err := st.SetSettings(ctx, map[string]string{"scrypt_fixture": marker}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "fixture.txt"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err := st.SnapshotDatabase(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if err := CreateEncryptedArchiveWithRecipient(ctx, dataDir, snapshot, key, "0.1.0-dev", recipient, time.Now(), &encrypted); err != nil {
		t.Fatal(err)
	}
	return encrypted.Bytes(), key, marker
}

// An archive from a newer panel, whose database is past the migrations of this binary, is refused with a clear message:
// goose would take that database, and this code would not know its tables.
func TestRestoreRefusesANewerSchema(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, databaseName)
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key, err := vault.LoadKey(dataDir, true)
	if err != nil {
		t.Fatal(err)
	}
	_, latest, err := store.SchemaVersions(ctx, dbPath)
	if err != nil || latest < 40 {
		t.Fatalf("this binary's schema = %d, %v", latest, err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, 1)`, latest+1); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err := st.SnapshotDatabase(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if err := CreateEncryptedArchive(ctx, dataDir, snapshot, key, "v9.9.9", identity.Recipient().String(), time.Now(), &encrypted); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	err = RestoreEncryptedArchive(ctx, bytes.NewReader(encrypted.Bytes()), []age.Identity{identity}, target)
	if err == nil || !strings.Contains(err.Error(), "newer Mistgate (v9.9.9)") {
		t.Fatalf("restore of a newer schema = %v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused restore created the destination: %v", err)
	}
}

func TestRestoreRejectsMasterKeyThatDoesNotOpenSealedDatabaseValues(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, databaseName)
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	correctKey := bytes.Repeat([]byte{0x42}, vault.KeySize)
	correctVault, err := vault.New(correctKey)
	if err != nil {
		t.Fatal(err)
	}
	sealed := correctVault.Seal([]byte("private test value"), "setting/turnstile_secret")
	if err := st.SetSettings(ctx, map[string]string{
		"turnstile_secret": base64.StdEncoding.EncodeToString(sealed),
	}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"DROP TABLE auth_ceremony",
		"DROP TABLE awg_retained_key",
		"DROP TABLE health_probe_cred",
		"ALTER TABLE node_server_access DROP COLUMN pending_password",
		"ALTER TABLE mcp_plan DROP COLUMN owner_secret",
	} {
		if _, err := st.W.ExecContext(ctx, statement); err != nil {
			t.Fatalf("make archive schema older at %q: %v", statement, err)
		}
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err := st.SnapshotDatabase(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	wrongKey := make([]byte, vault.KeySize)
	if _, err := rand.Read(wrongKey); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	var correctArchive bytes.Buffer
	if err := CreateEncryptedArchiveWithRecipient(ctx, dataDir, snapshot, correctKey, "test-version", identity.Recipient(), time.Now(), &correctArchive); err != nil {
		t.Fatal(err)
	}
	correctTarget := filepath.Join(t.TempDir(), "restore-correct")
	if err := RestoreEncryptedArchive(ctx, bytes.NewReader(correctArchive.Bytes()), []age.Identity{identity}, correctTarget); err != nil {
		t.Fatalf("restore with correct key and an older schema: %v", err)
	}
	restoredStore, err := store.Open(ctx, filepath.Join(correctTarget, databaseName))
	if err != nil {
		t.Fatalf("open restored database with dropped sealed schema: %v", err)
	}
	defer restoredStore.Close()
	for _, table := range []string{"auth_ceremony", "awg_retained_key", "health_probe_cred"} {
		var exists int
		if err := restoredStore.R.QueryRowContext(ctx,
			"SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)", table,
		).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != 0 {
			t.Errorf("restored schema unexpectedly contains dropped table %s", table)
		}
	}
	for _, missingColumn := range []struct{ table, column string }{
		{"node_server_access", "pending_password"},
		{"mcp_plan", "owner_secret"},
	} {
		var count int
		if err := restoredStore.R.QueryRowContext(ctx,
			"SELECT count(*) FROM pragma_table_info(?) WHERE name = ?", missingColumn.table, missingColumn.column,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("restored schema unexpectedly contains dropped column %s.%s", missingColumn.table, missingColumn.column)
		}
	}

	var encrypted bytes.Buffer
	if err := CreateEncryptedArchiveWithRecipient(ctx, dataDir, snapshot, wrongKey, "test-version", identity.Recipient(), time.Now(), &encrypted); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restore")
	err = RestoreEncryptedArchive(ctx, bytes.NewReader(encrypted.Bytes()), []age.Identity{identity}, target)
	if err == nil || !strings.Contains(err.Error(), "does not match master.key") {
		t.Fatalf("restore with mismatched master key = %v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused restore left destination behind: %v", err)
	}
}

func TestRestoreAllowsAnEmptyDatabaseWithNoSealedValues(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, databaseName))
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x19}, vault.KeySize)
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err := st.SnapshotDatabase(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	var encrypted bytes.Buffer
	if err := CreateEncryptedArchiveWithRecipient(ctx, dataDir, snapshot, key, "test-version", identity.Recipient(), time.Now(), &encrypted); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restore")
	if err := RestoreEncryptedArchive(ctx, bytes.NewReader(encrypted.Bytes()), []age.Identity{identity}, target); err != nil {
		t.Fatalf("empty archive restore with valid random master key: %v", err)
	}
}

func TestRestoreRejectsWrongIdentityAndNeverOverwrites(t *testing.T) {
	identity, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	archive := makeMinimalArchive(t, identity, "safe.txt", []byte("safe"))
	parent := t.TempDir()
	target := filepath.Join(parent, "new-panel")
	if err := RestoreEncryptedArchive(context.Background(), bytes.NewReader(archive), []age.Identity{wrong}, target); err == nil {
		t.Fatal("restore with a different identity succeeded")
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed restore created destination: %v", err)
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	const sentinel = "leave this directory alone"
	if err := os.WriteFile(filepath.Join(target, "sentinel"), []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreEncryptedArchive(context.Background(), bytes.NewReader(archive), []age.Identity{identity}, target); err == nil {
		t.Fatal("restore overwrote an existing destination")
	}
	got, err := os.ReadFile(filepath.Join(target, "sentinel"))
	if err != nil || string(got) != sentinel {
		t.Fatalf("existing destination changed: %q, %v", got, err)
	}
}

func TestRestoreRejectsTraversalAndCorruption(t *testing.T) {
	identity, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	traversal := makeTraversalArchive(t, identity)
	parent := t.TempDir()
	target := filepath.Join(parent, "restore")
	if err := RestoreEncryptedArchive(context.Background(), bytes.NewReader(traversal), []age.Identity{identity}, target); err == nil {
		t.Fatal("archive traversal was accepted")
	}
	if _, err := os.Lstat(filepath.Join(parent, "escape")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive wrote outside its staging directory: %v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected archive created destination: %v", err)
	}

	valid := makeMinimalArchive(t, identity, "safe.txt", []byte("safe"))
	valid[len(valid)/2] ^= 0x80
	if err := RestoreEncryptedArchive(context.Background(), bytes.NewReader(valid), []age.Identity{identity}, target); err == nil {
		t.Fatal("tampered encrypted archive was accepted")
	}
	trailing := makeMinimalArchive(t, identity, "safe.txt", []byte("safe"), []byte("not tar padding"))
	if err := RestoreEncryptedArchive(context.Background(), bytes.NewReader(trailing), []age.Identity{identity}, target); err == nil {
		t.Fatal("nonzero bytes after the tar end markers were accepted")
	}
}

func TestBackupKeygenWritesPrivateIdentityOnlyOnce(t *testing.T) {
	identityPath := filepath.Join(t.TempDir(), "recovery.txt")
	var out bytes.Buffer
	if err := RunCLI([]string{"keygen", "--identity-file", identityPath}, &out); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Errorf("private identity permissions are too broad: %o", info.Mode().Perm())
	}
	contents, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	identities, err := age.ParseIdentities(bytes.NewReader(contents))
	if err != nil || len(identities) != 1 {
		t.Fatalf("keygen wrote an invalid identity: %v", err)
	}
	_, publicPart, found := strings.Cut(out.String(), "R2 recipient: ")
	if !found {
		t.Fatal("keygen did not print a public recipient")
	}
	if _, err := age.ParseHybridRecipient(strings.TrimSpace(publicPart)); err != nil {
		t.Fatalf("keygen printed an invalid public recipient: %v", err)
	}
	if err := RunCLI([]string{"keygen", "--identity-file", identityPath}, &out); err == nil {
		t.Fatal("keygen replaced an existing recovery identity")
	}
}

// "mistgate backup -h" prints the usage instead of "unknown command".
func TestBackupHelp(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "help"} {
		var out bytes.Buffer
		if err := RunCLI([]string{arg}, &out); err != nil || !strings.Contains(out.String(), "keygen") || !strings.Contains(out.String(), "restore (--identity-file FILE | --passphrase | --passphrase-file FILE)") {
			t.Errorf("%s: err=%v out=%q", arg, err, out.String())
		}
	}
	if err := RunCLI([]string{"nope"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("unknown command: %v", err)
	}
}

func makeMinimalArchive(t *testing.T, identity *age.HybridIdentity, name string, content []byte, trailing ...[]byte) []byte {
	t.Helper()
	var output bytes.Buffer
	encrypted, err := age.Encrypt(&output, identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(encrypted)
	tw := tar.NewWriter(compressed)
	hash := sha256.Sum256(content)
	manifest, err := json.Marshal(backupManifest{
		FormatVersion: backupFormatVersion,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Files:         []backupFile{{Path: name, Size: int64(len(content)), SHA256: hex.EncodeToString(hash[:])}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeTarFile(tw, manifestName, manifest, 0o600, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	for _, bytes := range trailing {
		if _, err := compressed.Write(bytes); err != nil {
			t.Fatal(err)
		}
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encrypted.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func makeTraversalArchive(t *testing.T, identity *age.HybridIdentity) []byte {
	t.Helper()
	content := []byte("outside")
	hash := sha256.Sum256(content)
	name := "../escape"
	var output bytes.Buffer
	encrypted, err := age.Encrypt(&output, identity.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(encrypted)
	tw := tar.NewWriter(compressed)
	manifest, err := json.Marshal(backupManifest{
		FormatVersion: backupFormatVersion,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		Files:         []backupFile{{Path: name, Size: int64(len(content)), SHA256: hex.EncodeToString(hash[:])}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeTarFile(tw, manifestName, manifest, 0o600, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encrypted.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

// A passphrase restore refuses an archive whose header asks for more scrypt work than the cap, and takes one at the cap.
func TestPassphraseRestoreCapsTheScryptWorkFactor(t *testing.T) {
	const passphrase = "correct horse battery staple"
	defer func(v int) { maxScryptWorkFactor = v }(maxScryptWorkFactor)
	maxScryptWorkFactor = 10
	for _, tc := range []struct {
		logN int
		ok   bool
	}{{10, true}, {11, false}} {
		recipient, err := age.NewScryptRecipient(passphrase)
		if err != nil {
			t.Fatal(err)
		}
		recipient.SetWorkFactor(tc.logN)
		var enc bytes.Buffer
		w, err := age.Encrypt(&enc, recipient)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte("x"))
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		identity, err := passphraseIdentity(passphrase)
		if err != nil {
			t.Fatal(err)
		}
		_, err = age.Decrypt(bytes.NewReader(enc.Bytes()), identity)
		if (err == nil) != tc.ok {
			t.Errorf("logN %d: err = %v, want ok=%v", tc.logN, err, tc.ok)
		}
	}
}
