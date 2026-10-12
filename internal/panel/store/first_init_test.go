//go:build !js

package store

import (
	"context"
	"testing"
	"time"
)

func TestOfferSetupToken(t *testing.T) {
	ctx := context.Background()
	hash := hashOf("deployer-token")
	expires := t0.Add(30 * time.Minute)

	s := openTemp(t)
	offer, err := s.OfferSetupToken(ctx, hash, expires)
	if err != nil {
		t.Fatal(err)
	}
	if offer.AdminExists || !offer.Found || offer.Used || !offer.Expires.Equal(expires) {
		t.Fatalf("no admin: offer = %+v", offer)
	}
	if err := s.CheckSetupToken(ctx, hash, t0); err != nil {
		t.Fatalf("an offered token must pass CheckSetupToken: %v", err)
	}

	// the same hash again, later: one row, the first expiry
	again, err := s.OfferSetupToken(ctx, hash, expires.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Found || !again.Expires.Equal(expires) {
		t.Fatalf("second offer = %+v, want the first expiry %v", again, expires)
	}
	var rows int
	if err := s.R.QueryRow(`SELECT count(*) FROM setup_token`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("setup_token rows = %d, %v; want 1", rows, err)
	}

	// an expired token is not revived by another offer
	execT(t, s, `UPDATE setup_token SET expires_at = ?`, t0.Add(-time.Hour).Unix())
	expired, err := s.OfferSetupToken(ctx, hash, expires)
	if err != nil {
		t.Fatal(err)
	}
	if !expired.Found || expired.Used || !expired.Expires.Before(t0) {
		t.Fatalf("expired offer = %+v", expired)
	}
	if err := s.CheckSetupToken(ctx, hash, t0); err != ErrSetupClosed {
		t.Fatalf("expired token: CheckSetupToken = %v, want ErrSetupClosed", err)
	}

	// a used token is not revived either
	s = openTemp(t)
	execT(t, s, `INSERT INTO setup_token (hash, expires_at, used_at) VALUES (?, ?, ?)`, hash, expires.Unix(), t0.Unix())
	used, err := s.OfferSetupToken(ctx, hash, expires.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !used.Found || !used.Used || !used.Expires.Equal(expires) {
		t.Fatalf("used offer = %+v", used)
	}

	// an admin exists: nothing is offered
	s = openTemp(t)
	mkAdmin(t, s, "adm_a", "Ada")
	closed, err := s.OfferSetupToken(ctx, hash, expires)
	if err != nil {
		t.Fatal(err)
	}
	if !closed.AdminExists || closed.Found {
		t.Fatalf("admin exists: offer = %+v", closed)
	}
	if err := s.R.QueryRow(`SELECT count(*) FROM setup_token`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("setup_token rows = %d, %v; want 0", rows, err)
	}
}

func TestSetSettingsIfAbsent(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if err := s.SetSettings(ctx, map[string]string{"link_prefix": "/kept/", "admin_host": ""}); err != nil {
		t.Fatal(err)
	}
	got, err := s.SetSettingsIfAbsent(ctx, map[string]string{"link_prefix": "/new/", "agent_sni": "sni.example.com", "admin_host": "other.example.com", "sub_prefix": "/sub/"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"link_prefix": "/kept/", "agent_sni": "sni.example.com", "admin_host": "", "sub_prefix": "/sub/"}
	if len(got) != len(want) {
		t.Fatalf("returned %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("returned %s = %q, want %q", k, got[k], v)
		}
	}
	stored, err := s.SettingValues(ctx, []string{"link_prefix", "agent_sni", "admin_host", "sub_prefix"})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range want {
		if stored[k] != v {
			t.Errorf("stored %s = %q, want %q", k, stored[k], v)
		}
	}
	if out, err := s.SetSettingsIfAbsent(ctx, nil); err != nil || len(out) != 0 {
		t.Fatalf("no settings: %v, %v", out, err)
	}
}
