package auth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

func init() {
	// Cheap argon2 for the unit tests; the httpserver tests run the real cost.
	argon.time, argon.memKiB, argon.threads = 1, 8, 1
}

func newTestService(t *testing.T) (*Service, *store.Store, *time.Time) {
	t.Helper()
	clock := time.Now()
	clockPtr := &clock
	limiter := authTestLimiter{Memory: securitylimit.NewMemory(func() time.Time { return *clockPtr }, 0)}
	return newTestServiceWithLimiter(t, limiter, clockPtr)
}

func newTestServiceWithLimiter(t *testing.T, limiter securitylimit.Limiter, clock *time.Time) (*Service, *store.Store, *time.Time) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v, err := vault.New(bytes.Repeat([]byte{3}, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(st, Config{RPID: "localhost", Origins: []string{"http://localhost:8081"}, Vault: v, Limiter: limiter}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return *clock }
	return s, st, clock
}

type authTestLimiter struct {
	*securitylimit.Memory
}

func (l authTestLimiter) Take(ctx context.Context, b securitylimit.Bucket, key string) (securitylimit.Decision, error) {
	if b.Name == "auth" {
		return securitylimit.Decision{Allowed: true}, nil
	}
	return l.Memory.Take(ctx, b, key)
}

func TestAuthBurstRateLimit(t *testing.T) {
	clock := time.Now()
	clockPtr := &clock
	limiter := securitylimit.NewMemory(func() time.Time { return *clockPtr }, 0)
	s, _, limiterClock := newTestServiceWithLimiter(t, limiter, clockPtr)
	req := connect.NewRequest(&adminv1.BeginSetupRequest{})
	for i := 0; i < 10; i++ {
		if err := s.rateLimited(context.Background(), req); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	if err := s.rateLimited(context.Background(), req); codeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("11th rapid attempt: %v", err)
	}
	*limiterClock = limiterClock.Add(3 * time.Second)
	if err := s.rateLimited(context.Background(), req); err != nil {
		t.Fatalf("attempt after one refill: %v", err)
	}
}

func makeAdmin(t *testing.T, s *Service, st *store.Store) store.Admin {
	t.Helper()
	ctx := context.Background()
	tok, err := IssueSetupToken(ctx, st, s.now())
	if err != nil {
		t.Fatal(err)
	}
	a := store.Admin{ID: store.NewID("adm_"), DisplayName: "A", Role: store.RoleOwner, UserHandle: []byte("h")}
	pk := store.Passkey{ID: store.NewID("pk_"), AdminID: a.ID, CredentialID: []byte("c"), PublicKey: []byte("k")}
	if err := st.CreateFirstAdmin(ctx, hashToken(tok), s.now(), a, pk); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSessionLifetimes(t *testing.T) {
	s, st, clock := newTestService(t)
	ctx := context.Background()
	a := makeAdmin(t, s, st)

	cookie, err := s.newSession(ctx, a.ID, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !cookie.Secure || !cookie.HttpOnly || cookie.Name != "__Host-sid" || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("cookie: %+v", cookie)
	}
	if got, err := s.resolve(ctx, cookie.Value); err != nil || got.ID != a.ID {
		t.Fatalf("fresh session: %v", err)
	}

	// Activity within the idle window keeps the session alive for longer than 12 h in total...
	for range 4 {
		*clock = clock.Add(SessionIdleTTL - time.Hour)
		if _, err := s.resolve(ctx, cookie.Value); err != nil {
			t.Fatalf("active session expired at %v: %v", clock.Sub(time.Now()), err)
		}
	}
	// ...but 12 h of silence ends it, and it is gone for good.
	*clock = clock.Add(SessionIdleTTL)
	if _, err := s.resolve(ctx, cookie.Value); err == nil {
		t.Fatal("idle session still valid")
	}
	*clock = clock.Add(-SessionIdleTTL + time.Minute)
	if _, err := s.resolve(ctx, cookie.Value); err == nil {
		t.Fatal("expired session came back")
	}

	// The 30 day absolute limit applies however active the session is.
	cookie, _ = s.newSession(ctx, a.ID, "", "")
	start := *clock
	for clock.Add(6 * time.Hour).Before(start.Add(SessionMaxAge)) {
		*clock = clock.Add(6 * time.Hour)
		if _, err := s.resolve(ctx, cookie.Value); err != nil {
			t.Fatalf("session died early after %v: %v", clock.Sub(start), err)
		}
	}
	*clock = start.Add(SessionMaxAge)
	if _, err := s.resolve(ctx, cookie.Value); err == nil {
		t.Fatal("session outlived the absolute limit")
	}

	// Only the hash is stored.
	cookie, _ = s.newSession(ctx, a.ID, "", "")
	var n int
	st.R.QueryRow(`SELECT count(*) FROM session WHERE token_hash = ?`, []byte(cookie.Value)).Scan(&n)
	if n != 0 {
		t.Error("plaintext session token stored")
	}
}

func TestCeremonyGetConsumeWrongKindAndExpires(t *testing.T) {
	s, _, clock := newTestService(t)
	ctx := context.Background()
	id, err := s.putCeremony(ctx, &ceremony{kind: "login"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.getCeremony(ctx, id, "setup"); err != nil || ok {
		t.Fatal("ceremony of the wrong kind accepted")
	}
	if _, ok, err := s.getCeremony(ctx, id, "login"); err != nil || ok {
		t.Fatal("wrong-kind finish did not consume the ceremony")
	}
	id, _ = s.putCeremony(ctx, &ceremony{kind: "login"})
	c, ok, err := s.getCeremony(ctx, id, "login")
	if err != nil || !ok {
		t.Fatal("fresh ceremony rejected")
	}
	if consumed, err := s.consumeCeremony(ctx, id, c); err != nil || !consumed {
		t.Fatalf("consume ceremony: consumed=%v err=%v", consumed, err)
	}
	if _, ok, err := s.getCeremony(ctx, id, "login"); err != nil || ok {
		t.Fatal("consumed ceremony was reused")
	}
	id, _ = s.putCeremony(ctx, &ceremony{kind: "login"})
	*clock = clock.Add(CeremonyTTL + time.Second)
	if _, ok, err := s.getCeremony(ctx, id, "login"); err != nil || ok {
		t.Fatal("expired ceremony accepted")
	}
}

func TestSetupTokenIssue(t *testing.T) {
	s, st, _ := newTestService(t)
	ctx := context.Background()
	t1, err := IssueSetupToken(ctx, st, s.now())
	if err != nil || len(t1) < 43 {
		t.Fatalf("token: %q %v", t1, err)
	}
	t2, _ := IssueSetupToken(ctx, st, s.now())
	if t1 == t2 {
		t.Fatal("tokens repeat")
	}
	if err := st.CheckSetupToken(ctx, hashToken(t1), s.now()); !errors.Is(err, store.ErrSetupClosed) {
		t.Fatal("older token still valid after a new one was issued")
	}
	if err := st.CheckSetupToken(ctx, hashToken(t2), s.now()); err != nil {
		t.Fatal(err)
	}
	if err := st.CheckSetupToken(ctx, hashToken(t2), s.now().Add(SetupTokenTTL+time.Second)); !errors.Is(err, store.ErrSetupClosed) {
		t.Fatal("token outlived its 30 minute TTL")
	}
	makeAdmin(t, s, st)
	if _, err := IssueSetupToken(ctx, st, s.now()); !errors.Is(err, ErrAdminExists) {
		t.Fatalf("token issued although an admin exists: %v", err)
	}
}
