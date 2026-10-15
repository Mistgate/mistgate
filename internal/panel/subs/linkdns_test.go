package subs_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// One DNS for the apps that take the link, picked by the person on their page (POST <link>/dns {"link": true}), against the
// real access module and database: what each format carries, the endpoint, and the names the page shows.

func linkBody(preset string) string {
	b, _ := json.Marshal(map[string]any{"link": true, "preset": preset})
	return string(b)
}

type linkAnswer struct {
	Link         map[string]any `json:"link"`
	Server       map[string]any `json:"server"`
	StaleDevices []string       `json:"stale_devices"`
	Error        string         `json:"error"`
}

func decodeLink(t *testing.T, rec *httptest.ResponseRecorder) linkAnswer {
	t.Helper()
	var a linkAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatalf("not JSON (%d): %v %q", rec.Code, err, rec.Body.String())
	}
	return a
}

// adguard: the text names AdGuard DNS, by its name or its address.
func adguard(s string) bool {
	return strings.Contains(s, "adguard") || strings.Contains(s, "94.140.14")
}

// formatsDNS is what each link format of a person carries as its DNS: the Happ routing header, the dns of the Mihomo profile
// (the AmneziaWG proxies keep their own), of the Xray JSON array and of the sing-box profile.
func formatsDNS(t *testing.T, r *dnsRig, tok string) map[string]string {
	t.Helper()
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	x := parseXray(t, fetch(r.h, "/"+tok+"?format=xray-json", v2rayNGUA).Body.Bytes())
	return map[string]string{
		"happ":    fetch(r.h, "/"+tok, happUA).Header().Get("Routing"),
		"mihomo":  js(parseYAML(t, fetch(r.h, "/"+tok, mihomoUA).Body.Bytes()).DNS),
		"xray":    js(x[0].DNS),
		"singbox": js(parseSingbox(t, fetch(r.h, "/"+tok, sfaUA).Body.Bytes()).DNS),
	}
}

func TestLinkDNSPickReachesEveryLinkFormat(t *testing.T) {
	clock := time.Now()
	r := newDNSRig(t, func(c *subs.Config) { c.Now = func() time.Time { return clock } })
	must(0, r.st.DNS().SetLinkOptions(r.ctx, []string{"dns_builtin_standard", "dns_builtin_adblock"}, "dns_builtin_standard"))
	uid, tok := r.newUser("alice", nil)
	c := call{r.h, t, tok}
	adblock, err := dns.New(r.st).PresetByID(r.ctx, "dns_builtin_adblock")
	if err != nil {
		t.Fatal(err)
	}
	happAdblock, err := dns.HappRouting(adblock)
	if err != nil {
		t.Fatal(err)
	}

	add := decodeAnswer(t, c.post("/devices", r.addBody("phone")))
	dev := add.Device["id"].(string)
	before := formatsDNS(t, r, tok) // the offer's default, Cloudflare + Google: and the views are now cached
	keysBefore := awgProxyDNS(t, r.h, tok)
	for f, v := range before {
		if v == "" || adguard(v) {
			t.Errorf("%s before the pick: %s", f, v)
		}
	}

	rec := c.post("/dns", linkBody("dns_builtin_adblock"))
	ans := decodeLink(t, rec)
	if rec.Code != 200 || ans.Server != nil || len(ans.StaleDevices) != 0 || ans.Link["choice"] != "dns_builtin_adblock" || ans.Link["effective"] != "dns_builtin_adblock" ||
		ans.Link["default"] != "dns_builtin_standard" || strings.Join(strs(ans.Link["options"]), ",") != "dns_builtin_standard,dns_builtin_adblock" {
		t.Fatalf("pick: %d %s", rec.Code, rec.Body.String())
	}
	after := formatsDNS(t, r, tok)
	if after["happ"] != happAdblock {
		t.Errorf("Happ routing: %s", after["happ"])
	}
	for _, f := range []string{"mihomo", "xray", "singbox"} {
		if !adguard(after[f]) {
			t.Errorf("%s after the pick: %s", f, after[f])
		}
	}
	// The keys keep their DNS per server, and are not marked for a new key.
	if keys := awgProxyDNS(t, r.h, tok); keys["de1.example.com"] != keysBefore["de1.example.com"] || keys["nl1.example.com"] != keysBefore["nl1.example.com"] {
		t.Errorf("a pick for the link apps moved the keys: %v -> %v", keysBefore, keys)
	}
	d, _ := r.page(tok)
	if k := obj(arr(obj(d["amnezia"])["devices"])[0]); k["id"] != dev || k["stale"] != false {
		t.Errorf("the key after a pick for the link apps: %v", k)
	}
	if l := obj(obj(d["dns"])["link"]); l["choice"] != "dns_builtin_adblock" || l["effective"] != "dns_builtin_adblock" {
		t.Errorf("the page after the pick: %v", l)
	}

	// The owner stops offering it: the link apps fall back to the default, the pick is kept.
	must(0, r.st.DNS().SetLinkOptions(r.ctx, []string{"dns_builtin_standard"}, "dns_builtin_standard"))
	clock = clock.Add(11 * time.Second) // a change of the offer reaches the views of a token once they are older than MinInterval
	if got := formatsDNS(t, r, tok); got["happ"] == happAdblock || adguard(got["mihomo"]) || adguard(got["xray"]) || adguard(got["singbox"]) {
		t.Errorf("a pick that is no longer offered still applies: %v", got)
	}
	d, _ = r.page(tok)
	if l := obj(obj(d["dns"])["link"]); l["choice"] != "" || l["effective"] != "dns_builtin_standard" {
		t.Errorf("no longer offered, the page: %v", l)
	}

	// The audit row: the person and the preset, no link and no address.
	var actor, params, ip string
	if err := r.st.R.QueryRow(`SELECT actor, params, ip FROM audit WHERE action = 'page_link_dns_choice'`).Scan(&actor, &params, &ip); err != nil {
		t.Fatal(err)
	}
	if actor != "user:"+uid || ip != "" || !strings.Contains(params, `"user":"alice"`) || !strings.Contains(params, `"preset":"dns_builtin_adblock"`) ||
		!strings.Contains(params, `"preset_name":"AdGuard: без рекламы"`) || strings.Contains(params, tok) || strings.Contains(params, "node") {
		t.Errorf("audit: %q %q %q", actor, params, ip)
	}
}

func TestLinkDNSEndpoint(t *testing.T) {
	clock := time.Now()
	r := newDNSRig(t, func(c *subs.Config) { c.MaxWritesPerHour, c.Now = -1, func() time.Time { return clock } })
	_, tok := r.newUser("alice", nil)
	c := call{r.h, t, tok}
	want := func(name string, rec *httptest.ResponseRecorder, status int, errCode string) {
		t.Helper()
		if a := decodeLink(t, rec); rec.Code != status || a.Error != errCode {
			t.Errorf("%s: %d %q, want %d %q (%s)", name, rec.Code, a.Error, status, errCode, rec.Body.String())
		}
	}

	// No offer: nothing to pick for the link apps.
	d, _ := r.page(tok)
	if l := obj(obj(d["dns"])["link"]); len(arr(l["options"])) != 0 || l["choice"] != "" || l["effective"] != "dns_builtin_ru_split" || l["default"] != "dns_builtin_ru_split" {
		t.Errorf("no offer: %v", l)
	}
	want("no offer", c.post("/dns", linkBody("dns_builtin_adblock")), 409, "not_allowed")
	must(0, r.st.DNS().SetLinkOptions(r.ctx, []string{"dns_builtin_adblock", "dns_builtin_family"}, ""))
	clock = clock.Add(11 * time.Second)
	d, _ = r.page(tok)
	if l := obj(obj(d["dns"])["link"]); strings.Join(strs(l["options"]), ",") != "dns_builtin_adblock,dns_builtin_family" || l["default"] != "dns_builtin_ru_split" {
		t.Errorf("an offer without a default: %v", l)
	}
	want("not offered", c.post("/dns", linkBody("dns_builtin_quad9")), 409, "not_allowed")
	want("no such preset", c.post("/dns", linkBody("dns_nope")), 409, "not_allowed")
	want("a server and the link", c.post("/dns", `{"server":"nod_1","link":true,"preset":""}`), 400, "bad_request")
	want("the link off and no server", c.post("/dns", `{"link":false,"preset":"dns_builtin_adblock"}`), 400, "bad_request")
	if n := r.countRows(`SELECT count(*) FROM user_link_dns`); n != 0 {
		t.Fatalf("refused picks were stored: %d", n)
	}
	want("accepted", c.post("/dns", linkBody("dns_builtin_family")), 200, "")
	rec := c.post("/dns", linkBody(""))
	if a := decodeLink(t, rec); rec.Code != 200 || a.Link["choice"] != "" || a.Link["effective"] != "dns_builtin_ru_split" {
		t.Errorf("back to the default: %d %s", rec.Code, rec.Body.String())
	}
	if n := r.countRows(`SELECT count(*) FROM user_link_dns`); n != 0 {
		t.Errorf("the default left a row: %d", n)
	}
	// The owner's switch, as for the servers.
	r.choice(false)
	want("choice off", c.post("/dns", linkBody("dns_builtin_family")), 403, "dns_disabled")
	if d, _ := r.page(tok); obj(d["dns"])["endpoint"] != "" {
		t.Errorf("choice off: the page has an endpoint")
	}
}

// The pick for the link apps takes the same start as every write of the page: the page password and the budget of writes.
func TestLinkDNSPasswordAndBudget(t *testing.T) {
	g := newGate(t, nil)
	set := subsettings.Defaults()
	set.UserPage.AllowDnsChoice = proto.Bool(true)
	if _, err := g.cache.Update(g.ctx, set); err != nil {
		t.Fatal(err)
	}
	must(0, g.st.DNS().SetLinkOptions(g.ctx, []string{"dns_builtin_adblock"}, ""))
	uid, tok := g.newUser("alice", nil)
	if rec := (call{g.h, t, tok}).post("/dns", linkBody("dns_builtin_adblock")); rec.Code != 401 || decodeLink(t, rec).Error != "locked" {
		t.Errorf("without the cookie: %d %s", rec.Code, rec.Body.String())
	}
	cookie := cookieOf(t, g.unlock(t, tok, g.password(uid)))
	if rec := (call{g.h, t, tok}).post("/dns", linkBody("dns_builtin_adblock"), "Cookie", ck(cookie)); rec.Code != 200 {
		t.Errorf("with the cookie: %d %s", rec.Code, rec.Body.String())
	}

	clock := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	b := newDNSRig(t, func(c *subs.Config) { c.MaxWritesPerHour, c.Now = 2, func() time.Time { return clock } })
	must(0, b.st.DNS().SetLinkOptions(b.ctx, []string{"dns_builtin_adblock", "dns_builtin_family"}, ""))
	_, tok2 := b.newUser("bob", nil)
	c := call{b.h, t, tok2}
	if rec := c.post("/dns", linkBody("dns_builtin_adblock")); rec.Code != 200 {
		t.Fatalf("first: %d %s", rec.Code, rec.Body.String())
	}
	if rec := c.post("/dns", pickBody("nod_1", "dns_builtin_standard")); rec.Code != 200 { // one budget for both picks
		t.Fatalf("second: %d %s", rec.Code, rec.Body.String())
	}
	if rec := c.post("/dns", linkBody("dns_builtin_family")); rec.Code != 429 || decodeLink(t, rec).Error != "too_many_requests" {
		t.Errorf("over the budget: %d %s", rec.Code, rec.Body.String())
	}
	must(b.svc.SetUsersEnabled(b.ctx, connect.NewRequest(&adminv1.SetUsersEnabledRequest{UserIds: []string{b.userID("bob")}, Enabled: false})))
	clock = clock.Add(61 * time.Minute)
	if rec := c.post("/dns", linkBody("dns_builtin_family")); rec.Code != 409 || decodeLink(t, rec).Error != "user_inactive" {
		t.Errorf("inactive: %d %s", rec.Code, rec.Body.String())
	}
}

// The page names the built-in presets in the language of the browser and carries their stock words in ru, en and zh, so a page
// switched to another language than the browser's shows its own. A renamed preset and an owner's own one have one text.
func TestPresetNamesInEveryPageLanguage(t *testing.T) {
	r := newDNSRig(t, nil)
	must(0, r.st.DNS().SetLinkOptions(r.ctx, []string{"dns_builtin_adblock", "dns_builtin_family"}, ""))
	r.st.W.Exec(`UPDATE dns_preset SET name = 'Семья' WHERE id = 'dns_builtin_family'`)
	_, tok := r.newUser("alice", nil)
	d, _ := r.page(tok, "Accept-Language", "en-US,en;q=0.9")
	byID := map[string]map[string]any{}
	for _, p := range arr(d["dns_presets"]) {
		byID[obj(p)["id"].(string)] = obj(p)
	}
	ad := byID["dns_builtin_adblock"]
	if ad["name"] != "AdGuard: no ads" || obj(ad["names"])["ru"] != "AdGuard: без рекламы" || obj(ad["names"])["en"] != "AdGuard: no ads" || obj(ad["names"])["zh"] != "AdGuard：无广告" ||
		obj(ad["descriptions"])["ru"] != "AdGuard DNS: без рекламы и трекеров" || obj(ad["descriptions"])["zh"] != "AdGuard DNS：无广告，无跟踪器" {
		t.Errorf("a stock preset: %v", ad)
	}
	if fam := byID["dns_builtin_family"]; fam["name"] != "Семья" || fam["names"] != nil || obj(fam["descriptions"])["ru"] == nil {
		t.Errorf("a renamed preset keeps its name in every language: %v", fam)
	}
	if std := byID["dns_builtin_standard"]; std == nil || std["names"] != nil { // the same name in every language
		t.Errorf("Cloudflare + Google: %v", std)
	}
	// The answer of a pick per server carries the server only: the names are on the page already.
	rec := (call{r.h, t, tok}).post("/dns", pickBody("nod_1", "dns_builtin_family"))
	if a := decodeLink(t, rec); rec.Code != 200 || a.Link != nil || a.Server["id"] != "nod_1" {
		t.Errorf("a pick per server: %d %s", rec.Code, rec.Body.String())
	}
}
