package store

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"
)

type relayCounterFixtureRows struct {
	entryID string
	updates []RelayCounterUpdate
}

func relayCounterFixture(t *testing.T, st *Store, suffix string) relayCounterFixtureRows {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1, 0)
	nodeID := func(name string) string {
		t.Helper()
		var id string
		err := st.R.QueryRowContext(ctx, `SELECT id FROM node WHERE name = ?`, name).Scan(&id)
		if err == nil {
			return id
		}
		if err != sql.ErrNoRows {
			t.Fatal(err)
		}
		id = "nod_" + suffix + "_" + name
		if name == "msk1" {
			relayExec(t, st, `INSERT INTO node (id, name, address, state, created_at, agent_caps) VALUES (?, ?, ?, 'active', 1, 'relay-forward/1')`, id, name, name+".example.invalid")
		} else {
			relayExec(t, st, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, 'active', 1)`, id, name, name+".example.invalid")
		}
		return id
	}
	deID, nlID, entryID := nodeID("de1"), nodeID("nl1"), nodeID("msk1")
	inboundID := func(name, nodeID string) string {
		t.Helper()
		var id string
		err := st.R.QueryRowContext(ctx, `SELECT id FROM inbound WHERE node_id = ? ORDER BY rowid LIMIT 1`, nodeID).Scan(&id)
		if err == nil {
			return id
		}
		if err != sql.ErrNoRows {
			t.Fatal(err)
		}
		profileID, id := "prf_"+suffix+"_"+name, "inb_"+suffix+"_"+name
		relayExec(t, st, `INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES (?, 'hysteria2', ?, '{}', 1, 1)`, profileID, name)
		relayExec(t, st, `INSERT INTO inbound (id, profile_id, node_id, created_at, updated_at) VALUES (?, ?, ?, 1, 1)`, id, profileID, nodeID)
		return id
	}
	updates := []RelayCounterUpdate{
		{RelayID: "rly_" + suffix + "_de1", Network: "udp", Port: 443},
		{RelayID: "rly_" + suffix + "_nl1", Network: "udp", Port: 8443},
	}
	for i, target := range []struct{ name, id string }{{"de1", deID}, {"nl1", nlID}} {
		if err := st.CreateRelay(ctx, Relay{ID: updates[i].RelayID, InboundID: inboundID(target.name, target.id), ViaNodeID: entryID, Network: updates[i].Network, Port: updates[i].Port}, now); err != nil {
			t.Fatal(err)
		}
		on := true
		if err := st.UpdateRelay(ctx, updates[i].RelayID, RelayPatch{Enabled: &on}, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	return relayCounterFixtureRows{entryID: entryID, updates: updates}
}

func relayCounterToday(t *testing.T, st *Store, nodeID string, dayStart int64) uint64 {
	t.Helper()
	got, err := st.RelayTodayBytesByNode(context.Background(), dayStart)
	if err != nil {
		t.Fatal(err)
	}
	return got[nodeID]
}

func TestRelayCountersBPS(t *testing.T) {
	now := time.Unix(100_000, 0).UTC()
	for _, tc := range []struct {
		name string
		c    RelayCounters
		want uint64
	}{
		{name: "two readings", c: RelayCounters{PrevBytes: 100, PrevCountedUnix: 99_990, Bytes: 1_100, CountedUnix: 100_000}, want: 800},
		{name: "reset", c: RelayCounters{PrevBytes: 2_000, PrevCountedUnix: 99_990, Bytes: 100, CountedUnix: 100_000}, want: 80},
		{name: "one reading", c: RelayCounters{Bytes: 100, CountedUnix: 100_000}},
		{name: "equal timestamps", c: RelayCounters{PrevBytes: 100, PrevCountedUnix: 100_000, Bytes: 200, CountedUnix: 100_000}},
		{name: "stale readings", c: RelayCounters{PrevBytes: 100, PrevCountedUnix: 99_000, Bytes: 200, CountedUnix: 99_010}},
		{name: "absurd speed", c: RelayCounters{PrevCountedUnix: 99_999, Bytes: 125_000_000_001, CountedUnix: 100_000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.BPS(now); got != tc.want {
				t.Fatalf("BPS = %d, want %d", got, tc.want)
			}
		})
	}
}

// relayCounterStoreProof runs on SQLite and D1. The entry's daily sum is the sum of persisted per-relay increments.
func relayCounterStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	f := relayCounterFixture(t, st, "b1")
	day1 := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC).Unix()
	day2 := day1 + 24*60*60
	stamp := func(day int64, seconds int64) int64 { return day + seconds }
	set := func(i int, bytes uint64, at int64) {
		t.Helper()
		u := f.updates[i]
		u.RelayCounters = RelayCounters{Bytes: bytes, Packets: 1, CountedUnix: at}
		if err := st.SetRelayCounters(ctx, f.entryID, []RelayCounterUpdate{u}); err != nil {
			t.Fatal(err)
		}
	}

	set(0, 100, stamp(day1, 12*60*60))
	set(1, 50, stamp(day1, 12*60*60))
	if got := relayCounterToday(t, st, f.entryID, day1); got != 150 {
		t.Fatalf("first readings today = %d, want 150", got)
	}
	set(0, 150, stamp(day1, 12*60*60+30))
	set(1, 75, stamp(day1, 12*60*60+30))
	if got := relayCounterToday(t, st, f.entryID, day1); got != 225 {
		t.Fatalf("two relay increments today = %d, want 225", got)
	}
	set(0, 10, stamp(day1, 12*60*60+60)) // nft replaced: the lower value is the new increment.
	if got := relayCounterToday(t, st, f.entryID, day1); got != 235 {
		t.Fatalf("today after counter reset = %d, want 235", got)
	}
	set(0, 15, stamp(day2, 60))
	set(1, 80, stamp(day2, 60))
	if got := relayCounterToday(t, st, f.entryID, day2); got != 10 {
		t.Fatalf("today across day boundary = %d, want 10", got)
	}
	if got := relayCounterToday(t, st, f.entryID, day1); got != 0 {
		t.Fatalf("prior day total still visible = %d, want 0", got)
	}

	checkAt := stamp(day2, 120)
	check := RelayCheck{At: checkAt, Network: "udp", Port: 443, To: "de1.example.invalid:443",
		Leg: RelayStage{Status: "ok"}, Path: RelayStage{Status: "ok"},
		ReverseReach: RelayStage{Status: "ok", Sender: "nod_b1_de1", At: checkAt},
		ReverseSpeed: RelayStage{Status: "ok", Sender: "nod_b1_de1", At: checkAt, MB: 8, MbitS: 91.2, Bytes: 8 << 20,
			ConnectMS: 12, FirstByteMS: 31, DurationMS: 730}}
	check.Settle()
	u := f.updates[0]
	u.RelayCounters = RelayCounters{Bytes: 20, Packets: 2, CountedUnix: stamp(day2, 120)}
	monitor := RelayMonitorCheck{AtUnix: checkAt, OK: true, Stage: "ok", ResponseMS: 18}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	wg.Add(3)
	go func() { defer wg.Done(); errs <- st.SetRelayCounters(ctx, f.entryID, []RelayCounterUpdate{u}) }()
	go func() { defer wg.Done(); errs <- st.SetRelayCheck(ctx, u.RelayID, check) }()
	go func() { defer wg.Done(); errs <- st.SetRelayMonitorCheck(ctx, u.RelayID, monitor) }()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	link, err := st.RelayLink(ctx, u.RelayID)
	if err != nil {
		t.Fatal(err)
	}
	gotCheck, ok := link.Check()
	if !ok || gotCheck.Leg.Status != "ok" || gotCheck.ReverseReach != check.ReverseReach || gotCheck.ReverseSpeed != check.ReverseSpeed ||
		gotCheck.Monitor == nil || *gotCheck.Monitor != monitor || gotCheck.Counters == nil || gotCheck.Counters.Bytes != 20 || gotCheck.Counters.TodayBytes != 10 {
		t.Fatalf("concurrent check/counter writes lost a member: %+v", gotCheck)
	}
	var atType, portType, mbType, bytesType, todayDayType string
	if err := st.R.QueryRowContext(ctx, `SELECT json_type(check_json, '$.at'), json_type(check_json, '$.port'),
		json_type(check_json, '$.reverse_speed.mb'), json_type(check_json, '$.reverse_speed.bytes'),
		json_type(check_json, '$.counters.today_day') FROM relay WHERE id = ?`, u.RelayID).
		Scan(&atType, &portType, &mbType, &bytesType, &todayDayType); err != nil {
		t.Fatal(err)
	}
	if atType != "integer" || portType != "integer" || mbType != "integer" || bytesType != "integer" || todayDayType != "integer" {
		t.Fatalf("JSON number types = at %q port %q speed mb %q bytes %q counter day %q, want integer", atType, portType, mbType, bytesType, todayDayType)
	}

	// A snapshot repeated with the same time (a retried report) is not counted twice, and a deleted relay takes its share.
	set(0, 999, stamp(day2, 120))
	if got := relayCounterToday(t, st, f.entryID, day2); got != 15 {
		t.Fatalf("repeated snapshot changed today = %d, want 15", got)
	}
	if err := st.DeleteRelay(ctx, u.RelayID); err != nil {
		t.Fatal(err)
	}
	if got := relayCounterToday(t, st, f.entryID, day2); got != 5 {
		t.Fatalf("today after the relay was deleted = %d, want the other relay's 5", got)
	}
}
