package nets

import (
	"bytes"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/asndb"
)

func testDB(t *testing.T) *asndb.DB {
	t.Helper()
	table := asndb.Table{
		Built: time.Unix(1_800_000_000, 0),
		IPv4:  []asndb.IPv4Range{{Start: 0xc0000200, End: 0xc00002ff, ASN: 64496}},
		IPv6:  []asndb.IPv6Range{{Start: 0x20010db800000000, End: 0x20010db800000000, ASN: 64497}},
		ASNs: []asndb.Operator{
			{ASN: 64496, Country: "US", Name: "Documentation Four"},
			{ASN: 64497, Country: "US", Name: "Documentation Six"},
		},
	}
	var raw bytes.Buffer
	if err := asndb.Write(&raw, table); err != nil {
		t.Fatal(err)
	}
	db, err := asndb.Parse(bytes.NewReader(raw.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestTakeFoldsRowsWithFewerThanThreePeople(t *testing.T) {
	a := New(2)
	a.SetTable(testDB(t), "fixture-sha")
	for _, user := range []string{"alice", "bob"} {
		a.Add("inb_de1", 64496, Connect, "crd_"+user, user)
	}
	for _, user := range []string{"alice", "bob", "de1"} {
		a.Add("inb_de1", 64497, Connect, "crd_"+user, user)
	}
	a.Add("inb_nl1", 64500, Attempt, "", "")
	a.Add("inb_nl1", 64500, Unfinished, "", "")

	networks, people := a.Take()
	got := make(map[uint32]*pb.NetworkCounters, len(networks))
	for _, r := range networks {
		got[r.Asn] = r
	}
	if len(networks) != 3 || got[0] == nil || got[64497] == nil || got[64500] == nil {
		t.Fatalf("rows = %+v", networks)
	}
	if r := got[0]; r.Connects != 2 || r.People != 2 {
		t.Errorf("folded row = %+v", r)
	}
	if r := got[64497]; r.Connects != 3 || r.People != 3 {
		t.Errorf("three-person row = %+v", r)
	}
	if r := got[64500]; r.Attempts != 1 || r.Unfinished != 1 || r.People != 0 {
		t.Errorf("zero-person row = %+v", r)
	}
	if len(people) != 5 {
		t.Errorf("mode 2 person rows = %d, want 5 unfolded rows", len(people))
	}
	if next, nextPeople := a.Take(); len(next) != 0 || len(nextPeople) != 0 {
		t.Errorf("Take did not reset: rows=%+v people=%+v", next, nextPeople)
	}
}

func TestTakeFoldsPersonActivityAndUnidentifiedConnects(t *testing.T) {
	for _, tc := range []struct {
		name       string
		event      Event
		userID     string
		wantASN    uint32
		wantPeople uint32
	}{
		{name: "unidentified connect folds", event: Connect, wantASN: 0},
		{name: "one known broken folds", event: Broken, userID: "alice", wantASN: 0, wantPeople: 1},
		{name: "unidentified attempts stay", event: Attempt, wantASN: 64496},
		{name: "known attempts fold", event: Attempt, userID: "alice", wantASN: 0, wantPeople: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := New(1)
			a.SetTable(testDB(t), "fixture-sha")
			a.Add("inb_de1", 64496, tc.event, "", tc.userID)
			networks, _ := a.Take()
			if len(networks) != 1 || networks[0].Asn != tc.wantASN || networks[0].People != tc.wantPeople {
				t.Fatalf("network rows = %+v, want one ASN %d row with %d people", networks, tc.wantASN, tc.wantPeople)
			}
		})
	}
}

func TestModeZeroDoesNotLookUpAndDropsRows(t *testing.T) {
	a := New(0)
	a.SetTable(testDB(t), "fixture-sha")
	if allocs := testing.AllocsPerRun(100, func() { a.SetMode(0) }); allocs != 0 {
		t.Fatalf("unchanged mode allocates %v times per apply", allocs)
	}
	if allocs := testing.AllocsPerRun(100, func() { _, _ = a.Take() }); allocs != 0 {
		t.Fatalf("off-mode Take allocates %v times", allocs)
	}
	lookups := 0
	addrReads := 0
	a.lookupHook = func(*asndb.DB, netip.Addr) uint32 {
		lookups++
		return 64500
	}
	if got := LookupAddr(a, countingNetAddr{reads: &addrReads}); got != 0 || lookups != 0 || addrReads != 0 {
		t.Fatalf("off address lookup = %d, table lookups = %d, address reads = %d", got, lookups, addrReads)
	}
	if got := a.Lookup(netip.MustParseAddr("192.0.2.7")); got != 0 || lookups != 0 {
		t.Fatalf("off lookup = %d, table lookups = %d", got, lookups)
	}
	a.SetMode(2)
	a.Add("inb_de1", 64496, Connect, "crd_alice", "alice")
	a.SetMode(0)
	if networks, people := a.Take(); len(networks) != 0 || len(people) != 0 {
		t.Fatalf("off mode retained counts: %+v %+v", networks, people)
	}
}

type countingNetAddr struct{ reads *int }

func (a countingNetAddr) Network() string { return "tcp" }
func (a countingNetAddr) String() string {
	(*a.reads)++
	return "192.0.2.7:443"
}

func TestPersonRowsOnlyInModeTwo(t *testing.T) {
	for _, mode := range []uint32{1, 2} {
		a := New(mode)
		a.SetTable(testDB(t), "fixture-sha")
		a.Add("inb_de1", 64496, Connect, "crd_alice", "alice")
		_, people := a.Take()
		if (mode == 2) != (len(people) == 1) {
			t.Errorf("mode %d person rows = %+v", mode, people)
		}
	}
}

func TestCapsFoldOverflowAndSaturatePeople(t *testing.T) {
	a := New(1)
	a.SetTable(testDB(t), "fixture-sha")
	for inbound := range 32 {
		inboundID := "inb_de1_" + strconv.Itoa(inbound)
		for asn := uint32(64496); asn <= 64511; asn++ {
			a.Add(inboundID, asn, Attempt, "", "")
		}
	}
	a.Add("inb_de1_0", 0, Attempt, "", "")
	networks, _ := a.Take()
	if len(networks) != maxRows+1 {
		t.Fatalf("non-zero row cap plus overflow row = %d, want %d", len(networks), maxRows+1)
	}
	var total uint32
	var overflow bool
	for _, r := range networks {
		total += r.Attempts
		overflow = overflow || r.Asn == 0
	}
	if total != maxRows+1 || !overflow {
		t.Fatalf("row overflow total=%d has_asn_zero=%t", total, overflow)
	}

	a.SetMode(2)
	a.SetTable(testDB(t), "fixture-sha")
	a.peopleCount = maxPeople // The cap is the number of distinct user IDs held in the current window.
	a.Add("inb_de1", 64496, Connect, "crd_alice", "alice")
	networks, people := a.Take()
	if len(networks) != 1 || networks[0].People != 0 || len(people) != 1 || people[0].Connects != 1 {
		t.Fatalf("people cap networks=%+v person_rows=%+v", networks, people)
	}
}

func TestRowCapKeepsOverflowForNewInbound(t *testing.T) {
	a := New(1)
	a.SetTable(testDB(t), "fixture-sha")
	a.rowLimit = 16 // Exercise a full cap with the allowed fixture ASNs from one inbound.
	for asn := uint32(64496); asn <= 64511; asn++ {
		a.Add("inb_de1", asn, Attempt, "", "")
	}
	if a.nonZeroRows != a.rowLimit {
		t.Fatalf("first inbound filled %d rows, want %d", a.nonZeroRows, a.rowLimit)
	}
	a.Add("inb_nl1", 64500, Attempt, "", "")

	networks, _ := a.Take()
	var overflow *pb.NetworkCounters
	for _, r := range networks {
		if r.InboundId == "inb_nl1" && r.Asn == 0 {
			overflow = r
		}
	}
	if overflow == nil || overflow.Attempts != 1 {
		t.Fatalf("new inbound overflow row = %+v, all rows=%+v", overflow, networks)
	}
}

func TestPersonRowsStopGrowingAtTheEntryCap(t *testing.T) {
	a := New(2)
	a.SetTable(testDB(t), "fixture-sha")
	a.mu.Lock()
	a.peopleCount = maxPeople
	for i := range maxPersonRows {
		a.personRows[personKey{credID: "crd_" + strconv.Itoa(i), inbound: "inb_de1", asn: 64496}] = 1
	}
	a.mu.Unlock()
	a.Add("inb_de1", 64496, Connect, "crd_alice", "alice")
	if len(a.personRows) != maxPersonRows {
		t.Fatalf("person row cap grew to %d entries", len(a.personRows))
	}
}

func TestPeopleCapStillFoldsKnownAttemptActivity(t *testing.T) {
	a := New(1)
	a.SetTable(testDB(t), "fixture-sha")
	a.peopleCount = maxPeople
	a.Add("inb_de1", 64496, Attempt, "", "alice")
	networks, _ := a.Take()
	if len(networks) != 1 || networks[0].Asn != 0 || networks[0].Attempts != 1 || networks[0].People != 0 {
		t.Fatalf("person-cap row = %+v, want the private ASN-0 attempt row", networks)
	}
}

func TestNoClientAddressLeavesInMarshalledBatch(t *testing.T) {
	a := New(2)
	a.SetTable(testDB(t), "fixture-sha")
	addresses := []netip.Addr{
		netip.MustParseAddr("192.0.2.77"),
		netip.MustParseAddr("2001:db8::1234"),
	}
	for i, addr := range addresses {
		a.Add("inb_de1", a.Lookup(addr), Connect, []string{"crd_alice", "crd_bob"}[i], []string{"alice", "bob"}[i])
	}
	networks, people := a.Take()
	wire, err := proto.Marshal(&pb.StatsBatch{Networks: networks, PersonNetworks: people})
	if err != nil {
		t.Fatal(err)
	}
	for _, addr := range addresses {
		if bytes.Contains(wire, []byte(addr.String())) {
			t.Fatalf("marshalled batch contains an address text representation")
		}
		if addr.Is4() {
			v := addr.As4()
			if bytes.Contains(wire, v[:]) {
				t.Fatalf("marshalled batch contains an IPv4 byte representation")
			}
		}
		v := addr.As16()
		if bytes.Contains(wire, v[:]) {
			t.Fatalf("marshalled batch contains a 16-byte address representation")
		}
	}
}

func TestNilSafe(t *testing.T) {
	var a *Aggregator
	a.SetMode(1)
	a.SetTable(testDB(t), "fixture-sha")
	a.ClearTable()
	if a.Enabled() || a.Mode() != 0 || a.Lookup(netip.MustParseAddr("192.0.2.1")) != 0 || a.LoadedSHA() != "" {
		t.Fatal("nil aggregator returned active state")
	}
	a.Add("inb_de1", 64496, Connect, "crd_alice", "alice")
	if networks, people := a.Take(); networks != nil || people != nil {
		t.Fatalf("nil Take = %+v %+v", networks, people)
	}
}
