// Package nets maps client addresses to ASNs inside the engine and aggregates connection events without retaining addresses.
package nets

import (
	"net"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/asndb"
)

const (
	maxRows       = 512
	maxPeople     = 20_000
	maxPersonRows = 2_048
	kPeople       = 3
)

// Event is one connection-level observation. It contains no address data.
type Event uint8

const (
	Attempt Event = iota + 1
	Unfinished
	Connect
	Broken
)

type table struct {
	db  *asndb.DB
	sha string
}

type key struct {
	inbound string
	asn     uint32
}

type personKey struct {
	credID, inbound string
	asn             uint32
}

type row struct {
	attempts, unfinished, connects, broken uint32
	people                                 map[string]struct{}
	personActivity                         bool
}

// Aggregator owns the active lookup table and one ten-minute window of network counts.
type Aggregator struct {
	mode  atomic.Uint32
	table atomic.Pointer[table]

	mu          sync.Mutex
	rows        map[key]*row
	personRows  map[personKey]uint32
	peopleCount int
	nonZeroRows int
	rowLimit    int

	// lookupHook is set only by package tests to verify that off mode returns before touching the table.
	lookupHook func(*asndb.DB, netip.Addr) uint32
}

// New returns an empty aggregator. A nil aggregator is safe to pass to engine environments.
func New(mode uint32) *Aggregator {
	a := &Aggregator{rows: make(map[key]*row), personRows: make(map[personKey]uint32), rowLimit: maxRows}
	a.SetMode(mode)
	return a
}

// Enabled reports whether lookups and connection events can be counted.
func (a *Aggregator) Enabled() bool { return a != nil && a.mode.Load() != 0 }

// Mode returns 0 for a nil aggregator.
func (a *Aggregator) Mode() uint32 {
	if a == nil {
		return 0
	}
	return a.mode.Load()
}

// SetMode selects off, aggregate-only, or aggregate-plus-person rows. Turning off drops the current window.
func (a *Aggregator) SetMode(mode uint32) {
	if a == nil {
		return
	}
	if mode > 2 {
		mode = 0
	}
	previous := a.mode.Swap(mode)
	if previous == mode {
		return
	}
	if mode == 0 || (previous == 2 && mode != 2) {
		a.mu.Lock()
		if mode == 0 {
			a.resetLocked()
		} else {
			a.personRows = make(map[personKey]uint32)
		}
		a.mu.Unlock()
	}
}

// LookupAddr resolves a peer address only while network collection is enabled. The address stays in the caller's scope.
func LookupAddr(a *Aggregator, addr net.Addr) uint32 {
	if a == nil || !a.Enabled() || addr == nil {
		return 0
	}
	var ip netip.Addr
	switch value := addr.(type) {
	case *net.TCPAddr:
		if value != nil {
			ip, _ = netip.AddrFromSlice(value.IP)
		}
	case *net.UDPAddr:
		if value != nil {
			ip, _ = netip.AddrFromSlice(value.IP)
		}
	case *net.IPAddr:
		if value != nil {
			ip, _ = netip.AddrFromSlice(value.IP)
		}
	default:
		host, _, err := net.SplitHostPort(addr.String())
		if err == nil {
			ip, _ = netip.ParseAddr(host)
		}
	}
	if !ip.IsValid() {
		return 0
	}
	return a.Lookup(ip.Unmap())
}

// SetTable atomically replaces the address table and records the digest of its signed source file.
func (a *Aggregator) SetTable(db *asndb.DB, sha string) {
	if a == nil {
		return
	}
	if db == nil || sha == "" {
		a.ClearTable()
		return
	}
	a.table.Store(&table{db: db, sha: sha})
}

// ClearTable prevents further lookups until a matching table is loaded.
func (a *Aggregator) ClearTable() {
	if a == nil {
		return
	}
	a.table.Store(nil)
	a.mu.Lock()
	a.resetLocked()
	a.mu.Unlock()
}

// LoadedSHA returns the digest of the active table, or an empty string when none is loaded.
func (a *Aggregator) LoadedSHA() string {
	if a == nil {
		return ""
	}
	t := a.table.Load()
	if t == nil {
		return ""
	}
	return t.sha
}

// Lookup maps an address to its ASN. It does no work when the feature is off or no table is loaded.
func (a *Aggregator) Lookup(addr netip.Addr) uint32 {
	if a == nil || a.mode.Load() == 0 {
		return 0
	}
	t := a.table.Load()
	if t == nil || t.db == nil {
		return 0
	}
	if a.lookupHook != nil {
		return a.lookupHook(t.db, addr)
	}
	return t.db.Lookup(addr)
}

// Add records one connection event for an already-resolved ASN. The address must stay in the calling engine.
func (a *Aggregator) Add(inboundID string, asn uint32, event Event, credID, userID string) {
	a.AddN(inboundID, asn, event, credID, userID, 1)
}

// AddN records count connection-level observations for one already-resolved ASN.
func (a *Aggregator) AddN(inboundID string, asn uint32, event Event, credID, userID string, count uint32) {
	if a == nil || count == 0 || a.mode.Load() == 0 || a.table.Load() == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	mode := a.mode.Load()
	if mode == 0 || a.table.Load() == nil {
		return
	}
	r := a.rowLocked(inboundID, asn)
	if r == nil {
		return
	}
	if userID != "" {
		r.personActivity = true
		if _, ok := r.people[userID]; !ok && a.peopleCount < maxPeople {
			if r.people == nil {
				r.people = make(map[string]struct{})
			}
			r.people[userID] = struct{}{}
			a.peopleCount++
		}
	}
	switch event {
	case Attempt:
		r.attempts = saturatingAdd(r.attempts, count)
	case Unfinished:
		r.unfinished = saturatingAdd(r.unfinished, count)
	case Connect:
		r.connects = saturatingAdd(r.connects, count)
		if mode == 2 && credID != "" {
			pk := personKey{credID: credID, inbound: inboundID, asn: asn}
			if _, exists := a.personRows[pk]; exists || len(a.personRows) < maxPersonRows {
				a.personRows[pk] = saturatingAdd(a.personRows[pk], count)
			}
		}
	case Broken:
		r.broken = saturatingAdd(r.broken, count)
	}
}

// Take transfers the current window to the caller, applying k=3 folding and resetting the aggregator.
func (a *Aggregator) Take() ([]*pb.NetworkCounters, []*pb.PersonNetwork) {
	if a == nil || a.mode.Load() == 0 {
		return nil, nil
	}
	a.mu.Lock()
	mode := a.mode.Load()
	if mode == 0 {
		a.mu.Unlock()
		return nil, nil
	}
	rows, peopleRows := a.rows, a.personRows
	a.resetLocked()
	a.mu.Unlock()
	if mode == 0 || len(rows) == 0 {
		return nil, nil
	}

	for k, r := range rows {
		people := len(r.people)
		if people >= kPeople || k.asn == 0 || (!r.personActivity && r.connects == 0 && r.broken == 0) {
			continue
		}
		foldedKey := key{inbound: k.inbound, asn: 0}
		folded := rows[foldedKey]
		if folded == nil {
			folded = &row{}
			rows[foldedKey] = folded
		}
		mergeRow(folded, r)
		delete(rows, k)
	}

	keys := make([]key, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].inbound < keys[j].inbound || (keys[i].inbound == keys[j].inbound && keys[i].asn < keys[j].asn)
	})
	networks := make([]*pb.NetworkCounters, 0, len(keys))
	for _, k := range keys {
		r := rows[k]
		networks = append(networks, &pb.NetworkCounters{
			InboundId: k.inbound, Asn: k.asn, Attempts: r.attempts, Unfinished: r.unfinished,
			Connects: r.connects, Broken: r.broken, People: uint32(len(r.people)),
		})
	}
	if mode != 2 || len(peopleRows) == 0 {
		return networks, nil
	}
	personKeys := make([]personKey, 0, len(peopleRows))
	for k := range peopleRows {
		personKeys = append(personKeys, k)
	}
	sort.Slice(personKeys, func(i, j int) bool {
		a, b := personKeys[i], personKeys[j]
		if a.inbound != b.inbound {
			return a.inbound < b.inbound
		}
		if a.credID != b.credID {
			return a.credID < b.credID
		}
		return a.asn < b.asn
	})
	persons := make([]*pb.PersonNetwork, 0, len(personKeys))
	for _, k := range personKeys {
		persons = append(persons, &pb.PersonNetwork{CredId: k.credID, InboundId: k.inbound, Asn: k.asn, Connects: peopleRows[k]})
	}
	return networks, persons
}

func (a *Aggregator) rowLocked(inboundID string, asn uint32) *row {
	k := key{inbound: inboundID, asn: asn}
	if r := a.rows[k]; r != nil {
		return r
	}
	if asn == 0 {
		r := &row{}
		a.rows[k] = r
		return r
	}
	if a.nonZeroRows < a.rowLimit {
		r := &row{}
		a.rows[k] = r
		a.nonZeroRows++
		return r
	}
	return a.rowLocked(inboundID, 0)
}

func (a *Aggregator) resetLocked() {
	a.rows = make(map[key]*row)
	a.personRows = make(map[personKey]uint32)
	a.peopleCount = 0
	a.nonZeroRows = 0
}

func mergeRow(dst, src *row) {
	dst.attempts = saturatingAdd(dst.attempts, src.attempts)
	dst.unfinished = saturatingAdd(dst.unfinished, src.unfinished)
	dst.connects = saturatingAdd(dst.connects, src.connects)
	dst.broken = saturatingAdd(dst.broken, src.broken)
	dst.personActivity = dst.personActivity || src.personActivity
	for user := range src.people {
		if dst.people == nil {
			dst.people = make(map[string]struct{})
		}
		dst.people[user] = struct{}{}
	}
}

func saturatingAdd(a, b uint32) uint32 {
	if ^uint32(0)-a < b {
		return ^uint32(0)
	}
	return a + b
}
