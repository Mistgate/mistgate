package fleet

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/asndb"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/update"
	"google.golang.org/protobuf/encoding/protojson"
)

type fakeNetworkUpdates struct {
	*fakeUpdates
	table *asndb.DB
}

func (u *fakeNetworkUpdates) NetworksTable() *asndb.DB { return u.table }

type dirtyNetworkTable struct{ *asndb.DB }

func (t dirtyNetworkTable) Name(asn uint32) (string, string, bool) {
	if asn == 64496 {
		return "DE", "de1 203.0.113.77\n", true
	}
	return t.DB.Name(asn)
}

type networkTestResolver struct {
	*fakeHealth
	addresses map[string]netip.Addr
	calls     atomic.Int32
}

func (r *networkTestResolver) NodeIPv4(_ context.Context, node store.NodeRow) (netip.Addr, bool) {
	r.calls.Add(1)
	address, ok := r.addresses[node.ID]
	return address, ok
}

func TestGetNetworksAggregatesFlagsAndCleansNames(t *testing.T) {
	e := newEnv(t)
	now := time.Now().UTC()
	a := e.enroll("de1")
	ids := e.fixture(a.nodeID)
	e.f.now = func() time.Time { return now }
	e.f.SetNetworkMode(1) // the owner switched the collection on; it is off by default
	e.exec(`UPDATE node SET address = 'de1.example.com', state = 'active', agent_caps = 'nets/1', networks_db_sha256 = ? WHERE id = ?`, testNetworkSHA, a.nodeID)
	e.exec(`UPDATE node SET name = 'nl1', address = 'nl1.example.com', agent_caps = 'nets/1' WHERE id = ?`, ids.nodeB)
	e.exec(`INSERT INTO node (id, name, address, state, created_at, agent_caps) VALUES ('nod_fi1', 'fi1', 'fi1.example.com', 'active', 1, '')`)
	e.exec(`UPDATE profile SET protocol = 'vless', name = 'de1', settings_json = '{"cdn":{"host":"cdn.de1.example"}}' WHERE id = 'prf_1'`)
	base := networkTableFixture(t)
	updates := &fakeNetworkUpdates{fakeUpdates: &fakeUpdates{}, table: base}
	updates.setNetworkTable("mistgate-asndb-data-any", testNetworkSHA)
	e.f.SetUpdates(updates)
	resolver := &networkTestResolver{fakeHealth: &fakeHealth{}, addresses: map[string]netip.Addr{
		a.nodeID: netip.MustParseAddr("203.0.113.10"), ids.nodeB: netip.MustParseAddr("192.0.2.10"),
		"nod_fi1": netip.MustParseAddr("198.51.100.10"),
	}}
	e.f.SetHealth(resolver)
	e.f.networkTableOverride = dirtyNetworkTable{base}
	e.f.networkOperator = func(country string, asn uint32) string {
		if country == "DE" && asn == 64496 {
			return "MTS"
		}
		return ""
	}

	windowEnd := now.Truncate(time.Hour).Add(time.Hour)
	currentA := windowEnd.Add(-2 * time.Hour).Unix()
	currentB := windowEnd.Add(-time.Hour).Unix()
	baselineA := windowEnd.Add(-4 * time.Hour).Unix()
	baselineB := windowEnd.Add(-3 * time.Hour).Unix()
	insertNetworkHour(t, e.st, a.nodeID, currentA, ids.i1, 64496, 5, 2, 3, 1, 2)
	insertNetworkHour(t, e.st, a.nodeID, currentB, ids.i1, 64496, 5, 1, 4, 1, 5)
	insertNetworkHour(t, e.st, a.nodeID, baselineA, ids.i1, 64496, 2, 1, 1, 0, 3)
	insertNetworkHour(t, e.st, a.nodeID, baselineB, ids.i1, 64496, 1, 0, 2, 1, 4)
	insertNetworkHour(t, e.st, a.nodeID, currentB, ids.i2, 64497, 2, 1, 1, 0, 1)

	resp, err := (nodeService{e.f}).GetNetworks(e.ctx, connect.NewRequest(&adminv1.GetNetworksRequest{Hours: 2}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.SinceUnix != currentA || resp.Msg.NowUnix != now.Unix() || resp.Msg.Hours != 2 || resp.Msg.Mode != 1 || resp.Msg.TableBuiltUnix != 1_800_000_000 {
		t.Fatalf("window/status fields = %+v", resp.Msg)
	}
	if len(resp.Msg.Rows) != 2 || len(resp.Msg.NodesWithoutNets) != 1 || resp.Msg.NodesWithoutNets[0].NodeName != "fi1" ||
		len(resp.Msg.NodesWithoutTable) != 1 || resp.Msg.NodesWithoutTable[0].NodeName != "nl1" {
		t.Fatalf("rows/unsupported nodes = %+v / %+v / %+v", resp.Msg.Rows, resp.Msg.NodesWithoutNets, resp.Msg.NodesWithoutTable)
	}
	insertNetworkHour(t, e.st, ids.nodeB, currentB, ids.i3, 64496, 3, 0, 3, 0, 1)
	filtered, err := (nodeService{e.f}).GetNetworks(e.ctx, connect.NewRequest(&adminv1.GetNetworksRequest{Hours: 2, NodeId: ids.nodeB}))
	if err != nil || len(filtered.Msg.Rows) != 1 || filtered.Msg.Rows[0].NodeId != ids.nodeB || !filtered.Msg.Rows[0].Fleet {
		t.Fatalf("filtered fleet row = %+v, %v; want node B row marked as fleet from node A's ASN", filtered, err)
	}
	if got := resolver.calls.Load(); got != 3 {
		t.Fatalf("hostname resolver called %d times after unfiltered and filtered reads, want one lookup per node", got)
	}
	var row *adminv1.NetworkHourRow
	for _, candidate := range resp.Msg.Rows {
		if candidate.InboundId == ids.i1 && candidate.Asn == 64496 {
			row = candidate
		}
	}
	if row == nil {
		t.Fatalf("no ASN 64496 row: %+v", resp.Msg.Rows)
	}
	if row.NodeName != "de1" || row.ProfileName != "de1" || row.Protocol != "vless" || !row.Cdn || !row.Fleet || row.Country != "DE" || row.Name != "de1 <ip>" || row.Operator != "MTS" {
		t.Errorf("metadata and flags = %+v", row)
	}
	if row.Attempts != 10 || row.Unfinished != 3 || row.Connects != 7 || row.Broken != 2 || row.PeakPeople != 5 ||
		row.BaselineAttempts != 3 || row.BaselineUnfinished != 1 || row.BaselineConnects != 3 || row.BaselineBroken != 1 || row.BaselinePeakPeople != 4 {
		t.Errorf("window and baseline sums = %+v", row)
	}
	for _, candidate := range resp.Msg.Rows {
		if candidate.Asn == 64497 && (candidate.Fleet || candidate.Cdn || candidate.Country != "NL" || candidate.Name != "nl1") {
			t.Errorf("second ASN row flags/name = %+v", candidate)
		}
	}
	encoded, err := protojson.Marshal(resp.Msg)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"203.0.113.10", "203.0.113.77", "198.51.100.10", "\n"} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("GetNetworks response leaked %q: %s", secret, encoded)
		}
	}
	if _, err := (nodeService{e.f}).GetNetworks(e.ctx, connect.NewRequest(&adminv1.GetNetworksRequest{NodeId: "nod_missing"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown node error = %v, want NOT_FOUND", err)
	}
	if _, err := (nodeService{e.f}).GetNetworks(e.ctx, connect.NewRequest(&adminv1.GetNetworksRequest{Hours: 169})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("169-hour window error = %v, want INVALID_ARGUMENT", err)
	}

	stmt, err := e.st.W.PrepareContext(e.ctx, `INSERT INTO network_hour (node_id, hour_start, inbound_id, asn, attempts, unfinished, connects, broken, people)
		VALUES (?, ?, ?, ?, 1, 0, 0, 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5000; i++ {
		if _, err := stmt.Exec(a.nodeID, currentB, fmt.Sprintf("inb_cap_%04d", i), uint32(64496+i%16)); err != nil {
			stmt.Close()
			t.Fatal(err)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatal(err)
	}
	capped, err := (nodeService{e.f}).GetNetworks(e.ctx, connect.NewRequest(&adminv1.GetNetworksRequest{Hours: 2}))
	if err != nil {
		t.Fatal(err)
	}
	if len(capped.Msg.Rows) != maxNetworkRows || !capped.Msg.Truncated {
		t.Fatalf("capped GetNetworks rows=%d truncated=%v; want %d and true", len(capped.Msg.Rows), capped.Msg.Truncated, maxNetworkRows)
	}
	if got := resolver.calls.Load(); got != 3 {
		t.Fatalf("hostname resolver called %d times after cached reads, want 3 total", got)
	}
}

func TestGetNetworksWithoutLoadedProductionTableHasZeroBuiltUnix(t *testing.T) {
	e := newEnv(t)
	updater, err := update.New(e.st, e.f, nil, update.Config{StepUp: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	e.f.SetUpdates(updater)
	response, err := (nodeService{e.f}).GetNetworks(e.ctx, connect.NewRequest(&adminv1.GetNetworksRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if response.Msg.TableBuiltUnix != 0 {
		t.Fatalf("GetNetworks table_built_unix with no loaded production table = %d, want 0", response.Msg.TableBuiltUnix)
	}
}

func insertNetworkHour(t *testing.T, st *store.Store, nodeID string, hour int64, inboundID string, asn uint32,
	attempts, unfinished, connects, broken, people int64) {
	t.Helper()
	_, err := st.W.Exec(`INSERT INTO network_hour (node_id, hour_start, inbound_id, asn, attempts, unfinished, connects, broken, people)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, nodeID, hour, inboundID, asn, attempts, unfinished, connects, broken, people)
	if err != nil {
		t.Fatal(err)
	}
}

func networkTableFixture(t *testing.T) *asndb.DB {
	t.Helper()
	start := netip.MustParseAddr("203.0.113.0").As4()
	middle := netip.MustParseAddr("203.0.113.127").As4()
	end := netip.MustParseAddr("203.0.113.255").As4()
	var raw bytes.Buffer
	if err := asndb.Write(&raw, asndb.Table{Built: time.Unix(1_800_000_000, 0).UTC(),
		IPv4: []asndb.IPv4Range{
			{Start: binary.BigEndian.Uint32(start[:]), End: binary.BigEndian.Uint32(middle[:]), ASN: 64496},
			{Start: binary.BigEndian.Uint32(middle[:]) + 1, End: binary.BigEndian.Uint32(end[:]), ASN: 64497},
		},
		ASNs: []asndb.Operator{{ASN: 64496, Country: "DE", Name: "de1"}, {ASN: 64497, Country: "NL", Name: "nl1"}},
	}); err != nil {
		t.Fatal(err)
	}
	table, err := asndb.Parse(bytes.NewReader(raw.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return table
}
