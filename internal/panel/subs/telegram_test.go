package subs_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/subs"
)

// The Telegram proxy links of a person are no part of any subscription: Happ (a base64 list of URIs) and every other app of
// the link would list a tg:// line as a server. They live on the person's own page only.

type tgRig struct {
	*rig
	group, profile string
}

// newTGRig is the rig with an MTProto profile on its node, in a group that also holds the Hysteria2 profile.
func newTGRig(t *testing.T) *tgRig {
	t.Helper()
	r := newRig(t, "/k3xq8")
	p := must(r.svc.CreateProfile(r.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{
		Protocol: "mtproto", Name: "tg 443", SettingsJson: `{"port":8443}`,
	}))).Msg.Profile
	must(r.svc.CreateInbound(r.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: p.Id, NodeId: "nod_1"})))
	g := must(r.svc.CreateGroup(r.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "both", ProfileIds: []string{r.profile, p.Id}}))).Msg.Group.Id
	return &tgRig{rig: r, group: g, profile: p.Id}
}

func (m *tgRig) person(name string, telegram bool) (id, token string) {
	return m.user(name, func(c *adminv1.CreateUserRequest) {
		c.GroupId = m.group
		c.Apps = &adminv1.AppToggles{Happ: true, Amnezia: true, Telegram: telegram}
	})
}

func TestTelegramLinksAreInNoSubscription(t *testing.T) {
	m := newTGRig(t)
	h, _ := m.handler(func(c *subs.Config) { c.MinInterval = -1 }) // no cached view: every read is a fresh one
	_, tok := m.person("alice", true)

	for _, c := range []struct{ name, path, ua string }{
		{"base64 list, Happ", "/" + tok, happUA},
		{"base64 list, no app", "/" + tok, curlUA},
		{"Happ Xray JSON", "/" + tok + "?format=xray-json", happUA},
		{"Mihomo", "/" + tok, mihomoUA},
		{"sing-box", "/" + tok, sfaUA},
		{"v2rayNG", "/" + tok, v2rayNGUA},
	} {
		rec := fetch(h, c.path, c.ua)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.name, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body)); err == nil {
			body += string(dec)
		}
		if strings.Contains(body, "tg://") || strings.Contains(body, "tg%3A") || strings.Contains(strings.ToLower(body), "mtproto") {
			t.Errorf("%s: the Telegram link is in the subscription:\n%s", c.name, body)
		}
	}
	if lines := strings.Split(decode(t, fetch(h, "/"+tok, happUA).Body.String()), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], "hysteria2://") {
		t.Errorf("the Happ list = %q, want the Hysteria2 server alone", lines)
	}
}

func TestTelegramLinksAreOnTheUserPageNotInTheServerCount(t *testing.T) {
	m := newTGRig(t)
	h, _ := m.handler(func(c *subs.Config) { c.MinInterval = -1 }) // no cached view: every read is a fresh one
	_, alice := m.person("alice", true)
	_, bob := m.person("bob", false)
	fetch(h, "/"+alice, happUA)
	fetch(h, "/"+bob, happUA)

	pa, _ := pageData(t, fetch(h, "/"+alice, chrome).Body.String())
	pb, _ := pageData(t, fetch(h, "/"+bob, chrome).Body.String())
	if pa["server_count"] != pb["server_count"] || pa["server_count"] != float64(1) {
		t.Errorf("server_count: with Telegram %v, without %v, want 1 in both", pa["server_count"], pb["server_count"])
	}
	if got := len(pa["servers"].([]any)); got != len(pb["servers"].([]any)) {
		t.Errorf("servers: %d with Telegram, %d without", got, len(pb["servers"].([]any)))
	}
	for _, s := range pa["servers"].([]any) {
		for _, c := range s.(map[string]any)["connections"].([]any) {
			if c.(map[string]any)["way"] == "link" && strings.Contains(c.(map[string]any)["app_name"].(string), "Telegram") {
				t.Errorf("a Telegram link is a connection of a server: %v", c)
			}
		}
	}
	tg, _ := pa["telegram"].(map[string]any)
	links, _ := tg["links"].([]any)
	if len(links) != 1 {
		t.Fatalf("the page carries %v, want one tg://proxy link", tg)
	}
	u, err := url.Parse(links[0].(map[string]any)["url"].(string))
	if err != nil || u.Scheme != "tg" || u.Host != "proxy" || u.Query().Get("server") != "de1.example.com" || u.Query().Get("port") != "8443" ||
		!strings.HasPrefix(u.Query().Get("secret"), "ee") {
		t.Errorf("link = %v, %v", links[0], err)
	}
	if pb["telegram"] != nil {
		t.Errorf("a person without Telegram has Telegram data on the page: %v", pb["telegram"])
	}
}

// The key in the link of the page is the one the node is told to accept.
func TestTelegramPageLinkIsTheKeyOfTheNode(t *testing.T) {
	m := newTGRig(t)
	h, _ := m.handler(func(c *subs.Config) { c.MinInterval = -1 }) // no cached view: every read is a fresh one
	id, tok := m.person("alice", true)
	d, _ := pageData(t, fetch(h, "/"+tok, chrome).Body.String())
	links := d["telegram"].(map[string]any)["links"].([]any)
	if len(links) != 1 {
		t.Fatalf("links = %v", links)
	}
	u, err := url.Parse(links[0].(map[string]any)["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	secret := u.Query().Get("secret")
	var told []string
	for _, in := range must(m.svc.Desired(m.ctx, "nod_1")) {
		for _, c := range in.Creds {
			if in.Spec.Protocol == "mtproto" && c.UserID == id {
				told = append(told, string(c.Data))
			}
		}
	}
	if len(told) != 1 || !strings.Contains(told[0], secret[2:34]) {
		t.Errorf("the page has the secret %q, the node is told %v", secret, told)
	}
	// Reading the page again changes nothing: same link.
	d2, _ := pageData(t, fetch(h, "/"+tok, chrome).Body.String())
	if d2["telegram"].(map[string]any)["links"].([]any)[0].(map[string]any)["url"] != links[0].(map[string]any)["url"] {
		t.Error("the link changed between two reads")
	}
	// Telegram on, key revoked: the page has the section and no link (no throwaway secret).
	must(m.svc.RevokeTelegramKey(m.ctx, connect.NewRequest(&adminv1.RevokeTelegramKeyRequest{UserId: id})))
	d3, _ := pageData(t, fetch(h, "/"+tok, chrome).Body.String())
	if l := d3["telegram"].(map[string]any)["links"].([]any); len(l) != 0 {
		t.Errorf("links without a key: %v", l)
	}
}

// A person without Telegram gets what they got before MTProto existed: adding an MTProto profile to their group changes no byte
// of the list, the Xray, sing-box and Mihomo profiles or the servers of the page. (The goldens of the other tests pin what
// those bytes are on main.)
func TestNoTelegramMeansTheSubscriptionsOfMain(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, _ := r.handler(func(c *subs.Config) { c.Routing, c.MinInterval = subs.HappRouting(dns.New(r.st)), -1 })
	_, bob := r.user("bob", nil)
	type probe struct{ name, query, ua string }
	probes := []probe{
		{"base64 list", "", happUA}, {"plain client", "", curlUA}, {"xray json", "?format=xray-json", happUA}, {"sing-box", "", sfaUA},
		{"v2rayNG", "", v2rayNGUA}, {"Mihomo", "", mihomoUA},
	}
	take := func() map[string]string {
		out := map[string]string{}
		for _, c := range probes {
			rec := fetch(h, "/"+bob+c.query, c.ua)
			out[c.name] = fmt.Sprint(rec.Code, rec.Header().Get("Subscription-Userinfo"), rec.Header().Get("Content-Type"), "\n", rec.Body.String())
		}
		d, _ := pageData(t, fetch(h, "/"+bob, chrome).Body.String())
		for _, k := range []string{"server_count", "servers", "server_loads", "devices", "access", "apps", "user"} {
			out["page "+k] = string(mustJSON(d[k]))
		}
		return out
	}
	before := take()

	p := must(r.svc.CreateProfile(r.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "mtproto", Name: "tg 443", SettingsJson: `{"port":8443}`}))).Msg.Profile
	must(r.svc.CreateInbound(r.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: p.Id, NodeId: "nod_1"})))
	must(r.svc.UpdateGroup(r.ctx, connect.NewRequest(&adminv1.UpdateGroupRequest{GroupId: r.group, ProfileIds: &adminv1.ProfileIds{Values: []string{r.profile, p.Id}}})))
	after := take()

	for name, was := range before {
		if after[name] != was {
			t.Errorf("%s changed:\n%s\n--- after adding an MTProto profile to the group:\n%s", name, was, after[name])
		}
	}
	if !strings.Contains(before["base64 list"], "200") || len(before) < 10 {
		t.Errorf("the probes did not run: %v", before)
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// The public page names a Telegram link as it names a server: by country and place, with the mark of a twin, in the language of
// the page. The panel's node and profile names are for the owner only.
func TestTelegramPageLinksCarryNoInternalNames(t *testing.T) {
	m := newTGRig(t)
	h, _ := m.handler(func(c *subs.Config) { c.MinInterval = -1 })
	for _, q := range []string{
		`UPDATE node SET name = 'fra-edge-07', country_code = 'DE', location = 'Frankfurt', ipv6_entry = '2001:db8::5' WHERE id = 'nod_1'`,
		`INSERT INTO node (id, name, address, country_code, state, agent_caps, created_at) VALUES ('nod_2', 'hel-edge-02', 'fi1.example.com', 'FI', 'active', 'relay-forward/1', 2)`,
		`UPDATE profile SET name = 'private-profile-name' WHERE id = ?`,
	} {
		args := []any{}
		if strings.Contains(q, "?") {
			args = append(args, m.profile)
		}
		if _, err := m.st.W.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	var inbound string
	if err := m.st.R.QueryRow(`SELECT id FROM inbound WHERE profile_id = ?`, m.profile).Scan(&inbound); err != nil {
		t.Fatal(err)
	}
	if _, err := m.st.W.Exec(`INSERT INTO relay (id, inbound_id, via_node_id, network, port, enabled, state, check_json, created_at, updated_at)
		VALUES ('rly_1', ?, 'nod_2', 'tcp', 9443, 1, 'active', ?, 1, 1)`, inbound, `{"at":1,"leg":{"status":"ok"},"path":{"status":"ok"},"ok":true}`); err != nil {
		t.Fatal(err)
	}
	_, tok := m.person("alice", true)

	for _, c := range []struct {
		lang   string
		labels []string
	}{
		{"en", []string{"Germany · Frankfurt", "Germany · Frankfurt · IPv6", "Germany · Frankfurt · via FI"}},
		{"ru", []string{"Германия · Frankfurt", "Германия · Frankfurt · IPv6", "Германия · Frankfurt · через FI"}},
		{"zh", []string{"德国 · Frankfurt", "德国 · Frankfurt · IPv6", "德国 · Frankfurt · 经 FI"}},
	} {
		d, raw := pageData(t, fetch(h, "/"+tok, chrome, "Accept-Language", c.lang).Body.String())
		for _, name := range []string{"fra-edge-07", "hel-edge-02", "private-profile-name", "tg 443"} {
			if strings.Contains(raw, name) {
				t.Errorf("%s: the page data holds the internal name %q", c.lang, name)
			}
		}
		links := d["telegram"].(map[string]any)["links"].([]any)
		var got []string
		for _, l := range links {
			got = append(got, l.(map[string]any)["label"].(string))
		}
		if strings.Join(got, "|") != strings.Join(c.labels, "|") {
			t.Errorf("%s: labels %q, want %q", c.lang, got, c.labels)
		}
		if len(links) == 3 {
			v6, relay := links[1].(map[string]any), links[2].(map[string]any)
			if v6["ipv6"] != true || relay["relay"] != true || relay["via"] != "FI" || !strings.Contains(v6["url"].(string), "server=2001") || !strings.Contains(relay["url"].(string), "fi1.example.com") {
				t.Errorf("%s: twins %v %v", c.lang, v6, relay)
			}
		}
	}
}

// The page lists the Telegram key among the devices, marked as such, and counts it nowhere: devices_used is the places the
// limit fills.
func TestTelegramKeyIsListedButNotCountedOnThePage(t *testing.T) {
	m := newTGRig(t)
	h, _ := m.handler(func(c *subs.Config) { c.MinInterval = -1 })
	_, tok := m.person("alice", true)
	fetch(h, "/"+tok, happUA)
	d, _ := pageData(t, fetch(h, "/"+tok, chrome).Body.String())
	user := d["user"].(map[string]any)
	devices := d["devices"].([]any)
	var kinds []string
	for _, x := range devices {
		dev := x.(map[string]any)
		kinds = append(kinds, dev["kind"].(string))
		if dev["kind"] == "telegram" && (dev["can_remove"] != false || dev["can_rename"] != false || dev["app"] != "telegram") {
			t.Errorf("the key device on the page: %v", dev)
		}
	}
	slices.Sort(kinds)
	if user["devices_used"] != float64(1) || !slices.Equal(kinds, []string{"shared", "telegram"}) {
		t.Errorf("devices_used %v, kinds %v", user["devices_used"], kinds)
	}
}
