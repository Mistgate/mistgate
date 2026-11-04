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
	if _, err := st.W.ExecContext(ctx, `INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at)
		VALUES ('prf_port_check', 'hysteria2', 'port-check', '{}', ?, ?)`, now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO inbound (id, profile_id, node_id, created_at, updated_at)
		VALUES ('inb_port_check', 'prf_port_check', 'nod_port_check', ?, ?)`, now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertSample(ctx, CheckSample{InboundID: "inb_port_check", At: now, Status: 1}); err != nil {
		t.Fatal(err)
	}
	failedAt := now.Add(time.Minute)
	if err := st.InsertSample(ctx, CheckSample{InboundID: "inb_port_check", At: failedAt, Status: 3}); err != nil {
		t.Fatal(err)
	}
	latest, err := st.LatestProbeSamples(ctx, "nod_port_check")
	if err != nil || len(latest) != 1 || latest[0].InboundID != "inb_port_check" || latest[0].Status != 3 || !latest[0].At.Equal(failedAt) {
		t.Fatalf("latest probe samples = %+v, %v", latest, err)
	}
	allLatest, err := st.LatestProbeSamples(ctx, "")
	if err != nil || len(allLatest) != 1 || allLatest[0].InboundID != "inb_port_check" {
		t.Fatalf("all latest probe samples = %+v, %v", allLatest, err)
	}
	filteredAt, err := st.UDPFilteredAt(ctx, "nod_port_check")
	if err != nil || !filteredAt.IsZero() {
		t.Fatalf("initial UDP filtered time = %v, %v", filteredAt, err)
	}
	filteredAt = now.Add(2 * time.Minute)
	if err := st.SetUDPFilteredState(ctx, "nod_port_check", filteredAt, "nod_sender"); err != nil {
		t.Fatal(err)
	}
	gotFilteredAt, err := st.UDPFilteredAt(ctx, "nod_port_check")
	if err != nil || !gotFilteredAt.Equal(filteredAt) {
		t.Fatalf("stored UDP filtered time = %v, %v", gotFilteredAt, err)
	}
	filteredSender, err := st.UDPFilteredSender(ctx, "nod_port_check")
	if err != nil || filteredSender != "nod_sender" {
		t.Fatalf("stored UDP filtered sender = %q, %v", filteredSender, err)
	}
	filteredNodes, err := st.UDPFilteredNodes(ctx)
	if err != nil || len(filteredNodes) != 1 || filteredNodes[0].NodeID != "nod_port_check" || !filteredNodes[0].At.Equal(filteredAt) || !filteredNodes[0].CheckedAt.Equal(filteredAt) || filteredNodes[0].SenderID != "nod_sender" {
		t.Fatalf("UDP filtered nodes = %+v, %v", filteredNodes, err)
	}
	attemptAt := filteredAt.Add(time.Hour)
	if err := st.SetUDPFilteredCheckedAt(ctx, "nod_port_check", attemptAt); err != nil {
		t.Fatal(err)
	}
	filteredNodes, err = st.UDPFilteredNodes(ctx)
	if err != nil || len(filteredNodes) != 1 || !filteredNodes[0].CheckedAt.Equal(attemptAt) {
		t.Fatalf("updated UDP filtered attempt = %+v, %v", filteredNodes, err)
	}
	if err := st.SetUDPFilteredAt(ctx, "nod_port_check", time.Time{}); err != nil {
		t.Fatal(err)
	}
	gotFilteredAt, err = st.UDPFilteredAt(ctx, "nod_port_check")
	if err != nil || !gotFilteredAt.IsZero() {
		t.Fatalf("cleared UDP filtered time = %v, %v", gotFilteredAt, err)
	}
	filteredNodes, err = st.UDPFilteredNodes(ctx)
	if err != nil || len(filteredNodes) != 0 {
		t.Fatalf("cleared UDP filtered nodes = %+v, %v", filteredNodes, err)
	}
	checks := []PortCheck{
		{NodeID: "nod_port_check", Port: 2053, Address: "203.0.113.10", Sent: 300, Got: 210, Verdict: "broken", SenderVerdict: "broken",
			Sender: "nod_sender", ConfirmSender: "nod_sender2", ConfirmSent: 300, ConfirmGot: 220, ConfirmVerdict: "lossy", Blame: "node", CheckedAt: now},
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
	if got[1].SenderVerdict != "broken" || got[1].ConfirmSender != "nod_sender2" || got[1].ConfirmSent != 300 ||
		got[1].ConfirmGot != 220 || got[1].ConfirmVerdict != "lossy" || got[1].Blame != "node" {
		t.Fatalf("confirmation fields were not stored: %+v", got[1])
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
	pathAt := now.Add(2 * time.Hour)
	if err := st.PutPortChecks(ctx, []PortCheck{{NodeID: "nod_port_check", Port: 2053, Address: "203.0.113.10",
		Sent: 300, Got: 280, Verdict: "ok", SenderVerdict: "lossy", Sender: "nod_sender", ConfirmSender: "nod_sender2",
		ConfirmSent: 300, ConfirmGot: 300, ConfirmVerdict: "ok", Blame: "path", CheckedAt: pathAt}}); err != nil {
		t.Fatal(err)
	}
	got, err = st.PortChecks(ctx, "nod_port_check")
	if err != nil || len(got) != 2 || got[1].Blame != "path" || !got[1].BadAt.IsZero() || got[1].Bad(pathAt) {
		t.Fatalf("path-blamed run stayed bad for the node: %+v, %v", got, err)
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

	if _, err := st.W.ExecContext(ctx, `DELETE FROM inbound WHERE id = 'inb_port_check'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `DELETE FROM profile WHERE id = 'prf_port_check'`); err != nil {
		t.Fatal(err)
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

func udpPathLossStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	checked := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for _, node := range []struct{ id, name, country string }{
		{"nod_minsk1", "minsk1", "BY"}, {"nod_nl1", "nl1", "NL"}, {"nod_de1", "de1", "DE"},
	} {
		if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, country_code, created_at)
			VALUES (?, ?, '203.0.113.20', ?, ?)`, node.id, node.name, node.country, checked.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	loss := UDPPathLoss{SenderID: "nod_minsk1", TargetID: "nod_nl1", Ports: "2053, 2083", Sent: 300, Got: 237,
		OtherSenderID: "nod_de1", OtherGot: 299, CheckedAt: checked}
	if err := st.UpdateUDPPathLosses(ctx, "nod_nl1", []string{"nod_minsk1", "nod_de1"}, []UDPPathLoss{loss}); err != nil {
		t.Fatal(err)
	}
	loss.Got = 240
	loss.CheckedAt = checked.Add(time.Hour)
	check := PortCheck{NodeID: "nod_nl1", Address: "203.0.113.20", Port: 2053, Sent: 300, Got: 280,
		Verdict: "ok", Sender: "nod_minsk1", SenderVerdict: "lossy", ConfirmSender: "nod_de1", ConfirmSent: 300,
		ConfirmGot: 300, ConfirmVerdict: "ok", Blame: "path", CheckedAt: loss.CheckedAt}
	if err := st.PutPortChecksAndUDPPathLosses(ctx, []PortCheck{check}, "nod_nl1", []string{"nod_minsk1", "nod_de1"}, []UDPPathLoss{loss}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.UDPPathLosses(ctx, checked.Add(-time.Second))
	if err != nil || len(rows) != 1 || rows[0].Got != 240 || rows[0].OtherGot != 299 || rows[0].Ports != "2053, 2083" ||
		rows[0].SenderName != "minsk1" || rows[0].TargetName != "nl1" || rows[0].OtherSenderName != "de1" ||
		rows[0].SenderCountry != "BY" || rows[0].OtherSenderCountry != "DE" {
		t.Fatalf("path-loss upsert/read = %+v, %v", rows, err)
	}
	checks, err := st.PortChecks(ctx, "nod_nl1")
	if err != nil || len(checks) != 1 || checks[0].Blame != "path" || !checks[0].BadAt.IsZero() {
		t.Fatalf("combined port/path write = %+v, %v", checks, err)
	}
	if err := st.UpdateUDPPathLosses(ctx, "nod_nl1", []string{"nod_minsk1", "nod_de1"}, nil); err != nil {
		t.Fatal(err)
	}
	rows, err = st.UDPPathLosses(ctx, checked.Add(-time.Second))
	if err != nil || len(rows) != 0 {
		t.Fatalf("path-loss delete = %+v, %v", rows, err)
	}
}
