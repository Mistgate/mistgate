//go:build !js

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func firstAdmin(t *testing.T, s *Store, now time.Time) Admin {
	t.Helper()
	ctx := context.Background()
	if err := s.PutSetupToken(ctx, []byte("t"), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	a := Admin{ID: NewID("adm_"), DisplayName: "Ada", Role: RoleOwner, UserHandle: []byte("h-" + NewID(""))}
	pk := Passkey{ID: NewID("pk_"), AdminID: a.ID, CredentialID: []byte("cred-1"), PublicKey: []byte("k"), Name: "First"}
	if err := s.CreateFirstAdmin(ctx, []byte("t"), now, a, pk); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestLoginFailures(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	const window, lock = time.Hour, 15 * time.Minute

	if f, err := s.LoginFailures(ctx, "ada", now, window); err != nil || f.Failures != 0 || f.Locked(now) {
		t.Fatalf("fresh login: %+v %v", f, err)
	}
	for i := 1; i <= 4; i++ {
		f, err := s.RecordLoginFailure(ctx, "ada", now, 5, window, lock)
		if err != nil || f.Failures != i || f.Locked(now) {
			t.Fatalf("failure %d: %+v %v", i, f, err)
		}
	}
	f, err := s.RecordLoginFailure(ctx, "ada", now, 5, window, lock)
	if err != nil || f.Failures != 5 || !f.Locked(now) || !f.LockedUntil.Equal(now.Add(lock)) {
		t.Fatalf("fifth failure: %+v %v", f, err)
	}
	if got, _ := s.LoginFailures(ctx, "ada", now.Add(lock-time.Second), window); !got.Locked(now.Add(lock - time.Second)) {
		t.Error("lock ended early")
	}
	// After the lock: clean slate, and the next failure starts counting at one.
	after := now.Add(lock + time.Second)
	if got, _ := s.LoginFailures(ctx, "ada", after, window); got.Failures != 0 || got.Locked(after) {
		t.Errorf("lock did not end: %+v", got)
	}
	if f, _ := s.RecordLoginFailure(ctx, "ada", after, 5, window, lock); f.Failures != 1 || f.Locked(after) {
		t.Errorf("count after the lock: %+v", f)
	}
	// Failures older than the window do not add up; other logins are independent.
	s.RecordLoginFailure(ctx, "bob", now, 5, window, lock)
	s.RecordLoginFailure(ctx, "bob", now, 5, window, lock)
	later := now.Add(window + time.Minute)
	if f, _ := s.RecordLoginFailure(ctx, "bob", later, 5, window, lock); f.Failures != 1 {
		t.Errorf("stale failures counted: %+v", f)
	}
	if f, _ := s.LoginFailures(ctx, "carol", now, window); f.Failures != 0 {
		t.Error("logins share a counter")
	}
	// A success forgets.
	if err := s.ClearLoginFailures(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.LoginFailures(ctx, "bob", later, window); f.Failures != 0 {
		t.Errorf("not cleared: %+v", f)
	}
	// Stale rows (made-up logins) are dropped as new failures come in.
	for _, name := range []string{"x1", "x2", "x3"} {
		s.RecordLoginFailure(ctx, name, now, 5, window, lock)
	}
	s.RecordLoginFailure(ctx, "fresh", later.Add(time.Hour), 5, window, lock)
	var n int
	s.R.QueryRow(`SELECT count(*) FROM login_failure WHERE login IN ('x1','x2','x3')`).Scan(&n)
	if n != 0 {
		t.Errorf("%d stale rows survived", n)
	}
}

func TestPasswordCredential(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s.PutSetupToken(ctx, []byte("t"), now.Add(time.Hour))
	a := Admin{ID: NewID("adm_"), DisplayName: "Ada", Role: RoleOwner, UserHandle: []byte("h")}
	if err := s.CreateFirstAdminPassword(ctx, []byte("t"), now, a, PasswordCred{Login: "ada", Hash: "$argon2id$x", TOTPSecret: []byte("sealed"), TOTPStep: 7}); err != nil {
		t.Fatal(err)
	}
	// The token is spent, and there is no passkey row.
	if err := s.CreateFirstAdminPassword(ctx, []byte("t"), now, Admin{ID: "adm_2", DisplayName: "x", Role: RoleOwner, UserHandle: []byte("h2")}, PasswordCred{Login: "eve", Hash: "h", TOTPSecret: []byte("s")}); !errors.Is(err, ErrSetupClosed) {
		t.Fatalf("second password admin: %v", err)
	}
	if pks, _ := s.PasskeysByAdmin(ctx, a.ID); len(pks) != 0 {
		t.Error("password admin has a passkey")
	}
	c, err := s.PasswordByLogin(ctx, "ada")
	if err != nil || c.AdminID != a.ID || c.Hash != "$argon2id$x" || string(c.TOTPSecret) != "sealed" || c.TOTPStep != 7 {
		t.Fatalf("round trip: %+v %v", c, err)
	}
	if _, err := s.PasswordByLogin(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown login: %v", err)
	}
	if ok, _ := s.AdminHasPassword(ctx, a.ID); !ok {
		t.Error("AdminHasPassword")
	}
	if ok, _ := s.AdminHasPassword(ctx, "adm_other"); ok {
		t.Error("AdminHasPassword for another admin")
	}
	// A TOTP step can only move forward, once per value.
	for _, tc := range []struct {
		step int64
		want bool
	}{{7, false}, {6, false}, {8, true}, {8, false}, {20, true}, {9, false}} {
		if got, err := s.AdvanceTOTPStep(ctx, a.ID, tc.step); err != nil || got != tc.want {
			t.Errorf("step %d: %v %v, want %v", tc.step, got, err, tc.want)
		}
	}
	// A password sign-in makes its session only while the credential it checked is still the current one: a reset that
	// lands between the check and the insert wins.
	sess := Session{TokenHash: []byte("tok1"), AdminID: a.ID, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := s.CreatePasswordSession(ctx, sess, c); err != nil {
		t.Fatalf("session with the current credential: %v", err)
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE admin_password SET hash = 'new', totp_secret = x'00' WHERE admin_id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	sess.TokenHash = []byte("tok2")
	if err := s.CreatePasswordSession(ctx, sess, c); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session with a replaced credential: %v", err)
	}
	if _, _, err := s.SessionWithAdmin(ctx, []byte("tok2")); err == nil {
		t.Error("the stale sign-in left a session")
	}
}

func TestPasskeyAddRemove(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	a := firstAdmin(t, s, now)
	first, _ := s.PasskeysByAdmin(ctx, a.ID)
	if len(first) != 1 || !first[0].LastUsedAt.IsZero() {
		t.Fatalf("first passkey: %+v", first)
	}
	// The only passkey of an admin without a password stays.
	if err := s.DeletePasskey(ctx, a.ID, first[0].ID); !errors.Is(err, ErrLastMethod) {
		t.Fatalf("last passkey removed: %v", err)
	}
	if err := s.AddPasskey(ctx, Passkey{ID: "pk_2", AdminID: a.ID, CredentialID: []byte("cred-2"), PublicKey: []byte("k"), Name: "Phone", Transports: []string{"hybrid"}}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.AddPasskey(ctx, Passkey{ID: "pk_3", AdminID: a.ID, CredentialID: []byte("cred-2"), PublicKey: []byte("k")}, now); err == nil {
		t.Fatal("duplicate credential id accepted")
	}
	s.TouchPasskey(ctx, []byte("cred-2"), 1, 0, now.Add(time.Minute))
	got, _ := s.PasskeyByCredentialID(ctx, []byte("cred-2"))
	if !got.LastUsedAt.Equal(now.Add(time.Minute)) || got.Name != "Phone" {
		t.Errorf("passkey: %+v", got)
	}
	// Another admin's (or an unknown) id is NotFound, not a silent success.
	if err := s.DeletePasskey(ctx, "adm_other", "pk_2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign delete: %v", err)
	}
	if err := s.DeletePasskey(ctx, a.ID, "pk_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown delete: %v", err)
	}
	if err := s.DeletePasskey(ctx, a.ID, "pk_2"); err != nil {
		t.Fatal(err)
	}
	if pks, _ := s.PasskeysByAdmin(ctx, a.ID); len(pks) != 1 {
		t.Errorf("%d passkeys left", len(pks))
	}
	// With a password on the account the last passkey may go.
	s.W.Exec(`INSERT INTO admin_password (admin_id, login, hash, totp_secret, created_at) VALUES (?, 'ada', 'h', x'00', 1)`, a.ID)
	if err := s.DeletePasskey(ctx, a.ID, first[0].ID); err != nil {
		t.Fatalf("last passkey with a password fallback: %v", err)
	}
}

func TestAdminSessionManagement(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	a := firstAdmin(t, s, now)
	mk := func(tok string, seen time.Duration, admin string) {
		t.Helper()
		if err := s.CreateSession(ctx, Session{TokenHash: []byte(tok), AdminID: admin, CreatedAt: now, LastSeenAt: now.Add(seen), ExpiresAt: now.Add(time.Hour), IP: "203.0.113.1", UserAgent: "ua"}); err != nil {
			t.Fatal(err)
		}
	}
	mk("s1", 0, a.ID)
	mk("s2", time.Minute, a.ID)
	mk("s3", 2*time.Minute, a.ID)
	list, err := s.SessionsByAdmin(ctx, a.ID)
	if err != nil || len(list) != 3 || string(list[0].TokenHash) != "s3" || list[2].IP != "203.0.113.1" {
		t.Fatalf("list: %+v %v", list, err)
	}
	if ok, _ := s.DeleteAdminSession(ctx, "adm_other", []byte("s1")); ok {
		t.Fatal("deleted another admin's session")
	}
	if ok, err := s.DeleteAdminSession(ctx, a.ID, []byte("s1")); !ok || err != nil {
		t.Fatalf("delete own: %v %v", ok, err)
	}
	if ok, _ := s.DeleteAdminSession(ctx, a.ID, []byte("s1")); ok {
		t.Fatal("deleted twice")
	}
	if n, err := s.DeleteOtherSessions(ctx, a.ID, []byte("s2")); n != 1 || err != nil {
		t.Fatalf("others: %d %v", n, err)
	}
	if list, _ := s.SessionsByAdmin(ctx, a.ID); len(list) != 1 || string(list[0].TokenHash) != "s2" {
		t.Fatalf("remaining: %+v", list)
	}
	// Without a current session to keep, "all others" is all of them.
	if n, _ := s.DeleteOtherSessions(ctx, a.ID, nil); n != 1 {
		t.Errorf("nil keep deleted %d", n)
	}
}

func TestAuditList(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	a := firstAdmin(t, s, now)
	add := func(actor, action, source, ip string) {
		t.Helper()
		if err := s.Audit(ctx, now, AuditEntry{Actor: actor, Action: action, Result: "ok", Source: source, IP: ip}); err != nil {
			t.Fatal(err)
		}
	}
	add(a.ID, "login", "", "203.0.113.1") // defaults to panel
	add("anonymous", "login", AuditPanel, "")
	add(a.ID, "node_add", AuditBot, "")
	add(a.ID, "user_create", AuditMCP, "")
	add(a.ID, "user_create", AuditAPI, "198.51.100.2")
	add(a.ID, "node_restart", AuditBot, "")

	all, err := s.ListAudit(ctx, "", 0, 100)
	if err != nil || len(all) != 6 || all[0].Action != "node_restart" || all[5].Action != "login" {
		t.Fatalf("all: %+v %v", all, err)
	}
	if all[5].Source != AuditPanel || all[5].IP != "203.0.113.1" || all[5].ActorName != "Ada" || all[4].ActorName != "" || all[4].Actor != "anonymous" {
		t.Errorf("row fields: %+v / %+v", all[5], all[4])
	}
	bot, _ := s.ListAudit(ctx, AuditBot, 0, 100)
	if len(bot) != 2 || bot[0].Action != "node_restart" || bot[1].Action != "node_add" {
		t.Errorf("bot filter: %+v", bot)
	}
	// Paging by id, newest first, with the source filter.
	p1, _ := s.ListAudit(ctx, "", 0, 4)
	p2, _ := s.ListAudit(ctx, "", p1[len(p1)-1].ID, 4)
	if len(p1) != 4 || len(p2) != 2 || p1[3].ID <= p2[0].ID || p2[1].ID != all[5].ID {
		t.Errorf("paging: %d %d", len(p1), len(p2))
	}
	if rows, _ := s.ListAudit(ctx, AuditMCP, bot[0].ID, 10); len(rows) != 1 {
		t.Errorf("filter + cursor: %+v", rows)
	}
	if err := s.Audit(ctx, now, AuditEntry{Actor: "x", Action: "y", Result: "ok", Source: "telepathy"}); err == nil {
		t.Error("unknown audit source accepted")
	}
}

// One kind of action: some exact actions, some prefixes (with LIKE's own characters taken literally), the failures.
func TestAuditListByKind(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now()
	for _, r := range [][2]string{
		{"login", "fail"}, {"login", "ok"}, {"user_create", "ok"}, {"userXcreate", "ok"}, {"users_disable", "ok"},
		{"node.retire", "ok"}, {"nodeXretire", "ok"}, {"call", "ok"}, {"lockout", "locked"}, {"captcha", ""},
	} {
		if err := s.Audit(ctx, now, AuditEntry{Actor: "a", Action: r[0], Result: r[1]}); err != nil {
			t.Fatal(err)
		}
	}
	actions := func(q AuditQuery) string {
		t.Helper()
		q.Limit = 100
		rows, err := s.ListAuditQuery(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for i := len(rows) - 1; i >= 0; i-- {
			out = append(out, rows[i].Action+"/"+rows[i].Result)
		}
		return strings.Join(out, " ")
	}
	if got := actions(AuditQuery{Actions: []string{"login", "lockout"}}); got != "login/fail login/ok lockout/locked" {
		t.Errorf("actions: %s", got)
	}
	if got := actions(AuditQuery{Prefixes: []string{"user_", "users_", "node."}}); got != "user_create/ok users_disable/ok node.retire/ok" {
		t.Errorf("prefixes (an underscore or a dot is not a wildcard): %s", got)
	}
	if got := actions(AuditQuery{Actions: []string{"call"}, Prefixes: []string{"user_"}}); got != "user_create/ok call/ok" {
		t.Errorf("both: %s", got)
	}
	if got := actions(AuditQuery{Failed: true}); got != "login/fail lockout/locked" {
		t.Errorf("failures: %s", got)
	}
	if got := actions(AuditQuery{Failed: true, Actions: []string{"lockout"}, Source: AuditPanel}); got != "lockout/locked" {
		t.Errorf("failures of one action and source: %s", got)
	}
}

func TestDatabaseFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX mode bits on Windows")
	}
	old := umask(0o022) // a permissive umask: the code must not depend on the process having set a strict one
	defer umask(old)
	ctx := context.Background()
	dir := t.TempDir()
	check := func(path string) {
		t.Helper()
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s has mode %04o", filepath.Base(path), fi.Mode().Perm())
		}
	}
	// New database: main file, and the WAL/SHM files once there has been a write.
	path := filepath.Join(dir, "new.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s.SetSettings(ctx, map[string]string{"a": "b"})
	check(path)
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			check(path + suffix)
		}
	}
	s.Close()
	// An existing world-readable database (older install) is tightened on open.
	old2 := filepath.Join(dir, "old.db")
	if err := os.WriteFile(old2, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chmod(old2, 0o644)
	s2, err := Open(ctx, old2)
	if err != nil {
		t.Fatal(err)
	}
	s2.SetSettings(ctx, map[string]string{"a": "b"})
	check(old2)
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(old2 + suffix); err == nil {
			check(old2 + suffix)
		}
	}
	s2.Close()
}
