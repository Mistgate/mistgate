package subs_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"gopkg.in/yaml.v3"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The Mihomo profile and the self-service endpoints of the user page, against the real access module and
// database (one hysteria2 and one AmneziaWG profile on node de1).

const mihomoUA = "mihomo/1.19.31"

type m3rig struct {
	*rig
	awg    string // the AmneziaWG profile
	group2 string // a group with both profiles
}

func newM3Rig(t *testing.T) *m3rig {
	t.Helper()
	r := newRig(t, "/k3xq8")
	p := must(r.svc.CreateProfile(r.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "awg", Name: "awg31"}))).Msg.Profile
	must(r.svc.CreateInbound(r.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: p.Id, NodeId: "nod_1"})))
	g := must(r.svc.CreateGroup(r.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "g2", ProfileIds: []string{r.profile, p.Id}}))).Msg.Group.Id
	return &m3rig{rig: r, awg: p.Id, group2: g}
}

func (m *m3rig) newUser(name string, mut func(*adminv1.CreateUserRequest)) (id, token string) {
	return m.user(name, func(c *adminv1.CreateUserRequest) {
		c.GroupId = m.group2
		if mut != nil {
			mut(c)
		}
	})
}

// handler with the DNS module wired the way the panel does (HappRouting carries the preset lookup too).
func (m *m3rig) handler(mut func(*subs.Config)) (http.Handler, *subsettings.Cache) {
	return m.rig.handler(func(c *subs.Config) {
		c.Routing = subs.HappRouting(dns.New(m.st))
		if mut != nil {
			mut(c)
		}
	})
}

type profile struct {
	Proxies []map[string]any `yaml:"proxies"`
	Groups  []struct {
		Name    string   `yaml:"name"`
		Proxies []string `yaml:"proxies"`
	} `yaml:"proxy-groups"`
	DNS   map[string]any `yaml:"dns"`
	Rules []string       `yaml:"rules"`
}

func parseYAML(t *testing.T, b []byte) profile {
	t.Helper()
	var p profile
	if err := yaml.Unmarshal(b, &p); err != nil {
		t.Fatalf("not YAML: %v\n%s", err, b)
	}
	return p
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// byType returns the hysteria2 and the wireguard proxy of a profile that holds exactly those two.
func byType(t *testing.T, p profile) (hy, wg map[string]any) {
	t.Helper()
	if len(p.Proxies) != 2 {
		t.Fatalf("proxies = %v", p.Proxies)
	}
	for _, px := range p.Proxies {
		switch px["type"] {
		case "hysteria2":
			hy = px
		case "wireguard":
			wg = px
		}
	}
	if hy == nil || wg == nil {
		t.Fatalf("proxies = %v", p.Proxies)
	}
	return hy, wg
}

func TestMihomoSubscription(t *testing.T) {
	m := newM3Rig(t)
	h, _ := m.handler(func(c *subs.Config) { c.MinInterval = -1 }) // no response cache: every fetch is a fresh view
	_, tok := m.newUser("alice", nil)

	rec := fetch(h, "/"+tok, mihomoUA, "Accept-Encoding", "gzip")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	hd := rec.Header()
	title := unb64(t, hd.Get("Profile-Title"))
	if !strings.HasPrefix(hd.Get("Content-Type"), "text/yaml") || hd.Get("Content-Encoding") != "gzip" || hd.Get("Vary") != "Accept-Encoding" ||
		hd.Get("Cache-Control") != "no-store" || hd.Get("X-Content-Type-Options") != "nosniff" ||
		hd.Get("Content-Disposition") != "attachment; filename*=UTF-8''"+url.QueryEscape(title) ||
		hd.Get("Subscription-Userinfo") == "" || hd.Get("Profile-Update-Interval") != "12" ||
		hd.Get("Profile-Web-Page-Url") != "https://sub.example.com/k3xq8/"+tok {
		t.Errorf("headers: %v", hd)
	}
	body := gunzip(t, rec.Body.Bytes())
	p := parseYAML(t, body)
	hy, wg := byType(t, p)
	// Both servers are on de1 (no country): the default uses their saved profile names, so the protocol variants stay clear.
	nh, nw := hy["name"].(string), wg["name"].(string)
	if nh != "p" || nw != "awg31" {
		t.Errorf("names = %q %q", nh, nw)
	}
	if len(p.Groups) != 1 || p.Groups[0].Name != title || strings.Join(p.Groups[0].Proxies, "|") != p.Proxies[0]["name"].(string)+"|"+p.Proxies[1]["name"].(string)+"|DIRECT" {
		t.Errorf("groups = %+v (title %q)", p.Groups, title)
	}
	if p.Rules[len(p.Rules)-1] != "MATCH,"+title || p.Rules[0] != "DOMAIN-SUFFIX,ru,DIRECT" {
		t.Errorf("rules = %v", p.Rules)
	}
	ns, _ := p.DNS["nameserver"].([]any)
	if len(ns) == 0 || !strings.HasSuffix(ns[0].(string), "#"+url.PathEscape(title)) || p.DNS["enhanced-mode"] != "fake-ip" {
		t.Errorf("dns = %v", p.DNS)
	}
	if pol, _ := p.DNS["nameserver-policy"].(map[string]any); pol["+.ru"] == nil || pol["+.xn--p1ai"] == nil {
		t.Errorf("policy = %v", p.DNS["nameserver-policy"])
	}
	opt, _ := wg["amnezia-wg-option"].(map[string]any)
	if wg["server"] != "de1.example.com" || opt["version"] != 3 || opt["header-protection-key"] == nil || wg["private-key"] == "" {
		t.Errorf("wireguard proxy = %v", wg)
	}

	// Without Accept-Encoding the body is the same text, uncompressed.
	plain := fetch(h, "/"+tok, mihomoUA)
	if plain.Header().Get("Content-Encoding") != "" || plain.Body.String() != string(body) {
		t.Error("the uncompressed answer differs from the gzip one")
	}
	// The implicit device keeps its AWG credential: the keys do not change between fetches.
	if _, again := byType(t, parseYAML(t, plain.Body.Bytes())); again["private-key"] != wg["private-key"] || again["ip"] != wg["ip"] {
		t.Error("the AWG credential of the implicit device changed between fetches")
	}
	var creds int
	m.st.R.QueryRow(`SELECT count(*) FROM device_credential WHERE protocol = 'awg' AND revoked_at IS NULL`).Scan(&creds)
	if creds != 1 {
		t.Errorf("%d AWG credentials after three fetches, want one", creds)
	}
	if strings.Contains(string(body), tok) {
		t.Error("the token leaked into the profile")
	}

	// The Clash family gets the same profile; a Happ client and a browser keep what they had.
	if rec := fetch(h, "/"+tok, "ClashVerge/2.0"); !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/yaml") {
		t.Errorf("clash UA: %v", rec.Header())
	}
	list := fetch(h, "/"+tok, happUA)
	if got := decodeList(t, list.Body.String()); len(got) != 1 || !strings.HasPrefix(got[0], "hysteria2://") {
		t.Errorf("Happ list = %q (AWG must not be in it)", got)
	}
	if page := fetch(h, "/"+tok, chrome); !strings.HasPrefix(page.Header().Get("Content-Type"), "text/html") {
		t.Errorf("browser: %v", page.Header())
	}
	if rec := fetch(h, "/"+tok, "curl/8.5.0"); !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("curl: %v", rec.Header())
	}
}

// What the stack really serves (hysteria2 with and without hopping and Gecko, AWG 3.1, the DNS of the default preset and
// of a DoH one) is accepted by the Mihomo core; skipped without the binary (see mihomo_core_test.go).
func TestMihomoCoreAcceptsTheServedProfile(t *testing.T) {
	m := newM3Rig(t)
	hop := must(m.svc.CreateProfile(m.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{
		Protocol: "hysteria2", Name: "hop", SettingsJson: `{"port":8443,"hop":{"from":61000,"to":62000},"obfs":{"type":"gecko","password":"gecko-password-0123"}}`}))).Msg.Profile
	must(m.svc.CreateInbound(m.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: hop.Id, NodeId: "nod_1"})))
	g := must(m.svc.CreateGroup(m.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "g3", ProfileIds: []string{m.profile, m.awg, hop.Id}}))).Msg.Group.Id
	h, _ := m.handler(nil)
	for _, preset := range []string{"", "dns_builtin_adblock", "dns_builtin_quad9"} {
		_, tok := m.user("u"+preset, func(c *adminv1.CreateUserRequest) { c.GroupId, c.DnsPresetId = g, preset })
		rec := fetch(h, "/"+tok, mihomoUA)
		if p := parseYAML(t, rec.Body.Bytes()); len(p.Proxies) != 3 {
			t.Fatalf("preset %q: %d proxies\n%s", preset, len(p.Proxies), rec.Body.String())
		}
		subs.MihomoCheck(t, rec.Body.Bytes())
	}
}

func decodeList(t *testing.T, body string) []string {
	t.Helper()
	return strings.Split(strings.TrimSpace(decode(t, body)), "\n")
}

// A fetch inside MinInterval is answered from the cache of its own format: a Mihomo client is never given the base64
// list of a Happ client and the other way round.
func TestFormatsDoNotShareTheResponseCache(t *testing.T) {
	m := newM3Rig(t)
	h, _ := m.handler(nil) // the default MinInterval: second fetches come from the cache
	_, tok := m.newUser("alice", nil)
	for i := 0; i < 2; i++ {
		y := fetch(h, "/"+tok, mihomoUA)
		if p := parseYAML(t, y.Body.Bytes()); len(p.Proxies) != 2 {
			t.Fatalf("round %d: mihomo got %d proxies\n%s", i, len(p.Proxies), y.Body.String())
		}
		l := fetch(h, "/"+tok, happUA)
		if got := decodeList(t, l.Body.String()); len(got) != 1 || !strings.HasPrefix(got[0], "hysteria2://") {
			t.Fatalf("round %d: Happ got %q", i, got)
		}
	}
}

// The settings decide: a rule that sends the Mihomo UA to the list gets the list; a Source that cannot render formats
// serves the Mihomo rule the base64 list instead of failing.
func TestMihomoRuleFollowsTheSettings(t *testing.T) {
	m := newM3Rig(t)
	h, cache := m.handler(nil)
	_, tok := m.newUser("alice", nil)
	set := subsettings.Defaults()
	set.Rules = []*adminv1.ServeRule{{UaContains: "mihomo", Format: adminv1.SubFormat_SUB_FORMAT_BASE64_URIS}}
	if _, err := cache.Update(m.ctx, set); err != nil {
		t.Fatal(err)
	}
	if rec := fetch(h, "/"+tok, mihomoUA); !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("rule to the list: %v", rec.Header())
	}

	fs := &fakeFormatless{}
	fh := subs.Handler(fs, decoy, subs.Config{Title: "T"})
	if rec := fetch(fh, "/"+strings.Repeat("a", 43), mihomoUA); rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("formatless source: %d %v", rec.Code, rec.Header())
	}
}

type fakeFormatless struct{}

func (fakeFormatless) Subscription(context.Context, string) (access.SubView, error) {
	return access.SubView{UserName: "a", Status: access.StatusActive, Lines: []string{"hysteria2://x@de1.example.com:443/"}}, nil
}

// The access module is what the handler type-asserts its Source to; a drifting signature would silently turn the
// Mihomo format and the self-service endpoints off.
var (
	_ subs.FormatSource = (*access.Service)(nil)
	_ subs.Devices      = (*access.Service)(nil)
	_ subs.Source       = (*access.Service)(nil)
)

// The default subscription names include the profile name, so a WARP profile stays visibly separate from the direct one.
func TestWarpProfileIsASeparateServer(t *testing.T) {
	m := newM3Rig(t)
	w := must(m.svc.CreateProfile(m.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{
		Protocol: "hysteria2", Name: "WARP", SettingsJson: `{"port":8444,"egress":"warp"}`}))).Msg.Profile
	must(m.svc.CreateInbound(m.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: w.Id, NodeId: "nod_1"})))
	g := must(m.svc.CreateGroup(m.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "g3", ProfileIds: []string{m.profile, w.Id}}))).Msg.Group.Id
	h, _ := m.handler(nil)
	_, tok := m.user("alice", func(c *adminv1.CreateUserRequest) { c.GroupId = g })

	p := parseYAML(t, fetch(h, "/"+tok, mihomoUA).Body.Bytes())
	if len(p.Proxies) != 2 || p.Proxies[0]["port"] == p.Proxies[1]["port"] {
		t.Fatalf("proxies = %v", p.Proxies)
	}
	var names []string
	for _, px := range p.Proxies {
		names = append(names, px["name"].(string))
	}
	if strings.Join(names, "|") != "p|WARP" {
		t.Errorf("names = %q", names)
	}
	if got := decodeList(t, fetch(h, "/"+tok, happUA).Body.String()); len(got) != 2 ||
		decodeQueryName(t, got[0])+"|"+decodeQueryName(t, got[1]) != "p|WARP" {
		t.Errorf("list = %q", got)
	}
}

// decodeQueryName is the #fragment (server name) of a share link, percent-decoded.
func decodeQueryName(t *testing.T, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	return u.Fragment
}

// An expired or disabled user still gets a valid profile, and never an error: no server, only one entry named after the
// reason, first in the group (so the app shows why instead of quietly going direct).
func TestMihomoForInactiveUser(t *testing.T) {
	m := newM3Rig(t)
	h, _ := m.handler(nil)
	id, tok := m.newUser("alice", nil)
	m.st.W.Exec(`UPDATE user SET disabled = 1 WHERE id = ?`, id)
	if err := m.svc.Recompute(m.ctx, []string{id}); err != nil {
		t.Fatal(err)
	}
	rec := fetch(h, "/"+tok, mihomoUA)
	p := parseYAML(t, rec.Body.Bytes())
	if rec.Code != 200 || len(p.Proxies) != 1 || p.Proxies[0]["name"] != "Access paused" || p.Proxies[0]["server"] != "0.0.0.0" || p.Proxies[0]["port"] != 1 ||
		len(p.Groups) != 1 || strings.Join(p.Groups[0].Proxies, "|") != "Access paused|DIRECT" || rec.Header().Get("Profile-Update-Interval") != "1" ||
		unb64(t, rec.Header().Get("Announce")) != "Access paused" {
		t.Errorf("%d proxies=%v groups=%v hd=%v", rec.Code, p.Proxies, p.Groups, rec.Header())
	}
	subs.MihomoCheck(t, rec.Body.Bytes())
}

// Names (node, profile, brand) are hostile input: the profile stays one document with exactly the servers of the user.
func TestMihomoHostileNames(t *testing.T) {
	m := newM3Rig(t)
	m.st.W.Exec(`UPDATE node SET name = ? WHERE id = 'nod_1'`, "de1\"\n  - name: evil\n    type: direct")
	m.st.W.Exec(`UPDATE profile SET name = ? WHERE id = ?`, "- {a: b} # x", m.awg)
	h, cache := m.handler(nil)
	set := subsettings.Defaults()
	set.Title = "Brand, \"Inc\" #1 & co=\n</script>"
	set.ServerNameTemplate = "{flag} {node} {country} {profile}"
	if _, err := cache.Update(m.ctx, set); err == nil {
		t.Fatal("a title with control characters must be refused by the settings")
	}
	set.Title = "Brand, \"Inc\" #1 & co=</script>"
	if _, err := cache.Update(m.ctx, set); err != nil {
		t.Fatal(err)
	}
	_, tok := m.newUser("al\"ice - x", nil)
	rec := fetch(h, "/"+tok, mihomoUA)
	p := parseYAML(t, rec.Body.Bytes())
	if len(p.Proxies) != 2 || len(p.Groups) != 1 || len(p.Rules) < 2 {
		t.Fatalf("proxies=%d groups=%d rules=%d\n%s", len(p.Proxies), len(p.Groups), len(p.Rules), rec.Body.String())
	}
	for _, px := range p.Proxies {
		if px["type"] == "direct" || strings.Contains(px["name"].(string), "\n") {
			t.Errorf("a hostile name changed a proxy: %v", px["name"])
		}
	}
	g := p.Groups[0].Name
	if strings.ContainsAny(g, ",#&=\"") || p.Rules[len(p.Rules)-1] != "MATCH,"+g {
		t.Errorf("group %q, last rule %q", g, p.Rules[len(p.Rules)-1])
	}
}

// The user's DNS preset reaches the profile: a preset without a split has no policy and no DIRECT rule.
func TestMihomoDNSFollowsThePreset(t *testing.T) {
	m := newM3Rig(t)
	h, _ := m.handler(nil)
	_, tok := m.newUser("alice", func(c *adminv1.CreateUserRequest) { c.DnsPresetId = "dns_builtin_adblock" })
	p := parseYAML(t, fetch(h, "/"+tok, mihomoUA).Body.Bytes())
	ns, _ := p.DNS["nameserver"].([]any)
	if len(ns) != 2 || !strings.HasPrefix(ns[0].(string), "94.140.14.14#") || p.DNS["nameserver-policy"] != nil || len(p.Rules) != 1 {
		t.Errorf("dns = %v rules = %v", p.DNS, p.Rules)
	}
	// Without a DNS module the profile has no dns section.
	h2, _ := m.rig.handler(nil)
	if p := parseYAML(t, fetch(h2, "/"+tok, mihomoUA).Body.Bytes()); p.DNS != nil || len(p.Proxies) != 2 {
		t.Errorf("no dns module: %v", p.DNS)
	}
}

// ---- self-service ----

type call struct {
	h     http.Handler
	t     *testing.T
	token string
}

func (c call) post(path string, body string, hdr ...string) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest("POST", "/"+c.token+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

type answer struct {
	Device  map[string]any   `json:"device"`
	Configs []map[string]any `json:"configs"`
	Error   string           `json:"error"`
	Message string           `json:"message"`
	OK      bool             `json:"ok"`
}

func decodeAnswer(t *testing.T, rec *httptest.ResponseRecorder) answer {
	t.Helper()
	var a answer
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatalf("not JSON (%d): %v %q", rec.Code, err, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", rec.Header())
	}
	return a
}

func (m *m3rig) addBody(label string) string {
	b, _ := json.Marshal(map[string]string{"profile_id": m.awg, "platform": "ios", "label": label})
	return string(b)
}

func amneziaOf(t *testing.T, h http.Handler, tok string) map[string]any {
	t.Helper()
	body := fetch(h, "/"+tok, chrome).Body.String()
	d, raw := pageData(t, body)
	for _, secret := range []string{"PrivateKey", "client_priv_key", "vpn://", "PresharedKey", "private-key", "[Interface]"} {
		if strings.Contains(raw, secret) {
			t.Errorf("the page data holds %q", secret)
		}
	}
	if len(raw) > 100<<10 {
		t.Errorf("the page data is %d bytes", len(raw))
	}
	a, _ := d["amnezia"].(map[string]any)
	return a
}

func TestSelfServiceLifecycle(t *testing.T) {
	m := newM3Rig(t)
	h, _ := m.handler(nil)
	uid, tok := m.newUser("alice", nil)
	c := call{h, t, tok}

	// The page offers the section, with no key in it.
	a := amneziaOf(t, h, tok)
	if a == nil || a["can_add"] != true || a["self_service"] != true || a["endpoints"] != "https://sub.example.com/k3xq8/"+tok+"/devices" ||
		len(a["devices"].([]any)) != 0 || len(a["profiles"].([]any)) != 1 {
		t.Fatalf("amnezia = %v", a)
	}
	if pr := a["profiles"].([]any)[0].(map[string]any); pr["id"] != m.awg || pr["name"] != "awg31" || pr["version"] != "3.1" || pr["egress"] != "direct" ||
		len(pr["countries"].([]any)) != 0 {
		t.Errorf("profile = %v", pr)
	}

	// Add.
	rec := c.post("/devices", m.addBody("My phone"))
	if rec.Code != 200 {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
	add := decodeAnswer(t, rec)
	id, _ := add.Device["id"].(string)
	if !strings.HasPrefix(id, "dev_") || add.Device["label"] != "My phone" || add.Device["platform"] != "ios" || add.Device["version"] != "3.1" ||
		add.Device["profile_name"] != "awg31" || add.Device["stale"] != false || add.Device["online"] != false ||
		!strings.HasPrefix(add.Device["address"].(string), "10.66.4.2") || len(add.Device["min_clients"].([]any)) == 0 {
		t.Fatalf("device = %v", add.Device)
	}
	if len(add.Configs) != 1 {
		t.Fatalf("configs = %v", add.Configs)
	}
	cfg := add.Configs[0]
	conf := cfg["conf"].(string)
	// the server by its public name (de1 has no country or location), never the panel's node name
	if _, leaked := cfg["node_name"]; leaked || cfg["server"] != "Server" || !strings.Contains(conf, "[Interface]") || !strings.Contains(conf, "Endpoint = de1.example.com:") ||
		!strings.HasPrefix(cfg["vpn_key"].(string), "vpn://") || cfg["filename"] != "mistgate-awg.conf" || cfg["version"] != "3.1" ||
		len(cfg["warnings"].([]any)) == 0 {
		t.Errorf("config = %v", cfg)
	}
	priv := lineValue(conf, "PrivateKey")

	// The audit row names the user, not a key.
	var actor, params string
	if err := m.st.R.QueryRow(`SELECT actor, params FROM audit WHERE action = 'device_create'`).Scan(&actor, &params); err != nil || actor != "user:"+uid ||
		strings.Contains(params, priv) || strings.Contains(params, "vpn://") {
		t.Errorf("audit: %q %q %v", actor, params, err)
	}

	// The page lists it (the cached view of the first fetch was dropped); the keys are not in the page.
	a = amneziaOf(t, h, tok)
	devs := a["devices"].([]any)
	if len(devs) != 1 || devs[0].(map[string]any)["id"] != id || devs[0].(map[string]any)["label"] != "My phone" {
		t.Fatalf("devices on the page = %v", devs)
	}

	// Configs again: the same key; rotate: a new one on the same address.
	got := decodeAnswer(t, c.post("/devices/"+id+"/configs", ""))
	if lineValue(got.Configs[0]["conf"].(string), "PrivateKey") != priv {
		t.Error("configs gave another key")
	}
	rot := decodeAnswer(t, c.post("/devices/"+id+"/rotate", ""))
	if len(rot.Configs) != 1 || lineValue(rot.Configs[0]["conf"].(string), "PrivateKey") == priv ||
		lineValue(rot.Configs[0]["conf"].(string), "Address") != lineValue(conf, "Address") {
		t.Errorf("rotate: %v", rot.Configs)
	}

	// Rename.
	ren := decodeAnswer(t, c.post("/devices/"+id+"/rename", `{"label":"Work phone"}`))
	if ren.Device["label"] != "Work phone" || len(ren.Configs) != 0 {
		t.Errorf("rename: %+v", ren)
	}
	if rec := c.post("/devices/"+id+"/rename", `{"label":""}`); rec.Code != 400 {
		t.Errorf("empty label: %d", rec.Code)
	}

	// Revoke: gone from the page; again: not found.
	if rec := c.post("/devices/"+id+"/revoke", ""); rec.Code != 200 || !decodeAnswer(t, rec).OK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if a := amneziaOf(t, h, tok); len(a["devices"].([]any)) != 0 || a["can_add"] != true {
		t.Errorf("after revoke: %v", a)
	}
	if rec := c.post("/devices/"+id+"/revoke", ""); rec.Code != 404 || decodeAnswer(t, rec).Error != "not_found" {
		t.Errorf("second revoke: %d %s", rec.Code, rec.Body.String())
	}
	if rec := c.post("/devices/"+id+"/configs", ""); rec.Code != 404 {
		t.Errorf("configs of a revoked device: %d", rec.Code)
	}
	for _, act := range []string{"device_configs", "device_rotate", "device_revoke"} {
		var n int
		m.st.R.QueryRow(`SELECT count(*) FROM audit WHERE action = ? AND actor = ?`, act, "user:"+uid).Scan(&n)
		if n != 1 {
			t.Errorf("%s: %d audit rows", act, n)
		}
	}
}

func lineValue(conf, key string) string {
	for _, l := range strings.Split(conf, "\n") {
		if k, v, ok := strings.Cut(l, " = "); ok && k == key {
			return v
		}
	}
	return ""
}

// A device of another user answers "not found" whatever the action: a token holder cannot touch it.
func TestSelfServiceIsolation(t *testing.T) {
	m := newM3Rig(t)
	h, _ := m.handler(nil)
	_, tokA := m.newUser("alice", nil)
	_, tokB := m.newUser("bob", nil)
	id := decodeAnswer(t, call{h, t, tokA}.post("/devices", m.addBody("a"))).Device["id"].(string)
	b := call{h, t, tokB}
	for _, p := range []struct{ path, body string }{
		{"/configs", ""}, {"/rotate", ""}, {"/revoke", ""}, {"/rename", `{"label":"x"}`},
	} {
		if rec := b.post("/devices/"+id+p.path, p.body); rec.Code != 404 {
			t.Errorf("%s with the wrong token: %d %s", p.path, rec.Code, rec.Body.String())
		}
	}
	if a := amneziaOf(t, h, tokA); len(a["devices"].([]any)) != 1 || a["devices"].([]any)[0].(map[string]any)["label"] != "a" {
		t.Errorf("the device of alice changed: %v", a)
	}
}

// The user's device limit, the status of the user and the admin's switch.
func TestSelfServiceLimitsAndSwitches(t *testing.T) {
	m := newM3Rig(t)
	h, cache := m.handler(nil)
	id, tok := m.newUser("alice", func(c *adminv1.CreateUserRequest) { c.DeviceLimit = 3 }) // the implicit device (made by the first request) counts too
	c := call{h, t, tok}
	for i := 0; i < 2; i++ {
		if rec := c.post("/devices", m.addBody("")); rec.Code != 200 {
			t.Fatalf("device %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	rec := c.post("/devices", m.addBody(""))
	if a := decodeAnswer(t, rec); rec.Code != 409 || a.Error != "device_limit" || !strings.Contains(a.Message, "3/3") {
		t.Errorf("third device: %d %+v", rec.Code, a)
	}
	if a := amneziaOf(t, h, tok); a["can_add"] != false || len(a["devices"].([]any)) != 2 {
		t.Errorf("page at the limit: can_add=%v devices=%d", a["can_add"], len(a["devices"].([]any)))
	}

	// Bad input.
	m.st.W.Exec(`UPDATE user SET device_limit = 50 WHERE id = ?`, id)
	for name, body := range map[string]string{
		"platform":     `{"profile_id":"` + m.awg + `","platform":"toaster"}`,
		"control char": `{"profile_id":"` + m.awg + `","platform":"ios","label":"a\nb"}`,
		"long label":   `{"profile_id":"` + m.awg + `","platform":"ios","label":"` + strings.Repeat("x", 41) + `"}`,
		"no profile":   `{"platform":"ios"}`,
		"hy2 profile":  `{"profile_id":"` + m.profile + `","platform":"ios"}`,
	} {
		if rec := c.post("/devices", body); rec.Code != 400 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := c.post("/devices", `{"profile_id":"prf_nope","platform":"ios"}`); rec.Code != 404 {
		t.Errorf("unknown profile: %d", rec.Code)
	}
	if rec := c.post("/devices", m.addBody("a"+string(rune(0x2028))+"b")); rec.Code != 400 {
		t.Errorf("a line separator in the label: %d", rec.Code)
	}
	if rec := c.post("/devices", m.addBody("<b>\"x\"</b>")); rec.Code != 200 || !strings.Contains(rec.Body.String(), `\u003cb\u003e`) {
		t.Errorf("a label with markup must come back JSON-escaped: %d %s", rec.Code, rec.Body.String())
	}

	// The admin switches self-service off: the page shows the devices and nothing more, and the endpoints refuse.
	set := subsettings.Defaults()
	set.UserPage.AllowDeviceSelfService = new(bool)
	if _, err := cache.Update(m.ctx, set); err != nil {
		t.Fatal(err)
	}
	if rec := c.post("/devices", m.addBody("")); rec.Code != 403 || decodeAnswer(t, rec).Error != "self_service_disabled" {
		t.Errorf("switched off: %d %s", rec.Code, rec.Body.String())
	}
	if a := amneziaOf(t, h, tok); a["self_service"] != false || a["can_add"] != false || a["endpoints"] != "" || len(a["devices"].([]any)) != 3 {
		t.Errorf("page with self-service off: %v", a)
	}
	set.UserPage.AllowDeviceSelfService = nil // a settings document from before this setting existed: on
	if _, err := cache.Update(m.ctx, set); err != nil {
		t.Fatal(err)
	}
	if rec := c.post("/devices", m.addBody("")); rec.Code != 200 {
		t.Errorf("absent switch must mean on: %d", rec.Code)
	}

	// A disabled user gets no keys, but can still remove devices.
	m.st.W.Exec(`UPDATE user SET disabled = 1 WHERE id = ?`, id)
	if err := m.svc.Recompute(m.ctx, []string{id}); err != nil {
		t.Fatal(err)
	}
	h2, _ := m.handler(nil) // a fresh handler: no cached view of the active user
	c2 := call{h2, t, tok}
	for _, p := range []string{"", "/dev_abc/configs", "/dev_abc/rotate"} {
		if rec := c2.post("/devices"+p, m.addBody("")); rec.Code != 409 || decodeAnswer(t, rec).Error != "user_inactive" {
			t.Errorf("disabled user, %q: %d %s", p, rec.Code, rec.Body.String())
		}
	}
	if rec := c2.post("/devices/dev_abc/revoke", ""); rec.Code != 404 {
		t.Errorf("a disabled user may revoke (not found here): %d", rec.Code)
	}
}

// The write budget is per token and separate from the fetch budget.
func TestSelfServiceWriteBudget(t *testing.T) {
	m := newM3Rig(t)
	clock := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	h, _ := m.handler(func(c *subs.Config) {
		c.MaxWritesPerHour, c.MaxPerHour, c.Now = 3, 5, func() time.Time { return clock }
	})
	_, tokA := m.newUser("alice", func(c *adminv1.CreateUserRequest) { c.DeviceLimit = 50 })
	_, tokB := m.newUser("bob", nil)
	a, b := call{h, t, tokA}, call{h, t, tokB}
	for i := 0; i < 3; i++ {
		if rec := a.post("/devices", m.addBody("")); rec.Code != 200 {
			t.Fatalf("write %d: %d", i, rec.Code)
		}
	}
	rec := a.post("/devices", m.addBody(""))
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "3600" || decodeAnswer(t, rec).Error != "too_many_requests" {
		t.Errorf("fourth write: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	if rec := b.post("/devices", m.addBody("")); rec.Code != 200 {
		t.Errorf("another token is refused too: %d", rec.Code)
	}
	// Fetches have a budget of their own: the writes did not use it.
	for i := 0; i < 5; i++ {
		if rec := fetch(h, "/"+tokA, happUA); rec.Code != 200 {
			t.Fatalf("fetch %d after the write budget ran out: %d", i, rec.Code)
		}
	}
	if rec := fetch(h, "/"+tokA, happUA); rec.Code != 429 {
		t.Errorf("sixth fetch: %d", rec.Code)
	}
	// ...and fetches did not use the write budget: a new hour restores it.
	clock = clock.Add(61 * time.Minute)
	if rec := a.post("/devices", m.addBody("")); rec.Code != 200 {
		t.Errorf("after an hour: %d", rec.Code)
	}
	// A refused request (bad route, bad id, unknown action) is not a write.
	for _, p := range []string{"/devices/BAD/configs", "/devices/dev_x/nothing", "/devices/dev_x", "/devices/dev_x/configs/more", "/devices/"} {
		if rec := a.post(p, ""); rec.Code != 404 {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if rec := a.post("/devices", m.addBody("")); rec.Code != 200 {
		t.Errorf("malformed requests used the budget: %d", rec.Code)
	}
}

// An unknown token is the decoy and counts as a miss, like a fetch; the client network is blocked after enough of them.
func TestSelfServiceUnknownTokenIsADecoyAndAMiss(t *testing.T) {
	m := newM3Rig(t)
	h, _ := m.handler(func(c *subs.Config) {
		c.MissLimit, c.MissWindow, c.BlockFor = 3, time.Minute, time.Hour
		c.ClientIP = func(*http.Request) netip.Addr { return netip.MustParseAddr("198.51.100.7") }
	})
	_, tok := m.newUser("alice", nil)
	for i := 0; i < 3; i++ {
		bad := call{h, t, strings.Repeat(string(rune('a'+i)), 43)}
		if rec := bad.post("/devices", m.addBody("")); rec.Code != 404 || rec.Body.String() != decoyBody {
			t.Fatalf("unknown token %d: %d %q", i, rec.Code, rec.Body.String())
		}
	}
	// Blocked: even the valid token gets the decoy, for a fetch and for a write.
	if rec := (call{h, t, tok}).post("/devices", m.addBody("")); rec.Code != 404 || rec.Body.String() != decoyBody {
		t.Errorf("blocked client, valid token: %d %q", rec.Code, rec.Body.String())
	}
	if rec := fetch(h, "/"+tok, happUA); rec.Body.String() != decoyBody {
		t.Errorf("blocked client fetch: %q", rec.Body.String())
	}
}

// Not a self-service route, or not POST: the decoy (and a miss), never a JSON answer that tells the endpoint exists.
func TestSelfServiceRoutesAreOnlyForPOSTWithAToken(t *testing.T) {
	m := newM3Rig(t)
	h, _ := m.handler(nil)
	_, tok := m.newUser("alice", nil)
	for _, c := range []struct{ method, path string }{
		{"GET", "/" + tok + "/devices"}, {"PUT", "/" + tok + "/devices"}, {"DELETE", "/" + tok + "/devices/dev_x/revoke"},
		{"POST", "/" + tok}, {"POST", "/" + tok + "/other"}, {"POST", "/" + tok + "/devicesx"}, {"POST", "/" + tok + "/"},
		{"POST", "/short/devices"},
	} {
		req := httptest.NewRequest(c.method, c.path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 404 || rec.Body.String() != decoyBody {
			t.Errorf("%s %s: %d %q", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

// Cross-origin browsers cannot post; same-origin ones and non-browser clients (curl) can.
func TestSelfServiceCrossOrigin(t *testing.T) {
	m := newM3Rig(t)
	h, _ := m.handler(func(c *subs.Config) { c.BaseURL = "https://sub.example.com/k3xq8"; c.MaxWritesPerHour = -1 })
	_, tok := m.newUser("alice", func(c *adminv1.CreateUserRequest) { c.DeviceLimit = 50 })
	c := call{h, t, tok}
	for name, hdr := range map[string][]string{
		"cross-site fetch metadata": {"Sec-Fetch-Site", "cross-site"},
		"same-site fetch metadata":  {"Sec-Fetch-Site", "same-site"},
		"foreign origin":            {"Origin", "https://evil.example"},
	} {
		rec := c.post("/devices", m.addBody(""), hdr...)
		if rec.Code != 403 || decodeAnswer(t, rec).Error != "cross_origin" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	for name, hdr := range map[string][]string{
		"curl":                       nil,
		"same-origin fetch metadata": {"Sec-Fetch-Site", "same-origin"},
		"the public origin":          {"Origin", "https://sub.example.com"},
		"the public origin, behind a proxy that changed Host": {"Origin", "https://sub.example.com", "Host", "10.0.0.5:8080"},
	} {
		if rec := c.post("/devices", m.addBody(""), hdr...); rec.Code != 200 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

// A request body is small JSON and nothing else.
func TestSelfServiceBodyRules(t *testing.T) {
	m := newM3Rig(t)
	h, _ := m.handler(func(c *subs.Config) { c.MaxWritesPerHour = -1 })
	_, tok := m.newUser("alice", func(c *adminv1.CreateUserRequest) { c.DeviceLimit = 50 })
	c := call{h, t, tok}
	good := m.addBody("")
	req := func(ct, body string) int {
		r := httptest.NewRequest("POST", "/"+tok+"/devices", strings.NewReader(body))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	for name, want := range map[string]struct {
		ct, body string
		code     int
	}{
		"json":            {"application/json", good, 200},
		"json + charset":  {"application/json; charset=utf-8", good, 200},
		"form":            {"application/x-www-form-urlencoded", good, 415},
		"text":            {"text/plain", good, 415},
		"no content type": {"", good, 415},
		"unknown field":   {"application/json", `{"profile_id":"` + m.awg + `","platform":"ios","admin":true}`, 400},
		"trailing data":   {"application/json", good + good, 400},
		"not json":        {"application/json", `{profile`, 400},
		"too long":        {"application/json", `{"label":"` + strings.Repeat("x", 5000) + `"}`, 413},
	} {
		if got := req(want.ct, want.body); got != want.code {
			t.Errorf("%s: %d, want %d", name, got, want.code)
		}
	}
	// No body at all is an empty object: the profile is then required.
	if rec := c.post("/devices", ""); rec.Code != 400 {
		t.Errorf("empty body: %d", rec.Code)
	}
}

// The admin's preview of the page cannot write: no endpoints, no add button; it shows the admin's switch as it is (the
// page then knows whether to say "keys come from the admin").
func TestPreviewPageCannotWrite(t *testing.T) {
	m := newM3Rig(t)
	id, _ := m.newUser("alice", nil)
	cache := subsettings.NewCache(m.st, nil)
	pv := subs.PreviewHandler(m.svc, subs.Config{Title: "T", Settings: cache, Dist: pageDist})
	preview := func() map[string]any {
		rec := httptest.NewRecorder()
		pv(rec, httptest.NewRequest("GET", "/preview", nil), id)
		d, _ := pageData(t, rec.Body.String())
		a, _ := d["amnezia"].(map[string]any)
		return a
	}
	if a := preview(); a == nil || a["self_service"] != true || a["can_add"] != false || a["endpoints"] != "" || len(a["profiles"].([]any)) != 1 {
		t.Errorf("preview amnezia = %v", a)
	}
	set := subsettings.Defaults()
	set.UserPage.AllowDeviceSelfService = new(bool)
	if _, err := cache.Update(m.ctx, set); err != nil {
		t.Fatal(err)
	}
	if a := preview(); a["self_service"] != false || a["endpoints"] != "" {
		t.Errorf("preview with self-service off = %v", a)
	}
}

// The page names an AmneziaWG choice by what a friend can tell apart: its countries and its exit.
func TestPageProfilesCarryCountriesAndExit(t *testing.T) {
	m := newM3Rig(t)
	m.st.W.Exec(`UPDATE node SET country_code = 'de' WHERE id = 'nod_1'`)
	m.st.W.Exec(`INSERT INTO node (id, name, address, country_code, state, created_at) VALUES ('nod_2', 'fi1', 'fi1.example.com', 'FI', 'active', 1)`)
	m.st.W.Exec(`INSERT INTO node (id, name, address, country_code, state, created_at) VALUES ('nod_3', 'de2', 'de2.example.com', 'DE', 'active', 1)`)
	for _, n := range []string{"nod_2", "nod_3"} {
		must(m.svc.CreateInbound(m.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: m.awg, NodeId: n})))
	}
	w := must(m.svc.TwinProfile(m.ctx, connect.NewRequest(&adminv1.TwinProfileRequest{ProfileId: m.awg, Egress: "warp"}))).Msg.Profile
	h, _ := m.handler(nil)
	_, tok := m.newUser("alice", nil)
	got := map[string]string{}
	for _, p := range amneziaOf(t, h, tok)["profiles"].([]any) {
		p := p.(map[string]any)
		var cc []string
		for _, c := range p["countries"].([]any) {
			cc = append(cc, c.(string))
		}
		got[p["id"].(string)] = p["egress"].(string) + " " + strings.Join(cc, ",")
	}
	// nodes come in name order (de1, de2, fi1): DE once, then FI
	if got[m.awg] != "direct DE,FI" || got[w.Id] != "warp DE,FI" {
		t.Errorf("profiles = %v", got)
	}
}

// A user without the Amnezia app has no Amnezia section.
func TestPageWithoutAmneziaAccess(t *testing.T) {
	m := newM3Rig(t)
	h, _ := m.handler(nil)
	_, tok := m.newUser("alice", func(c *adminv1.CreateUserRequest) { c.Apps = &adminv1.AppToggles{Happ: true} })
	if a := amneziaOf(t, h, tok); a != nil {
		t.Errorf("amnezia = %v", a)
	}
	if rec := (call{h, t, tok}).post("/devices", m.addBody("")); rec.Code != 409 || decodeAnswer(t, rec).Error != "app_disabled" {
		t.Errorf("add without the app: %d %s", rec.Code, rec.Body.String())
	}
}
