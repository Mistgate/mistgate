package trusttunnel

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mistgate/mistgate/internal/node/ratelimit"
	"github.com/mistgate/mistgate/internal/plugin"
)

// index is immutable after publication. The handlers load it atomically and
// never take the engine-wide mutex while authenticating or copying traffic.
type index struct {
	byUser map[string]*credState
	byID   map[string]*credState
}

type credState struct {
	inboundID string
	id        string
	username  string
	passHash  [sha256.Size]byte

	validUntil atomic.Int64
	limit      atomic.Pointer[ratelimit.Limiter]
	person     atomic.Pointer[ratelimit.Limiter]
	up         atomic.Uint64
	down       atomic.Uint64
	authFailed atomic.Uint32
	connects   atomic.Uint32
	removed    atomic.Bool

	mu          sync.Mutex
	connections map[*connectionState]time.Time
	streams     map[*streamState]struct{}
	active      int
}

func buildIndex(inboundID string, old *index, creds []plugin.UserCred, users *ratelimit.Registry) (*index, []plugin.UserCred, []string) {
	ordered := cloneCredentials(creds)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].CredID < ordered[j].CredID })
	next := &index{
		byUser: make(map[string]*credState, len(ordered)),
		byID:   make(map[string]*credState, len(ordered)),
	}
	seenID := make(map[string]struct{}, len(ordered))
	var accepted []plugin.UserCred
	var warnings []string
	type parsed struct {
		credential plugin.UserCred
		username   string
		hash       [sha256.Size]byte
	}
	var parsedCreds []parsed
	for _, cred := range ordered {
		if cred.CredID == "" {
			warnings = append(warnings, "credential without an id was skipped")
			continue
		}
		if _, exists := seenID[cred.CredID]; exists {
			warnings = append(warnings, "duplicate credential id was skipped")
			continue
		}
		seenID[cred.CredID] = struct{}{}
		var data map[string]string
		if err := json.Unmarshal(cred.Data, &data); err != nil {
			warnings = append(warnings, "credential data was invalid")
			continue
		}
		username := data["user"]
		if username == "" {
			warnings = append(warnings, "credential user was missing")
			continue
		}
		rawHash, err := hex.DecodeString(data["pass_sha256"])
		if err != nil || len(rawHash) != sha256.Size {
			warnings = append(warnings, "credential password hash was invalid")
			continue
		}
		if _, exists := next.byUser[username]; exists {
			warnings = append(warnings, "duplicate credential user was skipped")
			continue
		}
		var passHash [sha256.Size]byte
		copy(passHash[:], rawHash)
		parsedCreds = append(parsedCreds, parsed{credential: cred, username: username, hash: passHash})
		// Reserve a username before constructing the immutable index so a later
		// credential can never shadow the earlier, sorted credential.
		next.byUser[username] = nil
	}

	holders := make([]ratelimit.Holder, len(parsedCreds))
	for i, value := range parsedCreds {
		holders[i] = ratelimit.Holder{CredID: value.credential.CredID, UserID: value.credential.UserID, BPS: value.credential.UserRateLimitBps}
	}
	personLimits := users.Sync(inboundID, holders)

	for _, value := range parsedCreds {
		cred := value.credential
		var cs *credState
		if old != nil {
			if previous := old.byID[cred.CredID]; previous != nil &&
				previous.username == value.username &&
				subtle.ConstantTimeCompare(previous.passHash[:], value.hash[:]) == 1 {
				cs = previous
			}
		}
		if cs == nil {
			cs = &credState{
				inboundID: inboundID, id: cred.CredID, username: value.username, passHash: value.hash,
				connections: make(map[*connectionState]time.Time), streams: make(map[*streamState]struct{}),
			}
		}
		cs.inboundID, cs.id, cs.username, cs.passHash = inboundID, cred.CredID, value.username, value.hash
		cs.removed.Store(false)
		var validUntil int64
		if !cred.ValidUntil.IsZero() {
			validUntil = cred.ValidUntil.Unix()
		}
		cs.validUntil.Store(validUntil)
		switch current := cs.limit.Load(); {
		case cred.RateLimitBps == 0:
			cs.limit.Store(nil)
		case current == nil || current.BPS() != cred.RateLimitBps:
			cs.limit.Store(ratelimit.New(cred.RateLimitBps))
		}
		cs.person.Store(personLimits[cred.CredID])
		next.byUser[value.username] = cs
		next.byID[cred.CredID] = cs
		accepted = append(accepted, cred)
	}
	return next, accepted, warnings
}

func (cs *credState) expired(now time.Time) bool {
	until := cs.validUntil.Load()
	return until != 0 && now.Unix() >= until
}

// retire prevents new streams and closes every connection that used this
// credential. The state remains in eng.retired until Collect drains its tail.
func (cs *credState) retire() {
	cs.mu.Lock()
	cs.removed.Store(true)
	connections := make([]*connectionState, 0, len(cs.connections))
	for conn := range cs.connections {
		connections = append(connections, conn)
	}
	streams := make([]*streamState, 0, len(cs.streams))
	for stream := range cs.streams {
		streams = append(streams, stream)
	}
	cs.mu.Unlock()
	for _, conn := range connections {
		conn.detach(cs)
	}
	for _, stream := range streams {
		stream.abort()
	}
}
