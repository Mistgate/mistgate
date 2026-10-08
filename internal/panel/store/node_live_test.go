//go:build !js

package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNodeSessionValueUsesD1SafeIntegerRange(t *testing.T) {
	const maxSafe = uint64(1<<53 - 1)
	for _, tc := range []struct {
		name    string
		session uint64
		want    int64
		wantErr bool
	}{
		{name: "zero", session: 0, want: 0},
		{name: "largest safe", session: maxSafe, want: int64(maxSafe)},
		{name: "first unsafe", session: 1 << 53, wantErr: true},
		{name: "above first unsafe", session: 1<<53 + 1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nodeSessionValue(tc.session)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("nodeSessionValue(%d) = %d, %v; want %d, error=%t", tc.session, got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestNodeLiveReadAndConnectedFilter(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	ids := []string{"nod_active", "nod_stale", "nod_retired", "nod_missing_live"}
	for _, id := range ids {
		if _, err := s.CreateEnrollment(ctx, &NodeRow{ID: id, Name: id, Address: "example.com"}, "", []byte(id), "adm_test", now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		id, state string
		seen      int64
	}{
		{ids[0], "active", now.Unix()},
		{ids[1], "active", now.Add(-91 * time.Second).Unix()},
		{ids[2], "active", now.Unix()},
		{ids[3], "active", now.Unix()},
	} {
		if _, err := s.W.ExecContext(ctx, `UPDATE node SET state = ?, last_seen_at = ?, liveness_timeout_s = 90, agent_caps = 'doctor/1 update/1' WHERE id = ?`, row.state, row.seen, row.id); err != nil {
			t.Fatal(err)
		}
		if row.id != ids[3] {
			if _, err := s.W.ExecContext(ctx, `INSERT INTO node_live (node_id, session, live_json) VALUES (?, 7, '{"proof":true}')`, row.id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.RetireNode(ctx, ids[2], now); err != nil {
		t.Fatal(err)
	}

	rows, err := s.NodeLive(ctx, now, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(ids) {
		t.Fatalf("NodeLive(all) returned %d nodes, want %d", len(rows), len(ids))
	}
	connected := map[string]bool{}
	for _, row := range rows {
		connected[row.NodeID] = row.Connected
		if row.LiveJSON != "" {
			t.Errorf("view=false returned live_json for %s: %q", row.NodeID, row.LiveJSON)
		}
		if row.NodeID == ids[0] && !reflect.DeepEqual(row.AgentCaps, []string{"doctor/1", "update/1"}) {
			t.Errorf("agent_caps = %v", row.AgentCaps)
		}
	}
	if !connected[ids[0]] || connected[ids[1]] || connected[ids[2]] || connected[ids[3]] {
		t.Fatalf("Connected by node = %v, want only active/recent node", connected)
	}

	view, err := s.NodeLive(ctx, now, ids[0], true)
	if err != nil {
		t.Fatal(err)
	}
	if len(view) != 1 || view[0].LiveJSON != `{"proof":true}` || view[0].Session != 7 || !view[0].Exists {
		t.Fatalf("NodeLive(view) = %+v", view)
	}
	missing, err := s.NodeLive(ctx, now, ids[3], true)
	if err != nil || len(missing) != 1 || missing[0].Exists || missing[0].LiveJSON != "" {
		t.Fatalf("NodeLive(missing projection) = %+v, %v", missing, err)
	}
}

func TestNodeLiveWritesAreSessionGuarded(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	now := time.Unix(1_800_000_100, 0).UTC()
	const nodeID = "nod_session_guard"
	if _, err := s.CreateEnrollment(ctx, &NodeRow{ID: nodeID, Name: nodeID, Address: "example.com"}, "", []byte("session-guard"), "adm_test", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.NodeHello(ctx, nodeID, 10, HelloInfo{Instance: "instance-old"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE node_live SET live_json = 'not json' WHERE node_id = ?`, nodeID); err != nil {
		t.Fatal(err)
	}
	stats := func(session uint64, seq uint64, instance, marker string) (FleetStatsOut, error) {
		return s.IngestStats(ctx, FleetStatsIn{NodeID: nodeID, Instance: instance, Session: session, Seq: seq, Now: now,
			HourStart: now.Unix() / 3600 * 3600, Live: func(map[string]FleetCredRef) FleetLive {
				return FleetLive{Apply: true, Drift: true, SampleAt: now.Unix(), RxBps: 1, TxBps: 2, CPUPct: 3,
					UsersJSON: `{"usr_test":1700000000}`, LiveJSON: fmt.Sprintf(`{"marker":%q}`, marker)}
			},
		})
	}
	if out, err := stats(10, 1, "instance-old", "old-session"); err != nil || out.Duplicate {
		t.Fatalf("first stats = %+v, %v", out, err)
	}
	first, err := s.NodeLive(ctx, now, nodeID, true)
	if err != nil || len(first) != 1 || first[0].Session != 10 || !first[0].Drift || !strings.Contains(first[0].LiveJSON, "old-session") {
		t.Fatalf("first projection = %+v, %v", first, err)
	}

	if _, _, err := s.NodeHello(ctx, nodeID, 11, HelloInfo{Instance: "instance-new"}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	reset, err := s.NodeLive(ctx, now.Add(time.Second), nodeID, true)
	if err != nil || len(reset) != 1 || reset[0].Session != 11 || reset[0].Drift || reset[0].LiveJSON != "{}" {
		t.Fatalf("new Hello did not replace projection: %+v, %v", reset, err)
	}
	if out, err := stats(10, 1, "instance-new", "foreign-session"); err != nil || out.Duplicate {
		t.Fatalf("foreign stats = %+v, %v", out, err)
	}
	if err := s.NodeDisconnected(ctx, nodeID, 10, now, now.Add(2*time.Second)); err != nil {
		t.Fatalf("foreign disconnect: %v", err)
	}
	if err := s.NodeApplied(ctx, nodeID, 10, false, 3, "old-hash", nil, now.Add(3*time.Second)); err != nil {
		t.Fatalf("foreign apply: %v", err)
	}
	foreign, err := s.NodeLive(ctx, now, nodeID, true)
	if err != nil || len(foreign) != 1 || foreign[0].Session != 11 || foreign[0].Drift || foreign[0].LiveJSON != "{}" {
		t.Fatalf("foreign session changed projection: %+v, %v", foreign, err)
	}
	var disconnected int64
	if err := s.R.QueryRowContext(ctx, `SELECT last_disconnected_at FROM node WHERE id = ?`, nodeID).Scan(&disconnected); err != nil || disconnected != 0 {
		t.Fatalf("foreign session changed last_disconnected_at to %d: %v", disconnected, err)
	}

	if out, err := stats(11, 2, "instance-new", "current-session"); err != nil || out.Duplicate {
		t.Fatalf("current stats = %+v, %v", out, err)
	}
	current, err := s.NodeLive(ctx, now, nodeID, true)
	if err != nil || len(current) != 1 || !current[0].Drift || !strings.Contains(current[0].LiveJSON, "current-session") {
		t.Fatalf("current projection = %+v, %v", current, err)
	}
	if out, err := stats(11, 2, "instance-new", "duplicate"); err != nil || !out.Duplicate {
		t.Fatalf("duplicate stats = %+v, %v", out, err)
	}
	duplicate, err := s.NodeLive(ctx, now, nodeID, true)
	if err != nil || duplicate[0].LiveJSON != current[0].LiveJSON {
		t.Fatalf("duplicate stats changed projection: before=%+v after=%+v err=%v", current[0], duplicate, err)
	}
	if err := s.NodeApplied(ctx, nodeID, 11, true, 4, "new-hash", nil, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.NodeDisconnected(ctx, nodeID, 11, now, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	ended, err := s.NodeLive(ctx, now, nodeID, true)
	if err != nil || len(ended) != 1 || ended[0].Exists || ended[0].Connected {
		t.Fatalf("current session end = %+v, %v", ended, err)
	}
}
