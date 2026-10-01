package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/release"
)

// keygen writes the private key with mode 0600, never replaces one, and prints the public key and its fingerprint.
func TestReleaseKeygen(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "release.key")
	var out bytes.Buffer
	if err := runRelease([]string{"keygen", "--out", keyFile}, &out); err != nil {
		t.Fatal(err)
	}
	pubB64 := lineValue(t, out.String(), "public key: ")
	fp := lineValue(t, out.String(), "fingerprint: ")
	if !strings.Contains(out.String(), "Keep the key file offline and back it up") {
		t.Fatalf("no warning: %q", out.String())
	}
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := release.DecodePrivateKey(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	pub, err := decodeTestKey(pubB64)
	if err != nil || !priv.Public().(ed25519.PublicKey).Equal(pub) {
		t.Fatalf("the printed public key is not the key file's: %v", err)
	}
	if fp != buildinfo.KeyFingerprint(pub) {
		t.Fatalf("fingerprint %q", fp)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(keyFile); fi.Mode().Perm() != 0o600 {
			t.Fatalf("private key mode %v, want 0600", fi.Mode().Perm())
		}
	}
	// a second run with the same path refuses and leaves the key alone
	if err := runRelease([]string{"keygen", "--out", keyFile}, &out); err == nil {
		t.Fatal("an existing key file was overwritten")
	}
	if again, _ := os.ReadFile(keyFile); !bytes.Equal(again, raw) {
		t.Fatal("the key file changed")
	}
	if err := runRelease([]string{"keygen"}, &out); err == nil {
		t.Fatal("keygen without --out")
	}
	if err := runRelease([]string{"frobnicate"}, &out); err == nil {
		t.Fatal("an unknown subcommand")
	}
	if err := runRelease(nil, &out); err == nil {
		t.Fatal("no subcommand")
	}
}

func lineValue(t *testing.T, out, prefix string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, prefix); ok {
			return strings.TrimSpace(v)
		}
	}
	t.Fatalf("no %q line in:\n%s", prefix, out)
	return ""
}

func newKeyFile(t *testing.T) (keyFile, pubB64 string) {
	t.Helper()
	keyFile = filepath.Join(t.TempDir(), "release.key")
	var out bytes.Buffer
	if err := runRelease([]string{"keygen", "--out", keyFile}, &out); err != nil {
		t.Fatal(err)
	}
	return keyFile, lineValue(t, out.String(), "public key: ")
}

func makeBinaries(t *testing.T, names ...string) (dir string, paths []string) {
	t.Helper()
	dir = t.TempDir()
	for i, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, bytes.Repeat([]byte{byte('a' + i)}, 1000+i), 0o755); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return dir, paths
}

// The bundle `sign` writes verifies with the printed public key, file by file; flags may follow the binaries.
func TestReleaseSignRoundTrip(t *testing.T) {
	keyFile, pubB64 := newKeyFile(t)
	_, bins := makeBinaries(t, "mistgate-node-linux-amd64", "mistgate-node-linux-arm64")
	outDir := filepath.Join(t.TempDir(), "dist")
	now := time.Unix(1_790_000_000, 0)
	var out bytes.Buffer
	args := []string{"--key", keyFile, "--version", "0.2.0-1a2b3c4", "--built", "1789000000", "--expires", "30d", bins[0], bins[1], "--out", outDir}
	if err := releaseSign(args, &out, now); err != nil {
		t.Fatal(err)
	}
	pub, err := decodeTestKey(pubB64)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	sig, _ := os.ReadFile(filepath.Join(outDir, "manifest.sig"))
	m, err := release.Verify(pub, body, sig)
	if err != nil {
		t.Fatalf("the written bundle does not verify with the printed key: %v", err)
	}
	if m.Version != "0.2.0-1a2b3c4" || m.Built != 1_789_000_000 || m.Expires != now.Add(30*24*time.Hour).Unix() || len(m.Files) != 2 {
		t.Fatalf("manifest %+v", m)
	}
	if len(sig) != 64 {
		t.Fatalf("signature is %d bytes: it is raw ed25519, not base64", len(sig))
	}
	for _, f := range m.Files {
		fh, err := os.Open(filepath.Join(outDir, f.Name))
		if err != nil {
			t.Fatal(err)
		}
		err = release.VerifyFile(f, fh)
		fh.Close()
		if err != nil {
			t.Fatalf("copied file %s: %v", f.Name, err)
		}
	}
	for _, want := range []string{"version 0.2.0-1a2b3c4", "mistgate-node-linux-amd64", "mistgate-node-linux-arm64", "signed with key " + buildinfo.KeyFingerprint(pub)} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	// another key does not verify it
	_, otherPub := newKeyFile(t)
	other, _ := decodeTestKey(otherPub)
	if _, err := release.Verify(other, body, sig); err == nil {
		t.Fatal("verified with a foreign key")
	}
	// tampering with a copied binary is noticed by the hash check
	if err := os.WriteFile(filepath.Join(outDir, m.Files[0].Name), []byte("evil"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBundle(outDir, pub); err == nil {
		t.Fatal("a swapped binary verifies")
	}
}

func TestReleaseSignRefusals(t *testing.T) {
	keyFile, _ := newKeyFile(t)
	_, good := makeBinaries(t, "mistgate-node-linux-amd64")
	_, noPlatform := makeBinaries(t, "mistgate-node")
	_, spaced := makeBinaries(t, "my node-linux-amd64")
	dirArg := t.TempDir()
	now := time.Unix(1_790_000_000, 0)
	base := func(extra ...string) []string {
		return append([]string{"--key", keyFile, "--version", "1.0.0", "--built", "1789000000"}, extra...)
	}
	badKey := filepath.Join(t.TempDir(), "bad.key")
	os.WriteFile(badKey, []byte("not a key"), 0o600)
	for _, tc := range []struct {
		name string
		args func(out string) []string
	}{
		{"built 0", func(o string) []string {
			return []string{"--key", keyFile, "--version", "1.0.0", "--built", "0", good[0], "--out", o}
		}},
		{"no built", func(o string) []string { return []string{"--key", keyFile, "--version", "1.0.0", good[0], "--out", o} }},
		{"negative built", func(o string) []string {
			return []string{"--key", keyFile, "--version", "1.0.0", "--built", "-5", good[0], "--out", o}
		}},
		{"a version that fails Validate", func(o string) []string {
			return []string{"--key", keyFile, "--version", "1.0 beta!", "--built", "1789000000", good[0], "--out", o}
		}},
		{"no version", func(o string) []string {
			return []string{"--key", keyFile, "--built", "1789000000", good[0], "--out", o}
		}},
		{"a name without platform", func(o string) []string { return base(noPlatform[0], "--out", o) }},
		{"an unsafe name", func(o string) []string { return base(spaced[0], "--out", o) }},
		{"a directory", func(o string) []string { return base(dirArg, "--out", o) }},
		{"a missing file", func(o string) []string { return base(filepath.Join(dirArg, "mistgate-node-linux-amd64"), "--out", o) }},
		{"no binaries", func(o string) []string { return base("--out", o) }},
		{"no out", func(string) []string { return base(good[0]) }},
		{"bad expires", func(o string) []string { return base("--expires", "soon", good[0], "--out", o) }},
		{"zero days", func(o string) []string { return base("--expires", "0d", good[0], "--out", o) }},
		{"negative duration", func(o string) []string { return base("--expires", "-5h", good[0], "--out", o) }},
		{"a bad key file", func(o string) []string {
			return []string{"--key", badKey, "--version", "1.0.0", "--built", "1789000000", good[0], "--out", o}
		}},
		{"a missing key file", func(o string) []string {
			return []string{"--key", filepath.Join(dirArg, "nope"), "--version", "1.0.0", "--built", "1789000000", good[0], "--out", o}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outDir := filepath.Join(t.TempDir(), "dist")
			if err := releaseSign(tc.args(outDir), &bytes.Buffer{}, now); err == nil {
				t.Fatal("accepted")
			}
			if _, err := os.Stat(filepath.Join(outDir, "manifest.json")); err == nil {
				t.Fatal("a manifest was written although signing was refused")
			}
		})
	}
}

// --out may be the directory the binaries already sit in; --expires takes days or a Go duration.
func TestReleaseSignInPlaceAndExpires(t *testing.T) {
	keyFile, pubB64 := newKeyFile(t)
	dir, bins := makeBinaries(t, "mistgate-node-linux-amd64")
	now := time.Unix(1_790_000_000, 0)
	if err := releaseSign([]string{"--key", keyFile, "--version", "1.0.0", "--built", "1789000000", "--expires", "720h", bins[0], "--out", dir}, &bytes.Buffer{}, now); err != nil {
		t.Fatal(err)
	}
	pub, _ := decodeTestKey(pubB64)
	m, err := verifyBundle(dir, pub)
	if err != nil || m.Expires != now.Add(720*time.Hour).Unix() {
		t.Fatalf("%+v %v", m, err)
	}
	if d, err := parseExpires("30d"); err != nil || d != 30*24*time.Hour {
		t.Fatalf("30d: %v %v", d, err)
	}
}

func TestVersionCommandPrintsTheBuild(t *testing.T) {
	var out bytes.Buffer
	printBuild(&out)
	if !strings.Contains(out.String(), "release key: none") { // tests are built without a key
		t.Fatalf("%q", out.String())
	}
}

func decodeTestKey(b64 string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("not a public key")
	}
	return ed25519.PublicKey(b), nil
}
