package store

import (
	"context"
	"testing"
	"time"
)

// A live database has doctor rows without a detail code: 00017 must keep them (code empty, so the UI shows their
// English detail), roll back, and apply again.
func TestDoctorDetailCodeMigrationKeepsOldRows(t *testing.T) {
	ctx := context.Background()
	s, p := openProvider(t)
	if _, err := p.UpTo(ctx, 16); err != nil {
		t.Fatalf("up to 16: %v", err)
	}
	nodeID, _ := fixtureInbound(t, s, "a")
	execT(t, s, `INSERT INTO doctor_result (node_id, check_id, status, title_key, detail, params_json, fix_id, measured_unix, received_unix)
		VALUES (?, 'disk_space', 2, 'doctor.disk_space.title', '/ 85% used', '{"used_pct":"85"}', '', 5, 6)`, nodeID)

	if _, err := p.UpTo(ctx, 17); err != nil {
		t.Fatalf("up to 17: %v", err)
	}
	got, err := s.DoctorResults(ctx, nodeID)
	if err != nil || len(got) != 1 || got[0].Detail != "/ 85% used" || got[0].DetailCode != "" || got[0].Params["used_pct"] != "85" {
		t.Fatalf("old row after the migration: %+v %v", got, err)
	}
	if err := s.PutDoctor(ctx, nodeID, []DoctorRow{{NodeID: nodeID, CheckID: "disk_space", Status: 2, DetailCode: "disk_space.usage"}}, false, time.Unix(9, 0)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.DoctorResults(ctx, nodeID); len(got) != 1 || got[0].DetailCode != "disk_space.usage" {
		t.Fatalf("code after an upsert: %+v", got)
	}

	if _, err := p.DownTo(ctx, 16); err != nil {
		t.Fatalf("down again: %v", err)
	}
	if n := countT(t, s, `SELECT count(*) FROM doctor_result`); n != 1 {
		t.Fatalf("rows after rolling back: %d", n)
	}
	if _, err := p.UpTo(ctx, 17); err != nil {
		t.Fatalf("up again: %v", err)
	}
}
