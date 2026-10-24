package hysteria2

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"github.com/apernet/hysteria/core/v2/server"
	quic "github.com/apernet/quic-go"
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

	validUntil   atomic.Int64                      // unix seconds, 0 = never
	limit        atomic.Pointer[ratelimit.Limiter] // the device limit: this credential's own bucket
	person       atomic.Pointer[ratelimit.Limiter] // the person limit: the bucket of the whole user on this node (ratelimit.Registry)
	up, down     atomic.Uint64                     // user's view: core tx = up, core rx = down
	flushed      atomic.Uint64                     // bytes reported by Collect; used only to classify idle drops
	conns        atomic.Int32                      // open QUIC connections
	connects     atomic.Uint32
	rejects      atomic.Uint32
	drops        atomic.Uint32
	clientCloses atomic.Uint32
	freezes      atomic.Uint32
	quiet        atomic.Uint32
	rttMSSum     atomic.Uint64
	rttSamples   atomic.Uint32
	pktsSent     atomic.Uint64
	pktsLost     atomic.Uint64
	kicks        atomic.Int32 // connections still to be closed at their next packet (see takeKick)
	kickUntil    atomic.Int64 // unix nano; unused kick tokens expire
	lastKill     atomic.Int64 // unix nano of the last token taken
	removed      atomic.Bool  // dropped from the index; its sessions are dying
	inflight     atomic.Int32 // LogTraffic callbacks running; a retired state is kept until they end
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
// already exist in old and updates their mutable fields. users hands out the person buckets of the inbound's
// credentials (nil: no person limits).
func buildIndex(inboundID string, old *index, creds []plugin.UserCred, users *ratelimit.Registry) (*index, error) {
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
	holders := make([]ratelimit.Holder, len(parsed))
	for i, p := range parsed {
		holders[i] = ratelimit.Holder{CredID: p.c.CredID, UserID: p.c.UserID, BPS: p.c.UserRateLimitBps}
	}
	shared := users.Sync(inboundID, holders)
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
		cs.person.Store(shared[p.c.CredID])
		ix.byHash[p.hash] = cs
		ix.byID[p.c.CredID] = cs
	}
	return ix, nil
}

// ---- hot path: hysteria core callbacks, all on *inbound ----

// Authenticate is called once per new QUIC connection.
func (in *inbound) Authenticate(_ net.Addr, auth string, _ uint64) (bool, string) {
	in.handshakes.Add(1)
	h := sha256.Sum256([]byte(auth))
	cs := in.idx.Load().byHash[h]
	// The map key is already sha256(token), so probing it leaks nothing usable about the token; the explicit
	// constant-time compare is belt and braces and costs nothing.
	if cs == nil {
		in.unknownKeys.Add(1)
		return false, ""
	}
	if subtle.ConstantTimeCompare(cs.hash[:], h[:]) != 1 {
		return false, ""
	}
	if vu := cs.validUntil.Load(); vu != 0 && in.e.now().Unix() >= vu {
		cs.rejects.Add(1)
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
	// A retired state is flushed by Collect only while no callback holds it, or the bytes added after the flush are lost.
	cs.inflight.Add(1)
	defer cs.inflight.Add(-1)
	if vu := cs.validUntil.Load(); vu != 0 && in.e.now().Unix() >= vu {
		return false
	}
	if cs.takeKick(time.Now()) {
		return false
	}
	if device, person := cs.limit.Load(), cs.person.Load(); device != nil || person != nil {
		// One bucket per direction, the device's then the person's; for UDP this stalls the connection's single
		// receive loop, which is acceptable for an experimental limit. Waits end when the inbound stops.
		if !ratelimit.WaitUp(in.ctx, device, person, tx) || !ratelimit.WaitDown(in.ctx, device, person, rx) {
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

// Sessions are keyed by the QUIC connection because its address can move during port hopping or NAT rebinding.
// The old EventLogger methods below retain address-keyed state only for compatibility with an unpatched core.
type session struct {
	cs                 *credState
	since              time.Time
	conn               *quic.Conn
	startBytes         uint64
	last               quic.ConnectionStats
	silencePacketsRecv uint64
	silencePacketsSent uint64
}

func (in *inbound) Connect(addr net.Addr, id string, _ uint64) {
	cs := in.idx.Load().byID[id]
	if cs == nil {
		return
	}
	s := &session{cs: cs, since: in.e.now(), startBytes: cs.bytesMoved()}
	cs.conns.Add(1)
	cs.connects.Add(1)
	in.smu.Lock()
	if in.legacySessions == nil {
		in.legacySessions = make(map[string]*session)
	}
	in.legacySessions[addr.String()] = s
	in.smu.Unlock()
}

func (in *inbound) Disconnect(addr net.Addr, id string, err error) {
	in.smu.Lock()
	s, ok := in.legacySessions[addr.String()]
	if !ok || s.cs.id != id {
		ok = false
	}
	if ok {
		delete(in.legacySessions, addr.String())
	}
	in.smu.Unlock()
	if ok {
		in.endSession(s, err)
	}
}

func (in *inbound) Accepted(*quic.Conn) { in.accepted.Add(1) }

func (in *inbound) ConnConnect(conn *quic.Conn, id string, _ uint64) {
	cs := in.idx.Load().byID[id]
	if cs == nil || conn == nil {
		return
	}
	stats := connStats(conn)
	s := &session{
		cs: cs, since: in.e.now(), conn: conn, startBytes: stats.BytesSent + stats.BytesReceived,
		last: stats, silencePacketsRecv: stats.PacketsReceived, silencePacketsSent: stats.PacketsSent,
	}
	cs.conns.Add(1)
	cs.connects.Add(1)
	in.smu.Lock()
	if in.sessions == nil {
		in.sessions = make(map[*quic.Conn]*session)
	}
	in.sessions[conn] = s
	in.smu.Unlock()
}

func (in *inbound) ConnDisconnect(conn *quic.Conn, _ string, err error) {
	in.smu.Lock()
	s, ok := in.sessions[conn]
	if ok {
		delete(in.sessions, conn)
	}
	in.smu.Unlock()
	if ok {
		in.endSession(s, err)
	}
}

// sample adds packet and RTT deltas for one open connection. PacketsLost can decrease when quic-go later learns that a
// declared-lost packet arrived, so a decrease resets the baseline without subtracting from accumulated volume.
func (s *session) sample() {
	if s.conn == nil {
		return
	}
	current := connStats(s.conn)
	addConnectionDelta(s.cs, s.last, current)
	if current.PacketsReceived != s.last.PacketsReceived {
		// This is the latest snapshot with this receive count. Sends since it are evidence of a frozen return path.
		s.silencePacketsRecv = current.PacketsReceived
		s.silencePacketsSent = current.PacketsSent
	}
	s.last = current
}

// connStats is the connection's statistics with the lost packets as the patched core counts them: quic-go's own PacketsLost
// stays zero under Hysteria's BBR and Brutal (third_party/hysteria-core/MISTGATE.md).
func connStats(conn *quic.Conn) quic.ConnectionStats {
	st := conn.ConnectionStats()
	st.PacketsLost = server.LostPackets(conn)
	return st
}

func addConnectionDelta(cs *credState, previous, current quic.ConnectionStats) {
	if current.PacketsSent > previous.PacketsSent {
		cs.pktsSent.Add(current.PacketsSent - previous.PacketsSent)
	}
	if current.PacketsLost > previous.PacketsLost {
		cs.pktsLost.Add(current.PacketsLost - previous.PacketsLost)
	}
	if (current.BytesSent > previous.BytesSent || current.BytesReceived > previous.BytesReceived) && current.PacketsReceived >= 10 {
		if rttMS := current.SmoothedRTT.Milliseconds(); rttMS > 0 {
			cs.rttSamples.Add(1)
			cs.rttMSSum.Add(uint64(rttMS))
		}
	}
}

func (in *inbound) endSession(s *session, err error) {
	cs := s.cs
	var stats quic.ConnectionStats
	if s.conn != nil {
		stats = connStats(s.conn)
		addConnectionDelta(cs, s.last, stats)
	}
	var idleTimeout *quic.IdleTimeoutError
	if errors.As(err, &idleTimeout) {
		if isFreezeAtClose(stats, s.last, s.silencePacketsRecv, s.silencePacketsSent) && s.conn != nil {
			cs.freezes.Add(1)
		} else {
			cs.quiet.Add(1)
		}
		moved := cs.bytesMoved()
		if s.conn != nil {
			moved = stats.BytesSent + stats.BytesReceived
		}
		if moved < s.startBytes || moved-s.startBytes < 64<<10 || in.e.now().Sub(s.since) < 60*time.Second {
			cs.drops.Add(1)
		}
	} else if isClientClose(err) {
		cs.clientCloses.Add(1)
	}
	cs.clampKicks(cs.conns.Add(-1))
}

func isFreezeAtClose(current, last quic.ConnectionStats, silencePacketsRecv, silencePacketsSent uint64) bool {
	return current.PacketsReceived == last.PacketsReceived && current.PacketsReceived == silencePacketsRecv && current.PacketsSent > silencePacketsSent
}

func isClientClose(err error) bool {
	if err == nil {
		return true
	}
	var appErr *quic.ApplicationError
	return errors.As(err, &appErr) && appErr.Remote
}

func (cs *credState) bytesMoved() uint64 {
	return cs.flushed.Load() + cs.up.Load() + cs.down.Load()
}

func (in *inbound) takeLinkStats() plugin.LinkStat {
	return plugin.LinkStat{
		InboundID: in.spec.ID, UnknownKeys: in.unknownKeys.Swap(0), Hellos: in.hellos.Swap(0), Handshakes: in.handshakes.Swap(0),
		Accepted: in.accepted.Swap(0), HellosForeign: in.hellosForeign.Swap(0),
	}
}

func (cs *credState) takeLinkStats() plugin.LinkStat {
	return plugin.LinkStat{
		CredID: cs.id, InboundID: cs.inboundID,
		Connects: cs.connects.Swap(0), Rejects: cs.rejects.Swap(0), Drops: cs.drops.Swap(0),
		ClientCloses: cs.clientCloses.Swap(0), Freezes: cs.freezes.Swap(0), Quiet: cs.quiet.Swap(0),
		RTTMSSum: cs.rttMSSum.Swap(0), RTTSamples: cs.rttSamples.Swap(0),
		PktsSent: cs.pktsSent.Swap(0), PktsLost: cs.pktsLost.Swap(0),
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
