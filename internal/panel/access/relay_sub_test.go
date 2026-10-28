package access

import (
	"strings"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The twin servers of a relay (cascade plan §7): the exit's server through an entry node, next to it, in every format.

// twinFix is a relay fixture with countries: the exit de1 is DE, the entry fi1 is FI.
func newTwinFix(t *testing.T) *relayFix {
	t.Helper()
	f := newRelayFix(t)
	f.sql(`UPDATE node SET country_code = 'DE', bandwidth_mbps = 1000 WHERE id = 'nod_de1'`)
	f.sql(`UPDATE node SET country_code = 'FI', bandwidth_mbps = 500 WHERE id = 'nod_fi1'`)
	f.sql(`UPDATE node SET country_code = 'NL' WHERE id = 'nod_nl1'`)
	return f
}

// switchOn plays a relay that the entry installed and the checks proved.
func (f *relayFix) switchOn(id string) {
	f.t.Helper()
	f.sql(`UPDATE relay SET enabled = 1, state = 'active' WHERE id = ?`, id)
	c := store.RelayCheck{At: 1, Leg: store.RelayStage{Status: "ok"}, Path: store.RelayStage{Status: "ok"}}
	c.Settle()
	if err := f.st.SetRelayCheck(f.ctx, id, c); err != nil {
		f.t.Fatal(err)
	}
}

// twinView is the person's view in a format.
func (f *relayFix) twinView(token string, format plugin.ClientFormat) SubView {
	f.t.Helper()
	return must(f.s.SubscriptionWith(f.ctx, token, SubOptions{Format: format, NoTouch: true, NoPageData: true}))
}

// swapAddress is the exit's line with the entry's host and port: what a twin of it has to be.
func swapAddress(line, from, to string) string { return strings.Replace(line, from, to, 1) }

func withoutFragment(uri string) string { uri, _, _ = strings.Cut(uri, "#"); return uri }

func TestTwinOfAVLESSServerKeepsEverythingButTheAddress(t *testing.T) {
	f := newTwinFix(t)
	p := must(f.s.CreateProfile(f.ctx, req(&adminv1.CreateProfileRequest{
		Protocol: "vless", Name: "vless", SettingsJson: `{"transport":"tcp","reality":{"target":"www.example.org:443"}}`,
	}))).Msg.Profile
	exit := f.inbound(p.Id, "nod_de1")
	r := must(f.create(exit.Id, "nod_fi1", 8443))
	f.switchOn(r.Id)
	group := f.group("g", p.Id)
	token := f.tokenOf(f.user("alice", group, nil).User.Id)

	v := f.twinView(token, plugin.FormatURIList)
	if len(v.Servers) != 2 || len(v.Lines) != 2 {
		t.Fatalf("servers %d, lines %d, want the server and its twin", len(v.Servers), len(v.Lines))
	}
	srv, twin := v.Servers[0], v.Servers[1]
	if srv.Mark != "" || twin.Mark != "via:FI" || twin.NodeID != srv.NodeID || twin.CountryCode != "DE" || twin.Made != srv.Made || twin.Profile != srv.Profile {
		t.Errorf("exit %+v, twin %+v", srv, twin)
	}
	if got, want := withoutFragment(twin.URI), swapAddress(withoutFragment(srv.URI), "@de1.example.com:443?", "@fi1.example.com:8443?"); got != want {
		t.Errorf("twin\n%s\nwant\n%s", got, want)
	}
	if twin.URI != v.Lines[1] {
		t.Error("the twin's server and line differ")
	}
	// The page lists it as a link of the exit's node, through FI.
	if len(v.Nodes) != 1 || len(v.Nodes[0].Conns) != 2 || !v.Nodes[0].Conns[1].Relay || v.Nodes[0].Conns[1].Via != "FI" || v.Nodes[0].Conns[1].Server != 1 || v.Nodes[0].Conns[0].Relay {
		t.Errorf("page connections: %+v", v.Nodes)
	}
	// Xray JSON, sing-box and Mihomo carry it the same way.
	for format, host := range map[plugin.ClientFormat]string{plugin.FormatXrayJSON: "fi1.example.com", plugin.FormatSingbox: "fi1.example.com", plugin.FormatMihomo: "fi1.example.com"} {
		v := f.twinView(token, format)
		if len(v.Lines) != 2 || !strings.Contains(v.Lines[1], host) || !strings.Contains(v.Lines[1], "8443") || strings.Contains(v.Lines[1], "de1.example.com") {
			t.Errorf("%s: lines %q", format, v.Lines)
		}
		for _, key := range []string{"reality", "pbk", "public", "sid", "short"} {
			if strings.Contains(strings.ToLower(v.Lines[0]), key) != strings.Contains(strings.ToLower(v.Lines[1]), key) {
				t.Errorf("%s: %q is in one of the two lines only", format, key)
			}
		}
	}
}

func TestTwinOfAHysteria2ServerHasNoHopRange(t *testing.T) {
	f := newTwinFix(t)
	p := f.profile("hop", `{"hop":{"from":20000,"to":30000}}`)
	exit := f.inbound(p.Id, "nod_nl1")
	r := must(f.create(exit.Id, "nod_fi1", 8443))
	f.switchOn(r.Id)
	token := f.tokenOf(f.user("alice", f.group("g", p.Id), nil).User.Id)

	v := f.twinView(token, plugin.FormatURIList)
	if len(v.Servers) != 2 {
		t.Fatalf("servers: %+v", v.Servers)
	}
	if got, want := withoutFragment(v.Servers[1].URI), swapAddress(withoutFragment(v.Servers[0].URI), "@nl1.example.com:443/", "@fi1.example.com:8443/"); got != want {
		t.Errorf("twin\n%s\nwant\n%s", got, want)
	}
	// Mihomo writes the hop range of the exit; the twin is a single port.
	m := f.twinView(token, plugin.FormatMihomo)
	if len(m.Lines) != 2 || !strings.Contains(m.Lines[0], "ports:") || !strings.Contains(m.Lines[0], "20000-30000") {
		t.Fatalf("the exit's Mihomo proxy has no hop range:\n%s", strings.Join(m.Lines, "\n"))
	}
	if twin := m.Lines[1]; strings.Contains(twin, "ports:") || strings.Contains(twin, "hop-interval") || strings.Contains(twin, "20000") ||
		!strings.Contains(twin, "server: \"fi1.example.com\"") || !strings.Contains(twin, "port: 8443") {
		t.Errorf("twin proxy:\n%s", twin)
	}
	// sing-box too, when it takes the server (a self-signed one is not for it).
	s := f.twinView(token, plugin.FormatSingbox)
	for i, line := range s.Lines {
		if i == 1 && (strings.Contains(line, "server_ports") || strings.Contains(line, "20000") || !strings.Contains(line, `"server_port":8443`)) {
			t.Errorf("sing-box twin: %s", line)
		}
	}
}

func TestTwinNamesAndLoad(t *testing.T) {
	f := newTwinFix(t)
	p, exit := f.base, f.server
	r := must(f.create(exit.Id, "nod_fi1", 8443))
	f.switchOn(r.Id)
	token := f.tokenOf(f.user("alice", f.group("g", p.Id), nil).User.Id)
	// A busy entry (50 Mbit/s of its 500, then 450) and a quiet exit (2 of 1000): the twin shows the larger load.
	sample := func(id string, rx int64) {
		f.sql(`UPDATE node SET last_seen_at = ? WHERE id = ?`, f.clock.Unix(), id)
		f.sql(`INSERT INTO node_live (node_id, session, sample_at, rx_bps, tx_bps, users, live_json) VALUES (?, 1, ?, ?, 0, '{}', '{}')`,
			id, f.clock.Add(-time.Second).Unix(), rx)
	}
	sample("nod_fi1", 400_000_000)
	sample("nod_de1", 2_000_000)
	f.s.online = subscriptionOnline{users: map[string]string{}}
	v := f.twinView(token, plugin.FormatURIList)
	if len(v.Servers) != 2 {
		t.Fatalf("servers: %+v", v.Servers)
	}
	exitLoad, twinLoad := v.Servers[0].LoadPercent, v.Servers[1].LoadPercent
	if exitLoad == nil || twinLoad == nil || *twinLoad <= *exitLoad || *twinLoad != 80 || *exitLoad != 0 {
		t.Errorf("loads: exit %v, twin %v, want the entry's 80, and the exit's own 0", exitLoad, twinLoad)
	}
}

func TestNoTwinUnlessTheRelayIsUsableForThePerson(t *testing.T) {
	f := newTwinFix(t)
	p, exit := f.base, f.server
	r := must(f.create(exit.Id, "nod_fi1", 8443))
	group := f.group("g", p.Id)
	token := f.tokenOf(f.user("alice", group, nil).User.Id)
	servers := func() int { return len(f.twinView(token, plugin.FormatURIList).Servers) }

	if n := servers(); n != 1 {
		t.Fatalf("a relay that is off: %d servers", n)
	}
	// On, but not proven yet (waiting for its entry, or failing its check).
	f.sql(`UPDATE relay SET enabled = 1, state = 'active' WHERE id = ?`, r.Id)
	if n := servers(); n != 1 {
		t.Errorf("a relay never checked: %d servers", n)
	}
	for name, c := range map[string]store.RelayCheck{
		"leg only":    {At: 1, Leg: store.RelayStage{Status: "ok"}, Path: store.RelayStage{Status: "skipped", Code: "off"}},
		"path failed": {At: 1, Leg: store.RelayStage{Status: "ok"}, Path: store.RelayStage{Status: "fail", Code: "broken"}},
		"leg failed":  {At: 1, Leg: store.RelayStage{Status: "fail", Code: "refused"}, Path: store.RelayStage{Status: "skipped"}},
	} {
		c.Settle()
		if err := f.st.SetRelayCheck(f.ctx, r.Id, c); err != nil {
			t.Fatal(err)
		}
		if n := servers(); n != 1 {
			t.Errorf("%s: %d servers", name, n)
		}
	}
	f.switchOn(r.Id)
	if n := servers(); n != 2 {
		t.Fatalf("a proven relay: %d servers", n)
	}
	// The entry could not install it, or has gone.
	f.sql(`UPDATE relay SET state = 'failed', last_error = 'forward_drop' WHERE id = ?`, r.Id)
	if n := servers(); n != 1 {
		t.Errorf("a forward that failed: %d servers", n)
	}
	f.sql(`UPDATE relay SET state = 'active' WHERE id = ?`, r.Id)
	f.sql(`UPDATE node SET state = 'pending' WHERE id = 'nod_fi1'`)
	if n := servers(); n != 1 {
		t.Errorf("an entry that is not active: %d servers", n)
	}
	f.sql(`UPDATE node SET state = 'active' WHERE id = 'nod_fi1'`)
	f.sql(`UPDATE inbound SET enabled = 0 WHERE id = ?`, exit.Id)
	if n := servers(); n != 0 {
		t.Errorf("the exit's server off: %d servers", n)
	}
	f.sql(`UPDATE inbound SET enabled = 1 WHERE id = ?`, exit.Id)
	if n := servers(); n != 2 {
		t.Errorf("back on: %d servers", n)
	}
}

// A person who is not allowed the entry node gets no twin (and the entry's other people are not affected).
func TestNoTwinForAPersonWhoseNodesLeaveTheEntryOut(t *testing.T) {
	f := newTwinFix(t)
	p, exit := f.base, f.server
	r := must(f.create(exit.Id, "nod_fi1", 8443))
	f.switchOn(r.Id)
	group := f.group("g", p.Id)
	only := f.user("only-exit", group, func(m *adminv1.CreateUserRequest) {
		m.Nodes = &adminv1.NodeSelection{NodeIds: []string{"nod_de1"}}
	}).User
	both := f.user("both", group, func(m *adminv1.CreateUserRequest) {
		m.Nodes = &adminv1.NodeSelection{NodeIds: []string{"nod_de1", "nod_fi1"}}
	}).User
	all := f.user("all", group, nil).User
	for name, want := range map[string]struct {
		id string
		n  int
	}{"only the exit": {only.Id, 1}, "exit and entry": {both.Id, 2}, "every node": {all.Id, 2}} {
		if got := len(f.twinView(f.tokenOf(want.id), plugin.FormatURIList).Servers); got != want.n {
			t.Errorf("%s: %d servers, want %d", name, got, want.n)
		}
	}
}

// A server that cannot go through a relay never gets a twin, whatever the rows say (CreateRelay refuses it, but the rule is also
// kept where the view is built): an AmneziaWG key has no twin in any format.
func TestNoTwinForAnAmneziaWGServer(t *testing.T) {
	f := newTwinFix(t)
	awg := must(f.s.CreateProfile(f.ctx, req(&adminv1.CreateProfileRequest{Protocol: "awg", Name: "awg31"}))).Msg.Profile
	hy := f.base
	awgIn := f.inbound(awg.Id, "nod_de1")
	f.sql(`INSERT INTO relay (id, inbound_id, via_node_id, network, port, enabled, state, check_json, created_at, updated_at)
		VALUES ('rly_awg', ?, 'nod_fi1', 'udp', 8443, 1, 'active', ?, 1, 1)`, awgIn.Id, `{"at":1,"leg":{"status":"ok"},"path":{"status":"ok"},"ok":true}`)
	token := f.tokenOf(f.user("alice", f.group("g", awg.Id, hy.Id), nil).User.Id)
	for _, format := range []plugin.ClientFormat{plugin.FormatURIList, plugin.FormatMihomo, plugin.FormatXrayJSON, plugin.FormatSingbox} {
		for _, srv := range f.twinView(token, format).Servers {
			if srv.Mark != "" {
				t.Errorf("%s: a twin of an AmneziaWG server: %+v", format, srv)
			}
		}
	}
}
