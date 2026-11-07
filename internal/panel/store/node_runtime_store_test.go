package store

import (
	"context"
	"testing"
	"time"
)

// nodeRuntimeStoreProof runs on SQLite and on D1: the apply metadata, the runtime inbound read and the hourly host rollup with
// its prune.
func nodeRuntimeStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 11, 2, 12, 30, 0, 0, time.UTC)
	hour := base.Truncate(time.Hour).Unix()
	for _, row := range [][4]string{
		{"nod_rt_a", "de1", "active", "203.0.113.10"},
		{"nod_rt_b", "nl1", "active", "203.0.113.11"},
		{"nod_rt_gone", "old1", "retired", "203.0.113.12"},
	} {
		if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, state, address, created_at) VALUES (?, ?, ?, ?, 1)`, row[0], row[1], row[2], row[3]); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Access().CreateProfile(ctx, AccessProfile{ID: "prf_rt", Protocol: "awg", Name: "Runtime profile", SettingsJSON: "{}", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	for _, in := range []AccessInbound{
		{ID: "inb_rt_a", ProfileID: "prf_rt", NodeID: "nod_rt_a", Enabled: true, CreatedAt: base},
		{ID: "inb_rt_gone", ProfileID: "prf_rt", NodeID: "nod_rt_gone", Enabled: true, CreatedAt: base},
	} {
		if err := st.Access().CreateInbound(ctx, in); err != nil {
			t.Fatal(err)
		}
	}

	// Apply metadata: a drift keeps the time it began until a report without drift clears it.
	if err := st.RecordNodeApplyMeta(ctx, "nod_rt_a", "applied", 250, true, base); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordNodeApplyMeta(ctx, "nod_rt_a", "partial", 300, true, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	metas, err := st.NodeApplyMetas(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	m := metas["nod_rt_a"]
	if m.Status != "partial" || m.DurationMS != 300 || !m.AppliedAt.Equal(base.Add(time.Minute)) || !m.DriftSince.Equal(base) {
		t.Fatalf("meta after two drifting applies = %+v, want the first drift time kept", m)
	}
	if err := st.RecordNodeApplyMeta(ctx, "nod_rt_a", "in_sync", 0, false, base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if metas, err = st.NodeApplyMetas(ctx, "nod_rt_a"); err != nil || !metas["nod_rt_a"].DriftSince.IsZero() || metas["nod_rt_a"].Status != "in_sync" {
		t.Fatalf("meta after a clean report = %+v, %v", metas, err)
	}
	if _, ok := metas["nod_rt_b"]; ok {
		t.Error("a node filter returned another node")
	}

	// The runtime read: what the node reported for the inbound, no pin, a retired node left out.
	if err := st.NodeApplied(ctx, "nod_rt_a", 1, false, 5, "hash5", []InboundApplied{{ID: "inb_rt_a", State: "failed", Error: "bind failed",
		SpecHash: "spec-hash-0123456789", CertPin: "pin-never-read", CertNotAfter: base.Add(24 * time.Hour)}}, base); err != nil {
		t.Fatal(err)
	}
	rows, err := st.NodeRuntimeInbounds(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("runtime inbounds = %+v, want the one of the live node", rows)
	}
	r := rows[0]
	if r.NodeID != "nod_rt_a" || r.ID != "inb_rt_a" || r.ProfileName != "Runtime profile" || !r.Enabled || r.State != "failed" || r.LastError != "bind failed" ||
		r.AppliedSpecHash != "spec-hash-0123456789" || !r.CertNotAfter.Equal(base.Add(24*time.Hour)) {
		t.Errorf("runtime inbound = %+v", r)
	}
	if one, err := st.NodeRuntimeInbounds(ctx, "nod_rt_b"); err != nil || len(one) != 0 {
		t.Errorf("runtime inbounds of a node without any = %+v, %v", one, err)
	}

	// The hourly host rollup: samples of an hour fold into one row (mean and peaks), the next hour is another row.
	seq := uint64(0)
	ingest := func(h int64, s HostSample) {
		t.Helper()
		seq++
		sample := s
		out, err := st.IngestStats(ctx, FleetStatsIn{NodeID: "nod_rt_a", Instance: "host-hour", Seq: seq, Now: base, HourStart: h, Host: &sample})
		if err != nil || out.Duplicate {
			t.Fatalf("IngestStats #%d = %+v, %v", seq, out, err)
		}
	}
	ingest(hour, HostSample{CPUPct: 10, SoftirqPct: 2, Load1: 0.5, RAMUsed: 100, RAMTotal: 1000, RxBps: 1000, TxBps: 2000, Sessions: 3})
	ingest(hour, HostSample{CPUPct: 30, SoftirqPct: 6.5, Load1: 1.25, RAMUsed: 300, RAMTotal: 1000, RxBps: 3000, TxBps: 500, Sessions: 9})
	ingest(hour-3600, HostSample{CPUPct: 50, RAMUsed: 50, RAMTotal: 1000, Sessions: 1})
	seq++
	if out, err := st.IngestStats(ctx, FleetStatsIn{NodeID: "nod_rt_a", Instance: "host-hour", Seq: seq, Now: base, HourStart: hour}); err != nil || out.Duplicate { // no HostMetrics: nothing stored
		t.Fatal(out, err)
	}
	hours, err := st.NodeHostHours(ctx, "nod_rt_a", base, 24)
	if err != nil {
		t.Fatal(err)
	}
	if len(hours) != 2 || hours[0].HourStart != hour-3600 || hours[1].HourStart != hour {
		t.Fatalf("host hours = %+v", hours)
	}
	h := hours[1]
	if h.Samples != 2 || h.CPUAvg != 20 || h.CPUMax != 30 || h.SoftirqMax != 6.5 || h.LoadMax != 1.25 || h.RAMUsedMax != 300 || h.RAMTotal != 1000 ||
		h.RxAvg != 2000 || h.RxMax != 3000 || h.TxAvg != 1250 || h.TxMax != 2000 || h.SessionsMax != 9 {
		t.Errorf("rolled hour = %+v", h)
	}
	if one, err := st.NodeHostHours(ctx, "nod_rt_a", base, 1); err != nil || len(one) != 1 || one[0].HourStart != hour {
		t.Errorf("a one-hour window = %+v, %v", one, err)
	}
	if none, err := st.NodeHostHours(ctx, "nod_rt_b", base, 24); err != nil || len(none) != 0 {
		t.Errorf("another node's hours = %+v, %v", none, err)
	}

	// The prune keeps the hour that touches the cut.
	n, err := st.PruneNodeHostHours(ctx, base.Add(-90*time.Minute)) // the cut falls in the older hour: that hour stays
	if err != nil || n != 0 {
		t.Fatalf("PruneNodeHostHours(inside) = %d, %v; want 0", n, err)
	}
	n, err = st.PruneNodeHostHours(ctx, base)
	if err != nil || n != 1 {
		t.Fatalf("PruneNodeHostHours = %d, %v; want the older hour", n, err)
	}
	if hours, err = st.NodeHostHours(ctx, "nod_rt_a", base, 24); err != nil || len(hours) != 1 || hours[0].HourStart != hour {
		t.Errorf("after the prune: %+v, %v", hours, err)
	}
	// The daily prune removes at most one batch; the next sweep clears the backlog.
	_, err = st.W.ExecContext(ctx, `WITH RECURSIVE seq(n) AS (
		SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?
	)
	INSERT INTO node_host_hour (node_id, hour_start)
	SELECT 'nod_rt_a', ? - (n * 3600) FROM seq`, nodeHostHourPruneBatch+1, hour)
	if err != nil {
		t.Fatal(err)
	}

	got, err := st.PruneNodeHostHours(ctx, base)
	if err != nil || got != nodeHostHourPruneBatch {
		t.Fatalf("first sweep pruned %d rows: %v, want %d", got, err, nodeHostHourPruneBatch)
	}
	var left int
	if err := st.R.QueryRowContext(ctx, `SELECT count(*) FROM node_host_hour WHERE node_id = 'nod_rt_a'`).Scan(&left); err != nil || left != 2 {
		t.Fatalf("rows after first sweep = %d, %v, want one expired and the current hour", left, err)
	}
	got, err = st.PruneNodeHostHours(ctx, base)
	if err != nil || got != 1 {
		t.Fatalf("next sweep pruned %d rows: %v, want the remaining expired row", got, err)
	}
}
