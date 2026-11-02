package mtproto

import (
	"hash/maphash"
	"net"
	"net/netip"
	"sync"
	"time"
)

// guard bounds what unauthenticated connections cost. Three limits, because each closes a different hole:
//
//   - handshake slots, per server and per remote address: a connection that sends nothing holds a goroutine and a
//     buffer until the deadline, so a few addresses must not be able to hold them all;
//   - the failure budget, per remote address: a hello for one of the node's domains costs one HMAC over up to 4 KB per
//     configured credential, and a browser visiting the decoy site sends exactly such hellos. Every HMAC scan that ends
//     without a match (or a replayed hello) spends one unit; an address with no budget left goes to the fallback
//     without a scan. A scan that matches is refunded, so clients that authenticate never run out;
//   - the replay cache size, in total and per credential (replayCache).
//
// Addresses are keyed by IPv4 /32 and IPv6 /64 (one end site). The failure budgets live in a fixed table of slots, two
// per address chosen by a keyed hash, and an address's budget is the fuller of its two (a count-min sketch with
// conservative update): constant memory whatever the number of addresses, and an address is limited by someone else
// only when both its slots are shared with spent ones. With 8192 slots and k addresses spending, that is about
// (2k/8192)^2 for a given client, against k/8192 with one slot; with a random seed nobody can aim at a client's slots.
type guard struct {
	max, maxPer int
	seed        maphash.Seed

	mu     sync.Mutex
	total  int
	active map[netip.Prefix]int
	full   [failSlots]int64 // per slot: when (unix ns) its budget is whole again
}

const (
	failSlots    = 8192            // 64 KB per inbound
	failBurst    = 16              // failed scans an address may make at once
	failInterval = 4 * time.Second // and one more per interval after that
)

func newGuard(max, maxPer int) *guard {
	return &guard{max: max, maxPer: maxPer, seed: maphash.MakeSeed(), active: make(map[netip.Prefix]int)}
}

// enter takes a handshake slot; false when the server or the address has none left.
func (g *guard) enter(p netip.Prefix) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.total >= g.max || g.active[p] >= g.maxPer {
		return false
	}
	g.total++
	g.active[p]++
	return true
}

func (g *guard) leave(p netip.Prefix) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.total--
	if g.active[p]--; g.active[p] <= 0 {
		delete(g.active, p)
	}
}

// spend takes one unit of the address's failure budget before a scan and returns the mark refund needs; false (and
// nothing taken) when none is left.
func (g *guard) spend(p netip.Prefix, now time.Time) (int64, bool) {
	i, j := g.slots(p)
	t := now.UnixNano()
	g.mu.Lock()
	defer g.mu.Unlock()
	next := max(min(g.full[i], g.full[j]), t) + int64(failInterval)
	if next-t > failBurst*int64(failInterval) {
		return 0, false
	}
	g.full[i], g.full[j] = max(g.full[i], next), max(g.full[j], next)
	return next, true
}

// refund gives the unit back after a scan that authenticated, in the slots nobody has spent from since.
func (g *guard) refund(p netip.Prefix, mark int64) {
	i, j := g.slots(p)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.full[i] == mark {
		g.full[i] -= int64(failInterval)
	}
	if j != i && g.full[j] == mark {
		g.full[j] -= int64(failInterval)
	}
}

func (g *guard) slots(p netip.Prefix) (int, int) {
	a := p.Addr().As16()
	h := maphash.Bytes(g.seed, a[:])
	return int(h % failSlots), int((h >> 32) % failSlots)
}

// remoteKey is the limit key of a connection's peer. Connections without an IP address (pipes in tests) share one.
func remoteKey(c net.Conn) netip.Prefix {
	var ap netip.AddrPort
	switch a := c.RemoteAddr().(type) {
	case *net.TCPAddr:
		ap = a.AddrPort()
	case nil:
	default:
		ap, _ = netip.ParseAddrPort(a.String())
	}
	addr := ap.Addr().Unmap()
	bits := 32
	if addr.Is6() {
		bits = 64
	}
	p, _ := addr.Prefix(bits)
	return p
}

// replayCache remembers the hellos that authenticated, for ReplayWindow: a censor that records a client's hello and
// sends it again must get the decoy site, not a ServerHello. The key is the first 16 bytes of the hello random (HMAC
// output, so only someone holding a key can add entries). The window is fixed, so insertion order is expiry order and
// a FIFO is enough. The size is a hard bound: when the cache is full of entries that have not expired, a new hello is
// rejected rather than an unexpired entry dropped (which would reopen the replay) or the cache grown.
//
// Every key holder can add entries, so each credential may hold at most perCred of them: one key (a leaked one, or a
// client gone wild) then fills its own share and its own new hellos are refused, not every other credential's.
type replayCache struct {
	window       time.Duration
	max, perCred int

	mu    sync.Mutex
	seen  map[[16]byte]struct{}
	count map[string]int // live entries per credential
	fifo  []replayEntry
	head  int
}

type replayEntry struct {
	key     [16]byte
	cred    string
	expires int64
}

type replayResult int

const (
	replayAdded replayResult = iota
	replaySeen
	replayFull
	replayQuota // the credential holds its whole share
)

func newReplayCache(window time.Duration, max, perCred int) *replayCache {
	return &replayCache{window: window, max: max, perCred: perCred, seen: make(map[[16]byte]struct{}), count: make(map[string]int)}
}

// has reports a hello seen within the window; it is checked before the HMAC so a replay costs nothing.
func (c *replayCache) has(key [16]byte, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire(now.UnixNano())
	_, ok := c.seen[key]
	return ok
}

// add records an authenticated hello of credential cred. Two copies of one hello racing through the HMAC both get here;
// one is added.
func (c *replayCache) add(key [16]byte, cred string, now time.Time) replayResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := now.UnixNano()
	c.expire(t)
	if _, ok := c.seen[key]; ok {
		return replaySeen
	}
	if len(c.fifo)-c.head >= c.max {
		return replayFull
	}
	if c.count[cred] >= c.perCred {
		return replayQuota
	}
	c.seen[key] = struct{}{}
	c.count[cred]++
	c.fifo = append(c.fifo, replayEntry{key: key, cred: cred, expires: t + int64(c.window)})
	return replayAdded
}

func (c *replayCache) expire(t int64) {
	for c.head < len(c.fifo) && c.fifo[c.head].expires <= t {
		e := &c.fifo[c.head]
		delete(c.seen, e.key)
		if c.count[e.cred]--; c.count[e.cred] <= 0 {
			delete(c.count, e.cred)
		}
		c.head++
	}
	if c.head > 0 && c.head >= len(c.fifo)/2 { // compact; amortized O(1), the slice never exceeds 2*max
		c.fifo = append(c.fifo[:0], c.fifo[c.head:]...)
		c.head = 0
	}
}
