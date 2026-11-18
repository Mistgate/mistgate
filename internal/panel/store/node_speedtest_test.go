//go:build !js

package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestSpeedTestTimesAreKeptForTheReportWindow(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	execT(t, s, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_de1', 'de1', '192.0.2.10', 'active', 1)`)
	now := time.Unix(1_800_000_000, 0)
	old := now.Add(-(HostMaxHours + 2) * time.Hour)
	for _, at := range []time.Time{old, now.Add(-time.Hour), now.Add(-time.Hour), now} {
		if err := s.NoteSpeedTest(ctx, "nod_de1", at); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.SpeedTestTimes(ctx, "nod_de1", old)
	if err != nil {
		t.Fatal(err)
	}
	// the one older than the window went when a newer one was written; the same second is one row
	if want := []int64{now.Add(-time.Hour).Unix(), now.Unix()}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// 00147 marks the speed measurements that were written down before 00145 started to mark them, and only those: the stored
// path speed tests (both ends), the audit row of a node's own bandwidth measurement and the first measurement's event.
func TestSpeedTestBackfill(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	p := cnProvider(t, s)
	if _, err := p.DownTo(ctx, 146); err != nil {
		t.Fatalf("down to 146: %v", err)
	}
	now := time.Now()
	ago := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	for _, q := range []string{
		`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_de1', 'de1', 'de1.example.com', 'active', 1), ('nod_nl1', 'nl1', 'nl1.example.com', 'active', 1)`,
		// a test between the two, one at the end of the window's reach, one a month old
		fmt.Sprintf(`INSERT INTO path_speed (from_node_id, to_node_id, direction, at_unix, result_json) VALUES ('nod_de1', 'nod_nl1', 'down', %d, '{}'), ('nod_de1', 'nod_nl1', 'down', %d, '{}')`, ago(2*time.Hour), ago(40*24*time.Hour)),
		// de1's own measurement, a row for a node that is gone, a damaged row, and a path speed audit that names nodes only
		fmt.Sprintf(`INSERT INTO audit (ts, actor, action, params, result) VALUES (%d, 'adm', 'node.bandwidth_measure', '{"node_id":"nod_de1","node":"de1"}', 'ok'),
			(%d, 'adm', 'node.bandwidth_measure', '{"node_id":"nod_gone"}', 'ok'), (%d, 'adm', 'node.bandwidth_measure', 'not json', 'ok'),
			(%d, 'adm', 'node.path_speed', '{"from":"nl1","to":"de1","error":"timeout"}', 'ok')`, ago(3*time.Hour), ago(3*time.Hour), ago(3*time.Hour), ago(5*time.Hour)),
		// nl1's first measurement after enrolling, and an event of another kind
		fmt.Sprintf(`INSERT INTO event (ts, severity, code, source, node_id) VALUES (%d, 1, 'bandwidth_measured', 'panel', 'nod_nl1'), (%d, 1, 'node_online', 'panel', 'nod_nl1')`, ago(4*time.Hour), ago(6*time.Hour)),
	} {
		if _, err := s.W.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.UpTo(ctx, 147); err != nil {
		t.Fatalf("up to 147: %v", err)
	}
	since := now.Add(-100 * 24 * time.Hour)
	for id, want := range map[string][]int64{
		"nod_de1": {ago(3 * time.Hour), ago(2 * time.Hour)},
		"nod_nl1": {ago(4 * time.Hour), ago(2 * time.Hour)},
	} {
		got, err := s.SpeedTestTimes(ctx, id, since)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%s: got %v (%v), want %v", id, got, err, want)
		}
	}
}
