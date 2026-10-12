package store

import (
	"context"
	"testing"
	"time"
)

func tcpCheckStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"nod_tcp_a", "nod_tcp_b"} {
		if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, created_at) VALUES (?, ?, '203.0.113.10', ?)`,
			id, "node-"+id, now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	first := []TCPCheck{
		{Port: 443, Role: "site", ALPN: true, Profile: "alice-hy2", Verdict: "ours", Sender: "panel", CheckedAt: now},
		{Port: 80, Role: "acme", Verdict: "other", Sender: "nod_sender", CheckedAt: now},
		{Port: 8443, Role: "vless", Profile: "alice-vless", Verdict: "", Reason: "agent_too_old", Sender: "panel", CheckedAt: now},
	}
	if err := st.PutTCPChecks(ctx, "nod_tcp_a", first); err != nil {
		t.Fatal(err)
	}
	other := []TCPCheck{{Port: 80, Role: "acme", Verdict: "ours", Sender: "panel", CheckedAt: now}}
	if err := st.PutTCPChecks(ctx, "nod_tcp_b", other); err != nil {
		t.Fatal(err)
	}
	got, err := st.TCPChecks(ctx, "nod_tcp_a")
	if err != nil || len(got) != 3 {
		t.Fatalf("TCPChecks() = %+v, %v", got, err)
	}
	if got[0].Port != 80 || got[0].Role != "acme" || got[0].ALPN || got[0].Verdict != "other" || got[0].Sender != "nod_sender" ||
		!got[0].CheckedAt.Equal(now) || got[0].NodeID != "nod_tcp_a" {
		t.Fatalf("port 80 row = %+v", got[0])
	}
	if got[1].Port != 443 || !got[1].ALPN || got[1].Profile != "alice-hy2" || got[1].Verdict != "ours" {
		t.Fatalf("port 443 row = %+v", got[1])
	}
	if got[2].Port != 8443 || got[2].Verdict != "" || got[2].Reason != "agent_too_old" {
		t.Fatalf("port 8443 row = %+v", got[2])
	}
	if all, err := st.TCPChecks(ctx); err != nil || len(all) != 4 {
		t.Fatalf("TCPChecks() for every node = %+v, %v", all, err)
	}

	// A run replaces the node's rows: the ports that are gone disappear, the other node's rows stay.
	later := now.Add(time.Hour)
	if err := st.PutTCPChecks(ctx, "nod_tcp_a", []TCPCheck{{Port: 443, Role: "vless", Profile: "bob-vless", Verdict: "timeout", Sender: "panel", CheckedAt: later}}); err != nil {
		t.Fatal(err)
	}
	got, err = st.TCPChecks(ctx, "nod_tcp_a")
	if err != nil || len(got) != 1 || got[0].Port != 443 || got[0].Role != "vless" || got[0].ALPN || got[0].Verdict != "timeout" || !got[0].CheckedAt.Equal(later) {
		t.Fatalf("replaced rows = %+v, %v", got, err)
	}
	if rows, err := st.TCPChecks(ctx, "nod_tcp_b"); err != nil || len(rows) != 1 {
		t.Fatalf("the other node's rows = %+v, %v", rows, err)
	}

	if err := st.PutTCPChecks(ctx, "nod_tcp_a", nil); err != nil {
		t.Fatal(err)
	}
	if rows, err := st.TCPChecks(ctx, "nod_tcp_a"); err != nil || len(rows) != 0 {
		t.Fatalf("an empty run left rows: %+v, %v", rows, err)
	}

	if _, err := st.W.ExecContext(ctx, `DELETE FROM node WHERE id = 'nod_tcp_b'`); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := st.R.QueryRowContext(ctx, `SELECT count(*) FROM node_tcp_check`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("node delete left %d TCP check rows", remaining)
	}
	if err := st.PutTCPChecks(ctx, "nod_missing", other); err == nil {
		t.Fatal("rows for a node that does not exist were stored")
	}
}
