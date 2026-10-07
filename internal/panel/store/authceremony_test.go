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
