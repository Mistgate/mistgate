package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func nodeJournalStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 10, 24, 12, 30, 0, 0, time.UTC)
	hour := base.Truncate(time.Hour).Unix()
	for _, row := range [][4]string{
		{"nod_journal_a", "de1", "active", "203.0.113.10"},
		{"nod_journal_b", "nl1", "active", "203.0.113.11"},
		{"nod_journal_retired", "old1", "retired", "203.0.113.12"},
	} {
		if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, state, address, created_at) VALUES (?, ?, ?, ?, 1)`, row[0], row[1], row[2], row[3]); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Access().CreateProfile(ctx, AccessProfile{ID: "prf_journal", Protocol: "awg", Name: "Journal profile", SettingsJSON: "{}", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	for _, in := range []AccessInbound{
		{ID: "inb_journal_a", ProfileID: "prf_journal", NodeID: "nod_journal_a", Enabled: true, CreatedAt: base},
		{ID: "inb_journal_b", ProfileID: "prf_journal", NodeID: "nod_journal_b", Enabled: true, CreatedAt: base},
		{ID: "inb_journal_old", ProfileID: "prf_journal", NodeID: "nod_journal_retired", Enabled: true, CreatedAt: base},
	} {
		if err := st.Access().CreateInbound(ctx, in); err != nil {
			t.Fatal(err)
		}
	}

	secrets := []string{"agent-secret.example.com", "secret-admin-path", "secret-sub-path", "secret-link-path"}
	seq := map[string]uint64{}
	ingest := func(nodeID string, bucket int64, entries ...FleetJournal) {
		t.Helper()
		seq[nodeID]++
		out, err := st.IngestStats(ctx, FleetStatsIn{NodeID: nodeID, Instance: "journal-store", Seq: seq[nodeID], Now: base,
			HourStart: bucket, Journal: entries, JournalSecrets: secrets})
		if err != nil || out.Duplicate {
			t.Fatalf("IngestStats journal for %s at %d = %+v, %v", nodeID, bucket, out, err)
		}
	}

	message := "Failed 198.51.100.11:456 from agent-secret.example.com /secret-admin-path/ /secret-sub-path/ /secret-link-path/"
	first := FleetJournal{Level: 2, Source: "awg", Message: message, InboundID: "inb_journal_a",
		Attrs: map[string]string{"code": "handshake", "state": "first", "peer": "203.0.113.22:9000", "path": "/secret-admin-path/admin"}, Count: 2, FirstAt: 110, LastAt: 100}
	second := FleetJournal{Level: 2, Source: "awg", Message: message, InboundID: "inb_journal_a",
		Attrs: map[string]string{"code": "handshake", "state": "second", "peer": "203.0.113.99", "path": "/secret-admin-path/admin"}, Count: 3, FirstAt: 90, LastAt: 120}
	ingest("nod_journal_a", hour, first, second,
		FleetJournal{Level: 2, Source: "awg", Message: message, InboundID: "inb_journal_b", Attrs: map[string]string{"code": "foreign"}, Count: 1, FirstAt: 130, LastAt: 130},
		FleetJournal{Level: 2, Source: "awg", Message: message, InboundID: "inb_journal_a", Attrs: map[string]string{"code": "new-hour"}, Count: 4, FirstAt: 140, LastAt: 150},
		FleetJournal{Level: 3, Source: "xray", Message: "AUTH%_FAILED", InboundID: "inb_journal_a",
			Attrs: map[string]string{"code": "auth_failed", "detail": "auth%_failed"}, Count: 1, FirstAt: 160, LastAt: 170})
	ingest("nod_journal_a", hour+3600,
		FleetJournal{Level: 2, Source: "awg", Message: message, InboundID: "inb_journal_a", Attrs: map[string]string{"code": "handshake"}, Count: 1, FirstAt: 200, LastAt: 210})
	ingest("nod_journal_b", hour, FleetJournal{Level: 2, Source: "agent", Message: "node b warning", InboundID: "inb_journal_b", Count: 1, FirstAt: 220, LastAt: 230})
	ingest("nod_journal_retired", hour, FleetJournal{Level: 2, Source: "agent", Message: "retired warning", InboundID: "inb_journal_old", Count: 1, FirstAt: 240, LastAt: 250})

	var gotCount int64
	if err := st.R.QueryRowContext(ctx, `SELECT count FROM node_journal WHERE node_id = 'nod_journal_a' AND hour_start = ? AND inbound_id = 'inb_journal_a' AND attrs_json LIKE '%first%'`, hour).Scan(&gotCount); err != nil || gotCount != 5 {
		t.Fatalf("merged hourly count = %d, %v; want 5", gotCount, err)
	}
	var sameKindBuckets int
	if err := st.R.QueryRowContext(ctx, `SELECT count(*) FROM node_journal WHERE node_id = 'nod_journal_a' AND attrs_json LIKE '%handshake%'`).Scan(&sameKindBuckets); err != nil || sameKindBuckets != 2 {
		t.Fatalf("same kind in two hours has %d rows, %v; want 2", sameKindBuckets, err)
	}
	var storedMessage, storedAttrs string
	var storedAttrsMap map[string]string
	var firstAt, lastAt int64
	if err := st.R.QueryRowContext(ctx, `SELECT message, attrs_json, first_at, last_at FROM node_journal
		WHERE node_id = 'nod_journal_a' AND hour_start = ? AND inbound_id = 'inb_journal_a' AND attrs_json LIKE '%first%'`, hour).
		Scan(&storedMessage, &storedAttrs, &firstAt, &lastAt); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(storedAttrs), &storedAttrsMap); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"agent-secret.example.com", "secret-admin-path", "secret-sub-path", "secret-link-path", "198.51.100.11", "203.0.113.22"} {
		if strings.Contains(storedMessage+storedAttrs, secret) {
			t.Errorf("stored journal contains secret/address %q: %s %s", secret, storedMessage, storedAttrs)
		}
	}
	if !strings.Contains(storedMessage, "<ip>:port") || !strings.Contains(storedMessage, "<secret>") ||
		storedAttrsMap["peer"] != "<ip>:port" || storedAttrsMap["path"] != "/<secret>/admin" || storedAttrsMap["state"] != "first" || firstAt != 90 || lastAt != 120 {
		t.Errorf("panel cleaning or merged times: %q %q first=%d last=%d", storedMessage, storedAttrs, firstAt, lastAt)
	}
	var foreignInbound string
	if err := st.R.QueryRowContext(ctx, `SELECT inbound_id FROM node_journal WHERE node_id = 'nod_journal_a' AND hour_start = ? AND attrs_json LIKE '%foreign%'`, hour).Scan(&foreignInbound); err != nil || foreignInbound != "" {
		t.Errorf("foreign inbound id = %q, %v; want empty", foreignInbound, err)
	}

	query := func(f NodeJournalFilter) []NodeJournalRow {
		t.Helper()
		rows, err := st.NodeJournal(ctx, f)
		if err != nil {
			t.Fatalf("NodeJournal(%+v): %v", f, err)
		}
		return rows
	}
	baseFilter := NodeJournalFilter{Since: base.Add(-time.Hour), Until: base.Add(2 * time.Hour), MinLevel: 2, Limit: 100}
	rows := query(baseFilter)
	if len(rows) != 6 { // four kinds in A's first hour, one in its next hour, and one on B; retired nodes are out of scope
		t.Errorf("all-node rows = %d, want 6", len(rows))
	}
	profileResolved := false
	for _, row := range rows {
		if row.NodeID == "nod_journal_a" && row.Attrs["code"] == "handshake" {
			profileResolved = row.NodeName == "de1" && row.ProfileName == "Journal profile"
		}
	}
	if !profileResolved {
		t.Errorf("node/profile names were not resolved in rows: %+v", rows)
	}
	if got := query(NodeJournalFilter{NodeID: "nod_journal_a", Since: base, Until: base, MinLevel: 2, Source: "awg", InboundID: "inb_journal_a", Query: "failed", Limit: 20}); len(got) != 2 {
		t.Errorf("source/inbound/case-insensitive message query rows = %d, want 2", len(got))
	}
	if got := query(NodeJournalFilter{NodeID: "nod_journal_a", Since: base, Until: base, MinLevel: 3, Query: "auth%_failed", Limit: 20}); len(got) != 1 || got[0].Source != "xray" {
		t.Errorf("ERROR and escaped attrs query rows = %+v, want one xray row", got)
	}
	if got := query(NodeJournalFilter{NodeID: "nod_journal_a", Since: base, Until: base.Add(-time.Hour), MinLevel: 2, Limit: 20}); len(got) != 0 {
		t.Errorf("outside hour window returned %d rows", len(got))
	}
	if got := query(NodeJournalFilter{Since: base, Until: base.Add(2 * time.Hour), MinLevel: 2, Limit: 1}); len(got) != 1 {
		t.Errorf("limit 1 returned %d rows", len(got))
	}
	if got := query(NodeJournalFilter{Since: base, Until: base.Add(2 * time.Hour), MinLevel: 2, Query: "retired warning", Limit: 20}); len(got) != 0 {
		t.Errorf("all-node query included retired node: %+v", got)
	}

	// More than 500 new keys in one node/hour only inserts the first 500; existing keys still update above the cap.
	capHour := hour + 100*3600
	for batch := 0; batch < 8; batch++ {
		entries := make([]FleetJournal, 64)
		for i := range entries {
			n := batch*64 + i
			code := fmt.Sprintf("bulk-%03d", n)
			entries[i] = FleetJournal{Level: 2, Source: "bulk", Message: code, InboundID: "inb_journal_a",
				Attrs: map[string]string{"code": code}, Count: 1, FirstAt: int64(n + 1), LastAt: int64(n + 1)}
		}
		ingest("nod_journal_a", capHour, entries...)
	}
	var capCount int
	if err := st.R.QueryRowContext(ctx, `SELECT count(*) FROM node_journal WHERE node_id = 'nod_journal_a' AND hour_start = ?`, capHour).Scan(&capCount); err != nil || capCount != maxNodeJournalPerHour {
		t.Fatalf("per-hour row cap = %d, %v; want %d", capCount, err, maxNodeJournalPerHour)
	}
	ingest("nod_journal_a", capHour,
		FleetJournal{Level: 2, Source: "bulk", Message: "bulk-000", InboundID: "inb_journal_a", Attrs: map[string]string{"code": "bulk-000"}, Count: 7, FirstAt: 1, LastAt: 999},
		FleetJournal{Level: 2, Source: "bulk", Message: "bulk-512", InboundID: "inb_journal_a", Attrs: map[string]string{"code": "bulk-512"}, Count: 1, FirstAt: 1, LastAt: 1})
	var bulkCount int64
	if err := st.R.QueryRowContext(ctx, `SELECT count FROM node_journal WHERE node_id = 'nod_journal_a' AND hour_start = ? AND message = 'bulk-000'`, capHour).Scan(&bulkCount); err != nil || bulkCount != 8 {
		t.Errorf("existing key above cap count = %d, %v; want 8", bulkCount, err)
	}
	if err := st.R.QueryRowContext(ctx, `SELECT count(*) FROM node_journal WHERE node_id = 'nod_journal_a' AND hour_start = ?`, capHour).Scan(&capCount); err != nil || capCount != maxNodeJournalPerHour {
		t.Errorf("row cap after existing update/new key = %d, %v; want %d", capCount, err, maxNodeJournalPerHour)
	}

	oldHour := hour - int64(NodeJournalKeepHours+1)*3600
	ingest("nod_journal_a", oldHour, FleetJournal{Level: 2, Source: "agent", Message: "old", Count: 1, FirstAt: 1, LastAt: 1})
	pruned, err := st.PruneNodeJournal(ctx, base.Add(-time.Duration(NodeJournalKeepHours)*time.Hour))
	if err != nil || pruned == 0 {
		t.Fatalf("PruneNodeJournal = %d, %v; want old rows pruned", pruned, err)
	}
	if err := st.R.QueryRowContext(ctx, `SELECT count(*) FROM node_journal WHERE node_id = 'nod_journal_a' AND hour_start = ?`, hour).Scan(&capCount); err != nil || capCount == 0 {
		t.Errorf("retention removed a current row: count=%d err=%v", capCount, err)
	}
}
