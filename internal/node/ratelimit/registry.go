package ratelimit

import "sync"

// Registry holds the person limit: one Limiter per user id, shared by every credential of that user on this node, in
// every inbound and every engine. The buckets are per node on purpose: a person on two nodes at once can reach twice the
// limit, there is no coordination between nodes.
//
// A credential is a holder of its user's bucket. Holders come and go inbound by inbound (Sync); the bucket lives while
// it has at least one holder, changes its rate in place when the value changes (everything that got it from Sync follows),
// and is dropped with its last holder. The credentials of one user carry the same value; if they disagree (an inbound
// applied before its neighbour) the bucket takes the smallest, so the outcome does not depend on the order of arrival.
type Registry struct {
	mu     sync.Mutex
	clk    clock
	users  map[string]*userEntry
	scopes map[string]map[string]string // scope -> credential id -> user id
}

type userEntry struct {
	lim     *Limiter
	holders map[holderKey]uint64 // -> the credential's person limit, never 0
}

type holderKey struct{ scope, cred string }

// Holder is one credential asking for its user's bucket. BPS 0 (or no user) means the credential has no person limit.
type Holder struct {
	CredID, UserID string
	BPS            uint64
}

func NewRegistry() *Registry { return newRegistry(realClock) }

func newRegistry(clk clock) *Registry {
	return &Registry{clk: clk, users: map[string]*userEntry{}, scopes: map[string]map[string]string{}}
}

// Sync makes hs the complete set of holders of scope (an inbound id; nil/empty releases the scope) and returns the
// bucket of each limited credential, keyed by credential id. The credentials the scope no longer lists let go of their
// buckets; a bucket nobody holds any more is dropped. Safe for concurrent use; the returned limiters are used without
// any registry lock.
func (r *Registry) Sync(scope string, hs []Holder) map[string]*Limiter {
	if r == nil { // no registry: no person limits
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	touched := map[string]struct{}{}
	for cred, uid := range r.scopes[scope] {
		delete(r.users[uid].holders, holderKey{scope, cred})
		touched[uid] = struct{}{}
	}
	delete(r.scopes, scope)
	var mine map[string]string
	for _, h := range hs {
		if h.BPS == 0 || h.UserID == "" {
			continue
		}
		e := r.users[h.UserID]
		if e == nil {
			e = &userEntry{holders: map[holderKey]uint64{}}
			r.users[h.UserID] = e
		}
		if mine == nil {
			mine = make(map[string]string, len(hs))
		}
		e.holders[holderKey{scope, h.CredID}] = h.BPS
		mine[h.CredID] = h.UserID
		touched[h.UserID] = struct{}{}
	}
	if mine != nil {
		r.scopes[scope] = mine
	}
	for uid := range touched {
		e := r.users[uid]
		var least uint64
		for _, bps := range e.holders {
			if least == 0 || bps < least {
				least = bps
			}
		}
		switch {
		case least == 0: // no holder left
			delete(r.users, uid)
		case e.lim == nil:
			e.lim = newWithClock(least, r.clk)
		default:
			e.lim.SetBPS(least)
		}
	}
	if mine == nil {
		return nil
	}
	out := make(map[string]*Limiter, len(mine))
	for cred, uid := range mine {
		out[cred] = r.users[uid].lim
	}
	return out
}

// Limiter returns the bucket of a user, nil when no credential of theirs is limited on this node.
func (r *Registry) Limiter(userID string) *Limiter {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.users[userID]; e != nil {
		return e.lim
	}
	return nil
}
