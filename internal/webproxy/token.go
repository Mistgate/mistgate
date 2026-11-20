package webproxy

import (
	"encoding/base64"
	"io"
	"sync"
	"time"
)

const (
	TokenBootstrap TokenKind = 1
	TokenSession   TokenKind = 2

	BootstrapTokenTTL = 2 * time.Minute
	tokenSize         = 32
	tokenTextSize     = 43
	tokenIssueRetries = 8
)

type TokenKind uint8

type TokenInfo struct {
	Kind      TokenKind
	ExpiresAt time.Time
}

type tokenError string

func (err tokenError) Error() string { return string(err) }

const (
	errInvalidTokenStore tokenError = "invalid token store configuration"
	errInvalidTokenKind  tokenError = "invalid token kind"
	errInvalidTokenTTL   tokenError = "invalid token lifetime"
	errTokenStoreFull    tokenError = "token store capacity reached"
	errTokenRandom       tokenError = "unable to generate token"
	errTokenCollision    tokenError = "unable to generate unique token"
)

type tokenRecord struct {
	kind      TokenKind
	expiresAt time.Time
}

// TokenStore keeps opaque bearer tokens in memory. Bootstrap tokens are removed
// on their first successful Use; session tokens remain until expiry or deletion.
type TokenStore struct {
	mu         sync.Mutex
	maxEntries int
	random     io.Reader
	now        func() time.Time
	entries    map[string]tokenRecord
}

func NewTokenStore(maxEntries int, random io.Reader, now func() time.Time) (*TokenStore, error) {
	if maxEntries < 1 || random == nil || now == nil {
		return nil, errInvalidTokenStore
	}
	return &TokenStore{
		maxEntries: maxEntries,
		random:     random,
		now:        now,
		entries:    make(map[string]tokenRecord),
	}, nil
}

// Issue creates a random 32-byte base64url token with the supplied lifetime.
func (s *TokenStore) Issue(kind TokenKind, lifetime time.Duration) (string, error) {
	if s == nil {
		return "", errInvalidTokenStore
	}
	if !knownTokenKind(kind) {
		return "", errInvalidTokenKind
	}
	if lifetime <= 0 {
		return "", errInvalidTokenTTL
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.purgeExpired(now)
	if len(s.entries) >= s.maxEntries {
		return "", errTokenStoreFull
	}

	for attempt := 0; attempt < tokenIssueRetries; attempt++ {
		var raw [tokenSize]byte
		if _, err := io.ReadFull(s.random, raw[:]); err != nil {
			return "", errTokenRandom
		}
		token := base64.RawURLEncoding.EncodeToString(raw[:])
		if _, exists := s.entries[token]; exists {
			continue
		}
		s.entries[token] = tokenRecord{kind: kind, expiresAt: now.Add(lifetime)}
		return token, nil
	}
	return "", errTokenCollision
}

// Use validates a token at the injected current time. A successful bootstrap
// use consumes the token; a session token may be used repeatedly until expiry.
func (s *TokenStore) Use(token string, expectedKind TokenKind) (TokenInfo, bool) {
	if s == nil || !knownTokenKind(expectedKind) {
		return TokenInfo{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	record, ok := s.entries[token]
	if !ok {
		return TokenInfo{}, false
	}
	if !s.now().Before(record.expiresAt) {
		delete(s.entries, token)
		return TokenInfo{}, false
	}
	if record.kind != expectedKind {
		return TokenInfo{}, false
	}
	if record.kind == TokenBootstrap {
		delete(s.entries, token)
	}
	return TokenInfo{Kind: record.kind, ExpiresAt: record.expiresAt}, true
}

func (s *TokenStore) Delete(token string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entries[token]; !ok {
		return false
	}
	delete(s.entries, token)
	return true
}

func (s *TokenStore) purgeExpired(now time.Time) {
	for token, record := range s.entries {
		if !now.Before(record.expiresAt) {
			delete(s.entries, token)
		}
	}
}

func knownTokenKind(kind TokenKind) bool {
	return kind == TokenBootstrap || kind == TokenSession
}
