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

	if changed, err := s.FailAuthCeremony(ctx, ceremony.ID, 0, 3); err != nil || !changed {
		t.Fatalf("first failure = changed %v, err %v", changed, err)
	}
	got, err = s.GetAuthCeremony(ctx, ceremony.ID, now)
	if err != nil || got.Tries != 1 {
		t.Fatalf("ceremony after first failure = %+v, %v", got, err)
	}
	if consumed, err := s.ConsumeAuthCeremony(ctx, ceremony.ID, 0); err != nil || consumed {
		t.Fatalf("stale consume = consumed %v, err %v; want no write", consumed, err)
	}
	if consumed, err := s.ConsumeAuthCeremony(ctx, ceremony.ID, 1); err != nil || !consumed {
		t.Fatalf("consume = consumed %v, err %v", consumed, err)
	}
	if _, err := s.GetAuthCeremony(ctx, ceremony.ID, now); !errors.Is(err, ErrAuthCeremonyNotFound) {
		t.Fatalf("consumed ceremony get = %v", err)
	}

	ceremony.ID = "ceremony_max_tries"
	if err := s.PutAuthCeremony(ctx, ceremony, now, 8, 100); err != nil {
		t.Fatal(err)
	}
	for tries := 0; tries < 3; tries++ {
		changed, err := s.FailAuthCeremony(ctx, ceremony.ID, tries, 3)
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
	stale, err := s.GetAuthCeremony(ctx, ceremony.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if consumed, err := s.ConsumeAuthCeremony(ctx, ceremony.ID, stale.Tries); err != nil || !consumed {
		t.Fatalf("consume before late failure = consumed %v, err %v", consumed, err)
	}
	if changed, err := s.FailAuthCeremony(ctx, ceremony.ID, stale.Tries, 3); err != nil || changed {
		t.Fatalf("late failure = changed %v, err %v; want lost compare-and-swap", changed, err)
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
