package vault

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testVault(t *testing.T) *Vault {
	t.Helper()
	v, err := New(bytes.Repeat([]byte{7}, KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSealOpen(t *testing.T) {
	v := testVault(t)
	blob := v.Seal([]byte("warp-private-key"), "nod_1")
	got, err := v.Open(blob, "nod_1")
	if err != nil || string(got) != "warp-private-key" {
		t.Fatalf("round trip: %q %v", got, err)
	}
	if _, err := v.Open(blob, "nod_2"); err == nil {
		t.Error("opened with the wrong AAD")
	}
	blob[len(blob)-1] ^= 1
	if _, err := v.Open(blob, "nod_1"); err == nil {
		t.Error("opened tampered ciphertext")
	}
	if _, err := v.Open([]byte("short"), "nod_1"); err == nil {
		t.Error("opened a truncated blob")
	}
	if bytes.Equal(v.Seal([]byte("x"), "a"), v.Seal([]byte("x"), "a")) {
		t.Error("nonce reused")
	}
	if _, err := New([]byte("short")); err == nil {
		t.Error("accepted a short key")
	}
}

func TestRedactedNeverPrints(t *testing.T) {
	const secret = "hunter2-very-secret"
	r := Redacted(secret)
	type holder struct {
		Token Redacted
		Ptr   *Redacted
	}
	h := holder{Token: r, Ptr: &r}

	var outs []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		outs = append(outs, fmt.Sprintf(verb, r), fmt.Sprintf(verb, h), fmt.Sprintf(verb, &r), fmt.Sprintf(verb, []Redacted{r}))
	}
	outs = append(outs, fmt.Sprint(r), fmt.Sprintln(r), r.String())
	j, _ := json.Marshal(h)
	outs = append(outs, string(j))
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("x", "tok", r, slog.Any("h", h))
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("x", "tok", r)
	outs = append(outs, buf.String())

	for _, o := range outs {
		if strings.Contains(o, secret) {
			t.Errorf("secret leaked: %q", o)
		}
	}
	if r.Reveal() != secret {
		t.Error("Reveal broken")
	}
}

func TestLoadKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	if _, err := LoadKey(dir, false); err == nil {
		t.Fatal("missing key without create must fail")
	}
	k1, err := LoadKey(dir, true)
	if err != nil || len(k1) != KeySize {
		t.Fatalf("create: %v", err)
	}
	k2, err := LoadKey(dir, false)
	if err != nil || !bytes.Equal(k1, k2) {
		t.Fatalf("reload: %v", err)
	}

	// systemd credential wins over the data dir.
	cred := t.TempDir()
	want := bytes.Repeat([]byte{9}, KeySize)
	os.WriteFile(filepath.Join(cred, keyFile), want, 0o600)
	t.Setenv("CREDENTIALS_DIRECTORY", cred)
	if got, err := LoadKey(dir, false); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("credentials dir: %v", err)
	}

	// a wrong-sized key file is an error, never silently regenerated.
	bad := t.TempDir()
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	os.WriteFile(filepath.Join(bad, keyFile), []byte("short"), 0o600)
	if _, err := LoadKey(bad, true); err == nil {
		t.Error("accepted a short key file")
	}
}

func TestLoadKeyRefusesWideMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX mode bits on Windows")
	}
	dir := t.TempDir()
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	if _, err := LoadKey(dir, true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, keyFile)
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o660, 0o666} {
		os.Chmod(path, mode)
		if _, err := LoadKey(dir, false); err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("mode %04o accepted: %v", mode, err)
		}
	}
	for _, mode := range []os.FileMode{0o600, 0o400} {
		os.Chmod(path, mode)
		if _, err := LoadKey(dir, false); err != nil {
			t.Errorf("mode %04o refused: %v", mode, err)
		}
	}
}

func TestDeriveIsStablePerLabelAndIndependent(t *testing.T) {
	v := testVault(t)
	a, b := v.Derive("sub-page-password"), v.Derive("other-purpose")
	if len(a) != KeySize || !bytes.Equal(a, testVault(t).Derive("sub-page-password")) {
		t.Fatal("a derived key must be KeySize bytes and the same for the same master key and label")
	}
	if bytes.Equal(a, b) {
		t.Error("two labels gave one key")
	}
	if bytes.Equal(a, bytes.Repeat([]byte{7}, KeySize)) {
		t.Error("the derived key is the master key")
	}
	other, _ := New(bytes.Repeat([]byte{8}, KeySize))
	if bytes.Equal(a, other.Derive("sub-page-password")) {
		t.Error("two master keys gave one derived key")
	}
	// the master key still seals and opens as before
	if pt, err := v.Open(v.Seal([]byte("x"), "r"), "r"); err != nil || string(pt) != "x" {
		t.Error("seal/open broke")
	}
}
