//go:build !js

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func debugNode(t *testing.T, st *Store, id, name string) {
	t.Helper()
	if _, err := st.W.ExecContext(context.Background(), `INSERT INTO node (id, name, state, address, created_at) VALUES (?, ?, 'active', '203.0.113.10', 1)`, id, name); err != nil {
		t.Fatal(err)
	}
}

func beginDebug(t *testing.T, st *Store, id, node string, at time.Time) DebugCaptureRow {
	t.Helper()
	row := DebugCaptureRow{ID: id, NodeID: node, Actor: "adm_1", Level: "debug", Sources: []string{"hysteria2", "xray"}, DurationS: 300,
		StartedAt: at, EndsAt: at.Add(5 * time.Minute)}
	if err := st.BeginDebugCapture(context.Background(), row, at); err != nil {
		t.Fatalf("BeginDebugCapture(%s): %v", id, err)
	}
	return row
}

func TestDebugCaptureLifecycleAndFilters(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	debugNode(t, st, "nod_1", "de1")
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	beginDebug(t, st, "req_a", "nod_1", now)
	if err := st.BeginDebugCapture(ctx, DebugCaptureRow{ID: "req_b", NodeID: "nod_1", Actor: "x", Level: "info", DurationS: 10, StartedAt: now, EndsAt: now.Add(10 * time.Second)}, now.Add(time.Second)); !errors.Is(err, ErrDebugBusy) {
		t.Fatalf("second running capture: %v", err)
	}
	secrets := []string{"agent-secret.example.com", "secret-admin-path"}
	lines := []DebugLineIn{
		{AtMs: now.UnixMilli(), Level: 4, Source: "hysteria2", Message: "dial 203.0.113.7:4433 failed", Attrs: map[string]string{"err": "refused by 198.51.100.4", "user": "alice", "code": "dial"}},
		{AtMs: now.UnixMilli() + 1, Level: 1, Source: "xray", Message: "accepted tcp:canary.example.net:443 via secret-admin-path", Attrs: map[string]string{"state": "ok"}},
		{AtMs: now.UnixMilli() + 2, Level: 2, Source: "agent", Message: "SNI agent-secret.example.com seen"},
		{AtMs: now.UnixMilli() + 3, Level: 3, Source: "bad source!", Message: "Boom_100%"},
	}
	n, ok, err := st.AppendDebugLines(ctx, "nod_1", "req_a", lines, secrets, now.Add(time.Second))
	if err != nil || !ok || n != 4 {
		t.Fatalf("AppendDebugLines = %d %v %v", n, ok, err)
	}
	// not running for another node or an unknown id
	if _, ok, _ := st.AppendDebugLines(ctx, "nod_other", "req_a", lines, nil, now); ok {
		t.Error("another node's report was taken")
	}
	if _, ok, _ := st.AppendDebugLines(ctx, "nod_1", "req_zz", lines, nil, now); ok {
		t.Error("an unknown capture took lines")
	}
	if err := st.FinishDebugCapture(ctx, "nod_1", "req_a", "stopped", 7); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.AppendDebugLines(ctx, "nod_1", "req_a", lines, nil, now); ok {
		t.Error("a finished capture took more lines")
	}
	row, err := st.DebugCapture(ctx, "req_a")
	if err != nil || row.State != "done" || row.EndReason != "stopped" || row.Records != 4 || row.Dropped != 7 || row.NodeName != "de1" || strings.Join(row.Sources, ",") != "hysteria2,xray" {
		t.Fatalf("capture = %+v, %v", row, err)
	}

	all, sources, err := st.DebugLines(ctx, "req_a", DebugLineFilter{})
	if err != nil || len(all) != 4 || strings.Join(sources, ",") != "agent,hysteria2,xray" {
		t.Fatalf("lines = %+v %v %v", all, sources, err)
	}
	var text strings.Builder
	for _, l := range all {
		fmt.Fprintf(&text, "%s|%s|%v\n", l.Source, l.Message, l.Attrs)
	}
	for _, leak := range []string{"203.0.113.7", "198.51.100.4", "canary.example.net", "secret-admin-path", "agent-secret.example.com", "alice"} {
		if strings.Contains(text.String(), leak) {
			t.Errorf("%q stored:\n%s", leak, text.String())
		}
	}
	for _, want := range []string{"<ip>:port", "<host>", "<secret>", "agent|Boom_100%"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("missing %q:\n%s", want, text.String())
		}
	}
	if all[0].Level != 0 || all[3].Level != 3 || all[0].Seq != 1 || all[3].Seq != 4 {
		t.Errorf("levels/seq: %+v", all)
	}
	if _, has := all[0].Attrs["user"]; has {
		t.Error("an attribute outside the allow-list was stored")
	}

	check := func(name string, f DebugLineFilter, want ...int64) {
		t.Helper()
		got, _, err := st.DebugLines(ctx, "req_a", f)
		if err != nil {
			t.Fatal(err)
		}
		var seqs []int64
		for _, l := range got {
			seqs = append(seqs, l.Seq)
		}
		if fmt.Sprint(seqs) != fmt.Sprint(want) {
			t.Errorf("%s: seqs %v, want %v", name, seqs, want)
		}
	}
	check("level", DebugLineFilter{MinLevel: 1}, 2, 3, 4)
	check("source", DebugLineFilter{Source: "xray"}, 2)
	check("cursor", DebugLineFilter{AfterSeq: 2}, 3, 4)
	check("limit", DebugLineFilter{Limit: 2}, 1, 2, 3) // limit+1: the caller trims and reads the extra one as "more"

	latest, err := st.LatestDebugCapture(ctx, "nod_1", now)
	if err != nil || latest.ID != "req_a" {
		t.Fatalf("latest = %+v %v", latest, err)
	}
	if _, err := st.LatestDebugCapture(ctx, "nod_none", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("latest of nothing: %v", err)
	}
	if _, err := st.DebugCapture(ctx, "req_zz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown capture: %v", err)
	}
}

func TestDebugCaptureIsBoundedOnIntake(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	debugNode(t, st, "nod_1", "de1")
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	beginDebug(t, st, "req_big", "nod_1", now)
	chunk := make([]DebugLineIn, 500)
	for i := range chunk {
		chunk[i] = DebugLineIn{AtMs: 1, Level: 1, Source: "agent", Message: "line"}
	}
	for i := 0; i < DebugMaxLines/500+2; i++ {
		if _, ok, err := st.AppendDebugLines(ctx, "nod_1", "req_big", chunk, nil, now); err != nil || !ok {
			t.Fatalf("chunk %d: %v %v", i, ok, err)
		}
	}
	row, _ := st.DebugCapture(ctx, "req_big")
	if row.Records != DebugMaxLines || row.Dropped != 2*500 {
		t.Fatalf("records %d dropped %d", row.Records, row.Dropped)
	}
	var n int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM debug_capture_line WHERE capture_id = 'req_big'`).Scan(&n); err != nil || n != DebugMaxLines {
		t.Fatalf("stored lines %d %v", n, err)
	}

	// the byte bound
	beginDebug(t, st, "req_bytes", "nod_2", now)
	big := strings.Repeat("word ", debugMessageBytes/5)
	for i := 0; i < 5000; i++ {
		_, _, _ = st.AppendDebugLines(ctx, "nod_2", "req_bytes", []DebugLineIn{{AtMs: 1, Level: 1, Source: "agent", Message: big}}, nil, now)
	}
	row, _ = st.DebugCapture(ctx, "req_bytes")
	if row.Bytes > DebugMaxBytes || row.Records == 0 || row.Records >= 5000 || row.Dropped == 0 {
		t.Fatalf("bytes %d records %d dropped %d", row.Bytes, row.Records, row.Dropped)
	}
}

func TestDebugCaptureTTLPerNodeCapAndPrune(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	debugNode(t, st, "nod_1", "de1")
	debugNode(t, st, "nod_2", "nl1")
	base := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	line := []DebugLineIn{{AtMs: 1, Level: 1, Source: "agent", Message: "x"}}
	// more captures than the per-node cap: the oldest go, with their lines; another node's stay
	for i := 0; i < DebugCapturesPerNode+3; i++ {
		at := base.Add(time.Duration(i) * time.Hour)
		id := fmt.Sprintf("req_%02d", i)
		beginDebug(t, st, id, "nod_1", at)
		if _, ok, err := st.AppendDebugLines(ctx, "nod_1", id, line, nil, at); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if err := st.FinishDebugCapture(ctx, "nod_1", id, "expired", 0); err != nil {
			t.Fatal(err)
		}
	}
	beginDebug(t, st, "req_other", "nod_2", base)
	list, err := st.DebugCaptures(ctx, "nod_1", base.Add(24*time.Hour))
	if err != nil || len(list) != DebugCapturesPerNode {
		t.Fatalf("captures of nod_1 = %d, %v", len(list), err)
	}
	if list[0].ID != "req_07" || list[len(list)-1].ID != "req_03" {
		t.Errorf("kept %s .. %s", list[0].ID, list[len(list)-1].ID)
	}
	var orphans int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM debug_capture_line WHERE capture_id NOT IN (SELECT id FROM debug_capture)`).Scan(&orphans); err != nil || orphans != 0 {
		t.Fatalf("orphan lines %d %v", orphans, err)
	}
	if all, _ := st.DebugCaptures(ctx, "", base); len(all) != DebugCapturesPerNode+1 {
		t.Errorf("all captures = %d", len(all))
	}

	// a running capture whose report never came reads as expired after the grace, and no longer takes lines
	running, _ := st.DebugCaptures(ctx, "nod_2", base.Add(time.Minute))
	if len(running) != 1 || running[0].State != "running" {
		t.Fatalf("running = %+v", running)
	}
	late := base.Add(5*time.Minute + DebugRunGrace + time.Second)
	expired, _ := st.DebugCaptures(ctx, "nod_2", late)
	if expired[0].State != "expired" {
		t.Errorf("state after the grace = %q", expired[0].State)
	}
	if _, ok, _ := st.AppendDebugLines(ctx, "nod_2", "req_other", line, nil, late); ok {
		t.Error("a capture past its grace took lines")
	}
	// ... and a new one may start on that node
	if err := st.BeginDebugCapture(ctx, DebugCaptureRow{ID: "req_next", NodeID: "nod_2", Actor: "x", Level: "info", DurationS: 5, StartedAt: late, EndsAt: late.Add(5 * time.Second)}, late); err != nil {
		t.Errorf("a new capture after an expired one: %v", err)
	}

	// a start the node refused is forgotten; one that has lines stays
	if err := st.DropDebugCapture(ctx, "req_next"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DebugCapture(ctx, "req_next"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a dropped capture is still there: %v", err)
	}
	if err := st.DropDebugCapture(ctx, "req_07"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DebugCapture(ctx, "req_07"); err != nil {
		t.Errorf("a finished capture was dropped: %v", err)
	}

	// the TTL: the prune takes the captures older than the cut, with their lines
	cut := base.Add(5 * time.Hour)
	n, err := st.PruneDebugCaptures(ctx, cut)
	if err != nil || n < 1 {
		t.Fatalf("prune = %d, %v", n, err)
	}
	rest, _ := st.DebugCaptures(ctx, "nod_1", late)
	for _, c := range rest {
		if c.StartedAt.Before(cut) {
			t.Errorf("%s survived the prune", c.ID)
		}
	}
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM debug_capture_line WHERE capture_id NOT IN (SELECT id FROM debug_capture)`).Scan(&orphans); err != nil || orphans != 0 {
		t.Fatalf("orphan lines after the prune %d %v", orphans, err)
	}
}
