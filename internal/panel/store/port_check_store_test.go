package store

import (
	"context"
	"testing"
	"time"
)

func portCheckStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, created_at)
		VALUES ('nod_port_check', 'node-port-check', '203.0.113.10', ?)`, now.Unix()); err != nil {
		t.Fatal(err)
	}
	checks := []PortCheck{
		{NodeID: "nod_port_check", Port: 2053, Address: "203.0.113.10", Sent: 300, Got: 210, Verdict: "broken", Sender: "nod_sender", CheckedAt: now},
		{NodeID: "nod_port_check", Port: 443, Address: "203.0.113.10", Sent: 300, Got: 300, Verdict: "ok", Sender: "panel", CheckedAt: now},
	}
	if err := st.PutPortChecks(ctx, checks); err != nil {
		t.Fatal(err)
	}
	got, err := st.PortChecks(ctx, "nod_port_check")
	if err != nil || len(got) != 2 {
		t.Fatalf("initial PortChecks() = %+v, %v", got, err)
	}
	if got[0].Port != 443 || got[1].Port != 2053 || got[1].BadAt != now || !got[1].Bad(now) {
		t.Fatalf("initial checks = %+v", got)
	}
	if !got[0].Fresh(now) || got[0].Fresh(now.Add(portCheckFreshFor)) || got[1].Fresh(now) {
		t.Fatalf("Fresh() boundary or verdict = %+v", got)
	}
	if !got[1].Bad(now.Add(portCheckBadFor-time.Second)) || got[1].Bad(now.Add(portCheckBadFor)) {
		t.Fatalf("Bad() boundary = %+v", got[1])
	}

	cleanAt := now.Add(time.Hour)
	if err := st.PutPortChecks(ctx, []PortCheck{{NodeID: "nod_port_check", Port: 2053, Address: "203.0.113.10",
		Sent: 300, Got: 300, Verdict: "ok", Sender: "panel", CheckedAt: cleanAt}}); err != nil {
		t.Fatal(err)
	}
	got, err = st.PortChecks(ctx, "nod_port_check")
	if err != nil || len(got) != 2 || got[1].BadAt != now || !got[1].Bad(cleanAt) || !got[1].Fresh(cleanAt) {
		t.Fatalf("clean run did not keep bad_at and update the result: %+v, %v", got, err)
	}

	if _, err := st.W.ExecContext(ctx, `UPDATE node SET address = '203.0.113.11' WHERE id = 'nod_port_check'`); err != nil {
		t.Fatal(err)
	}
	got, err = st.PortChecks(ctx, "nod_port_check")
	if err != nil || len(got) != 0 {
		t.Fatalf("old-address rows were returned: %+v, %v", got, err)
	}
	changedAt := now.Add(2 * time.Hour)
	if err := st.PutPortChecks(ctx, []PortCheck{{NodeID: "nod_port_check", Port: 2053, Address: "203.0.113.11",
		Sent: 300, Got: 300, Verdict: "ok", Sender: "panel", CheckedAt: changedAt}}); err != nil {
		t.Fatal(err)
	}
	got, err = st.PortChecks(ctx, "nod_port_check")
	if err != nil || len(got) != 1 || got[0].BadAt != (time.Time{}) || got[0].Bad(changedAt) {
		t.Fatalf("address change did not reset bad_at or filter old rows: %+v, %v", got, err)
	}

	if _, err := st.W.ExecContext(ctx, `DELETE FROM node WHERE id = 'nod_port_check'`); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := st.R.QueryRowContext(ctx, `SELECT count(*) FROM node_port_check WHERE node_id = 'nod_port_check'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("node delete left %d port-check rows", remaining)
	}
}
