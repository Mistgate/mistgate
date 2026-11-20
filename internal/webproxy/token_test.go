package webproxy

import (
	"bytes"
	"encoding/base64"
	"testing"
	"time"
)

func TestTokenStoreOpaqueTokensAndBootstrapOneTimeUse(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	store, err := NewTokenStore(3, bytes.NewReader(tokenTestBytes(1, 3)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	bootstrap, err := store.Issue(TokenBootstrap, BootstrapTokenTTL)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(bootstrap)
	if err != nil || len(decoded) != tokenSize || len(bootstrap) != tokenTextSize {
		t.Fatal("issued token is not canonical 32-byte base64url")
	}
	info, ok := store.Use(bootstrap, TokenBootstrap)
	if !ok || info.Kind != TokenBootstrap || !info.ExpiresAt.Equal(now.Add(BootstrapTokenTTL)) {
		t.Fatal("bootstrap token was not accepted with its expiry")
	}
	if _, ok := store.Use(bootstrap, TokenBootstrap); ok {
		t.Fatal("bootstrap token was accepted more than once")
	}

	session, err := store.Issue(TokenSession, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if info, ok := store.Use(session, TokenSession); !ok || info.Kind != TokenSession {
			t.Fatal("session token was not reusable before expiry")
		}
	}
	if store.Delete(session) != true || store.Delete(session) {
		t.Fatal("Delete did not remove the session token exactly once")
	}
}

func TestTokenStoreExpiryAndCapacity(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	store, err := NewTokenStore(1, bytes.NewReader(tokenTestBytes(10, 3)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	bootstrap, err := store.Issue(TokenBootstrap, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Issue(TokenSession, time.Minute); err == nil {
		t.Fatal("token store exceeded its capacity")
	}
	now = now.Add(5 * time.Second)
	if _, ok := store.Use(bootstrap, TokenBootstrap); ok {
		t.Fatal("expired bootstrap token was accepted")
	}
	if _, err := store.Issue(TokenSession, time.Minute); err != nil {
		t.Fatalf("expired token did not release capacity: %v", err)
	}
}

func TestTokenStoreRejectsInvalidIssueArguments(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	store, err := NewTokenStore(1, bytes.NewReader(tokenTestBytes(20, 1)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Issue(TokenKind(99), time.Minute); err == nil {
		t.Fatal("Issue accepted an unknown token kind")
	}
	if _, err := store.Issue(TokenSession, 0); err == nil {
		t.Fatal("Issue accepted a zero lifetime")
	}
}

func tokenTestBytes(start byte, count int) []byte {
	data := make([]byte, 0, count*tokenSize)
	for n := 0; n < count; n++ {
		for i := 0; i < tokenSize; i++ {
			data = append(data, start+byte(n))
		}
	}
	return data
}
