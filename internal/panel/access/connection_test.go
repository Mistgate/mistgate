package access

import (
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// GetUserConnection reads a person's counters per device, node and inbound, only the non-zero ones, in the window; the nodes
// the person can use whose agent does not list links/1 are named instead of showing zeros.
func TestGetUserConnection(t *testing.T) {
	f := newFixture(t)
	e := f.e
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	p2 := e.profile("hy2 8443", "")
	in2 := e.inbound(p2.Id, "nod_nl1")
	g := e.group("both", f.profile, p2.Id)
	e.sql(`UPDATE node SET agent_caps = 'links/1 awg/1' WHERE id = 'nod_de1'`)

	alice := e.user("alice", g, nil).User
	bob := e.user("bob", g, nil).User
	var cred, device string
	if err := e.st.R.QueryRow(`SELECT id, device_id FROM device_credential WHERE user_id = ? ORDER BY id LIMIT 1`, alice.Id).Scan(&cred, &device); err != nil {
		t.Fatal(err)
	}
	e.sql(`UPDATE device SET platform = 'android', client = 'Happ/3.4.2', kind = 'link' WHERE id = ?`, device)
	hour := e.clock.Unix() / 3600 * 3600
	row := func(node, inbound, credID, user string, at int64, counters string) {
		e.sql(`INSERT INTO link_hour (node_id, hour_start, inbound_id, cred_id, user_id, `+counters+`) VALUES (?, ?, ?, ?, ?, 1, 1, 1, 1, 1)`,
			node, at, inbound, credID, user)
	}
	cols := `connects, rejects, drops, stalls, online_s`
	row("nod_de1", f.inbound, cred, alice.Id, hour, cols)
	row("nod_de1", f.inbound, cred, alice.Id, hour-3600, cols)
	row("nod_nl1", in2.Id, cred, alice.Id, hour-2*3600, cols)
	row("nod_de1", f.inbound, cred, alice.Id, hour-30*3600, cols) // outside the default window
	e.sql(`INSERT INTO link_hour (node_id, hour_start, inbound_id, cred_id, user_id, hellos) VALUES ('nod_de1', ?, ?, 'crd_ghost', ?, 4)`, hour, f.inbound, alice.Id)
	e.sql(`INSERT INTO link_hour (node_id, hour_start, inbound_id, cred_id, user_id, hellos) VALUES ('nod_de1', ?, ?, ?, ?, 4)`, hour-5*3600, f.inbound, cred, alice.Id) // no counter a person has: not the newest hour

	resp, err := e.s.GetUserConnection(e.ctx, req(&adminv1.GetUserConnectionRequest{UserId: alice.Id}))
	if err != nil {
		t.Fatal(err)
	}
	m := resp.Msg
	if m.NowUnix != e.clock.Unix() || m.NowUnix-m.SinceUnix != 24*3600 {
		t.Errorf("window %d..%d, want 24 h to now", m.SinceUnix, m.NowUnix)
	}
	if len(m.Rows) != 2 {
		t.Fatalf("rows = %+v, want de1 and nl1", m.Rows)
	}
	de, nl := m.Rows[0], m.Rows[1]
	if de.DeviceId != device || de.NodeId != "nod_de1" || de.NodeName != "de1" || de.InboundId != f.inbound || de.Protocol != "hysteria2" ||
		de.Connects != 2 || de.Rejects != 2 || de.Drops != 2 || de.Stalls != 2 || de.OnlineS != 2 || de.LastHourUnix != hour ||
		de.Platform != "android" || de.Client != "Happ/3.4.2" || de.Kind != "link" || len(de.Hours) != 0 {
		t.Errorf("de1 row = %+v", de)
	}
	if nl.NodeName != "nl1" || nl.InboundId != in2.Id || nl.Connects != 1 || nl.LastHourUnix != hour-2*3600 {
		t.Errorf("nl1 row = %+v", nl)
	}
	if len(m.NodesWithoutCounters) != 1 || m.NodesWithoutCounters[0] != "nod_nl1" {
		t.Errorf("nodes without counters = %v, want [nod_nl1] (the ids)", m.NodesWithoutCounters)
	}
	if resp, err = e.s.GetUserConnection(e.ctx, req(&adminv1.GetUserConnectionRequest{UserId: alice.Id, Hours: 168})); err != nil || resp.Msg.Rows[0].Connects != 3 {
		t.Errorf("168 hours = %+v, %v, want the older hour counted", resp, err)
	}
	byHour, err := e.s.GetUserConnection(e.ctx, req(&adminv1.GetUserConnectionRequest{UserId: alice.Id, Hours: 24, ByHour: true}))
	if err != nil || len(byHour.Msg.Rows) != 2 || len(byHour.Msg.Rows[0].Hours) != 2 ||
		byHour.Msg.Rows[0].Hours[0].HourStartUnix != hour-3600 || byHour.Msg.Rows[0].Hours[1].HourStartUnix != hour ||
		byHour.Msg.Rows[0].Hours[0].Connects != 1 || byHour.Msg.Rows[0].Hours[1].Connects != 1 {
		t.Errorf("by_hour response = %+v, %v", byHour, err)
	}
	if resp, err = e.s.GetUserConnection(e.ctx, req(&adminv1.GetUserConnectionRequest{UserId: bob.Id})); err != nil || len(resp.Msg.Rows) != 0 || len(resp.Msg.NodesWithoutCounters) != 1 {
		t.Errorf("a person without counters = %+v, %v", resp, err)
	}
	_, err = e.s.GetUserConnection(e.ctx, req(&adminv1.GetUserConnectionRequest{UserId: "usr_missing"}))
	wantCode(t, err, connect.CodeNotFound)
}
