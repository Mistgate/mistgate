//go:build !js

package store

import (
	"context"
	"testing"
	"time"
)

func TestRecoveryTokenOfferSingleUseAndExpiry(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	mkAdmin(t, s, "adm_alice", "Alice")
	hash := hashOf("recovery-token")
	expires := t0.Add(30 * time.Minute)

	offer, err := s.OfferRecoveryToken(ctx, hash, expires)
	if err != nil || !offer.AdminExists || !offer.Found || offer.Used || !offer.Expires.Equal(expires) {
		t.Fatalf("first recovery offer = %+v, %v", offer, err)
	}
	if live, err := s.RecoveryTokenLive(ctx, hash, t0); err != nil || !live {
		t.Fatalf("live recovery token = %v, %v; want true", live, err)
	}
	again, err := s.OfferRecoveryToken(ctx, hash, expires.Add(time.Hour))
	if err != nil || !again.Found || again.Used || !again.Expires.Equal(expires) {
		t.Fatalf("repeated recovery offer = %+v, %v", again, err)
	}

	if won, err := s.ConsumeRecoveryToken(ctx, hash, expires.Add(-time.Second)); err != nil || !won {
		t.Fatalf("first consume = %v, %v", won, err)
	}
	if live, err := s.RecoveryTokenLive(ctx, hash, expires.Add(-time.Second)); err != nil || live {
		t.Fatalf("used recovery token live = %v, %v; want false", live, err)
	}
	if won, err := s.ConsumeRecoveryToken(ctx, hash, expires.Add(-time.Second)); err != nil || won {
		t.Fatalf("second consume = %v, %v; want used token refused", won, err)
	}
	used, err := s.OfferRecoveryToken(ctx, hash, expires.Add(time.Hour))
	if err != nil || !used.Found || !used.Used || !used.Expires.Equal(expires) {
		t.Fatalf("used token offer = %+v, %v", used, err)
	}

	expiredHash := hashOf("expired-recovery-token")
	expired, err := s.OfferRecoveryToken(ctx, expiredHash, expires)
	if err != nil || !expired.Found || expired.Used {
		t.Fatalf("expired token offer = %+v, %v", expired, err)
	}
	if won, err := s.ConsumeRecoveryToken(ctx, expiredHash, expires); err != nil || won {
		t.Fatalf("expired consume = %v, %v; want refused", won, err)
	}
	if live, err := s.RecoveryTokenLive(ctx, expiredHash, expires); err != nil || live {
		t.Fatalf("expired recovery token live = %v, %v; want false", live, err)
	}

	var count int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM recovery_token`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("recovery token rows = %d, %v; want used and expired rows retained", count, err)
	}
}

func TestRecoveryTokenOfferNeverRevivesSupersededHash(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	mkAdmin(t, s, "adm_alice", "Alice")
	a, b := hashOf("recovery-token-a"), hashOf("recovery-token-b")
	expiresA := t0.Add(30 * time.Minute)
	if _, err := s.OfferRecoveryToken(ctx, a, expiresA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OfferRecoveryToken(ctx, b, t0.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	again, err := s.OfferRecoveryToken(ctx, a, t0.Add(6*time.Hour))
	if err != nil || !again.Found || !again.Expires.Equal(expiresA) {
		t.Fatalf("re-offered A = %+v, %v; want its original expiry %v", again, err, expiresA)
	}
	if live, err := s.RecoveryTokenLive(ctx, a, t0.Add(6*time.Hour-time.Minute)); err != nil || live {
		t.Fatalf("superseded recovery token live = %v, %v; want false", live, err)
	}
	if won, err := s.ConsumeRecoveryToken(ctx, a, t0.Add(6*time.Hour-time.Minute)); err != nil || won {
		t.Fatalf("re-offered A consume = %v, %v; want false", won, err)
	}
}

func TestRecoveryTokenOfferRequiresAnAdmin(t *testing.T) {
	s := openTemp(t)
	offer, err := s.OfferRecoveryToken(context.Background(), hashOf("recovery-token"), t0.Add(time.Hour))
	if err != nil || offer.AdminExists || offer.Found {
		t.Fatalf("recovery offer before first admin = %+v, %v; want no offer", offer, err)
	}
}
