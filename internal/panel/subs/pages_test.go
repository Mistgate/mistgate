package subs_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

const (
	pageScript = `console.log("page")`
	pageHTML   = `<!doctype html><html><head><meta charset="utf-8"><title>t</title><style>body{margin:0}</style></head>` +
		`<body><div id="root"></div><!--MG_DATA--><script type="module">` + pageScript + `</script></body></html>`

	chrome  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36"
	happUA  = "Happ/2.1.0/ios"
	curlUA  = "curl/8.5.0"
	unknown = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFG" // 43 chars, like an issued token, but nobody's
)

var pageDist = fstest.MapFS{"sub.html": {Data: []byte(pageHTML)}}

// fakeRouting counts calls and returns a recognisable header value.
type fakeRouting struct{ calls atomic.Int32 }

func (f *fakeRouting) HappRouting(_ context.Context, userID string) (string, error) {
	f.calls.Add(1)
	return "happ://routing/onadd/" + base64.StdEncoding.EncodeToString([]byte(userID)), nil
}

// handler builds a handler over the rig with the settings cache and brand of the rig's database.
func (r *rig) handler(mut func(*subs.Config)) (http.Handler, *subsettings.Cache) {
	cache := subsettings.NewCache(r.st, nil)
	cfg := subs.Config{
		Title: "Fallback", BaseURL: "https://sub.example.com/k3xq8", Settings: cache, BrandTTL: -1, // no brand cache: a rename is seen at once
		Brand: func(ctx context.Context) (instance.Settings, error) { return instance.Load(ctx, r.st) },
		Dist:  pageDist,
	}
	if mut != nil {
		mut(&cfg)
	}
	return subs.Handler(r.svc, decoy, cfg), cache
}

func fetch(h http.Handler, path, ua string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func unb64(t *testing.T, h string) string {
	t.Helper()
	v, ok := strings.CutPrefix(h, "base64:")
	if !ok {
		t.Fatalf("header %q is not base64:", h)
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The subscription name comes from the brand at request time: a rename shows up without a restart.
func TestProfileTitleFollowsTheBrandWithoutRestart(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, cache := r.handler(nil)
	_, tok := r.user("alice", nil)

	if got := unb64(t, fetch(h, "/"+tok, curlUA).Header().Get("Profile-Title")); got != "Mistgate" {
		t.Errorf("default brand: title %q", got)
	}
	head, tail := "harbor", "light"
	if _, err := instance.Update(r.ctx, r.st, instance.Patch{BrandHead: &head, BrandTail: &tail}); err != nil {
		t.Fatal(err)
	}
	if got := unb64(t, fetch(h, "/"+tok, curlUA).Header().Get("Profile-Title")); got != "harborlight" {
		t.Errorf("after the rename: title %q, want harborlight", got)
	}
	// A title in the settings wins over the brand, non-ASCII included (base64 keeps the header ASCII).
	set := subsettings.Defaults()
	set.Title = "Кот и туман 🐈"
	if _, err := cache.Update(r.ctx, set); err != nil {
		t.Fatal(err)
	}
	rec := fetch(h, "/"+tok, curlUA)
	if got := unb64(t, rec.Header().Get("Profile-Title")); got != set.Title {
		t.Errorf("settings title: %q", got)
	}
	for k, vs := range rec.Header() {
		for _, v := range vs {
			if strings.ContainsFunc(v, func(c rune) bool { return c > 0x7e || c < 0x20 }) {
				t.Errorf("header %s carries non-ASCII or control bytes: %q", k, v)
			}
		}
	}
}

func TestSubscriptionHeadersFromSettings(t *testing.T) {
	r := newRig(t, "/k3xq8")
	rt := &fakeRouting{}
	h, cache := r.handler(func(c *subs.Config) { c.Routing = rt })
	uid, tok := r.user("alice", nil)

	set := subsettings.Defaults()
	set.Announcement = strings.Repeat("я", 250) + "🐈"
	set.SupportUrl = "https://t.me/example_support"
	set.UpdateIntervalHours = 6
	if _, err := cache.Update(r.ctx, set); err != nil {
		t.Fatal(err)
	}
	rec := fetch(h, "/"+tok, happUA)
	hd := rec.Header()
	if ann := unb64(t, hd.Get("Announce")); ann != strings.Repeat("я", 199)+"…" {
		t.Errorf("announce is %q (%d runes), want what Happ shows: 200 with the ellipsis", ann, len([]rune(ann)))
	}
	if hd.Get("Support-Url") != "https://t.me/example_support" {
		t.Errorf("support-url %q", hd.Get("Support-Url"))
	}
	if hd.Get("Profile-Update-Interval") != "6" {
		t.Errorf("interval %q", hd.Get("Profile-Update-Interval"))
	}
	if hd.Get("Profile-Web-Page-Url") != "https://sub.example.com/k3xq8/"+tok {
		t.Errorf("profile-web-page-url %q", hd.Get("Profile-Web-Page-Url"))
	}
	if want := "happ://routing/onadd/" + base64.StdEncoding.EncodeToString([]byte(uid)); hd.Get("Routing") != want {
		t.Errorf("routing %q, want %q", hd.Get("Routing"), want)
	}

	// Only Happ-like clients get the routing header; other clients never cost a routing computation.
	before := rt.calls.Load()
	for _, ua := range []string{curlUA, "v2rayNG/1.9", "mihomo/1.19", ""} {
		if got := fetch(h, "/"+tok, ua).Header().Get("Routing"); got != "" {
			t.Errorf("UA %q got a routing header", ua)
		}
	}
	if rt.calls.Load() != before {
		t.Error("routing was computed for a client that does not read it")
	}

	// No announcement, no support link: no headers; the default interval is 12 hours.
	if _, err := cache.Update(r.ctx, subsettings.Defaults()); err != nil {
		t.Fatal(err)
	}
	rec = fetch(h, "/"+tok, curlUA)
	if rec.Header().Get("Announce") != "" || rec.Header().Get("Support-Url") != "" || rec.Header().Get("Profile-Update-Interval") != "12" {
		t.Errorf("defaults: %v", rec.Header())
	}

	// A user without access gets no server but the one entry that says why (the announcement says it too), the short
	// interval and no routing header.
	lim, limTok := r.user("limited", func(m *adminv1.CreateUserRequest) { m.QuotaBytes = 10 })
	r.st.W.Exec(`UPDATE user SET used_bytes = 10 WHERE id = ?`, lim)
	if err := r.svc.Recompute(r.ctx, []string{lim}); err != nil {
		t.Fatal(err)
	}
	rec = fetch(h, "/"+limTok, happUA)
	names := fragmentsOf(t, rec)
	if len(names) != 1 || !strings.HasPrefix(names[0], "Traffic used up, resets on ") || !strings.HasPrefix(decode(t, rec.Body.String()), "hysteria2://off@0.0.0.0:1/#") ||
		unb64(t, rec.Header().Get("Announce")) != names[0] || rec.Header().Get("Profile-Update-Interval") != "1" || rec.Header().Get("Routing") != "" {
		t.Errorf("limited: list %q headers %v", names, rec.Header())
	}
}

func fragmentsOf(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(decode(t, rec.Body.String()), "\n") {
		_, frag, ok := strings.Cut(l, "#")
		if !ok {
			t.Fatalf("no fragment in %q", l)
		}
		dec, err := url.PathUnescape(frag)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(frag, " 🇩🇪·") {
			t.Errorf("fragment %q is not percent-encoded", frag)
		}
		out = append(out, dec)
	}
	return out
}

// Server names: flag and compact country code by default, the template from the settings (an edit shows at once even
// though the token's data is cached), a number for a name that repeats.
func TestServerNamesInTheSubscription(t *testing.T) {
	const de = "\U0001F1E9\U0001F1EA"
	r := newRig(t, "/k3xq8")
	if _, err := r.st.W.Exec(`UPDATE node SET country_code = 'DE' WHERE id = 'nod_1'`); err != nil {
		t.Fatal(err)
	}
	h, cache := r.handler(nil)
	_, tok := r.user("alice", nil)

	if got := fragmentsOf(t, fetch(h, "/"+tok, curlUA)); len(got) != 1 || got[0] != de+" DE · p" {
		t.Fatalf("default name = %q, want flag, compact country code, and profile", got)
	}
	if got := fragmentsOf(t, fetch(h, "/"+tok, happUA)); len(got) != 1 || got[0] != de+" DE · p" {
		t.Fatalf("Happ name = %q, want flag, compact country code, and profile", got)
	}
	ru := "ru"
	if _, err := instance.Update(r.ctx, r.st, instance.Patch{Language: &ru}); err != nil {
		t.Fatal(err)
	}
	if got := fragmentsOf(t, fetch(h, "/"+tok, curlUA)); len(got) != 1 || got[0] != de+" DE · p" {
		t.Fatalf("a Russian instance: %q", got)
	}
	en := "en"
	if _, err := instance.Update(r.ctx, r.st, instance.Patch{Language: &en}); err != nil {
		t.Fatal(err)
	}
	// An edit of the template is visible immediately (only the token's data is cached, not the rendering).
	set := subsettings.Defaults()
	set.ServerNameTemplate = "{country} · {node}"
	if _, err := cache.Update(r.ctx, set); err != nil {
		t.Fatal(err)
	}
	if got := fragmentsOf(t, fetch(h, "/"+tok, curlUA)); len(got) != 1 || got[0] != "DE · de1" {
		t.Errorf("edited template: %q", got)
	}

	// A second profile on the same node: both names follow the profile labels saved in the panel.
	prof2 := must(r.svc.CreateProfile(r.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "hysteria2", Name: "second", SettingsJson: secondSettings(t, r)}))).Msg.Profile
	must(r.svc.CreateInbound(r.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: prof2.Id, NodeId: "nod_1"})))
	g := must(r.svc.CreateGroup(r.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "g2", ProfileIds: []string{prof2.Id, r.profile}}))).Msg.Group.Id
	_, tok2 := r.user("bob", func(m *adminv1.CreateUserRequest) { m.GroupId = g })
	if _, err := cache.Update(r.ctx, subsettings.Defaults()); err != nil {
		t.Fatal(err)
	}
	got := fragmentsOf(t, fetch(h, "/"+tok2, curlUA))
	want := map[string]bool{de + " DE · p": true, de + " DE · second": true}
	if len(got) != len(want) || !want[got[0]] || !want[got[1]] {
		t.Errorf("two profiles on one node: %q", got)
	}
}

// A server added later never takes the name of an older one: the list is sorted by country and node name (here the new
// nodes' names sort first), but repeated names are numbered in the order the servers were made.
func TestServerNamesKeepTheirNumberWhenAServerIsAdded(t *testing.T) {
	const de = "\U0001F1E9\U0001F1EA"
	r := newRig(t, "/k3xq8")
	if _, err := r.st.W.Exec(`UPDATE node SET country_code = 'DE' WHERE id = 'nod_1'`); err != nil {
		t.Fatal(err)
	}
	h, _ := r.handler(nil)
	_, tok := r.user("alice", nil)
	if got := fragmentsOf(t, fetch(h, "/"+tok, curlUA)); strings.Join(got, "|") != de+" DE · p" {
		t.Fatalf("before: %q", got)
	}
	for _, name := range []string{"ade0", "ade00"} {
		if _, err := r.st.W.Exec(`INSERT INTO node (id, name, address, country_code, state, created_at) VALUES (?, ?, ?, 'DE', 'active', 1)`, "nod_"+name, name, name+".example.com"); err != nil {
			t.Fatal(err)
		}
		must(r.svc.CreateInbound(r.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: r.profile, NodeId: "nod_" + name})))
	}
	_, tok = r.user("bob", nil) // a token whose data is not cached yet
	got := fragmentsOf(t, fetch(h, "/"+tok, curlUA))
	if strings.Join(got, "|") != de+" DE · p 2|"+de+" DE · p 3|"+de+" DE · p" {
		t.Fatalf("after two servers were added: %q", got)
	}
	// listed by node name: the new servers before the old one, which kept its name
	lines := strings.Split(decode(t, fetch(h, "/"+tok, curlUA).Body.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "@ade0.example.com") || !strings.Contains(lines[1], "@ade00.example.com") || !strings.Contains(lines[2], "@de1.example.com") {
		t.Fatalf("order: %q", lines)
	}
}

// secondSettings is the defaults of a hysteria2 profile on another port (two profiles may share a node).
func secondSettings(t *testing.T, r *rig) string {
	t.Helper()
	ps := must(r.svc.ListProtocols(r.ctx, connect.NewRequest(&adminv1.ListProtocolsRequest{}))).Msg.Protocols
	var m map[string]any
	for _, p := range ps {
		if p.Id == "hysteria2" {
			if err := json.Unmarshal([]byte(p.DefaultSettingsJson), &m); err != nil {
				t.Fatal(err)
			}
		}
	}
	m["port"] = 8443
	b, _ := json.Marshal(m)
	return string(b)
}

var dataRe = regexp.MustCompile(`(?s)<script type="application/json" id="mg-data">(.*?)</script>`)

func pageData(t *testing.T, body string) (map[string]any, string) {
	t.Helper()
	m := dataRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no mg-data block in %.300q", body)
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(m[1]), &d); err != nil {
		t.Fatalf("mg-data is not JSON: %v\n%s", err, m[1])
	}
	return d, m[1]
}

func TestUserPageForBrowsers(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, cache := r.handler(nil)
	set := subsettings.Defaults()
	set.Announcement = "Maintenance tonight"
	set.SupportUrl = "https://t.me/example_support"
	if _, err := cache.Update(r.ctx, set); err != nil {
		t.Fatal(err)
	}
	_, tok := r.user("alice", func(m *adminv1.CreateUserRequest) { m.QuotaBytes = 5000; m.TermDays = 30 })

	rec := fetch(h, "/"+tok, chrome, "Accept-Language", "ru-RU,ru;q=0.9")
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("browser: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	hd := rec.Header()

	// The CSP allows exactly the inline script that is served: its hash, nothing else for scripts.
	sum := sha256.Sum256([]byte(pageScript))
	wantCSP := "default-src 'none'; script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; style-src 'unsafe-inline'; " +
		"img-src 'self' data:; font-src data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	if hd.Get("Content-Security-Policy") != wantCSP {
		t.Errorf("csp\n got %s\nwant %s", hd.Get("Content-Security-Policy"), wantCSP)
	}
	m := regexp.MustCompile(`(?s)<script type="module">(.*?)</script>`).FindStringSubmatch(body)
	if m == nil || m[1] != pageScript {
		t.Fatalf("the served inline script differs from the built one: %v", m)
	}
	if got := sha256.Sum256([]byte(m[1])); !bytes.Equal(got[:], sum[:]) {
		t.Error("the hash of the served script does not match")
	}
	if strings.Count(body, "<script") != 2 || strings.Contains(body, "<!--MG_DATA-->") {
		t.Errorf("unexpected scripts or the marker left in: %s", body)
	}
	if hd.Get("Cache-Control") != "no-store" || hd.Get("X-Content-Type-Options") != "nosniff" || hd.Get("Referrer-Policy") != "no-referrer" ||
		hd.Get("X-Robots-Tag") == "" || hd.Get("Subscription-Userinfo") != "" {
		t.Errorf("headers: %v", hd)
	}
	if strings.Contains(body, "src=") || strings.Contains(body, "href=") {
		t.Error("the page must not reference any asset")
	}

	d, _ := pageData(t, body)
	if d["v"] != float64(1) || d["lang"] != "ru" || d["title"] != "Mistgate" || d["subscription_url"] != "https://sub.example.com/k3xq8/"+tok ||
		d["announcement"] != "Maintenance tonight" || d["support_url"] != "https://t.me/example_support" || d["amnezia"] != nil ||
		d["server_count"] != float64(1) {
		t.Errorf("data: %v", d)
	}
	u := d["user"].(map[string]any)
	if u["name"] != "alice" || u["status"] != "active" || u["quota_bytes"] != float64(5000) || u["used_bytes"] != float64(0) || u["quota_reset"] != "month" ||
		u["expires_unix"].(float64) == 0 || u["devices_used"] != float64(1) {
		t.Errorf("user: %v", u)
	}
	if acc := d["access"].(map[string]any); acc["happ"] != true || acc["amnezia"] != false {
		t.Errorf("access: %v", acc)
	}
	if dv := d["devices"].([]any); len(dv) != 1 || dv[0].(map[string]any)["app"] != "happ" {
		t.Errorf("devices: %v", d["devices"])
	}
	var ios map[string]any
	for _, a := range d["apps"].([]any) {
		if a := a.(map[string]any); a["platform"] == "ios" && a["kind"] == "happ" {
			ios = a
		}
	}
	if ios == nil || ios["name"] != "Happ" || ios["add_url"] != "happ://add/https://sub.example.com/k3xq8/"+tok || !strings.HasPrefix(ios["download_url"].(string), "https://") {
		t.Errorf("ios app: %v", ios)
	}
	if b := d["brand"].(map[string]any); b["accent"] != "#b8acf2" || len(b["parts"].([]any)) != 2 {
		t.Errorf("brand: %v", b)
	}
	if o := d["options"].(map[string]any); o["show_qr"] != true || o["show_announcement"] != true {
		t.Errorf("options: %v", o)
	}

	// The language: Accept-Language first, else the instance language.
	if d, _ := pageData(t, fetch(h, "/"+tok, chrome).Body.String()); d["lang"] != "en" {
		t.Errorf("instance language: %v", d["lang"])
	}

	// Non-browsers never get the page; a rule can send them there, and can hide the link from a client.
	if rec := fetch(h, "/"+tok, curlUA); strings.Contains(rec.Body.String(), "<script") || rec.Header().Get("Content-Security-Policy") != "" {
		t.Error("curl got the page")
	}
	set.Rules = []*adminv1.ServeRule{
		{UaContains: "curl", Format: adminv1.SubFormat_SUB_FORMAT_USER_PAGE},
		{UaContains: "chrome", Format: adminv1.SubFormat_SUB_FORMAT_DECOY},
	}
	if _, err := cache.Update(r.ctx, set); err != nil {
		t.Fatal(err)
	}
	if rec := fetch(h, "/"+tok, curlUA); !strings.Contains(rec.Body.String(), `id="mg-data"`) {
		t.Error("the rule did not send curl to the page")
	}
	if rec := fetch(h, "/"+tok, chrome); rec.Code != 404 || rec.Body.String() != decoyBody {
		t.Errorf("the decoy rule: %d %q", rec.Code, rec.Body.String())
	}
}

func TestSubscriptionPageUsesSeparateNameAndKeepsAccountName(t *testing.T) {
	r := newRig(t, "/k3xq8")
	uid, tok := r.user("internal-alias", nil)
	for _, invalidName := range []string{strings.Repeat("x", 65), "two\nlines"} {
		if _, err := r.svc.UpdateUser(r.ctx, connect.NewRequest(&adminv1.UpdateUserRequest{
			UserId: uid, SubscriptionName: &invalidName,
		})); err == nil {
			t.Errorf("accepted invalid subscription name %q", invalidName)
		}
	}
	publicName := "Алина"
	updated, err := r.svc.UpdateUser(r.ctx, connect.NewRequest(&adminv1.UpdateUserRequest{
		UserId: uid, SubscriptionName: &publicName,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Msg.User.Name != "internal-alias" || updated.Msg.User.SubscriptionName != publicName {
		t.Fatalf("admin user = (%q, %q), want internal and public names to stay separate", updated.Msg.User.Name, updated.Msg.User.SubscriptionName)
	}

	v, err := r.svc.Subscription(r.ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if v.UserName != "internal-alias" || v.SubscriptionName != publicName {
		t.Fatalf("subscription view names = (%q, %q)", v.UserName, v.SubscriptionName)
	}

	h, _ := r.handler(nil)
	d, _ := pageData(t, fetch(h, "/"+tok, chrome).Body.String())
	if got := d["user"].(map[string]any)["name"]; got != publicName {
		t.Fatalf("public page name = %v, want %q", got, publicName)
	}

	// Clearing the custom name restores the current account name on the public page.
	blank := ""
	if _, err := r.svc.UpdateUser(r.ctx, connect.NewRequest(&adminv1.UpdateUserRequest{UserId: uid, SubscriptionName: &blank})); err != nil {
		t.Fatal(err)
	}
	// Each handler caches a rendered subscription response briefly; a fresh handler models the next uncached page fetch.
	h, _ = r.handler(nil)
	d, _ = pageData(t, fetch(h, "/"+tok, chrome).Body.String())
	if got := d["user"].(map[string]any)["name"]; got != "internal-alias" {
		t.Fatalf("fallback page name = %v, want internal account name", got)
	}
}

// A name that tries to end the data block must stay data: no second script, JSON intact.
func TestUserPageDataCannotBreakOut(t *testing.T) {
	const evil = `</script><script>alert(1)</script><!-- & "quotes" ' \ ` + "  x"
	r := newRig(t, "/k3xq8")
	h, cache := r.handler(nil)
	set := subsettings.Defaults()
	set.Title = evil
	set.Announcement = evil
	if _, err := cache.Update(r.ctx, set); err != nil {
		t.Fatal(err)
	}
	_, tok := r.user(evil, nil)

	rec := fetch(h, "/"+tok, chrome)
	body := rec.Body.String()
	if strings.Contains(body, "alert(1)</script>") || strings.Contains(body, "</script><script>alert") || strings.Contains(body, "<!-- &") {
		t.Fatalf("hostile text reached the page unescaped:\n%s", body)
	}
	if strings.Count(body, "<script") != 2 || strings.Count(body, "</script>") != 2 {
		t.Errorf("the number of script elements changed: %s", body)
	}
	d, raw := pageData(t, body)
	if strings.ContainsAny(raw, "<>&  ") {
		t.Errorf("data block holds raw markup characters: %s", raw)
	}
	if d["user"].(map[string]any)["name"] != evil || d["title"] != evil || d["announcement"] != evil {
		t.Errorf("the text did not survive the round trip: %v", d["user"])
	}
}

// Without a usable sub.html browsers get the base64 list, and the reason is logged once.
func TestUserPageMissingFallsBackToBase64AndLogsOnce(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"missing":          {},
		"no marker":        {"sub.html": {Data: []byte(`<html><script type="module">1</script></html>`)}},
		"two markers":      {"sub.html": {Data: []byte(`<!--MG_DATA--><!--MG_DATA--><script type="module">1</script>`)}},
		"external script":  {"sub.html": {Data: []byte(`<!--MG_DATA--><script type="module" src="/a.js"></script>`)}},
		"linked style":     {"sub.html": {Data: []byte(`<link rel="stylesheet" href="a.css"><!--MG_DATA--><script type="module">1</script>`)}},
		"two inline":       {"sub.html": {Data: []byte(`<!--MG_DATA--><script type="module">1</script><script type="module">2</script>`)}},
		"classic inline":   {"sub.html": {Data: []byte(`<!--MG_DATA--><script>alert(1)</script>`)}},
		"no script at all": {"sub.html": {Data: []byte(`<!--MG_DATA-->`)}},
	}
	for name, dist := range cases {
		r := newRig(t, "/k3xq8")
		var logs bytes.Buffer
		h, _ := r.handler(func(c *subs.Config) { c.Dist = dist; c.Log = slog.New(slog.NewTextHandler(&logs, nil)) })
		_, tok := r.user("alice", nil)
		for range 3 {
			rec := fetch(h, "/"+tok, chrome)
			if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") || strings.HasPrefix(decode(t, rec.Body.String()), "<") {
				t.Fatalf("%s: %d %s", name, rec.Code, rec.Header().Get("Content-Type"))
			}
		}
		if n := strings.Count(logs.String(), "user page is not available"); n != 1 {
			t.Errorf("%s: logged %d times, want once:\n%s", name, n, logs.String())
		}
		if strings.Contains(logs.String(), tok) {
			t.Errorf("%s: the token is in the log", name)
		}
	}
}

// An unknown or blocked token behaves exactly like the decoy, for a browser too: nothing of the page, its CSP
// or any settings-driven header shows.
func TestUnknownTokenStaysDecoyForBrowsers(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, _ := r.handler(nil)
	_, tok := r.user("alice", nil)
	wrong := tok[:len(tok)-1] + map[bool]string{true: "A", false: "B"}[tok[len(tok)-1] != 'A']

	ref := fetch(h, "/not-a-token", chrome) // any path the decoy answers
	for name, path := range map[string]string{"unknown token": "/" + unknown, "wrong last char": "/" + wrong, "valid shape under prefix": "/k3xq8/" + unknown} {
		rec := fetch(h, path, chrome)
		if rec.Code != ref.Code || rec.Body.String() != ref.Body.String() || rec.Body.String() != decoyBody {
			t.Errorf("%s: %d %q differs from the decoy", name, rec.Code, rec.Body.String())
		}
		if len(rec.Header()) != len(ref.Header()) || rec.Header().Get("Content-Security-Policy") != "" || rec.Header().Get("Profile-Title") != "" {
			t.Errorf("%s: headers %v, decoy %v", name, rec.Header(), ref.Header())
		}
	}
	// The same after the client network was blocked for guessing: even the valid token gets the decoy.
	h2, _ := r.handler(func(c *subs.Config) {
		c.MissLimit = 2
		c.ClientIP = func(*http.Request) netip.Addr { return netip.MustParseAddr("192.0.2.7") }
	})
	for range 2 {
		fetch(h2, "/"+unknown, chrome)
	}
	if rec := fetch(h2, "/"+tok, chrome); rec.Code != 404 || rec.Body.String() != decoyBody {
		t.Errorf("blocked client with a valid token: %d %q", rec.Code, rec.Body.String())
	}
}

// The admin preview renders the same page for a user id, framed by the admin (frame-ancestors 'self').
func TestPreviewHandler(t *testing.T) {
	r := newRig(t, "/k3xq8")
	id, tok := r.user("alice", nil)
	pv := subs.PreviewHandler(r.svc, subs.Config{Title: "Fallback", Settings: subsettings.NewCache(r.st, nil), Dist: pageDist})

	rec := httptest.NewRecorder()
	pv(rec, httptest.NewRequest("GET", "/preview/user-page/"+id, nil), id)
	if rec.Code != 200 {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'self'") || strings.Contains(csp, "frame-ancestors 'none'") || rec.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Errorf("framing: csp %q xfo %q", csp, rec.Header().Get("X-Frame-Options"))
	}
	d, _ := pageData(t, rec.Body.String())
	if d["subscription_url"] != "https://sub.example.com/k3xq8/"+tok || d["user"].(map[string]any)["name"] != "alice" {
		t.Errorf("preview data: %v", d)
	}

	rec = httptest.NewRecorder()
	pv(rec, httptest.NewRequest("GET", "/preview/user-page/usr_nobody", nil), "usr_nobody")
	if rec.Code != 404 || strings.Contains(rec.Body.String(), "mg-data") {
		t.Errorf("unknown user: %d", rec.Code)
	}
}
