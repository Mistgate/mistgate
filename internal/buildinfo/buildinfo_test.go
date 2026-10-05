package buildinfo

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltUnix(t *testing.T) {
	defer func(old string) { Built = old }(Built)
	for in, want := range map[string]int64{"": 0, "abc": 0, "-5": 0, "1790000000": 1790000000} {
		Built = in
		if got := BuiltUnix(); got != want {
			t.Errorf("Built %q: %d, want %d", in, got, want)
		}
	}
}

func TestReleasePublicKey(t *testing.T) {
	defer func(old string) { ReleaseKey = old }(ReleaseKey)
	ReleaseKey = ""
	if _, err := ReleasePublicKey(); !errors.Is(err, ErrUnsignedBuild) {
		t.Fatalf("no key: %v", err)
	}
	ReleaseKey = "not base64!"
	if _, err := ReleasePublicKey(); err == nil || errors.Is(err, ErrUnsignedBuild) {
		t.Fatalf("a broken key must not look unsigned: %v", err)
	}
	pub := make([]byte, ed25519.PublicKeySize)
	pub[0] = 7
	ReleaseKey = base64.StdEncoding.EncodeToString(pub)
	got, err := ReleasePublicKey()
	if err != nil || got[0] != 7 || len(KeyFingerprint(got)) != 16 {
		t.Fatalf("got %v %v", got, err)
	}
}

func TestLoadReleasePublicKeyPersistsAndKeepsInstallationKey(t *testing.T) {
	defer func(old string) { ReleaseKey = old }(ReleaseKey)
	dir := t.TempDir()
	want := make(ed25519.PublicKey, ed25519.PublicKeySize)
	want[0] = 42
	ReleaseKey = base64.StdEncoding.EncodeToString(want)

	got, err := LoadReleasePublicKey(dir)
	if err != nil || string(got) != string(want) {
		t.Fatalf("first load = %x, %v; want %x", got, err, want)
	}
	path := filepath.Join(dir, "release.pub")
	b, err := os.ReadFile(path)
	if err != nil || string(b) != base64.StdEncoding.EncodeToString(want)+"\n" {
		t.Fatalf("persisted release key = %q, %v", b, err)
	}

	// A generic panel binary has no compiled key; it must keep using this install's trust root.
	ReleaseKey = ""
	got, err = LoadReleasePublicKey(dir)
	if err != nil || string(got) != string(want) {
		t.Fatalf("load after binary replacement = %x, %v; want %x", got, err, want)
	}
}

// A release.pub that differs from the compiled-in key is never trusted silently, in either direction: nothing is
// trusted until the owner confirms the binary's key with TrustCompiledReleaseKey.
func TestLoadReleasePublicKeyRefusesAMismatchUntilTheKeyIsTrusted(t *testing.T) {
	defer func(old string) { ReleaseKey = old }(ReleaseKey)
	dir := t.TempDir()
	stored := make(ed25519.PublicKey, ed25519.PublicKeySize)
	stored[0] = 1
	compiled := make(ed25519.PublicKey, ed25519.PublicKeySize)
	compiled[0] = 2
	if err := os.WriteFile(filepath.Join(dir, "release.pub"), []byte(base64.StdEncoding.EncodeToString(stored)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ReleaseKey = base64.StdEncoding.EncodeToString(compiled)
	if got, err := LoadReleasePublicKey(dir); !errors.Is(err, ErrReleaseKeyMismatch) || got != nil {
		t.Fatalf("mismatch = %x, %v", got, err)
	}

	previous, current, err := TrustCompiledReleaseKey(dir)
	if err != nil || !previous.Equal(stored) || !current.Equal(compiled) {
		t.Fatalf("trust = %x %x %v", previous, current, err)
	}
	if got, err := LoadReleasePublicKey(dir); err != nil || !got.Equal(compiled) {
		t.Fatalf("after the rotation = %x, %v", got, err)
	}

	ReleaseKey = ""
	if _, _, err := TrustCompiledReleaseKey(dir); !errors.Is(err, ErrUnsignedBuild) {
		t.Fatalf("an unsigned build trusted its (missing) key: %v", err)
	}
}

func TestLoadReleasePublicKeyRejectsInvalidStoredKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "release.pub"), []byte("broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReleasePublicKey(dir); err == nil {
		t.Fatal("accepted malformed persisted release key")
	}
}

func TestLoadReleasePublicKeyRequiresDataDirectory(t *testing.T) {
	if _, err := LoadReleasePublicKey(""); err == nil {
		t.Fatal("accepted an empty data directory")
	}
}

func TestWriteReleaseKeyDoesNotReplaceExistingTrustRoot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "release.pub")
	want := make(ed25519.PublicKey, ed25519.PublicKeySize)
	want[0] = 42
	other := make(ed25519.PublicKey, ed25519.PublicKeySize)
	other[0] = 99

	if err := writeReleaseKey(path, want, os.Link); err != nil {
		t.Fatalf("write initial trust root: %v", err)
	}
	if err := writeReleaseKey(path, other, os.Link); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second write error = %v, want %v", err, os.ErrExist)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeReleaseKey(strings.TrimSpace(string(b)))
	if err != nil || string(got) != string(want) {
		t.Fatalf("trust root after competing write = %x, %v; want original %x", got, err, want)
	}
}
