//go:build !js

package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRestoreCLIRequiresExactlyOneIdentitySource(t *testing.T) {
	err := RunCLI([]string{
		"restore", "--identity-file", "identity.txt", "--passphrase", "--file", "backup.tar.gz.age", "--data-dir", "restored",
	}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "usage: mistgate backup restore") {
		t.Fatalf("restore with multiple identity sources = %v", err)
	}

	err = RunCLI([]string{"restore", "--file", "backup.tar.gz.age", "--data-dir", "restored"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "usage: mistgate backup restore") {
		t.Fatalf("restore without an identity source = %v", err)
	}
}

func TestRestoreCLIReadsFirstPassphraseFileLine(t *testing.T) {
	passphrase := "  passphrase with spaces  "
	archive, _, marker := makeScryptTestArchive(t, passphrase)
	directory := t.TempDir()
	archivePath := filepath.Join(directory, "backup.tar.gz.age")
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	passphrasePath := filepath.Join(directory, "passphrase.txt")
	if err := os.WriteFile(passphrasePath, []byte(passphrase+"\r\nignored second line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(directory, "restored")
	if err := RunCLI([]string{
		"restore", "--passphrase-file", passphrasePath, "--file", archivePath, "--data-dir", restored,
	}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(restored, "fixture.txt"))
	if err != nil || string(got) != marker {
		t.Fatalf("restored fixture = %q, %v; want %q", got, err, marker)
	}
}

func TestRestoreCLIPrintsTheRestoredPanelURL(t *testing.T) {
	s, _, identity := newTestService(t)
	if err := s.st.SetSettings(context.Background(), map[string]string{
		"public_url": "https://panel.example.com/sub/", "admin_prefix": "/admin/", "admin_host": "",
	}); err != nil {
		t.Fatal(err)
	}
	archive := restoreArchiveFixture(t, s, identity.Recipient(), "v1.2.3", time.Now().UTC())
	directory := t.TempDir()
	archivePath := filepath.Join(directory, "backup.tar.gz.age")
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(directory, "identity.txt")
	if err := os.WriteFile(identityPath, []byte(identity.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := RunCLI([]string{"restore", "--identity-file", identityPath, "--file", archivePath, "--data-dir", filepath.Join(directory, "restored")}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Panel URL: https://panel.example.com/sub/admin/") {
		t.Fatalf("restore output does not identify the saved admin URL: %s", output.String())
	}
}
