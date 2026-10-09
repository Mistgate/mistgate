package hysteria2

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mistgate/mistgate/internal/node/ratelimit"
	"github.com/mistgate/mistgate/internal/plugin"
)

// credState is one credential on one inbound. Counters and limiters are atomics so the data path (LogTraffic)
// takes no lock; the struct survives credential index swaps (and inbound restarts) so counters, open sessions
// and kick tokens stay attached to the same credential.
type credState struct {
	id        string
	inboundID string
	hash      [32]byte // sha256(auth token); immutable (a changed token makes a new credState)
	owner     atomic.Pointer[credIdentity]

	validUntil atomic.Int64 // unix seconds, 0 = never
	limit      atomic.Pointer[ratelimit.Limiter]
	up, down   atomic.Uint64 // user's view: core tx = up, core rx = down
	conns      atomic.Int32  // open QUIC connections
	kicks      atomic.Int32  // connections still to be closed at their next packet (see takeKick)
	kickUntil  atomic.Int64  // unix nano; unused kick tokens expire
	lastKill   atomic.Int64  // unix nano of the last token taken
	removed    atomic.Bool   // dropped from the index; its sessions are dying
}

type credIdentity struct{ userID string }

// index is an immutable view of the credentials of one inbound, swapped as a whole.
type index struct {
	byHash map[[32]byte]*credState
	byID   map[string]*credState
}

type parsedCred struct {
	c    plugin.UserCred
	hash [32]byte
}

// buildIndex validates creds (nothing is touched on error), then reuses the states of credentials that
// already exist in old and updates their mutable fields.
func buildIndex(inboundID string, old *index, creds []plugin.UserCred) (*index, error) {
	parsed := make([]parsedCred, 0, len(creds))
	seenID := make(map[string]struct{}, len(creds))
	seenHash := make(map[[32]byte]string, len(creds))
	for _, c := range creds {
		if c.CredID == "" {
			return nil, fmt.Errorf("credential without id")
		}
		var d struct {
			AuthSHA256 string `json:"auth_sha256"`
		}
		if err := json.Unmarshal(c.Data, &d); err != nil {
			return nil, fmt.Errorf("credential %s: data: %w", c.CredID, err)
		}
		raw, err := hex.DecodeString(d.AuthSHA256)
		if err != nil || len(raw) != sha256.Size {
			return nil, fmt.Errorf("credential %s: auth_sha256 must be 64 hex chars", c.CredID)
		}
		var h [32]byte
		copy(h[:], raw)
		if _, dup := seenID[c.CredID]; dup {
			return nil, fmt.Errorf("duplicate credential id %s", c.CredID)
		}
		if other, dup := seenHash[h]; dup {
			return nil, fmt.Errorf("credentials %s and %s share one token", other, c.CredID)
		}
		seenID[c.CredID] = struct{}{}
		seenHash[h] = c.CredID
		parsed = append(parsed, parsedCred{c, h})
	}

	ix := &index{
		byHash: make(map[[32]byte]*credState, len(parsed)),
		byID:   make(map[string]*credState, len(parsed)),
	}
	for _, p := range parsed {
		var cs *credState
		if old != nil {
			if prev := old.byID[p.c.CredID]; prev != nil && prev.hash == p.hash {
				cs = prev
			}
			// A rotated token gets a fresh credState; connections opened with the old token keep
			// running (LogTraffic only sees the id) until a Kick or their idle timeout.
		}
		if cs == nil {
			cs = &credState{id: p.c.CredID, inboundID: inboundID, hash: p.hash}
		}
		cs.owner.Store(&credIdentity{userID: p.c.UserID})
		var vu int64
		if !p.c.ValidUntil.IsZero() {
			vu = p.c.ValidUntil.Unix()
		}
		cs.validUntil.Store(vu)
		switch cur := cs.limit.Load(); {
		case p.c.RateLimitBps == 0:
			cs.limit.Store(nil)
		case cur == nil || cur.BPS() != p.c.RateLimitBps:
			cs.limit.Store(ratelimit.New(p.c.RateLimitBps))
		}
		ix.byHash[p.hash] = cs
		ix.byID[p.c.CredID] = cs
	}
	return ix, nil
}

// ---- hot path: hysteria core callbacks, all on *inbound ----

// Authenticate is called once per new QUIC connection.
func (in *inbound) Authenticate(_ net.Addr, auth string, _ uint64) (bool, string) {
	h := sha256.Sum256([]byte(auth))
	cs := in.idx.Load().byHash[h]
	// The map key is already sha256(token), so probing it leaks nothing usable about the token; the explicit
	// constant-time compare is belt and braces and costs nothing.
	if cs == nil || subtle.ConstantTimeCompare(cs.hash[:], h[:]) != 1 {
		return false, ""
	}
	if vu := cs.validUntil.Load(); vu != 0 && in.e.now().Unix() >= vu {
		return false, ""
	}
	return true, cs.id
}

// LogTraffic runs for every chunk. Returning false makes the core close the whole QUIC connection (it has
// no per-stream kick), so it is also the kick mechanism: unknown/removed credential, expired term, or an
// explicit Kick token. tx/rx are server-remote: tx = client upload, rx = client download.
func (in *inbound) LogTraffic(id string, tx, rx uint64) bool {
	cs := in.idx.Load().byID[id]
	if cs == nil {
		return false
	}
	if vu := cs.validUntil.Load(); vu != 0 && in.e.now().Unix() >= vu {
		return false
	}
	if cs.takeKick(time.Now()) {
		return false
	}
	if l := cs.limit.Load(); l != nil {
		// One bucket per direction; for UDP this stalls the connection's single receive loop,
		// which is acceptable for an experimental limit. Waits end when the inbound stops.
		if !l.WaitUp(in.ctx, tx) || !l.WaitDown(in.ctx, rx) {
			return false
		}
	}
	cs.up.Add(tx)
	cs.down.Add(rx)
	return true
}

// Kick model. LogTraffic only sees the credential id, never which connection is speaking, so Kick hands out
// one token per connection open at that moment and the next callbacks take them: each token closes one
// connection (the core closes the whole QUIC connection when LogTraffic returns false).
//   - Callbacks arriving within sameIncident of a taken token are one dying connection (its other stream
//     or direction) and fail without taking another token.
//   - Tokens are clamped to the number of open connections when one disconnects, and expire after kickTTL.
//
// An idle connection is reached only when it speaks (kickTTL bounds how long its token waits), and a
// fresh connection that speaks first may take a token meant for an idle one and be closed once; clients
// reconnect. Removing the credential (index swap) has no such ambiguity.
const (
	kickTTL      = 2 * time.Minute
	sameIncident = 250 * time.Millisecond
)

func (cs *credState) takeKick(now time.Time) bool {
	for {
		k := cs.kicks.Load()
		if k <= 0 {
			return false
		}
		n := now.UnixNano()
		if n > cs.kickUntil.Load() {
			cs.kicks.CompareAndSwap(k, 0)
			return false
		}
		if n-cs.lastKill.Load() < int64(sameIncident) {
			return true
		}
		if cs.kicks.CompareAndSwap(k, k-1) {
			cs.lastKill.Store(n)
			return true
		}
	}
}

func (cs *credState) kick(open int32, now time.Time) {
	cs.lastKill.Store(0)
	cs.kickUntil.Store(now.Add(kickTTL).UnixNano())
	cs.kicks.Store(open)
}

// clampKicks keeps tokens <= open connections: a connection that died on its own must not leave a
// token behind that would later kill a fresh connection.
func (cs *credState) clampKicks(open int32) {
	for {
		k := cs.kicks.Load()
		if k <= open || cs.kicks.CompareAndSwap(k, open) {
			return
		}
	}
}

// Sessions come from EventLogger.Connect/Disconnect, which carry the remote address and fire once per
// QUIC connection; TrafficLogger.LogOnlineState has no address and is not needed.

type session struct {
	cs    *credState
	since time.Time
}

func (in *inbound) Connect(addr net.Addr, id string, _ uint64) {
	cs := in.idx.Load().byID[id]
	if cs == nil {
		return
	}
	s := session{cs: cs, since: in.e.now()}
	in.smu.Lock()
	in.sessions[addr.String()] = s
	in.smu.Unlock()
	cs.conns.Add(1)
}

func (in *inbound) Disconnect(addr net.Addr, _ string, _ error) {
	in.smu.Lock()
	s, ok := in.sessions[addr.String()]
	delete(in.sessions, addr.String())
	in.smu.Unlock()
	if ok {
		s.cs.clampKicks(s.cs.conns.Add(-1))
	}
}

// Everything else the core offers is deliberately ignored: per-request events would be destination logs.
func (in *inbound) LogOnlineState(string, bool)               {}
func (in *inbound) TCPRequest(_ net.Addr, id, reqAddr string) { in.rememberRequest(reqAddr, id) }
func (in *inbound) TCPError(net.Addr, string, string, error)  {}
func (in *inbound) UDPRequest(_ net.Addr, id string, _ uint32, reqAddr string) {
	in.rememberRequest(reqAddr, id)
}
func (in *inbound) UDPError(net.Addr, string, uint32, error) {}

const pendingRequestTTL = 5 * time.Second

type pendingRequest struct {
	userID    string
	count     int
	ambiguous bool
	expires   time.Time
}

func requestKey(addr string) string { return strings.ToLower(strings.TrimSpace(addr)) }

// rememberRequest correlates Hysteria's EventLogger callback with its following Outbound call. The
// server API does not carry a user identifier to Outbound; concurrent requests to the same destination
// are deliberately marked ambiguous when their owners differ.
func (in *inbound) rememberRequest(addr, credID string) {
	if in == nil || !in.e.torrentEnabled.Load() {
		return
	}
	key := requestKey(addr)
	if key == "" {
		return
	}
	var userID string
	if ix := in.idx.Load(); ix != nil {
		if cs := ix.byID[credID]; cs != nil {
			if owner := cs.owner.Load(); owner != nil {
				userID = owner.userID
			}
		}
	}
	now := time.Now()
	in.requestMu.Lock()
	if in.pendingRequests == nil {
		in.pendingRequests = make(map[string]pendingRequest)
	}
	for k, pending := range in.pendingRequests {
		if !pending.expires.After(now) {
			delete(in.pendingRequests, k)
		}
	}
	if pending, ok := in.pendingRequests[key]; ok {
		pending.count++
		if pending.userID != userID {
			pending.ambiguous = true
		}
		pending.expires = now.Add(pendingRequestTTL)
		in.pendingRequests[key] = pending
	} else if len(in.pendingRequests) < 256 {
		in.pendingRequests[key] = pendingRequest{userID: userID, count: 1, expires: now.Add(pendingRequestTTL)}
	}
	in.requestMu.Unlock()
}

func (in *inbound) takeRequestUserID(addr string) string {
	if in == nil {
		return ""
	}
	key := requestKey(addr)
	now := time.Now()
	in.requestMu.Lock()
	defer in.requestMu.Unlock()
	pending, ok := in.pendingRequests[key]
	if !ok || !pending.expires.After(now) {
		delete(in.pendingRequests, key)
		return ""
	}
	if pending.count <= 1 {
		delete(in.pendingRequests, key)
	} else {
		pending.count--
		in.pendingRequests[key] = pending
	}
	if pending.ambiguous {
		return ""
	}
	return pending.userID
}
