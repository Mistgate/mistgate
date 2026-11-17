package store

import (
	"context"
	"testing"
	"time"
)

// tunnelRefusalsStoreProof runs on SQLite and on D1 (link_hour_test.go, d1_store_test.go): the trusttunnel-refusals/1 counters
// (migration 00142) of an inbound row go in with the batch's statement, add up inside an hour, and come back in the node report
// (totals and hours) and the fleet sums, each reason in its own place.
func tunnelRefusalsStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 11, 17, 12, 20, 0, 0, time.UTC)
	hour := base.Unix() / 3600 * 3600
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_tr', 'de1', '203.0.113.21', 'active', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := st.Access().CreateProfile(ctx, AccessProfile{ID: "prf_tr", Protocol: "trusttunnel", Name: "Tunnel", SettingsJSON: "{}", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	if err := st.Access().CreateInbound(ctx, AccessInbound{ID: "inb_tr", ProfileID: "prf_tr", NodeID: "nod_tr", Enabled: true, CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	var seq uint64
	ingest := func(at int64, links ...FleetLink) {
		t.Helper()
		seq++
		if out, err := st.IngestStats(ctx, FleetStatsIn{NodeID: "nod_tr", Instance: "tr", Seq: seq, Now: base, HourStart: at, Links: links}); err != nil || out.Duplicate || out.Skipped != 0 {
			t.Fatalf("IngestStats(%d) = %+v, %v", seq, out, err)
		}
	}
	var first, second, third [tunnelRefusalKinds]uint32
	for i := range first {
		first[i], second[i] = uint32(i+1), uint32(10*(i+1))
	}
	third[1] = 5
	ingest(hour, FleetLink{InboundID: "inb_tr", Tunnel: first})
	ingest(hour, FleetLink{InboundID: "inb_tr", Tunnel: second})
	ingest(hour+3600, FleetLink{InboundID: "inb_tr", Tunnel: third}) // a row of nothing but refusals is stored

	reports, err := st.NodeLinkReports(ctx, "nod_tr", base, true)
	if err != nil || len(reports) != 1 {
		t.Fatalf("NodeLinkReports = %+v, %v", reports, err)
	}
	var want [tunnelRefusalKinds]uint64
	for i := range want {
		want[i] = uint64(11 * (i + 1)) // first + second, reason by reason: a swapped column shows
	}
	want[1] += 5
	if got := reports[0].Totals.Tunnel; got != want {
		t.Errorf("totals = %v, want %v", got, want)
	}
	if h := reports[0].Hours; len(h) != 2 || h[0].Tunnel[0] != 11 || h[0].Tunnel[13] != 154 || h[1].Tunnel[1] != 5 || h[1].Tunnel[0] != 0 {
		t.Errorf("hours = %+v", h)
	}
	fleet, err := st.FleetNodeLinkSums(ctx, base)
	if err != nil || len(fleet) != 1 || fleet[0].Tunnel != want {
		t.Errorf("fleet sums = %+v, %v", fleet, err)
	}
}
