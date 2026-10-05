package subs_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The servers of the page and the DNS of each of them, and POST <link>/dns: the contract the new page is built against
// (DATA.md part 2), against the real access module and database. Two nodes, each with a Hysteria2 and an AmneziaWG profile.

const (
	adblockPair = "94.140.14.14, 94.140.15.15"
	familyPair  = "94.140.14.15, 94.140.15.16"
	// The panel's own names of the nodes are unlike anything else in the page data, so that a leak is found by a plain search.
	de1Name, nl1Name = "inner-de1", "inner-nl1"
)

// liveFleet is the fleet as the access module sees it: which agents hold a session, and a fresh network sample for the nodes
// with an rx rate.
type liveFleet struct {
	agents map[string]bool
	rx     map[string]uint64
}

func (liveFleet) OnlineUsers() map[string]string { return nil }
func (l liveFleet) AgentConnected(nodeID string) bool {
	return l.agents[nodeID]
}
func (l liveFleet) NetworkUsage(nodeID string) (uint64, uint64, time.Time, bool) {
	rx, ok := l.rx[nodeID]
	return rx, 0, time.Now(), ok
}

type dnsRig struct {
	*m3rig
	h     http.Handler
	cache *subsettings.Cache
}

// newDNSRig: de1 (Germany, Frankfurt, 100 Mbit/s at 85 %, its agent answers) and nl1 (Netherlands, no capacity, no agent),
// both running the Hysteria2 profile and the AmneziaWG one; the choice of DNS is on. de1 offers AdGuard (the default),
// Standard and Family; nl1 offers Yandex (the default) and Standard.
func newDNSRig(t *testing.T, mut func(*subs.Config)) *dnsRig {
	t.Helper()
	m := newM3RigOnline(t, liveFleet{agents: map[string]bool{"nod_1": true}, rx: map[string]uint64{"nod_1": 85_000_000}})
	m.st.W.Exec(`UPDATE node SET name = ?, country_code = 'DE', location = 'Frankfurt', bandwidth_mbps = 100 WHERE id = 'nod_1'`, de1Name)
	m.st.W.Exec(`INSERT INTO node (id, name, address, country_code, state, created_at) VALUES ('nod_2', ?, 'nl1.example.com', 'NL', 'active', 1)`, nl1Name)
	for _, p := range []string{m.profile, m.awg} {
		must(m.svc.CreateInbound(m.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: p, NodeId: "nod_2"})))
	}
	d := m.st.DNS()
	must(0, d.SetNodeOptions(m.ctx, "nod_1", []string{"dns_builtin_adblock", "dns_builtin_standard", "dns_builtin_family"}, "dns_builtin_adblock"))
	must(0, d.SetNodeOptions(m.ctx, "nod_2", []string{"dns_builtin_yandex", "dns_builtin_standard"}, "dns_builtin_yandex"))
	h, cache := m.handler(func(c *subs.Config) {
		c.MinInterval = 10 * time.Second // the cache the pick has to drop
		if mut != nil {
			mut(c)
		}
	})
	r := &dnsRig{m3rig: m, h: h, cache: cache}
	r.choice(true)
	return r
}

// choice switches the owner's "DNS choice on the page".
func (r *dnsRig) choice(on bool) {
	r.t.Helper()
	set := subsettings.Defaults()
	if on {
		set.UserPage.AllowDnsChoice = proto.Bool(true)
	}
	if _, err := r.cache.Update(r.ctx, set); err != nil {
		r.t.Fatal(err)
	}
}

func (r *dnsRig) page(tok string, hdr ...string) (map[string]any, string) {
	r.t.Helper()
	return pageData(r.t, fetch(r.h, "/"+tok, chrome, hdr...).Body.String())
}

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func arr(v any) []any          { a, _ := v.([]any); return a }

func strs(v any) []string {
	out := []string{}
	for _, x := range arr(v) {
		out = append(out, x.(string))
	}
	return out
}

func serverByID(d map[string]any, id string) map[string]any {
	for _, s := range arr(d["servers"]) {
		if obj(s)["id"] == id {
			return obj(s)
		}
	}
	return nil
}

func TestServersOnThePage(t *testing.T) {
	r := newDNSRig(t, nil)
	_, tok := r.newUser("alice", nil)
	d, raw := r.page(tok, "Accept-Language", "en")

	if len(arr(d["servers"])) != 2 {
		t.Fatalf("servers = %v", d["servers"])
	}
	de, nl := serverByID(d, "nod_1"), serverByID(d, "nod_2")
	if de["country_code"] != "DE" || de["place"] != "Frankfurt" || de["label"] != "Germany · Frankfurt" || de["online"] != true || de["load"] != "high" {
		t.Errorf("de1 = %v", de)
	}
	if nl["country_code"] != "NL" || nl["place"] != "" || nl["label"] != "Netherlands" || nl["online"] != false || nl["load"] != nil {
		t.Errorf("nl1 = %v", nl)
	}
	// What the apps by link call it: the owner's template, without the load that Happ adds to its own names.
	if names := strs(de["app_names"]); len(names) != 1 || names[0] != "\U0001F1E9\U0001F1EA DE · p" {
		t.Errorf("app_names = %v", names)
	}
	var link, key map[string]any
	for _, c := range arr(de["connections"]) {
		switch obj(c)["way"] {
		case "link":
			link = obj(c)
		case "key":
			key = obj(c)
		}
	}
	if link == nil || link["exit"] != "direct" || link["app_name"] != "\U0001F1E9\U0001F1EA DE · p" || link["profile_id"] != nil ||
		key == nil || key["exit"] != "direct" || key["profile_id"] != r.awg || key["app_name"] != nil || len(arr(de["connections"])) != 2 {
		t.Errorf("connections = %v", de["connections"])
	}
	// The old list is derived from it: the servers that have a level, named the same.
	if loads := arr(d["server_loads"]); len(loads) != 1 || obj(loads[0])["name"] != "Germany · Frankfurt" || obj(loads[0])["level"] != "high" {
		t.Errorf("server_loads = %v", d["server_loads"])
	}
	if d["server_count"] != float64(2) {
		t.Errorf("server_count = %v", d["server_count"])
	}
	// No node name, no address and no rate anywhere in the data of the page.
	for _, leak := range []string{de1Name, nl1Name, "de1.example.com", "nl1.example.com", "85000000", "rx", "\"percent\"", "load_percent"} {
		if strings.Contains(raw, leak) {
			t.Errorf("the page data holds %q", leak)
		}
	}
	if m := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`).FindString(raw); m != "" {
		t.Errorf("the page data holds an IP address: %s", m)
	}

	// DNS of each server and the settings of the choice.
	dd := obj(de["dns"])
	if dd["choice"] != "" || dd["effective"] != "dns_builtin_adblock" || len(arr(dd["keys_to_refresh"])) != 0 ||
		strings.Join(strs(dd["options"]), ",") != "dns_builtin_adblock,dns_builtin_standard,dns_builtin_family" {
		t.Errorf("de1 dns = %v", dd)
	}
	if nd := obj(nl["dns"]); nd["effective"] != "dns_builtin_yandex" || len(arr(nd["options"])) != 2 {
		t.Errorf("nl1 dns = %v", nd)
	}
	dns := obj(d["dns"])
	if dns["enabled"] != true || dns["endpoint"] != "https://sub.example.com/k3xq8/"+tok+"/dns" || dns["refresh_hours"] != float64(12) ||
		obj(dns["link"])["per_server"] != false || obj(dns["link"])["effective"] != "dns_builtin_ru_split" {
		t.Errorf("dns = %v", dns)
	}
	// The presets that are offered or apply, with names and descriptions in the language of the page.
	byID := map[string]map[string]any{}
	for _, p := range arr(d["dns_presets"]) {
		byID[obj(p)["id"].(string)] = obj(p)
	}
	if len(byID) != 5 || byID["dns_builtin_adblock"]["name"] != "AdGuard: no ads" || byID["dns_builtin_adblock"]["description"] != "AdGuard DNS: no ads, no trackers" ||
		byID["dns_builtin_adblock"]["category"] != "no_ads" || byID["dns_builtin_family"]["category"] != "family" || byID["dns_builtin_yandex"]["category"] != "russia" ||
		byID["dns_builtin_standard"]["category"] != "regular" || byID["dns_builtin_ru_split"]["name"] != "Russia: .ru direct" {
		t.Errorf("dns_presets = %v", d["dns_presets"])
	}
	ru, _ := r.page(tok, "Accept-Language", "ru")
	for _, p := range arr(ru["dns_presets"]) {
		if obj(p)["id"] == "dns_builtin_adblock" && (obj(p)["name"] != "AdGuard: без рекламы" || obj(p)["description"] != "AdGuard DNS: без рекламы и трекеров") {
			t.Errorf("Russian preset = %v", p)
		}
	}
	if serverByID(ru, "nod_1")["label"] != "Германия · Frankfurt" {
		t.Errorf("Russian label = %v", serverByID(ru, "nod_1")["label"])
	}

	// The owner switched the choice off: the servers still say what DNS they have; nobody can pick.
	r.choice(false)
	d, _ = r.page(tok)
	if dns := obj(d["dns"]); dns["enabled"] != false || dns["endpoint"] != "" || obj(serverByID(d, "nod_1")["dns"])["effective"] != "dns_builtin_adblock" {
		t.Errorf("choice off: dns = %v", dns)
	}
}

// A server that no node offers anything on has no DNS row; with nothing offered anywhere there is nothing to name.
func TestServerWithoutDNSOptions(t *testing.T) {
	r := newDNSRig(t, nil)
	must(0, r.st.DNS().SetNodeOptions(r.ctx, "nod_2", nil, ""))
	must(0, r.st.DNS().SetNodeOptions(r.ctx, "nod_1", nil, ""))
	_, tok := r.newUser("alice", nil)
	d, _ := r.page(tok)
	for _, id := range []string{"nod_1", "nod_2"} {
		if s := serverByID(d, id); s == nil || s["dns"] != nil {
			t.Errorf("%s = %v", id, s)
		}
	}
	if len(arr(d["dns_presets"])) != 0 || obj(d["dns"])["link"].(map[string]any)["effective"] != "dns_builtin_ru_split" {
		t.Errorf("presets %v dns %v", d["dns_presets"], d["dns"])
	}
}

// The names the apps by link give a server are shown only when they cannot carry the node's name: not with {node} in the
// template, and never as the fallback of an empty name.
func TestAppNamesNeverCarryTheNodeName(t *testing.T) {
	r := newDNSRig(t, nil)
	_, tok := r.newUser("alice", nil)
	set := subsettings.Defaults()
	set.UserPage.AllowDnsChoice = proto.Bool(true)
	for name, c := range map[string]struct {
		tmpl  string
		names []string // for nl1, which has a country
	}{
		"node in the template": {"{flag} {node}", nil},
		"node and more":        {"{country} · {node} · {profile}", nil},
		"empty name":           {"{flag}", []string{"server"}}, // the name of nl1 would be empty: the fallback is not the node
	} {
		set.ServerNameTemplate = c.tmpl
		if _, err := r.cache.Update(r.ctx, set); err != nil {
			t.Fatal(err)
		}
		r.st.W.Exec(`UPDATE node SET country_code = '' WHERE id = 'nod_2'`)
		d, raw := r.page(tok)
		for _, id := range []string{"nod_1", "nod_2"} {
			s := serverByID(d, id)
			if strings.Contains(raw, nl1Name) || strings.Contains(raw, de1Name) {
				t.Fatalf("%s: the page data holds a node name: %s", name, raw)
			}
			if c.names == nil && (len(arr(s["app_names"])) != 0 || s["connections"] == nil) {
				t.Errorf("%s: %s app_names = %v", name, id, s["app_names"])
			}
			for _, cn := range arr(s["connections"]) {
				if c.names == nil && obj(cn)["app_name"] != nil {
					t.Errorf("%s: %s connection = %v", name, id, cn)
				}
			}
		}
		if c.names != nil {
			if got := strs(serverByID(d, "nod_2")["app_names"]); strings.Join(got, ",") != strings.Join(c.names, ",") {
				t.Errorf("%s: nl1 app_names = %v, want %v", name, got, c.names)
			}
		}
	}
}

// The owner's preview and a locked page have no endpoints to write to.
func TestPreviewAndLockedPageHaveNoEndpoints(t *testing.T) {
	r := newDNSRig(t, nil)
	id, _ := r.newUser("alice", nil)
	pv := subs.PreviewHandler(r.svc, subs.Config{Title: "T", Settings: r.cache, Dist: pageDist})
	rec := httptest.NewRecorder()
	pv(rec, httptest.NewRequest("GET", "/preview", nil), id)
	d, _ := pageData(t, rec.Body.String())
	if dns := obj(d["dns"]); dns["enabled"] != true || dns["endpoint"] != "" || obj(d["amnezia"])["endpoints"] != "" || len(arr(d["servers"])) != 2 {
		t.Errorf("preview: dns %v amnezia %v", d["dns"], d["amnezia"])
	}

	g := newGate(t, nil)
	_, tok := g.newUser("bob", nil)
	locked := dataOf(t, g.page(tok))
	if locked["locked"] != true || locked["servers"] == nil || len(arr(locked["servers"])) != 0 || locked["dns_presets"] == nil || len(arr(locked["dns_presets"])) != 0 ||
		obj(locked["dns"])["enabled"] != false || obj(locked["dns"])["endpoint"] != "" {
		t.Errorf("locked page: %v", locked)
	}
}

type dnsAnswer struct {
	Server       map[string]any `json:"server"`
	StaleDevices []string       `json:"stale_devices"`
	Error        string         `json:"error"`
}

func decodeDNS(t *testing.T, rec *httptest.ResponseRecorder) dnsAnswer {
	t.Helper()
	var a dnsAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatalf("not JSON (%d): %v %q", rec.Code, err, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", rec.Header())
	}
	return a
}

func pickBody(server, preset string) string {
	b, _ := json.Marshal(map[string]string{"server": server, "preset": preset})
	return string(b)
}

func awgProxyDNS(t *testing.T, h http.Handler, tok string) map[string]string {
	t.Helper()
	p := parseYAML(t, fetch(h, "/"+tok, mihomoUA).Body.Bytes())
	out := map[string]string{}
	n := 0
	for _, px := range p.Proxies {
		if px["type"] != "wireguard" {
			continue
		}
		var ips []string
		for _, ip := range px["dns"].([]any) {
			ips = append(ips, ip.(string))
		}
		out[px["server"].(string)] = strings.Join(ips, ", ")
		n++
	}
	if n != 2 {
		t.Fatalf("%d AmneziaWG proxies", n)
	}
	return out
}

func TestPickingADNS(t *testing.T) {
	r := newDNSRig(t, nil)
	uid, tok := r.newUser("alice", nil)
	c := call{r.h, t, tok}

	// A Mihomo client fetched the profile a moment ago: the cached view must not hide the pick from its next refresh.
	before := awgProxyDNS(t, r.h, tok)
	if before["de1.example.com"] != adblockPair || before["nl1.example.com"] != "77.88.8.8, 77.88.8.1" {
		t.Fatalf("node defaults in the Mihomo profile: %v", before)
	}
	mihomoBefore := parseYAML(t, fetch(r.h, "/"+tok, mihomoUA).Body.Bytes())

	add := decodeAnswer(t, c.post("/devices", r.addBody("phone")))
	dev := add.Device["id"].(string)
	if got := lineValue(add.Configs[0]["conf"].(string), "DNS"); got != adblockPair && got != "77.88.8.8, 77.88.8.1" {
		t.Errorf("a new key carries the DNS of its node: %q", got)
	}
	time.Sleep(5 * time.Millisecond) // a pick and a fetch are told apart by the millisecond

	rec := c.post("/dns", pickBody("nod_1", "dns_builtin_family"))
	if rec.Code != 200 {
		t.Fatalf("pick: %d %s", rec.Code, rec.Body.String())
	}
	ans := decodeDNS(t, rec)
	dd := obj(ans.Server["dns"])
	if ans.Server["id"] != "nod_1" || ans.Server["label"] != "Germany · Frankfurt" || dd["choice"] != "dns_builtin_family" || dd["effective"] != "dns_builtin_family" ||
		strings.Join(strs(dd["keys_to_refresh"]), ",") != dev || len(ans.StaleDevices) != 1 || ans.StaleDevices[0] != dev {
		t.Errorf("answer = %+v", ans)
	}
	if strings.Contains(rec.Body.String(), de1Name) || strings.Contains(rec.Body.String(), "de1.example.com") {
		t.Errorf("the answer carries a node name: %s", rec.Body.String())
	}

	// The page lists the key as stale because of the DNS, and the server as having a key to refresh.
	d, _ := r.page(tok)
	k := obj(arr(obj(d["amnezia"])["devices"])[0])
	if k["stale"] != true || k["stale_reason"] != "dns" || obj(serverByID(d, "nod_1")["dns"])["choice"] != "dns_builtin_family" {
		t.Errorf("key after the pick: %v", k)
	}
	// The cached Mihomo view was dropped: the AmneziaWG proxy of de1 has the new DNS at once; Hysteria2's DNS did not move.
	after := awgProxyDNS(t, r.h, tok)
	if after["de1.example.com"] != familyPair || after["nl1.example.com"] != before["nl1.example.com"] {
		t.Errorf("Mihomo AmneziaWG proxies after the pick: %v", after)
	}
	mihomoAfter := parseYAML(t, fetch(r.h, "/"+tok, mihomoUA).Body.Bytes())
	if strings.Join(anyStrings(mihomoAfter.DNS["nameserver"]), ",") != strings.Join(anyStrings(mihomoBefore.DNS["nameserver"]), ",") {
		t.Errorf("a pick moved the DNS of Hysteria2: %v -> %v", mihomoBefore.DNS["nameserver"], mihomoAfter.DNS["nameserver"])
	}
	// Fetching the key again carries the new DNS and clears the mark.
	got := decodeAnswer(t, c.post("/devices/"+dev+"/configs", ""))
	if got.Device["stale"] != false || got.Device["stale_reason"] != "" {
		t.Errorf("device after the fetch: %v", got.Device)
	}
	for _, cfg := range got.Configs {
		want := map[string]string{"nod_1": familyPair, "nod_2": "77.88.8.8, 77.88.8.1"}[cfg["node_id"].(string)]
		if lineValue(cfg["conf"].(string), "DNS") != want {
			t.Errorf("%v: DNS = %q, want %q", cfg["node_id"], lineValue(cfg["conf"].(string), "DNS"), want)
		}
	}
	if d, _ := r.page(tok); obj(arr(obj(d["amnezia"])["devices"])[0])["stale"] != false || len(arr(obj(serverByID(d, "nod_1")["dns"])["keys_to_refresh"])) != 0 {
		t.Errorf("the page still says the key is stale")
	}

	// "" = back to the node's default.
	rec = c.post("/dns", pickBody("nod_1", ""))
	if dd := obj(decodeDNS(t, rec).Server["dns"]); rec.Code != 200 || dd["choice"] != "" || dd["effective"] != "dns_builtin_adblock" {
		t.Errorf("back to the default: %d %v", rec.Code, dd)
	}

	// The audit row: the person, the node and the preset; no link, no address, no key.
	var actor, params, ip string
	if err := r.st.R.QueryRow(`SELECT actor, params, ip FROM audit WHERE action = 'page_dns_choice' ORDER BY id LIMIT 1`).Scan(&actor, &params, &ip); err != nil {
		t.Fatal(err)
	}
	if actor != "user:"+uid || ip != "" || !strings.Contains(params, `"user":"alice"`) || !strings.Contains(params, `"node":"`+de1Name+`"`) ||
		!strings.Contains(params, `"preset":"dns_builtin_family"`) || strings.Contains(params, tok) {
		t.Errorf("audit: %q %q %q", actor, params, ip)
	}
	var n int
	r.st.R.QueryRow(`SELECT count(*) FROM audit WHERE action = 'page_dns_choice'`).Scan(&n)
	if n != 2 {
		t.Errorf("%d audit rows", n)
	}
}

func anyStrings(v any) []string {
	var out []string
	for _, x := range arr(v) {
		out = append(out, x.(string))
	}
	return out
}

// Every refusal of POST <link>/dns, in the order the chain gives them.
func TestPickingADNSRefusals(t *testing.T) {
	r := newDNSRig(t, func(c *subs.Config) { c.MaxWritesPerHour = -1 })
	uid, tok := r.newUser("alice", nil)
	c := call{r.h, t, tok}
	code := func(rec *httptest.ResponseRecorder) (int, string) { return rec.Code, decodeDNS(t, rec).Error }
	want := func(name string, rec *httptest.ResponseRecorder, status int, errCode string) {
		t.Helper()
		if s, e := code(rec); s != status || e != errCode {
			t.Errorf("%s: %d %q, want %d %q (%s)", name, s, e, status, errCode, rec.Body.String())
		}
	}
	good := pickBody("nod_1", "dns_builtin_standard")

	// The body: small JSON of exactly these fields.
	for name, b := range map[string]struct {
		ct, body string
		status   int
		err      string
	}{
		"form":          {"application/x-www-form-urlencoded", good, 415, "unsupported_media_type"},
		"no type":       {"", good, 415, "unsupported_media_type"},
		"unknown field": {"application/json", `{"server":"nod_1","preset":"dns_builtin_standard","admin":true}`, 400, "bad_request"},
		"trailing data": {"application/json", good + good, 400, "bad_request"},
		"not json":      {"application/json", `{server`, 400, "bad_request"},
		"not an object": {"application/json", `["nod_1"]`, 400, "bad_request"},
		"no server":     {"application/json", `{"preset":"dns_builtin_standard"}`, 400, "bad_request"},
		"empty body":    {"application/json", ``, 400, "bad_request"},
		"too long":      {"application/json", `{"server":"` + strings.Repeat("x", 5000) + `"}`, 413, "too_large"},
	} {
		req := httptest.NewRequest("POST", "/"+tok+"/dns", strings.NewReader(b.body))
		if b.ct != "" {
			req.Header.Set("Content-Type", b.ct)
		}
		rec := httptest.NewRecorder()
		r.h.ServeHTTP(rec, req)
		want(name, rec, b.status, b.err)
	}
	// The server and the preset.
	want("a server that is not theirs", c.post("/dns", pickBody("nod_nope", "dns_builtin_standard")), 404, "not_found")
	want("a preset the node does not offer", c.post("/dns", pickBody("nod_1", "dns_builtin_quad9")), 409, "not_allowed")
	want("a preset that does not exist", c.post("/dns", pickBody("nod_1", "dns_nope")), 409, "not_allowed")
	want("a preset of another node", c.post("/dns", pickBody("nod_1", "dns_builtin_yandex")), 409, "not_allowed")
	must(0, r.st.DNS().SetNodeOptions(r.ctx, "nod_2", nil, ""))
	want("a node that offers nothing", c.post("/dns", pickBody("nod_2", "dns_builtin_standard")), 409, "not_allowed")
	only := call{r.h, t, func() string {
		_, tk := r.newUser("bob", func(m *adminv1.CreateUserRequest) { m.Nodes = &adminv1.NodeSelection{NodeIds: []string{"nod_2"}} })
		return tk
	}()}
	want("a node outside their selection", only.post("/dns", pickBody("nod_1", "dns_builtin_standard")), 404, "not_found")
	if n := r.countRows(`SELECT count(*) FROM user_node_dns`); n != 0 {
		t.Fatalf("refused picks were stored: %d", n)
	}
	want("accepted", c.post("/dns", good), 200, "")

	// Not a route of the page: only POST to exactly /dns under a token; anything else is the decoy.
	for _, m := range []string{"GET", "PUT", "DELETE"} {
		rec := httptest.NewRecorder()
		r.h.ServeHTTP(rec, httptest.NewRequest(m, "/"+tok+"/dns", nil))
		if rec.Code != 404 || rec.Body.String() != decoyBody {
			t.Errorf("%s: %d %q", m, rec.Code, rec.Body.String())
		}
	}
	for _, p := range []string{"/dnsx", "/dns/x", "/dns/"} {
		rec := httptest.NewRecorder()
		r.h.ServeHTTP(rec, httptest.NewRequest("POST", "/"+tok+p, strings.NewReader(good)))
		if rec.Code != 404 || rec.Body.String() != decoyBody {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Body.String())
		}
	}
	// The switch of the owner, and where it sits in the chain: after the cross-origin check, before the user's status.
	r.choice(false)
	want("choice off", c.post("/dns", good), 403, "dns_disabled")
	want("cross-origin beats the switch", c.post("/dns", good, "Sec-Fetch-Site", "cross-site"), 403, "cross_origin")
	must(r.svc.SetUsersEnabled(r.ctx, connect.NewRequest(&adminv1.SetUsersEnabledRequest{UserIds: []string{uid}, Enabled: false})))
	want("the switch beats the status", c.post("/dns", good), 403, "dns_disabled")
	r.choice(true)
	want("inactive", c.post("/dns", good), 409, "user_inactive")
	want("cross-origin beats the status", c.post("/dns", good, "Sec-Fetch-Site", "same-site"), 403, "cross_origin")
}

func (r *dnsRig) countRows(q string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// The same start as the device calls, in the same order: the page password, then the token (unknown = the decoy and a miss),
// the cross-origin check, the switch, the status, the budget (shared with the devices), and only then the body.
func TestPickingADNSSharesTheChainOfTheDeviceCalls(t *testing.T) {
	// 1. A locked page locks the pick, for a token that exists and for one that does not (the same answer).
	g := newGate(t, nil)
	set := subsettings.Defaults()
	set.UserPage.AllowDnsChoice = proto.Bool(true)
	if _, err := g.cache.Update(g.ctx, set); err != nil {
		t.Fatal(err)
	}
	uid, tok := g.newUser("alice", nil)
	for name, token := range map[string]string{"a token": tok, "no such token": strings.Repeat("z", 43)} {
		rec := call{g.h, t, token}.post("/dns", pickBody("nod_1", ""))
		if rec.Code != 401 || decodeDNS(t, rec).Error != "locked" {
			t.Errorf("%s without the cookie: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	cookie := cookieOf(t, g.unlock(t, tok, g.password(uid)))
	if rec := (call{g.h, t, tok}).post("/dns", pickBody("nod_1", ""), "Cookie", ck(cookie)); rec.Code != 200 {
		t.Errorf("with the cookie: %d %s", rec.Code, rec.Body.String())
	}

	// 2. An unknown token is the decoy and a miss; after enough of them the client network gets only the decoy.
	m := newDNSRig(t, func(c *subs.Config) {
		c.MissLimit, c.MissWindow, c.BlockFor = 3, time.Minute, time.Hour
		c.ClientIP = func(*http.Request) netip.Addr { return netip.MustParseAddr("198.51.100.9") }
	})
	_, tok2 := m.newUser("bob", nil)
	for i := 0; i < 3; i++ {
		rec := call{m.h, t, strings.Repeat(string(rune('a'+i)), 43)}.post("/dns", pickBody("nod_1", ""))
		if rec.Code != 404 || rec.Body.String() != decoyBody {
			t.Fatalf("unknown token %d: %d %q", i, rec.Code, rec.Body.String())
		}
	}
	if rec := (call{m.h, t, tok2}).post("/dns", pickBody("nod_1", "")); rec.Code != 404 || rec.Body.String() != decoyBody {
		t.Errorf("blocked client, valid token: %d %q", rec.Code, rec.Body.String())
	}

	// 3. The budget of writes is one for the devices and the DNS: 3 an hour here. A refusal that comes before it costs
	// nothing; one that comes after it (a server that is not theirs) costs a write, as a device call that fails does.
	clock := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	b := newDNSRig(t, func(c *subs.Config) { c.MaxWritesPerHour, c.Now = 3, func() time.Time { return clock } })
	uid3, tok3 := b.newUser("carol", func(c *adminv1.CreateUserRequest) { c.DeviceLimit = 50 })
	c := call{b.h, t, tok3}
	for i := 0; i < 5; i++ { // never reach the budget
		if rec := c.post("/dns", pickBody("nod_1", ""), "Sec-Fetch-Site", "cross-site"); rec.Code != 403 {
			t.Fatalf("cross-origin %d: %d", i, rec.Code)
		}
	}
	if rec := c.post("/devices", b.addBody("one")); rec.Code != 200 {
		t.Fatalf("device write: %d", rec.Code)
	}
	if rec := c.post("/dns", pickBody("nod_1", "dns_builtin_standard")); rec.Code != 200 {
		t.Fatalf("dns write: %d %s", rec.Code, rec.Body.String())
	}
	if rec := c.post("/dns", pickBody("nod_nope", "")); rec.Code != 404 { // after the budget: counts as the third
		t.Fatalf("not found: %d", rec.Code)
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"dns":     c.post("/dns", pickBody("nod_1", "dns_builtin_standard")),
		"devices": c.post("/devices", b.addBody("two")),
	} {
		if rec.Code != 429 || rec.Header().Get("Retry-After") != "3600" || decodeDNS(t, rec).Error != "too_many_requests" {
			t.Errorf("%s over the budget: %d %v %s", name, rec.Code, rec.Header(), rec.Body.String())
		}
	}
	// The status comes before the budget: a user who lost access is told so, not "too many".
	must(b.svc.SetUsersEnabled(b.ctx, connect.NewRequest(&adminv1.SetUsersEnabledRequest{UserIds: []string{uid3}, Enabled: false})))
	if rec := c.post("/dns", pickBody("nod_1", "")); rec.Code != 409 || decodeDNS(t, rec).Error != "user_inactive" {
		t.Errorf("inactive over the budget: %d %s", rec.Code, rec.Body.String())
	}
	// A new hour restores the budget.
	must(b.svc.SetUsersEnabled(b.ctx, connect.NewRequest(&adminv1.SetUsersEnabledRequest{UserIds: []string{uid3}, Enabled: true})))
	clock = clock.Add(61 * time.Minute)
	if rec := c.post("/dns", pickBody("nod_1", "")); rec.Code != 200 {
		t.Errorf("after an hour: %d %s", rec.Code, rec.Body.String())
	}
}

// A person whose subscription ended still sees the keys they hold and can rename and remove them; nothing can be added, no
// key is shown, and there are no servers, no DNS and no endpoint to pick on.
func TestInactiveUserKeepsTheirKeysOnThePage(t *testing.T) {
	r := newDNSRig(t, nil)
	uid, tok := r.newUser("alice", nil)
	c := call{r.h, t, tok}
	dev := decodeAnswer(t, c.post("/devices", r.addBody("phone"))).Device["id"].(string)
	must(r.svc.SetUsersEnabled(r.ctx, connect.NewRequest(&adminv1.SetUsersEnabledRequest{UserIds: []string{uid}, Enabled: false})))

	d, raw := r.page(tok)
	a := obj(d["amnezia"])
	if a == nil || a["can_add"] != false || len(arr(a["profiles"])) != 0 || a["endpoints"] != "https://sub.example.com/k3xq8/"+tok+"/devices" || a["self_service"] != true ||
		len(arr(a["devices"])) != 1 || obj(arr(a["devices"])[0])["id"] != dev || obj(arr(a["devices"])[0])["label"] != "phone" {
		t.Fatalf("amnezia of an inactive user = %v", a)
	}
	if obj(d["access"])["amnezia"] != false || obj(d["access"])["happ"] != false || len(arr(d["servers"])) != 0 || len(arr(d["dns_presets"])) != 0 ||
		obj(d["dns"])["endpoint"] != "" || d["server_count"] != float64(0) || len(arr(d["server_loads"])) != 0 {
		t.Errorf("an inactive user's page: %s", raw)
	}
	for _, leak := range []string{"PrivateKey", "vpn://", "[Interface]"} {
		if strings.Contains(raw, leak) {
			t.Errorf("the page data holds %q", leak)
		}
	}
	// Shown or added: no. Renamed and removed: yes.
	for _, p := range []string{"/devices/" + dev + "/configs", "/devices/" + dev + "/rotate"} {
		if rec := c.post(p, ""); rec.Code != 409 || decodeAnswer(t, rec).Error != "user_inactive" {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body.String())
		}
	}
	if rec := c.post("/devices", r.addBody("again")); rec.Code != 409 || decodeAnswer(t, rec).Error != "user_inactive" {
		t.Errorf("add: %d %s", rec.Code, rec.Body.String())
	}
	if rec := c.post("/devices/"+dev+"/rename", `{"label":"old phone"}`); rec.Code != 200 || decodeAnswer(t, rec).Device["label"] != "old phone" {
		t.Errorf("rename: %d %s", rec.Code, rec.Body.String())
	}
	if d, _ := r.page(tok); obj(arr(obj(d["amnezia"])["devices"])[0])["label"] != "old phone" {
		t.Errorf("the rename is not on the page")
	}
	if rec := c.post("/devices/"+dev+"/revoke", ""); rec.Code != 200 {
		t.Errorf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	d, _ = r.page(tok)
	if a := obj(d["amnezia"]); a == nil || len(arr(a["devices"])) != 0 {
		t.Errorf("after the revoke: %v", a)
	}

	// Without the app and without keys there is nothing to show; the owner's switch of self-service still reads as set.
	_, none := r.newUser("bob", func(m *adminv1.CreateUserRequest) {
		m.Apps = &adminv1.AppToggles{Happ: true}
	})
	must(r.svc.SetUsersEnabled(r.ctx, connect.NewRequest(&adminv1.SetUsersEnabledRequest{UserIds: []string{r.userID("bob")}, Enabled: false})))
	if d, _ := r.page(none); d["amnezia"] != nil {
		t.Errorf("a user of the link alone: amnezia = %v", d["amnezia"])
	}
}

func (r *dnsRig) userID(name string) string {
	r.t.Helper()
	var id string
	if err := r.st.R.QueryRow(`SELECT id FROM user WHERE name = ?`, name).Scan(&id); err != nil {
		r.t.Fatal(err)
	}
	return id
}

// The key's server is named like servers[].label; the node's own name is for one case, the old connection to delete.
func TestKeyConfigsNameTheServerByLabel(t *testing.T) {
	r := newDNSRig(t, nil)
	_, tok := r.newUser("alice", nil)
	c := call{r.h, t, tok}
	add := decodeAnswer(t, c.post("/devices", r.addBody("phone")))
	if len(add.Configs) != 2 {
		t.Fatalf("configs = %v", add.Configs)
	}
	for _, cfg := range add.Configs {
		label, legacy := cfg["label"], cfg["legacy_name"]
		switch cfg["node_id"] {
		case "nod_1":
			if label != "Germany · Frankfurt" || legacy != de1Name || cfg["filename"] != "mistgate-de.conf" {
				t.Errorf("de1 config: %v", cfg)
			}
		case "nod_2":
			if label != "Netherlands" || legacy != nl1Name {
				t.Errorf("nl1 config: %v", cfg)
			}
		}
		if cfg["server"] != label { // the old name of the field, kept for the old page
			t.Errorf("server %v, label %v", cfg["server"], label)
		}
		if _, ok := cfg["node_name"]; ok {
			t.Error("the old node_name is back")
		}
		if strings.Contains(cfg["conf"].(string), de1Name) || strings.Contains(cfg["vpn_key"].(string), "inner") {
			t.Errorf("a node name in the key: %v", cfg["filename"])
		}
	}
	// The page data lists no legacy name: it is in the answers of the key calls only.
	_, raw := r.page(tok)
	if strings.Contains(raw, "legacy_name") || strings.Contains(raw, de1Name) {
		t.Error("the page data holds the legacy name")
	}
}
