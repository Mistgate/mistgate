package access

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"
)

// The token index is the hashes of the subscription tokens this process knows to exist. The public endpoint asks it, for a
// request from a network that is blocked for guessing, whether a token can be real without a database call: a token in it
// is served by the normal route, one that is not is checked against the database only within a small per-network budget.
//
// It is a positive filter only. A hash in it that no longer exists (a deleted user, a rotated link) costs one lookup, which
// removes it. A token missing from it that exists (made by another panel process or directly in the database) is found by
// that bounded check, or by the next reload. It is filled lazily, by the first question it cannot answer, and reloaded at most
// once per tokenIndexRefresh; every create, rotate and successful lookup in this process adds to it at once.
const (
	tokenIndexRefresh = 5 * time.Minute
	tokenIndexRetry   = 10 * time.Second // after a failed load
)

type tokenIndex struct {
	mu  sync.RWMutex
	set map[[32]byte]struct{}

	loadMu   sync.Mutex // one load at a time; held while the database is read
	loadedAt time.Time  // of the last successful load (zero: never); guarded by loadMu
	retryAt  time.Time  // no load before it; guarded by loadMu
}

func tokenKey(token string) [32]byte { return sha256.Sum256([]byte(token)) }

func (ix *tokenIndex) has(k [32]byte) bool {
	ix.mu.RLock()
	_, ok := ix.set[k]
	ix.mu.RUnlock()
	return ok
}

func (ix *tokenIndex) add(k [32]byte) {
	if ix.has(k) {
		return
	}
	ix.mu.Lock()
	if ix.set == nil {
		ix.set = map[[32]byte]struct{}{}
	}
	ix.set[k] = struct{}{}
	ix.mu.Unlock()
}

func (ix *tokenIndex) remove(k [32]byte) {
	if !ix.has(k) { // the common case: a guess, not a deleted link
		return
	}
	ix.mu.Lock()
	delete(ix.set, k)
	ix.mu.Unlock()
}

// KnownSubscriptionToken reports whether the token is in the index. A token that is not in it makes the index reload from the
// database when the last load is older than tokenIndexRefresh (never more often, however many tokens ask); a failed load
// answers false and is not retried for tokenIndexRetry.
func (s *Service) KnownSubscriptionToken(ctx context.Context, token string) bool {
	ix, k := &s.tokens, tokenKey(token)
	if ix.has(k) {
		return true
	}
	ix.loadMu.Lock()
	defer ix.loadMu.Unlock()
	if ix.has(k) { // loaded or added while this call waited
		return true
	}
	now := s.now()
	if now.Before(ix.retryAt) || (!ix.loadedAt.IsZero() && now.Sub(ix.loadedAt) < tokenIndexRefresh) {
		return false
	}
	hashes, err := s.st.Access().SubTokenHashes(ctx)
	if err != nil {
		ix.retryAt = now.Add(tokenIndexRetry)
		return false
	}
	ix.mu.Lock()
	if ix.set == nil {
		ix.set = make(map[[32]byte]struct{}, len(hashes))
	}
	for _, h := range hashes {
		ix.set[h] = struct{}{}
	}
	ix.mu.Unlock()
	ix.loadedAt = now
	return ix.has(k)
}
