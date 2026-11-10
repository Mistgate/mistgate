//go:build !js

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAuthCeremonyGetConsumeAndFail(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	ceremony := AuthCeremony{ID: "ceremony_cas", Kind: "setup-password", SessionData: "{}", ExpiresAt: now.Add(30 * time.Second)}
	if err := s.PutAuthCeremony(ctx, ceremony, now, 8, 100); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetAuthCeremony(ctx, ceremony.ID, now)
	if err != nil || got.Tries != 0 || !got.ExpiresAt.Equal(ceremony.ExpiresAt) {
		t.Fatalf("get ceremony = %+v, %v", got, err)
	}
	var expires int64
	if err := s.R.QueryRowContext(ctx, `SELECT expires_at FROM auth_ceremony WHERE id = ?`, ceremony.ID).Scan(&expires); err != nil || expires != ceremony.ExpiresAt.Unix() {
		t.Fatalf("stored expiry = %d, err %v; want unix seconds %d", expires, err, ceremony.ExpiresAt.Unix())
	}

	if changed, err := s.FailAuthCeremony(ctx, ceremony.ID, 3); err != nil || !changed {
		t.Fatalf("first failure = changed %v, err %v", changed, err)
	}
	got, err = s.GetAuthCeremony(ctx, ceremony.ID, now)
	if err != nil || got.Tries != 1 {
		t.Fatalf("ceremony after first failure = %+v, %v", got, err)
	}
	if consumed, err := s.ConsumeAuthCeremony(ctx, ceremony.ID); err != nil || !consumed {
		t.Fatalf("consume after a wrong code = consumed %v, err %v", consumed, err)
	}
	if _, err := s.GetAuthCeremony(ctx, ceremony.ID, now); !errors.Is(err, ErrAuthCeremonyNotFound) {
		t.Fatalf("consumed ceremony get = %v", err)
	}

	ceremony.ID = "ceremony_max_tries"
	if err := s.PutAuthCeremony(ctx, ceremony, now, 8, 100); err != nil {
		t.Fatal(err)
	}
	for tries := 0; tries < 3; tries++ {
		changed, err := s.FailAuthCeremony(ctx, ceremony.ID, 3)
		if err != nil || !changed {
			t.Fatalf("failure %d = changed %v, err %v", tries+1, changed, err)
		}
	}
	if _, err := s.GetAuthCeremony(ctx, ceremony.ID, now); !errors.Is(err, ErrAuthCeremonyNotFound) {
		t.Fatalf("ceremony survived maximum failures: %v", err)
	}

	ceremony.ID = "ceremony_late_failure"
	if err := s.PutAuthCeremony(ctx, ceremony, now, 8, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAuthCeremony(ctx, ceremony.ID, now); err != nil {
		t.Fatal(err)
	}
	if consumed, err := s.ConsumeAuthCeremony(ctx, ceremony.ID); err != nil || !consumed {
		t.Fatalf("consume before late failure = consumed %v, err %v", consumed, err)
	}
	if changed, err := s.FailAuthCeremony(ctx, ceremony.ID, 3); err != nil || changed {
		t.Fatalf("late failure = changed %v, err %v; want a deleted ceremony", changed, err)
	}
	if _, err := s.GetAuthCeremony(ctx, ceremony.ID, now); !errors.Is(err, ErrAuthCeremonyNotFound) {
		t.Fatalf("late failure restored consumed ceremony: %v", err)
	}

	if _, err := s.GetAuthCeremony(ctx, "missing", now); !errors.Is(err, ErrAuthCeremonyNotFound) {
		t.Fatalf("missing ceremony get = %v", err)
	}
	ceremony.ID = "ceremony_expired"
	if err := s.PutAuthCeremony(ctx, ceremony, now, 8, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAuthCeremony(ctx, ceremony.ID, now.Add(30*time.Second)); !errors.Is(err, ErrAuthCeremonyNotFound) {
		t.Fatalf("expired ceremony get = %v", err)
	}
}

func TestAuthCeremonyBudgetsSeparateAnonymousAndSessionBoundRows(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	put := func(id, kind, source string) error {
		return s.PutAuthCeremony(ctx, AuthCeremony{
			ID: id, Kind: kind, Source: source, SessionData: "{}", ExpiresAt: now.Add(time.Minute),
		}, now, 2, 3)
	}
	for _, row := range [][3]string{{"anon_c", "login", "source_a"}, {"anon_b", "setup", "source_b"}, {"anon_a", "login", "source_b"}} {
		if err := put(row[0], row[1], row[2]); err != nil {
			t.Fatal(err)
		}
	}
	if err := put("anon_d", "setup-password", "source_b"); !errors.Is(err, ErrAuthCeremonyLimit) {
		t.Fatalf("same-source anonymous budget = %v, want limit", err)
	}
	if err := put("anon_e", "login", "source_c"); err != nil {
		t.Fatalf("anonymous insertion at its full total budget: %v", err)
	}
	if _, err := s.GetAuthCeremony(ctx, "anon_c", now); !errors.Is(err, ErrAuthCeremonyNotFound) {
		t.Fatalf("oldest anonymous ceremony survived eviction: %v", err)
	}
	for _, row := range [][3]string{{"bound_a", "stepup", "source_b"}, {"bound_b", "totp-enroll", "source_b"}, {"bound_c", "add-passkey", "source_c"}} {
		if err := put(row[0], row[1], row[2]); err != nil {
			t.Fatalf("session-bound ceremony with an anonymous source at its cap: %v", err)
		}
	}
	if err := put("bound_d", "add-passkey", "source_d"); !errors.Is(err, ErrAuthCeremonyLimit) {
		t.Fatalf("session-bound total budget = %v, want limit", err)
	}
}

func TestAuthCeremonyEvictionOnlyRemovesLoginRows(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	put := func(t *testing.T, s *Store, id, kind, source string, maxTotal int) error {
		t.Helper()
		return s.PutAuthCeremony(ctx, AuthCeremony{
			ID: id, Kind: kind, Source: source, SessionData: "{}", ExpiresAt: now.Add(time.Minute),
		}, now, 8, maxTotal)
	}

	t.Run("setup survives login eviction", func(t *testing.T) {
		s := openTemp(t)
		if err := put(t, s, "setup_password", "setup-password", "owner", 2); err != nil {
			t.Fatal(err)
		}
		if err := put(t, s, "old_login", "login", "first_client", 2); err != nil {
			t.Fatal(err)
		}
		if err := put(t, s, "new_login", "login", "second_client", 2); err != nil {
			t.Fatalf("login insertion at capacity: %v", err)
		}
		if _, err := s.GetAuthCeremony(ctx, "setup_password", now); err != nil {
			t.Fatalf("setup-password ceremony was evicted: %v", err)
		}
		if _, err := s.GetAuthCeremony(ctx, "old_login", now); !errors.Is(err, ErrAuthCeremonyNotFound) {
			t.Fatalf("old login survived eviction: %v", err)
		}
	})

	t.Run("non-login class refuses at capacity", func(t *testing.T) {
		s := openTemp(t)
		for _, row := range [][2]string{{"setup", "setup"}, {"setup_password", "setup-password"}} {
			if err := put(t, s, row[0], row[1], row[0], 2); err != nil {
				t.Fatal(err)
			}
		}
		if err := put(t, s, "login", "login", "client", 2); !errors.Is(err, ErrAuthCeremonyLimit) {
			t.Fatalf("login insertion into full non-login class = %v, want limit", err)
		}
		for _, id := range []string{"setup", "setup_password"} {
			if _, err := s.GetAuthCeremony(ctx, id, now); err != nil {
				t.Errorf("non-login ceremony %q was evicted: %v", id, err)
			}
		}
	})
}

func TestAuthCeremonyConcurrentWrongCodeFailuresRespectLimit(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	ceremony := AuthCeremony{ID: "ceremony_concurrent_failures", Kind: "setup-password", SessionData: "{}", ExpiresAt: now.Add(time.Minute)}
	if err := s.PutAuthCeremony(ctx, ceremony, now, 8, 100); err != nil {
		t.Fatal(err)
	}

	const maxTries = 5
	const attempts = 100
	start := make(chan struct{})
	results := make(chan struct {
		changed bool
		err     error
	}, attempts)
	for range attempts {
		go func() {
			<-start
			changed, err := s.FailAuthCeremony(ctx, ceremony.ID, maxTries)
			results <- struct {
				changed bool
				err     error
			}{changed: changed, err: err}
		}()
	}
	close(start)
	counted := 0
	for range attempts {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.changed {
			counted++
		}
	}
	if counted != maxTries {
		t.Fatalf("counted concurrent wrong codes = %d, want %d", counted, maxTries)
	}

	_, err := s.GetAuthCeremony(ctx, ceremony.ID, now)
	if !errors.Is(err, ErrAuthCeremonyNotFound) {
		t.Fatalf("ceremony after %d concurrent wrong codes: err=%v; want it deleted", attempts, err)
	}
}

func TestAuthCeremonyWrongCodeDoesNotBeatCorrectFinish(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	ceremony := AuthCeremony{ID: "ceremony_wrong_and_correct", Kind: "setup-password", SessionData: "{}", ExpiresAt: now.Add(time.Minute)}
	if err := s.PutAuthCeremony(ctx, ceremony, now, 8, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAuthCeremony(ctx, ceremony.ID, now); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	wrongDone := make(chan error, 1)
	correctDone := make(chan struct {
		consumed bool
		err      error
	}, 1)
	go func() {
		<-start
		_, err := s.FailAuthCeremony(ctx, ceremony.ID, 5)
		wrongDone <- err
	}()
	go func() {
		<-start
		if err := <-wrongDone; err != nil {
			correctDone <- struct {
				consumed bool
				err      error
			}{err: err}
			return
		}
		consumed, err := s.ConsumeAuthCeremony(ctx, ceremony.ID)
		correctDone <- struct {
			consumed bool
			err      error
		}{consumed: consumed, err: err}
	}()
	close(start)
	result := <-correctDone
	if result.err != nil || !result.consumed {
		t.Fatalf("correct finish after one concurrent typo = consumed %v, err %v; want it to win", result.consumed, result.err)
	}
}
