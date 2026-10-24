package store

import (
	"context"
	"testing"
	"time"
)

// clientProbeStoreProof runs on SQLite and on D1 (see client_probe_report_test.go and d1_store_test.go): a report goes in as
// one statement with its rows as one JSON argument, and the counts fold a report once per node.
func clientProbeStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	mts := ClientProbeNetwork{DirectChecked: true, ASN: 8359, Operator: "MTS PJSC", Country: "RU", Region: "Moscow", NetworkType: "mobile"}
	// the statement checks the switch itself: off (absent, then "0"), nothing is stored
	for _, v := range []string{"", "0"} {
		if v != "" {
			if err := st.SetSettings(ctx, map[string]string{SettingClientProbe: v}); err != nil {
				t.Fatal(err)
			}
		}
		if n, err := st.InsertClientProbeReport(ctx, "off", base, mts, []ClientProbeResult{{NodeID: "nod_de1", Kind: "vless", Port: 443, Verdict: "ok"}}); err != nil || n != 0 {
			t.Fatalf("switch %q: stored %d, %v", v, n, err)
		}
	}
	if err := st.SetSettings(ctx, map[string]string{SettingClientProbe: "1"}); err != nil {
		t.Fatal(err)
	}
	// report a: vless froze and hysteria2 passed on de1 (one report, one freeze, not ok), nl1 passed
	if n, err := st.InsertClientProbeReport(ctx, "a", base, mts, []ClientProbeResult{
		{NodeID: "nod_de1", Kind: "vless", Port: 443, Verdict: "freeze", DetailJSON: `{"flaky":false,"dl_bytes":10985,"dl_stalled":true,"delay_ms":100}`},
		{NodeID: "nod_de1", Kind: "hysteria2", Port: 443, Verdict: "ok"},
		{NodeID: "nod_nl1", Kind: "vless", Port: 443, Verdict: "ok"},
	}); err != nil || n != 3 {
		t.Fatalf("report a: stored %d, %v", n, err)
	}
	// report b from the same network: de1 all ok; report c from an unknown network an hour later: de1 tcp_blocked
	if _, err := st.InsertClientProbeReport(ctx, "b", base.Add(time.Minute), mts, []ClientProbeResult{{NodeID: "nod_de1", Kind: "vless", Port: 443, Verdict: "ok", DetailJSON: `{"dl_stalled":false,"delay_ms":140}`}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertClientProbeReport(ctx, "c", base.Add(time.Hour), ClientProbeNetwork{}, []ClientProbeResult{{NodeID: "nod_de1", Kind: "vless", Port: 443, Verdict: "tcp_blocked"}}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.InsertClientProbeReport(ctx, "empty", base, mts, nil); err != nil || n != 0 {
		t.Fatalf("empty report: stored %d, %v", n, err)
	}

	var detail string
	if err := st.R.QueryRowContext(ctx, `SELECT detail_json FROM client_probe_report WHERE report = 'a' AND kind = 'vless' AND node_id = 'nod_de1'`).Scan(&detail); err != nil ||
		detail != `{"flaky":false,"dl_bytes":10985,"dl_stalled":true,"delay_ms":100}` {
		t.Fatalf("detail %q, %v", detail, err)
	}
	if err := st.R.QueryRowContext(ctx, `SELECT detail_json FROM client_probe_report WHERE report = 'a' AND kind = 'hysteria2'`).Scan(&detail); err != nil || detail != "{}" {
		t.Fatalf("empty detail %q, %v", detail, err)
	}

	counts, err := st.ClientProbeCounts(ctx, base.Add(-time.Hour))
	if err != nil || len(counts) != 3 {
		t.Fatalf("counts %+v, %v", counts, err)
	}
	unknown, moscow, nl := counts[0], counts[1], counts[2]
	if unknown.NodeID != "nod_de1" || unknown.Operator != "" || unknown.Reports != 1 || unknown.TCPBlocked != 1 || !unknown.Last.Equal(base.Add(time.Hour)) {
		t.Errorf("de1 from an unknown network: %+v", unknown)
	}
	if moscow.NodeID != "nod_de1" || moscow.ASN != 8359 || moscow.Region != "Moscow" || moscow.Reports != 2 || moscow.Freeze != 1 || moscow.OK != 1 || moscow.TCPBlocked != 0 ||
		!moscow.Last.Equal(base.Add(time.Minute)) {
		t.Errorf("de1 from MTS: %+v", moscow)
	}
	if nl.NodeID != "nod_nl1" || nl.Reports != 1 || nl.OK != 1 {
		t.Errorf("nl1: %+v", nl)
	}
	kinds, err := st.ClientProbeKindCounts(ctx, base.Add(-time.Hour))
	if err != nil || len(kinds) != 3 {
		t.Fatalf("kind counts %+v, %v", kinds, err)
	}
	var vless ClientProbeKindCount
	for _, kind := range kinds {
		if kind.NodeID == "nod_de1" && kind.Kind == "vless" {
			vless = kind
		}
	}
	if vless.Reports != 3 || vless.OK != 1 || vless.Freeze != 1 || vless.TCPBlocked != 1 || vless.DLChecked != 2 || vless.DLStalled != 1 ||
		len(vless.DelaySamples) != 2 || vless.DelaySamples[0] != 100 || vless.DelaySamples[1] != 140 {
		t.Errorf("vless kind counts: %+v", vless)
	}
	if n, last, err := st.ClientProbeTotals(ctx, base.Add(-time.Hour)); err != nil || n != 3 || !last.Equal(base.Add(time.Hour)) {
		t.Errorf("totals %d %v %v", n, last, err)
	}
	// the window and the retention
	if counts, err := st.ClientProbeCounts(ctx, base.Add(30*time.Minute)); err != nil || len(counts) != 1 || counts[0].TCPBlocked != 1 {
		t.Errorf("counts since +30 min: %+v, %v", counts, err)
	}
	if n, err := st.PruneClientProbeReports(ctx, base.Add(30*time.Minute)); err != nil || n != 4 {
		t.Errorf("pruned %d, %v", n, err)
	}
	if err := st.DeleteClientProbeReports(ctx); err != nil {
		t.Fatal(err)
	}
	if n, last, err := st.ClientProbeTotals(ctx, time.Time{}); err != nil || n != 0 || !last.IsZero() {
		t.Errorf("after delete: %d %v %v", n, last, err)
	}
}
