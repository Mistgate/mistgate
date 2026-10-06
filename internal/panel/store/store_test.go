//go:build !js

package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrationsAndPragmas(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()

	for _, table := range []string{"admin", "passkey", "session", "setup_token", "audit", "setting"} {
		var n int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
	var mode string
	s.W.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode)
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	var fk int
	s.W.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk)
	if fk != 1 {
		t.Error("foreign_keys not enabled")
	}
	if _, err := s.R.ExecContext(ctx, `INSERT INTO setting (k, v) VALUES ('a', 'b')`); err == nil {
		t.Error("read pool accepted a write")
	}
	// Reopening an existing database is a no-op for migrations.
	path := filepath.Join(t.TempDir(), "again.db")
	for range 2 {
		s2, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		s2.Close()
	}
}

func TestNewID(t *testing.T) {
	a, b := NewID("adm_"), NewID("adm_")
	if a == b || !strings.HasPrefix(a, "adm_") || len(a) != len("adm_")+26 || a != strings.ToLower(a) {
		t.Errorf("bad ids %q %q", a, b)
	}
}

func TestSettings(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.Setting(ctx, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing setting: %v", err)
	}
	s.SetSettings(ctx, map[string]string{"x": "1", "y": "2"})
	s.SetSettings(ctx, map[string]string{"x": "3"})
	if v, _ := s.Setting(ctx, "x"); v != "3" {
		t.Errorf("x = %q", v)
	}
}

func TestSetupTokenSingleUse(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now()
	hash := []byte("tokenhash-tokenhash-tokenhash-00")
	if err := s.CheckSetupToken(ctx, hash, now); !errors.Is(err, ErrSetupClosed) {
		t.Fatalf("unknown token accepted: %v", err)
	}
	if err := s.PutSetupToken(ctx, hash, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckSetupToken(ctx, hash, now); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckSetupToken(ctx, hash, now.Add(2*time.Minute)); !errors.Is(err, ErrSetupClosed) {
		t.Fatalf("expired token accepted: %v", err)
	}

	a := Admin{ID: NewID("adm_"), DisplayName: "Owner", Role: RoleOwner, UserHandle: []byte("handle-1")}
	p := Passkey{ID: NewID("pk_"), AdminID: a.ID, CredentialID: []byte("cred-1"), PublicKey: []byte("pk"),
		Transports: []string{"usb", "internal"}, Flags: 0x45, AAGUID: []byte{1, 2, 3}}
	if err := s.CreateFirstAdmin(ctx, hash, now, a, p); err != nil {
		t.Fatal(err)
	}
	// second use of the same token, and a fresh token, both fail: an admin exists.
	a2 := Admin{ID: NewID("adm_"), DisplayName: "X", Role: RoleOwner, UserHandle: []byte("handle-2")}
	p2 := Passkey{ID: NewID("pk_"), AdminID: a2.ID, CredentialID: []byte("cred-2"), PublicKey: []byte("pk")}
	if err := s.CreateFirstAdmin(ctx, hash, now, a2, p2); !errors.Is(err, ErrSetupClosed) {
		t.Fatalf("token reuse: %v", err)
	}
	if err := s.CheckSetupToken(ctx, hash, now); !errors.Is(err, ErrSetupClosed) {
		t.Fatalf("used token still checks ok: %v", err)
	}
	if n, _ := s.AdminCount(ctx); n != 1 {
		t.Fatalf("admins = %d", n)
	}

	got, err := s.PasskeyByCredentialID(ctx, []byte("cred-1"))
	if err != nil || got.Flags != 0x45 || len(got.Transports) != 2 || got.AdminID != a.ID {
		t.Fatalf("passkey round trip: %+v %v", got, err)
	}
	if err := s.TouchPasskey(ctx, []byte("cred-1"), 7, 0x5d, now); err != nil {
		t.Fatal(err)
	}
	got, _ = s.PasskeyByCredentialID(ctx, []byte("cred-1"))
	if got.SignCount != 7 || got.Flags != 0x5d {
		t.Errorf("touch not stored: %+v", got)
	}
}

func TestSessions(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	h := []byte("tok")
	s.PutSetupToken(ctx, []byte("t"), now.Add(time.Minute))
	a := Admin{ID: NewID("adm_"), DisplayName: "O", Role: RoleOwner, UserHandle: []byte("h")}
	if err := s.CreateFirstAdmin(ctx, []byte("t"), now, a, Passkey{ID: "pk_1", AdminID: a.ID, CredentialID: []byte("c"), PublicKey: []byte("k")}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, Session{TokenHash: h, AdminID: a.ID, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	sess, adm, err := s.SessionWithAdmin(ctx, h)
	if err != nil || adm.ID != a.ID || !sess.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("session lookup: %+v %+v %v", sess, adm, err)
	}
	if err := s.PurgeSessions(ctx, now.Add(2*time.Hour), 12*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SessionWithAdmin(ctx, h); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session not purged: %v", err)
	}
	if err := s.Audit(ctx, now, AuditEntry{Actor: "anonymous", Action: "x", Result: "ok"}); err != nil {
		t.Fatal(err)
	}
}
