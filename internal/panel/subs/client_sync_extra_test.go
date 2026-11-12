package subs_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// What sync tells beyond the servers of the first release: the Telegram proxy links, the country of a relay, the label of a
// profile and the VLESS paths. The node of these tests is named nodeAlias, the relay's node relayAlias; neither may be in any answer.

const (
	relayAlias = "relay-helsinki-3"
	vlessLabel = "VLESS Reality"
)

// extras puts an MTProto and a VLESS profile on nod_1 next to the Hysteria2 and AmneziaWG ones, and returns the group with all
// four and the id of the VLESS profile.
func (s *syncEnv) extras() (group, vless string) {
	s.t.Helper()
	tg := must(s.svc.CreateProfile(s.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "mtproto", Name: "tg 443", SettingsJson: `{"port":8443}`}))).Msg.Profile
	vl := must(s.svc.CreateProfile(s.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "vless", Name: vlessLabel}))).Msg.Profile
	for _, id := range []string{tg.Id, vl.Id} {
		must(s.svc.CreateInbound(s.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: id, NodeId: "nod_1"})))
	}
	group = must(s.svc.CreateGroup(s.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "all", ProfileIds: []string{s.profile, s.awg, tg.Id, vl.Id}}))).Msg.Group.Id
	return group, vl.Id
}

// personIn is a person of the group with the given apps (the Mistgate app is on unless mistgate is false).
func (s *syncEnv) personIn(name, group string, telegram bool, mistgate bool) (id, tok string) {
	return s.user(name, func(m *adminv1.CreateUserRequest) {
		m.GroupId = group
		m.Apps = &adminv1.AppToggles{Happ: true, Amnezia: true, Telegram: telegram, Mistgate: &mistgate}
	})
}

func (s *syncEnv) inboundOf(profile string) string {
	s.t.Helper()
	var id string
	if err := s.st.R.QueryRow(`SELECT id FROM inbound WHERE profile_id = ? ORDER BY created_at LIMIT 1`, profile).Scan(&id); err != nil {
		s.t.Fatal(err)
	}
	return id
}

// addRelay makes a node relayAlias (fi1.example.com, in the country cc) and a relay of the inbound through it.
func (s *syncEnv) addRelay(inbound, cc, network string) {
	s.t.Helper()
	for _, q := range []string{
		`INSERT OR IGNORE INTO node (id, name, address, country_code, state, agent_caps, created_at) VALUES ('nod_2', '` + relayAlias + `', 'fi1.example.com', '` + cc + `', 'active', 'relay-forward/1', 2)`,
		`UPDATE node SET country_code = '` + cc + `' WHERE id = 'nod_2'`,
	} {
		if _, err := s.st.W.Exec(q); err != nil {
			s.t.Fatal(err)
		}
	}
	if _, err := s.st.W.Exec(`INSERT INTO relay (id, inbound_id, via_node_id, network, port, enabled, state, check_json, created_at, updated_at)
		VALUES ('rly_'||?, ?, 'nod_2', ?, 8443, 1, 'active', ?, 1, 1)`, inbound, inbound, network, `{"at":1,"leg":{"status":"ok"},"path":{"status":"ok"},"ok":true}`); err != nil {
		s.t.Fatal(err)
	}
}

// assertNoInternals: neither node's name, nor an internal id, nor the link or the person is in an answer.
func assertNoInternals(t *testing.T, what, raw string, extra ...string) {
	t.Helper()
	for _, forbidden := range append([]string{nodeAlias, relayAlias, "nod_", "inb_", "prf_", "usr_", "k3xq8", "alice-liddell", "bob-the-builder", "carol"}, extra...) {
		if strings.Contains(raw, forbidden) {
			t.Errorf("%s: the body holds %q", what, forbidden)
		}
	}
}

func TestSyncTelegramLinks(t *testing.T) {
	e := newSyncEnv(t, nil)
	group, _ := e.extras()
	_, aliceTok := e.personIn("alice-liddell", group, true, true)
	_, bobTok := e.personIn("bob-the-builder", group, false, true)
	carolID, carolTok := e.personIn("carol", group, true, true)
	a, b, c := e.enrolDevice(aliceTok, hwidA), e.enrolDevice(bobTok, hwidB2), e.enrolDevice(carolTok, "7d6c5b4a-3f2e-4d1c-8b0a-9f8e7d6c5b4a")

	alice := e.mustSync(a.Key)
	if len(alice.Telegram) != 1 || len(alice.Servers) != 1 {
		t.Fatalf("alice: %s", alice.raw)
	}
	tg := alice.Telegram[0]
	u, err := url.Parse(tg.Link)
	if err != nil || u.Scheme != "tg" || u.Host != "proxy" || u.Query().Get("server") != "de1.example.com" || u.Query().Get("port") != "8443" || !strings.HasPrefix(u.Query().Get("secret"), "ee") {
		t.Fatalf("link = %q, %v", tg.Link, err)
	}
	if tg.ServerID != alice.Servers[0].ID || tg.Country != "DE" || tg.City != "Frankfurt" {
		t.Errorf("telegram entry = %+v, servers[0] = %q", tg, alice.Servers[0].ID)
	}
	assertNoInternals(t, "alice-liddell", alice.raw)

	// The link is the one the page of the subscription shows: one renderer.
	page, _ := pageData(t, fetch(e.h, "/"+aliceTok, chrome).Body.String())
	links := page["telegram"].(map[string]any)["links"].([]any)
	if len(links) != 1 || links[0].(map[string]any)["url"] != tg.Link {
		t.Errorf("page links = %v, sync link = %q", links, tg.Link)
	}

	// Telegram is never a path of a server.
	for _, p := range alice.Servers[0].Paths {
		if strings.Contains(p.Protocol, "mtproto") || strings.Contains(fmt.Sprint(p.Settings), "tg://") {
			t.Errorf("a Telegram path in servers: %+v", p)
		}
	}

	// A person with Telegram switched off has no field at all.
	if bob := e.mustSync(b.Key); len(bob.Telegram) != 0 || strings.Contains(bob.raw, "telegram") || strings.Contains(bob.raw, "tg://") {
		t.Errorf("bob: %s", bob.raw)
	}

	// The key rotates: the link and the ETag follow it.
	must(e.svc.RotateTelegramKey(e.ctx, connect.NewRequest(&adminv1.RotateTelegramKeyRequest{UserId: carolID})))
	before := e.mustSync(c.Key)
	must(e.svc.RotateTelegramKey(e.ctx, connect.NewRequest(&adminv1.RotateTelegramKeyRequest{UserId: carolID})))
	if after := e.mustSync(c.Key); len(after.Telegram) != 1 || after.Telegram[0].Link == before.Telegram[0].Link || after.etg == before.etg {
		t.Errorf("a rotated key left the answer as it was: %s", after.raw)
	}

	// The Mistgate app off for the person: the reason, no servers and no Telegram, though the Telegram app is on.
	e.setApps(carolID, &adminv1.AppToggles{Happ: true, Amnezia: true, Telegram: true, Mistgate: new(false)})
	off := e.mustSync(c.Key)
	if off.Reason != "app_disabled" || len(off.Telegram) != 0 || strings.Contains(off.raw, "telegram") || strings.Contains(off.raw, "tg://") {
		t.Errorf("carol with the Mistgate app off: %s", off.raw)
	}
	e.setApps(carolID, &adminv1.AppToggles{Happ: true, Amnezia: true, Telegram: true, Mistgate: new(true)})
	if back := e.mustSync(c.Key); len(back.Telegram) != 1 {
		t.Errorf("carol with the app back on: %s", back.raw)
	}

	// Telegram off for a person who had it: gone from the answer.
	e.setApps(carolID, &adminv1.AppToggles{Happ: true, Amnezia: true, Telegram: false, Mistgate: new(true)})
	if gone := e.mustSync(c.Key); len(gone.Telegram) != 0 || strings.Contains(gone.raw, "tg://") {
		t.Errorf("carol with Telegram off: %s", gone.raw)
	}

	// Without access the status says why, and the link is not given.
	for _, q := range []string{`UPDATE user SET disabled = 1`} {
		if _, err := e.st.W.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if dis := e.mustSync(a.Key); dis.Status != "disabled" || len(dis.Telegram) != 0 || strings.Contains(dis.raw, "tg://") {
		t.Errorf("alice disabled: %s", dis.raw)
	}
}

// A group with no MTProto profile has no Telegram links, whatever the person's switch says.
func TestSyncTelegramNeedsAnMTProtoServer(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.personIn("alice-liddell", e.group2, true, true)
	a := e.enrolDevice(tok, hwidA)
	if b := e.mustSync(a.Key); len(b.Telegram) != 0 || strings.Contains(b.raw, "telegram") || strings.Contains(b.raw, "tg://") {
		t.Errorf("a group with no MTProto profile: %s", b.raw)
	}
}

func TestSyncVLESSPaths(t *testing.T) {
	e := newSyncEnv(t, nil)
	group, vless := e.extras()
	_, tok := e.personIn("alice-liddell", group, false, true)
	a := e.enrolDevice(tok, hwidA)

	b := e.mustSync(a.Key)
	vl := b.paths("vless")
	if len(b.Servers) != 1 || len(vl) != 1 || len(b.Servers[0].Paths) != 3 {
		t.Fatalf("paths of the server: %s", b.raw)
	}
	p := vl[0]
	if p.Via != "direct" || p.IPv6 || p.Label != vlessLabel || p.ViaCountry != "" {
		t.Errorf("vless path = %+v", p)
	}
	s := p.Settings
	if s["server"] != "de1.example.com" || s["port"] != float64(443) || s["transport"] != "xhttp" || s["security"] != "reality" || s["mode"] != "auto" ||
		s["sni"] != "de1.example.com" || s["fingerprint"] != "chrome" || s["path"] == "" || s["x_padding_bytes"] != "100-1000" {
		t.Errorf("vless settings = %v", s)
	}
	for _, k := range []string{"uuid", "public_key", "short_id", "spider_x"} {
		if v, _ := s[k].(string); v == "" {
			t.Errorf("vless settings have no %s: %v", k, s)
		}
	}
	for _, k := range []string{"flow", "alpn", "host"} {
		if _, ok := s[k]; ok {
			t.Errorf("vless settings carry %s for XHTTP+REALITY: %v", k, s)
		}
	}
	assertNoInternals(t, "vless", b.raw)

	// Only a client that announced VLESS is sent the path (as AmneziaWG goes to one that announced its version); the rest stays.
	for _, caps := range []string{"v=1; hysteria2=2; awg=2.0", "v=1", "v=1; vless=2"} {
		if old := e.mustSync(a.Key, "X-Mistgate-Caps", caps); len(old.paths("vless")) != 0 || len(old.paths("hysteria2")) != 1 || strings.Contains(old.raw, "vless") {
			t.Errorf("caps %q: %s", caps, old.raw)
		}
	}

	// The same server through a relay: the entry's address and port, and the entry's country.
	e.addRelay(e.inboundOf(vless), "FI", "tcp")
	b = e.mustSync(a.Key)
	var got []string
	for _, p := range b.paths("vless") {
		got = append(got, p.Via+"/"+p.ViaCountry+"/"+p.Settings["server"].(string))
	}
	if want := "direct//de1.example.com relay/FI/fi1.example.com"; strings.Join(got, " ") != want {
		t.Errorf("vless paths with a relay: %v, want %s", got, want)
	}
	if relay := b.paths("vless")[1]; relay.Settings["port"] != float64(8443) || relay.Settings["sni"] != "de1.example.com" || relay.Label != vlessLabel {
		t.Errorf("the relay path keeps the exit's names and the forwarded port: %+v", relay)
	}
	assertNoInternals(t, "vless relay", b.raw)

	// A relay whose node has no country has no via_country, and the answer changes with it.
	withFI := b
	if _, err := e.st.W.Exec(`UPDATE node SET country_code = '' WHERE id = 'nod_2'`); err != nil {
		t.Fatal(err)
	}
	b = e.mustSync(a.Key)
	if r := b.paths("vless")[1]; r.Via != "relay" || r.ViaCountry != "" || strings.Contains(b.raw, "via_country") {
		t.Errorf("a relay with no country: %+v\n%s", r, b.raw)
	}
	if b.etg == withFI.etg {
		t.Error("the ETag did not follow the relay's country")
	}
}

// A node that has VLESS and nothing else is a server of the app now.
func TestSyncVLESSOnlyNodeIsListed(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, vless := e.extras()
	const only = "nl1-edge"
	if _, err := e.st.W.Exec(`INSERT INTO node (id, name, address, country_code, location, state, created_at) VALUES ('nod_3', '` + only + `', 'nl1.example.com', 'NL', 'Amsterdam', 'active', 3)`); err != nil {
		t.Fatal(err)
	}
	must(e.svc.CreateInbound(e.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: vless, NodeId: "nod_3"})))
	g := must(e.svc.CreateGroup(e.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "vless only", ProfileIds: []string{vless}}))).Msg.Group.Id
	_, tok := e.personIn("alice-liddell", g, false, true)
	a := e.enrolDevice(tok, hwidA)

	b := e.mustSync(a.Key)
	if len(b.Servers) != 2 {
		t.Fatalf("servers: %s", b.raw)
	}
	var nl bool
	for _, s := range b.Servers {
		if s.Country == "NL" && s.City == "Amsterdam" && len(s.Paths) == 1 && s.Paths[0].Protocol == "vless" && s.Paths[0].Settings["server"] == "nl1.example.com" {
			nl = true
		}
	}
	if !nl {
		t.Errorf("no NL server with a VLESS path: %s", b.raw)
	}
	assertNoInternals(t, "vless only", b.raw, only)
}

// Two profiles of one protocol on one server are told apart by their labels, and the label is the owner's name of the profile and
// nothing of the node or the way.
func TestSyncLabelsTellProfilesApart(t *testing.T) {
	e := newSyncEnv(t, nil)
	fast := must(e.svc.CreateProfile(e.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "hysteria2", Name: "Hysteria2 fast", SettingsJson: `{"port":4443}`}))).Msg.Profile
	must(e.svc.CreateInbound(e.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: fast.Id, NodeId: "nod_1"})))
	g := must(e.svc.CreateGroup(e.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "two", ProfileIds: []string{e.profile, fast.Id, e.awg}}))).Msg.Group.Id
	_, tok := e.personIn("alice-liddell", g, false, true)
	a := e.enrolDevice(tok, hwidA)

	b := e.mustSync(a.Key)
	var got []string
	for _, p := range b.Servers[0].Paths {
		got = append(got, p.Protocol+"="+p.Label)
	}
	sort.Strings(got)
	if want := "awg=awg-profile hysteria2=Hysteria2 fast hysteria2=p"; strings.Join(got, " ") != want {
		t.Errorf("labels: %v, want %s", got, want)
	}
	for _, p := range b.Servers[0].Paths {
		if strings.Contains(p.Label, "de1") || strings.Contains(p.Label, "DE") || strings.Contains(strings.ToLower(p.Label), "via") {
			t.Errorf("the label %q holds the node, the country or the way", p.Label)
		}
	}
	assertNoInternals(t, "labels", b.raw)

	// The owner renames the profile: the label and the ETag follow.
	if _, err := e.st.W.Exec(`UPDATE profile SET name = ? WHERE id = ?`, "Hysteria2 turbo", fast.Id); err != nil {
		t.Fatal(err)
	}
	c := e.mustSync(a.Key)
	if !strings.Contains(c.raw, `"label":"Hysteria2 turbo"`) || c.etg == b.etg {
		t.Errorf("after the rename: %s", c.raw)
	}
}

// The public label of a node is the owner's text for people: on the server when it is set, nowhere when it is not; the node's own
// name is never sent, and the answer follows the label.
func TestSyncServerCarriesThePublicLabel(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice-liddell", nil)
	a := e.enrolDevice(tok, hwidA)
	serverKeys := func(b syncBody) map[string]any {
		t.Helper()
		var doc struct{ Servers []map[string]any }
		if err := json.Unmarshal([]byte(b.raw), &doc); err != nil || len(doc.Servers) != 1 {
			t.Fatalf("servers: %v %s", err, b.raw)
		}
		return doc.Servers[0]
	}

	plain := e.mustSync(a.Key)
	if _, ok := serverKeys(plain)["label"]; ok || plain.Servers[0].Label != "" {
		t.Fatalf("a node with no public label has a label: %s", plain.raw)
	}

	if _, err := e.st.W.Exec(`UPDATE node SET public_label = ? WHERE id = 'nod_1'`, "  Fast route "); err != nil {
		t.Fatal(err)
	}
	named := e.mustSync(a.Key)
	if named.Servers[0].Label != "Fast route" || named.Servers[0].Country != "DE" || named.Servers[0].City != "Frankfurt" {
		t.Errorf("server = %+v", named.Servers[0])
	}
	if named.etg == plain.etg {
		t.Error("the ETag did not follow the public label")
	}
	assertNoInternals(t, "label", named.raw)
	if keys := serverKeys(named); keys["label"] != "Fast route" {
		t.Errorf("server keys: %v", keys)
	}

	// The label of the node is not the label of a path: the path keeps the profile's name.
	if p := named.Servers[0].Paths[0]; p.Label != "p" {
		t.Errorf("path label = %q", p.Label)
	}

	if _, err := e.st.W.Exec(`UPDATE node SET public_label = '' WHERE id = 'nod_1'`); err != nil {
		t.Fatal(err)
	}
	if back := e.mustSync(a.Key); back.raw != plain.raw {
		t.Errorf("a cleared label left a trace: %s | %s", plain.raw, back.raw)
	}
}

// The twins of a Telegram link say what they are, as the paths of a server do: via, via_country (only when the relay's entry has a
// country) and ipv6. The page lists the same three links.
func TestSyncTelegramTwinsAreTold(t *testing.T) {
	e := newSyncEnv(t, nil)
	group, _ := e.extras()
	if _, err := e.st.W.Exec(`UPDATE node SET ipv6_entry = '2001:db8::5' WHERE id = 'nod_1'`); err != nil {
		t.Fatal(err)
	}
	var mt string
	if err := e.st.R.QueryRow(`SELECT i.id FROM inbound i JOIN profile p ON p.id = i.profile_id WHERE p.protocol = 'mtproto'`).Scan(&mt); err != nil {
		t.Fatal(err)
	}
	e.addRelay(mt, "FI", "tcp")
	_, tok := e.personIn("alice-liddell", group, true, true)
	a := e.enrolDevice(tok, hwidA)

	b := e.mustSync(a.Key)
	var got []string
	for _, l := range b.Telegram {
		u, err := url.Parse(l.Link)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s/%s/%t/%s", l.Via, l.ViaCountry, l.IPv6, u.Query().Get("server")))
	}
	if want := "direct//false/de1.example.com direct//true/2001:db8::5 relay/FI/false/fi1.example.com"; strings.Join(got, " ") != want {
		t.Errorf("telegram: %v, want %s", got, want)
	}
	for _, l := range b.Telegram {
		if l.ServerID != b.Servers[0].ID {
			t.Errorf("a twin has another server id: %+v", l)
		}
	}
	assertNoInternals(t, "telegram twins", b.raw)
	page, _ := pageData(t, fetch(e.h, "/"+tok, chrome).Body.String())
	if links := page["telegram"].(map[string]any)["links"].([]any); len(links) != 3 {
		t.Errorf("the page lists %d links, want 3", len(links))
	}

	// A relay whose node has no country has no via_country, and the answer follows.
	if _, err := e.st.W.Exec(`UPDATE node SET country_code = '' WHERE id = 'nod_2'`); err != nil {
		t.Fatal(err)
	}
	c := e.mustSync(a.Key)
	if r := c.Telegram[2]; r.Via != "relay" || r.ViaCountry != "" || c.etg == b.etg {
		t.Errorf("relay with no country: %+v (etag changed: %v)", r, c.etg != b.etg)
	}
}
