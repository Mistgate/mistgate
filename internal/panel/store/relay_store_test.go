package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// relayFixture seeds nodes de1 (a Hysteria2 and a VLESS server), nl1 (two Hysteria2 servers) and fi1 (the entry).
func relayFixture(t *testing.T, st *Store) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_de1', 'de1', '192.0.2.10', 'active', 1)`,
		`INSERT INTO node (id, name, address, state, created_at, agent_caps) VALUES ('nod_fi1', 'fi1', '198.51.100.20', 'active', 1, 'relay-forward/1')`,
		`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_nl1', 'nl1', '192.0.2.30', 'active', 1)`,
		`INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES ('prf_hy', 'hysteria2', 'hy', '{}', 1, 1)`,
		`INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES ('prf_hy2', 'hysteria2', 'hy2', '{}', 1, 1)`,
		`INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES ('prf_vl', 'vless', 'vl', '{}', 1, 1)`,
		`INSERT INTO inbound (id, profile_id, node_id, created_at, updated_at) VALUES ('inb_de1_hy', 'prf_hy', 'nod_de1', 1, 1)`,
		`INSERT INTO inbound (id, profile_id, node_id, created_at, updated_at) VALUES ('inb_de1_vl', 'prf_vl', 'nod_de1', 1, 1)`,
		`INSERT INTO inbound (id, profile_id, node_id, created_at, updated_at) VALUES ('inb_nl1_hy', 'prf_hy', 'nod_nl1', 1, 1)`,
		`INSERT INTO inbound (id, profile_id, node_id, created_at, updated_at) VALUES ('inb_nl1_hy2', 'prf_hy2', 'nod_nl1', 1, 1)`,
	} {
		relayExec(t, st, q)
	}
}

func relayExec(t *testing.T, st *Store, q string, args ...any) {
	t.Helper()
	if _, err := st.W.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// relayStoreProof runs on SQLite and on D1 (see relay_test.go and d1_store_test.go).
func relayStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	relayFixture(t, st)

	if err := st.CreateRelay(ctx, Relay{ID: "rly_a", InboundID: "inb_de1_hy", ViaNodeID: "nod_fi1", Network: "udp", Port: 8443}, now); err != nil {
		t.Fatal(err)
	}
	l, err := st.RelayLink(ctx, "rly_a")
	if err != nil {
		t.Fatal(err)
	}
	if l.Enabled || l.State != "pending" || l.Port != 8443 || l.Network != "udp" || l.CheckJSON != "{}" || !l.CreatedAt.Equal(now) ||
		l.ExitNodeID != "nod_de1" || l.ExitNodeName != "de1" || l.ViaNodeName != "fi1" || l.ProfileName != "hy" || l.Protocol != "hysteria2" ||
		len(l.ViaNodeCaps) != 1 || l.ViaNodeCaps[0] != "relay-forward/1" || !l.Live() || !l.AppliedAt.IsZero() {
		t.Fatalf("a new relay: %+v", l)
	}

	// The same server through the same entry, and the same network and port on one entry, are refused.
	if err := st.CreateRelay(ctx, Relay{ID: "rly_dup", InboundID: "inb_de1_hy", ViaNodeID: "nod_fi1", Network: "udp", Port: 9443}, now); !errors.Is(err, ErrRelayExists) {
		t.Fatalf("the same server twice: %v", err)
	}
	if err := st.CreateRelay(ctx, Relay{ID: "rly_clash", InboundID: "inb_nl1_hy", ViaNodeID: "nod_fi1", Network: "udp", Port: 8443}, now); !errors.Is(err, ErrRelayPortTaken) {
		t.Fatalf("the same udp port on one entry: %v", err)
	}
	// Another network on the same port is another forward; the same port on another entry is too.
	if err := st.CreateRelay(ctx, Relay{ID: "rly_tcp", InboundID: "inb_de1_vl", ViaNodeID: "nod_fi1", Network: "tcp", Port: 8443}, now.Add(time.Second)); err != nil {
		t.Fatalf("tcp on the udp port: %v", err)
	}
	if err := st.CreateRelay(ctx, Relay{ID: "rly_nl", InboundID: "inb_nl1_hy", ViaNodeID: "nod_de1", Network: "udp", Port: 8443}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("the same port on another entry: %v", err)
	}
	// An entry that is the server's own node, an unknown server or node.
	for name, r := range map[string]Relay{
		"own node":       {ID: "rly_x1", InboundID: "inb_de1_hy", ViaNodeID: "nod_de1", Network: "udp", Port: 7443},
		"unknown server": {ID: "rly_x2", InboundID: "inb_nope", ViaNodeID: "nod_fi1", Network: "udp", Port: 7443},
		"unknown entry":  {ID: "rly_x3", InboundID: "inb_de1_hy", ViaNodeID: "nod_nope", Network: "udp", Port: 7443},
	} {
		if err := st.CreateRelay(ctx, r, now); !errors.Is(err, ErrRelayNodes) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The table refuses a port the agent would refuse.
	if err := st.CreateRelay(ctx, Relay{ID: "rly_x4", InboundID: "inb_nl1_hy", ViaNodeID: "nod_fi1", Network: "udp", Port: 80}, now); err == nil {
		t.Error("port 80 was stored")
	}

	all, err := st.RelayLinks(ctx, RelayFilter{})
	if err != nil || len(all) != 3 || all[0].ID != "rly_a" || all[1].ID != "rly_tcp" || all[2].ID != "rly_nl" {
		t.Fatalf("all relays oldest first: %+v, %v", all, err)
	}
	for _, c := range []struct {
		f    RelayFilter
		want int
	}{
		{RelayFilter{ViaNodeID: "nod_fi1"}, 2}, {RelayFilter{NodeID: "nod_de1"}, 3}, {RelayFilter{NodeID: "nod_nl1"}, 1},
		{RelayFilter{NodeID: "nod_fi1"}, 2}, {RelayFilter{InboundID: "inb_de1_vl"}, 1}, {RelayFilter{EnabledOnly: true}, 0},
		{RelayFilter{ViaNodeID: "nod_nl1"}, 0},
	} {
		if got, err := st.RelayLinks(ctx, c.f); err != nil || len(got) != c.want {
			t.Errorf("%+v: %d relays, %v; want %d", c.f, len(got), err, c.want)
		}
	}
	if _, err := st.RelayLink(ctx, "rly_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown relay: %v", err)
	}

	// Switching on and moving the port: both put the relay back to pending, and a port taken is refused.
	on, port := true, uint16(9443)
	if err := st.UpdateRelay(ctx, "rly_a", RelayPatch{Enabled: &on, Port: &port}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.RelayLink(ctx, "rly_a"); !got.Enabled || got.Port != 9443 || got.State != "pending" || !got.UpdatedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("after the update: %+v", got)
	}
	if err := st.UpdateRelay(ctx, "rly_tcp", RelayPatch{Port: &port}, now); err != nil {
		t.Fatalf("a tcp relay moves onto a udp relay's port: %v", err)
	}
	if err := st.CreateRelay(ctx, Relay{ID: "rly_b", InboundID: "inb_nl1_hy", ViaNodeID: "nod_fi1", Network: "udp", Port: 8443}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateRelay(ctx, "rly_b", RelayPatch{Port: &port}, now); !errors.Is(err, ErrRelayPortTaken) {
		t.Fatalf("moving onto a taken port: %v", err)
	}
	if err := st.UpdateRelay(ctx, "rly_nope", RelayPatch{Enabled: &on}, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("updating an unknown relay: %v", err)
	}

	// What the entry reports: ok -> active, a refusal -> failed with the words; a relay that is off, and one of another node,
	// ignore the report.
	at := now.Add(2 * time.Minute)
	if err := st.RelaysApplied(ctx, "nod_fi1", []RelayApplied{
		{ID: "rly_a", OK: true, ToIP: "192.0.2.10"},
		{ID: "rly_tcp", OK: false, Error: "forward_drop"},
		{ID: "rly_b", OK: true},
	}, at); err != nil {
		t.Fatal(err)
	}
	a, _ := st.RelayLink(ctx, "rly_a")
	if a.State != "active" || a.LastError != "" || a.ToIP != "192.0.2.10" || !a.AppliedAt.Equal(at) {
		t.Errorf("an installed forward: %+v", a)
	}
	if b, _ := st.RelayLink(ctx, "rly_b"); b.State != "pending" || !b.AppliedAt.IsZero() {
		t.Errorf("a relay that is off took a report: %+v", b)
	}
	if c, _ := st.RelayLink(ctx, "rly_tcp"); c.State != "pending" {
		t.Errorf("a relay that is off took a report: %+v", c)
	}
	relayExec(t, st, `UPDATE relay SET enabled = 1 WHERE id = 'rly_tcp'`)
	if err := st.RelaysApplied(ctx, "nod_fi1", []RelayApplied{{ID: "rly_tcp", OK: false, Error: "forward_drop"}, {ID: "rly_nl", OK: true}}, at); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.RelayLink(ctx, "rly_tcp"); c.State != "failed" || c.LastError != "forward_drop" {
		t.Errorf("a refused forward: %+v", c)
	}
	if n, _ := st.RelayLink(ctx, "rly_nl"); n.State != "pending" {
		t.Errorf("a result for a relay of another entry: %+v", n)
	}
	// A switch puts it back to pending and forgets the report.
	off := false
	if err := st.UpdateRelay(ctx, "rly_a", RelayPatch{Enabled: &off}, at); err != nil {
		t.Fatal(err)
	}
	if a, _ := st.RelayLink(ctx, "rly_a"); a.Enabled || a.State != "pending" || a.ToIP != "" || !a.AppliedAt.IsZero() {
		t.Errorf("a switched-off relay keeps its report: %+v", a)
	}

	// The server's own state decides whether the entry may forward.
	relayExec(t, st, `UPDATE relay SET enabled = 1 WHERE id = 'rly_a'`)
	live := func() int {
		got, err := st.RelayLinks(ctx, RelayFilter{ViaNodeID: "nod_fi1", EnabledOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, l := range got {
			if l.Live() {
				n++
			}
		}
		return n
	}
	if live() != 2 {
		t.Fatalf("live relays: %d, want 2", live())
	}
	relayExec(t, st, `UPDATE inbound SET state = 'failed' WHERE id = 'inb_de1_hy'`)
	if live() != 1 {
		t.Fatalf("a failed server is forwarded: %d", live())
	}
	relayExec(t, st, `UPDATE inbound SET state = 'active', enabled = 0 WHERE id = 'inb_de1_hy'`)
	if live() != 1 {
		t.Fatalf("a switched-off server is forwarded: %d", live())
	}
	relayExec(t, st, `UPDATE inbound SET enabled = 1 WHERE id = 'inb_de1_hy'`)
	relayExec(t, st, `UPDATE node SET state = 'pending' WHERE id = 'nod_de1'`)
	if live() != 0 {
		t.Fatalf("a server of a node that is not enrolled is forwarded: %d", live())
	}
	relayExec(t, st, `UPDATE node SET state = 'active' WHERE id = 'nod_de1'`)

	if has, err := st.HasRelaysOnNode(ctx, "nod_de1"); err != nil || !has {
		t.Errorf("HasRelaysOnNode(exit) = %v, %v", has, err)
	}
	if has, err := st.HasRelaysOnNode(ctx, "nod_fi1"); err != nil || has {
		t.Errorf("HasRelaysOnNode(entry only) = %v, %v", has, err)
	}

	if err := st.DeleteRelay(ctx, "rly_b"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRelay(ctx, "rly_b"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice: %v", err)
	}

	// The server goes: its relays go with it (the foreign key).
	relayExec(t, st, `DELETE FROM inbound WHERE id = 'inb_de1_vl'`)
	if _, err := st.RelayLink(ctx, "rly_tcp"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a relay outlived its server: %v", err)
	}

	// A node retired: the relays it enters and the ones that lead to it go, others stay.
	if err := st.CreateRelay(ctx, Relay{ID: "rly_c", InboundID: "inb_nl1_hy2", ViaNodeID: "nod_fi1", Network: "udp", Port: 7443}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.RetireNode(ctx, "nod_nl1", now); err != nil {
		t.Fatal(err)
	}
	left, err := st.RelayLinks(ctx, RelayFilter{})
	if err != nil || len(left) != 1 || left[0].ID != "rly_a" {
		t.Fatalf("after the exit was retired: %+v, %v", left, err)
	}
	if err := st.CreateRelay(ctx, Relay{ID: "rly_d", InboundID: "inb_de1_hy", ViaNodeID: "nod_nl1", Network: "udp", Port: 7443}, now); !errors.Is(err, ErrRelayNodes) {
		t.Fatalf("a relay through a retired node: %v", err)
	}
	if err := st.RetireNode(ctx, "nod_fi1", now); err != nil {
		t.Fatal(err)
	}
	if left, err = st.RelayLinks(ctx, RelayFilter{}); err != nil || len(left) != 0 {
		t.Fatalf("after the entry was retired: %+v, %v", left, err)
	}
}

// Two creates at once: one wins and the other is told why, on either backend.
func relayRaceProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	relayFixture(t, st)
	now := time.Unix(1000, 0)
	race := func(a, b Relay) (errs [2]error) {
		var wg sync.WaitGroup
		for i, r := range []Relay{a, b} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = st.CreateRelay(ctx, r, now)
			}()
		}
		wg.Wait()
		return errs
	}
	one := func(errs [2]error, want error) {
		t.Helper()
		created, refused := 0, 0
		for _, err := range errs {
			switch {
			case err == nil:
				created++
			case errors.Is(err, want):
				refused++
			default:
				t.Errorf("unexpected error: %v (want nil or %v)", err, want)
			}
		}
		if created != 1 || refused != 1 {
			t.Errorf("%d created, %d refused; want one of each (%v)", created, refused, errs)
		}
	}
	// The same server through the same entry (the ports differ): the keys of the relay refuse the second.
	one(race(Relay{ID: "rly_r1", InboundID: "inb_de1_hy", ViaNodeID: "nod_fi1", Network: "udp", Port: 8443},
		Relay{ID: "rly_r2", InboundID: "inb_de1_hy", ViaNodeID: "nod_fi1", Network: "udp", Port: 9443}), ErrRelayExists)
	// Two servers, one network and port on one entry.
	one(race(Relay{ID: "rly_r3", InboundID: "inb_nl1_hy", ViaNodeID: "nod_fi1", Network: "udp", Port: 5443},
		Relay{ID: "rly_r4", InboundID: "inb_nl1_hy2", ViaNodeID: "nod_fi1", Network: "udp", Port: 5443}), ErrRelayPortTaken)
}
