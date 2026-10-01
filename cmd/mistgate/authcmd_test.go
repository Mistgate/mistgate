package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // RFC 6238, what authenticator apps compute
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"github.com/mistgate/mistgate/internal/qr"
)

// `mistgate auth turnstile off` is the way out when the captcha locks the owner out. It must work while the
// panel runs (here: a second open handle on the same database, as the panel's) and not invent a database.
func TestAuthTurnstileOffKillSwitch(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	var out bytes.Buffer
	if err := setup(ctx, dir, setupOpts{publicURL: "https://example.com"}, &out, time.Now()); err != nil {
		t.Fatal(err)
	}
	panel, err := store.Open(ctx, dbPath(dir)) // the running panel's handle
	if err != nil {
		t.Fatal(err)
	}
	defer panel.Close()
	if err := panel.SetSettings(ctx, map[string]string{"turnstile_enabled": "1", "turnstile_site_key": "site-key", "turnstile_secret": "c2VhbGVk"}); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := runAuth([]string{"turnstile", "off", "--data-dir", dir}, &out); err != nil {
		t.Fatalf("turnstile off: %v", err)
	}
	if !strings.Contains(out.String(), "Turnstile is off") {
		t.Errorf("output: %q", out.String())
	}
	get := func(k string) string { v, _ := panel.Setting(ctx, k); return v }
	if get("turnstile_enabled") != "0" {
		t.Errorf("turnstile_enabled = %q, want 0 (seen by the running panel at once)", get("turnstile_enabled"))
	}
	if get("turnstile_site_key") != "site-key" || get("turnstile_secret") != "c2VhbGVk" {
		t.Error("the kill switch must keep the keys")
	}
	var n int
	panel.R.QueryRow(`SELECT count(*) FROM audit WHERE action = 'turnstile_off' AND actor = 'cli'`).Scan(&n)
	if n != 1 {
		t.Errorf("audit rows: %d", n)
	}
	if err := runAuth([]string{"turnstile", "off", "--data-dir", dir}, &out); err != nil {
		t.Errorf("second run: %v", err)
	}

	// A wrong directory is an error and creates nothing.
	wrong := filepath.Join(t.TempDir(), "nothing-here")
	if err := runAuth([]string{"turnstile", "off", "--data-dir", wrong}, &out); err == nil {
		t.Error("a missing database was accepted")
	}
	if _, err := os.Stat(dbPath(wrong)); err == nil {
		t.Error("the kill switch created a database")
	}
	// Anything else prints the usage.
	for _, args := range [][]string{nil, {"turnstile"}, {"turnstile", "on"}, {"nothing", "off"}, {"reset-login", "a", "b"}} {
		if err := runAuth(args, &out); err == nil || !strings.Contains(err.Error(), "usage: mistgate auth turnstile off") {
			t.Errorf("args %v: %v", args, err)
		}
	}
}

// totpAt is RFC 6238 (SHA-1, 6 digits, 30 s), the way an authenticator app computes it.
func totpAt(secret []byte, at time.Time) string {
	var c [8]byte
	binary.BigEndian.PutUint64(c[:], uint64(at.Unix()/30))
	m := hmac.New(sha1.New, secret)
	m.Write(c[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1_000_000)
}

// checkResetLoginQR asserts that the reset-login output holds, above the text key, the QR code of exactly the otpauth
// link it prints, with the whole quiet zone, and that the text key and the link are still there.
func checkResetLoginQR(t *testing.T, text string, invert bool) {
	t.Helper()
	m := regexp.MustCompile(`or open this on the phone: (otpauth://\S+)\n`).FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no otpauth link in %q", text)
	}
	code, err := qr.Encode([]byte(m[1]), qr.M)
	if err != nil {
		t.Fatal(err)
	}
	block := code.Terminal(invert)
	at, key := strings.Index(text, block), strings.Index(text, "    Key:")
	if at < 0 || key < 0 || at > key {
		t.Fatalf("the QR code of %q is not above the key in %q", m[1], text)
	}
	rows := strings.Split(strings.TrimSuffix(block, "\n"), "\n")
	if want := (code.Size + 8 + 1) / 2; len(rows) != want {
		t.Errorf("%d QR rows for %d modules, want %d", len(rows), code.Size, want)
	}
	quiet := strings.Repeat("█", code.Size+8) // the light quiet zone is drawn as blocks on a dark terminal
	if invert {
		quiet = strings.Repeat(" ", code.Size+8)
	}
	if rows[0] != quiet {
		t.Errorf("the first QR row is not the quiet zone: %q", rows[0])
	}
	if !strings.Contains(text, "Key:     ") || !strings.Contains(text, "Password:") {
		t.Error("the text key is gone")
	}
}

// `mistgate auth reset-login` is the way back in after a lost phone: it lists the admins, gives a passkey-only owner a
// password login (or resets an existing one), ends the admin's sessions and prints the new secrets once. It works next
// to a running panel (a second handle on the same database).
func TestAuthResetLoginCommand(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	var out bytes.Buffer
	now := time.Now()
	if err := setup(ctx, dir, setupOpts{publicURL: "https://example.com"}, &out, now); err != nil {
		t.Fatal(err)
	}
	panel, err := store.Open(ctx, dbPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer panel.Close()

	out.Reset()
	if err := runAuth([]string{"reset-login", "--data-dir", dir}, &out); err != nil || !strings.Contains(out.String(), "No admin yet") {
		t.Fatalf("no admin: %v %q", err, out.String())
	}

	// A passkey-only owner with a live session (the lost phone's).
	tok, err := auth.IssueSetupToken(ctx, panel, now)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(tok))
	owner := store.Admin{ID: store.NewID("adm_"), DisplayName: "Alice", Role: store.RoleOwner, UserHandle: []byte("handle")}
	if err := panel.CreateFirstAdmin(ctx, h[:], now, owner, store.Passkey{ID: store.NewID("pk_"), AdminID: owner.ID, CredentialID: []byte("c"), PublicKey: []byte("k")}); err != nil {
		t.Fatal(err)
	}
	if err := panel.CreateSession(ctx, store.Session{TokenHash: []byte("lost-phone-session-hash"), AdminID: owner.ID, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := runAuth([]string{"reset-login", "--data-dir", dir}, &out); err != nil || !strings.Contains(out.String(), owner.ID) || !strings.Contains(out.String(), "passkeys only") {
		t.Fatalf("list: %v %q", err, out.String())
	}

	// The flags may follow the login.
	out.Reset()
	if err := runAuth([]string{"reset-login", "Owner", "--data-dir", dir}, &out); err != nil {
		t.Fatalf("reset-login: %v", err)
	}
	text := out.String()
	pw := regexp.MustCompile(`Password:\s+(\S+)`).FindStringSubmatch(text)
	key := regexp.MustCompile(`Key:\s+([A-Z2-7 ]+)\n`).FindStringSubmatch(text)
	if pw == nil || key == nil || !strings.Contains(text, "Login:     owner") || !strings.Contains(text, "otpauth://totp/") || !strings.Contains(text, "(1)") {
		t.Fatalf("output: %q", text)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ReplaceAll(key[1], " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	if sess, _ := panel.SessionsByAdmin(ctx, owner.ID); len(sess) != 0 {
		t.Error("the lost phone's session survived")
	}
	var actor, params string
	if err := panel.R.QueryRow(`SELECT actor, params FROM audit WHERE action = 'reset_login'`).Scan(&actor, &params); err != nil || actor != "cli" || strings.Contains(params, pw[1]) {
		t.Errorf("audit: %q %q %v", actor, params, err)
	}

	// The printed password and a code of the printed key sign in, through the panel's own service.
	keyBytes, err := vault.LoadKey(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := vault.New(keyBytes)
	svc, err := auth.New(panel, auth.Config{RPID: "example.com", Origins: []string{"https://example.com"}, Vault: v}, nil)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(30 * time.Second) // a step the reset has not used
	if _, err := svc.PasswordLogin(ctx, connect.NewRequest(&adminv1.PasswordLoginRequest{Login: "owner", Password: pw[1], TotpCode: totpAt(secret, at)})); err != nil {
		t.Fatalf("sign-in with the printed password and key: %v", err)
	}

	// The QR code of the printed otpauth link comes above the text key (the fallback), drawn for a dark terminal
	// by default and for a light one with --qr-invert.
	checkResetLoginQR(t, text, false)
	out.Reset()
	if err := runAuth([]string{"reset-login", "owner", "--qr-invert", "--data-dir", dir}, &out); err != nil {
		t.Fatalf("reset-login --qr-invert: %v", err)
	}
	checkResetLoginQR(t, out.String(), true)

	// A wrong directory is an error and creates nothing; so is a missing master key.
	wrong := filepath.Join(t.TempDir(), "nothing-here")
	if err := runAuth([]string{"reset-login", "owner", "--data-dir", wrong}, &out); err == nil {
		t.Error("a missing database was accepted")
	}
	if _, err := os.Stat(dbPath(wrong)); err == nil {
		t.Error("reset-login created a database")
	}
	if err := os.Remove(filepath.Join(dir, "master.key")); err != nil {
		t.Fatal(err)
	}
	if err := runAuth([]string{"reset-login", "owner", "--data-dir", dir}, &out); err == nil || !strings.Contains(err.Error(), "master key") {
		t.Errorf("no master key: %v", err)
	}
}
