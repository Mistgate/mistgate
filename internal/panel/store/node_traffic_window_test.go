//go:build !js

package store

import (
	"context"
	"testing"
	"time"
)

// The windows count whole hourly rows: the hour in progress and the 23 before it for 24 hours, 167 more for the week, and
// both directions of every protocol.
func TestAddTrafficParams(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	now := time.Date(2026, 10, 7, 12, 40, 0, 0, time.UTC)
	if _, err := s.W.ExecContext(ctx, `INSERT INTO node (id, name, address, created_at) VALUES ('nod_a', 'de1', '203.0.113.10', ?), ('nod_b', 'nl1', '203.0.113.11', ?)`, now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	cur := now.Truncate(time.Hour).Unix()
	for _, r := range []struct {
		node, proto string
		hoursAgo    int64
		up, down    int64
	}{
		{"nod_a", "vless", 0, 1, 2},             // the hour in progress
		{"nod_a", "hysteria2", 0, 10, 20},       // another protocol, same hour
		{"nod_a", "vless", 23, 100, 200},        // the oldest hour of the day
		{"nod_a", "vless", 24, 1000, 2000},      // the week only
		{"nod_a", "vless", 167, 10000, 20000},   // the oldest hour of the week
		{"nod_a", "vless", 168, 100000, 200000}, // outside
		{"nod_b", "vless", 0, 5, 5},             // another node
	} {
		if _, err := s.W.ExecContext(ctx, `INSERT INTO node_traffic_hour (node_id, protocol, hour_start, bytes_up, bytes_down) VALUES (?, ?, ?, ?, ?)`,
			r.node, r.proto, cur-r.hoursAgo*3600, r.up, r.down); err != nil {
			t.Fatal(err)
		}
	}
	p := map[string]string{"node": "de1"}
	if err := s.AddTrafficParams(ctx, "nod_a", now, p); err != nil {
		t.Fatal(err)
	}
	if p["traffic_24h_bytes"] != "333" || p["traffic_7d_bytes"] != "33333" || p["node"] != "de1" {
		t.Fatalf("params %v, want 333 for the day and 33333 for the week", p)
	}
	p = map[string]string{}
	if err := s.AddTrafficParams(ctx, "nod_none", now, p); err != nil || p["traffic_24h_bytes"] != "0" || p["traffic_7d_bytes"] != "0" {
		t.Fatalf("a node without rows: %v %v", p, err)
	}
}
