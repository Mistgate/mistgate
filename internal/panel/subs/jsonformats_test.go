package subs_test

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The Xray JSON array and the sing-box profile through the real handler, access module and database: one hysteria2 and one
// AmneziaWG profile on node de1 (the AWG key has no place in either format).

const (
	sfaUA     = "SFA (sing-box 1.14.3; language en_US)"
	v2rayNGUA = "v2rayNG/1.10.20"
	hiddifyUA = "HiddifyNext/2.5.7 (windows) like ClashMeta v2ray sing-box"
)

type xrayDoc []struct {
	Remarks   string `json:"remarks"`
	DNS       any    `json:"dns"`
	Inbounds  []struct{ Port int }
	Outbounds []map[string]any `json:"outbounds"`
}

func parseXray(t *testing.T, b []byte) xrayDoc {
	t.Helper()
	var d xrayDoc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("not an Xray JSON array: %v\n%s", err, b)
	}
	return d
}

type singboxDoc struct {
	DNS       map[string]any   `json:"dns"`
	Inbounds  []map[string]any `json:"inbounds"`
	Outbounds []map[string]any `json:"outbounds"`
	Route     map[string]any   `json:"route"`
}

func parseSingbox(t *testing.T, b []byte) singboxDoc {
	t.Helper()
	var d singboxDoc
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("not a sing-box profile: %v\n%s", err, b)
	}
	return d
}

// xrayAuth and singboxAuth read the credential of the one Hysteria2 server of a document.
func xrayAuth(t *testing.T, b []byte) string {
	t.Helper()
	d := parseXray(t, b)
	if len(d) != 1 {
		t.Fatalf("%d configs, want the hysteria2 server alone", len(d))
	}
	ss := d[0].Outbounds[0]["streamSettings"].(map[string]any)
	return ss["hysteriaSettings"].(map[string]any)["auth"].(string)
}

func singboxAuth(t *testing.T, b []byte) string {
	t.Helper()
	d := parseSingbox(t, b)
	if len(d.Outbounds) != 3 { // selector, the server, direct
		t.Fatalf("outbounds %v", d.Outbounds)
	}
	return d.Outbounds[1]["password"].(string)
}

func TestJSONFormatsFollowTheUserAgentAndTheHiddenParameter(t *testing.T) {
	m := newM3Rig(t)
	h, cache := m.handler(func(c *subs.Config) { c.MinInterval = -1 })
	_, tok := m.newUser("alice", nil)

	// The sing-box apps are caught by the default rule `sing-box`; Hiddify, which also says "sing-box", keeps the Mihomo profile
	// (its `ClashMeta` is met first).
	rec := fetch(h, "/"+tok, sfaUA)
	d := parseSingbox(t, rec.Body.Bytes())
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") || len(d.Outbounds) != 3 || d.Outbounds[1]["type"] != "hysteria2" ||
		d.Inbounds[0]["type"] != "tun" {
		t.Fatalf("SFA: %d %v\n%s", rec.Code, rec.Header(), rec.Body.String())
	}
	if rec := fetch(h, "/"+tok, "SFI/1.14.3"); !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("a UA without `sing-box` gets %v", rec.Header().Get("Content-Type")) // the app names itself differently: the owner writes a rule
	}
	if rec := fetch(h, "/"+tok, hiddifyUA); !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/yaml") {
		t.Errorf("Hiddify: %v", rec.Header().Get("Content-Type"))
	}
	// Happ and v2rayNG keep the list; the hidden parameter gives them a JSON format for the request.
	for _, ua := range []string{happUA, v2rayNGUA} {
		if rec := fetch(h, "/"+tok, ua); !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
			t.Errorf("%s: %v", ua, rec.Header().Get("Content-Type"))
		}
		rec := fetch(h, "/"+tok+"?format=xray-json", ua)
		x := parseXray(t, rec.Body.Bytes())
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") || len(x) != 1 || x[0].Outbounds[0]["protocol"] != "hysteria" ||
			x[0].Inbounds[0].Port != 10808 || x[0].Remarks != "p" {
			t.Errorf("%s ?format=xray-json: %d %s", ua, rec.Code, rec.Body.String())
		}
	}
	if d := parseSingbox(t, fetch(h, "/"+tok+"?format=sing-box", happUA).Body.Bytes()); d.Outbounds[0]["type"] != "selector" {
		t.Errorf("?format=sing-box: %v", d)
	}
	// The parameter overrides a rule of the other format, and is read like a rule for a browser: no page.
	if rec := fetch(h, "/"+tok+"?format=xray-json", sfaUA); !strings.HasPrefix(rec.Body.String(), "[") {
		t.Errorf("?format over a sing-box rule: %s", rec.Body.String())
	}
	if rec := fetch(h, "/"+tok+"?format=xray-json", chrome); !strings.HasPrefix(rec.Body.String(), "[") {
		t.Errorf("?format in a browser: %s", rec.Body.String())
	}
	// Anything else in the query is ignored.
	for _, q := range []string{"?format=", "?format=mihomo", "?format=XRAY-JSON", "?format=singbox", "?format=xray-json%00", "?other=xray-json", "?format[]=xray-json"} {
		if rec := fetch(h, "/"+tok+q, happUA); !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
			t.Errorf("%s changed the format: %v", q, rec.Header().Get("Content-Type"))
		}
	}
	if rec := fetch(h, "/"+tok+"?format=sing-box", chrome); !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("browser with ?format=sing-box: %v", rec.Header().Get("Content-Type"))
	}

	// A rule of the owner does the same without the parameter.
	set := subsettings.Defaults()
	set.Rules = append([]*adminv1.ServeRule{{UaContains: "v2rayng", Format: adminv1.SubFormat_SUB_FORMAT_XRAY_JSON}}, set.Rules...)
	if _, err := cache.Update(m.ctx, set); err != nil {
		t.Fatal(err)
	}
	if rec := fetch(h, "/"+tok, v2rayNGUA); !strings.HasPrefix(rec.Body.String(), "[") {
		t.Errorf("rule to Xray JSON: %s", rec.Body.String())
	}
}

// The parameter grants nothing a User-Agent rule could not: a decoy rule stays the decoy, and an unknown token gets the decoy
// whatever the query says (and counts as a miss).
func TestHiddenFormatParameterGrantsNothing(t *testing.T) {
	m := newM3Rig(t)
	h, cache := m.handler(nil)
	_, tok := m.newUser("alice", nil)
	set := subsettings.Defaults()
	set.Rules = append([]*adminv1.ServeRule{{UaContains: "curl", Format: adminv1.SubFormat_SUB_FORMAT_DECOY}}, set.Rules...)
	if _, err := cache.Update(m.ctx, set); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"", "?format=xray-json", "?format=sing-box"} {
		if rec := fetch(h, "/"+tok+q, "curl/8.5.0"); rec.Code != 404 || rec.Body.String() != decoyBody {
			t.Errorf("decoy rule with %q: %d %q", q, rec.Code, rec.Body.String())
		}
	}
	unknown := "/" + strings.Repeat("u", 43)
	for _, q := range []string{"", "?format=xray-json", "?format=sing-box"} {
		for _, ua := range []string{sfaUA, happUA, chrome} {
			if rec := fetch(h, unknown+q, ua); rec.Code != 404 || rec.Body.String() != decoyBody {
				t.Errorf("unknown token with %q and %s: %d %q", q, ua, rec.Code, rec.Body.String())
			}
		}
	}
	// POST routes do not take the parameter either.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("PUT", "/"+tok+"?format=xray-json", nil))
	if rec.Code != 404 || rec.Body.String() != decoyBody {
		t.Errorf("PUT: %d", rec.Code)
	}
}

func TestJSONFormatHeaders(t *testing.T) {
	m := newM3Rig(t)
	h, _ := m.handler(func(c *subs.Config) { c.MinInterval = -1 })
	_, tok := m.newUser("alice", func(c *adminv1.CreateUserRequest) { c.QuotaBytes = 5000 })

	// The list still carries Happ's routing header (the control: the module is wired); no JSON format does.
	if rec := fetch(h, "/"+tok, happUA); rec.Header().Get("Routing") == "" {
		t.Fatal("the control: Happ gets no Routing header with the list")
	}
	title := "Mistgate"
	for name, c := range map[string]struct{ path, ua, ctype string }{
		"xray":                     {"/" + tok + "?format=xray-json", happUA, "application/json; charset=utf-8"},
		"xray ng":                  {"/" + tok + "?format=xray-json", v2rayNGUA, "application/json; charset=utf-8"},
		"sing-box":                 {"/" + tok, sfaUA, "application/json; charset=utf-8"},
		"sing-box under a Happ UA": {"/" + tok + "?format=sing-box", happUA, "application/json; charset=utf-8"},
	} {
		rec := fetch(h, c.path, c.ua)
		hd := rec.Header()
		if rec.Code != 200 || hd.Get("Content-Type") != c.ctype || hd.Get("Routing") != "" || hd.Get("Cache-Control") != "no-store" || hd.Get("X-Content-Type-Options") != "nosniff" ||
			unb64(t, hd.Get("Profile-Title")) != title || hd.Get("Profile-Update-Interval") != "12" || hd.Get("Profile-Web-Page-Url") != "https://sub.example.com/k3xq8/"+tok ||
			!strings.HasPrefix(hd.Get("Subscription-Userinfo"), "upload=0; download=0; total=5000") ||
			hd.Get("Content-Disposition") != "attachment; filename*=UTF-8''"+url.QueryEscape(title) || hd.Get("Vary") != "Accept-Encoding" {
			t.Errorf("%s: %d %v", name, rec.Code, hd)
		}
		if strings.Contains(rec.Body.String(), tok) {
			t.Errorf("%s: the token leaked into the document", name)
		}
	}

	// gzip when the client takes it, the same document plain; HEAD answers the headers.
	for name, c := range map[string]struct{ path, ua string }{"xray": {"/" + tok + "?format=xray-json", v2rayNGUA}, "sing-box": {"/" + tok, sfaUA}} {
		plain := fetch(h, c.path, c.ua)
		zipped := fetch(h, c.path, c.ua, "Accept-Encoding", "gzip, br")
		if zipped.Header().Get("Content-Encoding") != "gzip" || plain.Header().Get("Content-Encoding") != "" || string(gunzip(t, zipped.Body.Bytes())) != plain.Body.String() {
			t.Errorf("%s: gzip %v", name, zipped.Header())
		}
		if fetch(h, c.path, c.ua, "Accept-Encoding", "gzip;q=0").Header().Get("Content-Encoding") != "" {
			t.Errorf("%s: gzip with q=0", name)
		}
		head := httptest.NewRecorder()
		req := httptest.NewRequest("HEAD", c.path, nil)
		req.Header.Set("User-Agent", c.ua)
		h.ServeHTTP(head, req)
		if head.Code != 200 || head.Header().Get("Content-Type") != plain.Header().Get("Content-Type") {
			t.Errorf("%s: HEAD %d %v", name, head.Code, head.Header())
		}
	}
}

// The credentials are the device's, in both JSON formats: two HWIDs within MinInterval get different ones, each repeats from
// its cache, and a format is never served from the cache of another (the list, Xray JSON and sing-box of one token side by side).
func TestJSONFormatsKeepCredentialsPerDeviceAndPerFormat(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, _ := r.handler(func(c *subs.Config) { c.MinInterval = 10 * time.Minute; c.MaxPerHour = -1 })
	_, tok := r.user("alice", nil)

	xa := xrayAuth(t, fetch(h, "/"+tok+"?format=xray-json", v2rayNGUA, "X-HWID", hwidA).Body.Bytes())
	xb := xrayAuth(t, fetch(h, "/"+tok+"?format=xray-json", v2rayNGUA, "X-HWID", hwidB).Body.Bytes())
	xn := xrayAuth(t, fetch(h, "/"+tok+"?format=xray-json", v2rayNGUA).Body.Bytes())
	sa := singboxAuth(t, fetch(h, "/"+tok, sfaUA, "X-HWID", hwidA).Body.Bytes())
	sb := singboxAuth(t, fetch(h, "/"+tok, sfaUA, "X-HWID", hwidB).Body.Bytes())
	sn := singboxAuth(t, fetch(h, "/"+tok, sfaUA).Body.Bytes())
	la := decode(t, fetch(h, "/"+tok, happUA, "X-HWID", hwidA).Body.String())
	if xa == xb || xa == xn || xb == xn || sa == sb || sa == sn || sb == sn {
		t.Fatalf("the devices share credentials:\nxray %s %s %s\nsing-box %s %s %s", xa, xb, xn, sa, sb, sn)
	}
	// One device, one credential, whatever the format (the Hysteria2 password is the device's).
	if xa != sa || xb != sb || xn != sn || !strings.Contains(la, "hysteria2://"+xa+"@") {
		t.Errorf("a device has another credential in another format: %s %s | %s %s | %s", xa, sa, xb, sb, la)
	}
	// The second round comes from the caches and is the same.
	if got := xrayAuth(t, fetch(h, "/"+tok+"?format=xray-json", v2rayNGUA, "X-HWID", hwidB).Body.Bytes()); got != xb {
		t.Errorf("xray from the cache: %s, want %s", got, xb)
	}
	if got := singboxAuth(t, fetch(h, "/"+tok, sfaUA, "X-HWID", hwidA).Body.Bytes()); got != sa {
		t.Errorf("sing-box from the cache: %s, want %s", got, sa)
	}
	// And a format asked for after another one still gets its own document.
	if rec := fetch(h, "/"+tok+"?format=sing-box", v2rayNGUA, "X-HWID", hwidA); !strings.Contains(rec.Body.String(), `"selector"`) {
		t.Errorf("sing-box after xray: %s", rec.Body.String())
	}
	if rec := fetch(h, "/"+tok+"?format=xray-json", sfaUA, "X-HWID", hwidA); !strings.HasPrefix(rec.Body.String(), "[") {
		t.Errorf("xray after sing-box: %s", rec.Body.String())
	}
	var devices int
	r.st.R.QueryRow(`SELECT count(*) FROM device`).Scan(&devices)
	if devices != 3 {
		t.Errorf("%d devices, want three (A, B and the shared one)", devices)
	}
}

// A refused device gets the one entry in the format of its app, with the headers the apps read and an interval of one hour.
func TestJSONFormatsAnswerARefusalWithAnEntry(t *testing.T) {
	_, h, _, tok := refusalRig(t, store.LinkModeEnforce, 1)
	fetch(h, "/"+tok, happUA, "X-HWID", hwidA)
	const note = "Device limit: 1 of 1. Remove one you no longer use on the subscription page"
	for name, c := range map[string]struct{ path, ua string }{"xray": {"/" + tok + "?format=xray-json", v2rayNGUA}, "sing-box": {"/" + tok, sfaUA}} {
		rec := fetch(h, c.path, c.ua, "X-HWID", hwidB, "X-Device-OS", "Android")
		hd := rec.Header()
		if rec.Code != 200 || !strings.HasPrefix(hd.Get("Content-Type"), "application/json") || hd.Get("Profile-Update-Interval") != "1" || hd.Get("X-Hwid-Active") != "true" ||
			hd.Get("X-Hwid-Max-Devices-Reached") != "true" || hd.Get("X-Hwid-Limit") != "1" || unb64(t, hd.Get("Announce")) != note {
			t.Errorf("%s: %d %v", name, rec.Code, hd)
		}
		if !strings.Contains(rec.Body.String(), note) || strings.Contains(rec.Body.String(), "hysteria") && name == "xray" {
			t.Errorf("%s: the entry is not the placeholder: %s", name, rec.Body.String())
		}
		if name == "xray" {
			if d := parseXray(t, rec.Body.Bytes()); len(d) != 1 || d[0].Remarks != note || d[0].Outbounds[0]["protocol"] != "socks" {
				t.Errorf("xray placeholder: %s", rec.Body.String())
			}
		} else if d := parseSingbox(t, rec.Body.Bytes()); len(d.Outbounds) != 3 || d.Outbounds[1]["tag"] != note || d.Outbounds[1]["type"] != "socks" || d.DNS != nil {
			t.Errorf("sing-box placeholder: %s", rec.Body.String())
		}
	}
	// STRICT: an app that reports no device ID is told so, and Not-Supported is on.
	_, h2, _, tok2 := refusalRig(t, store.LinkModeStrict, 5)
	rec := fetch(h2, "/"+tok2, sfaUA)
	if rec.Code != 200 || rec.Header().Get("X-Hwid-Not-Supported") != "true" || !strings.Contains(unb64(t, rec.Header().Get("Announce")), "device ID") ||
		!strings.Contains(rec.Body.String(), "device ID") {
		t.Errorf("STRICT without HWID: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
}

// The end of access is the same entry as in the other formats; an active person with no server this format can carry gets the
// entry that says so, in the app's format, and the same words in the announcement. The page and the list are unchanged.
func TestJSONFormatsPlaceholders(t *testing.T) {
	m := newM3Rig(t)
	h, _ := m.handler(func(c *subs.Config) { c.MinInterval = -1 })
	// A group with the AmneziaWG key alone: nothing for either JSON format.
	awgOnly := must(m.svc.CreateGroup(m.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "keys", ProfileIds: []string{m.awg}}))).Msg.Group.Id
	_, tok := m.user("bob", func(c *adminv1.CreateUserRequest) { c.GroupId = awgOnly })
	const none = "Your servers do not work in this app. The subscription page shows which app does"
	for name, c := range map[string]struct{ path, ua string }{"xray": {"/" + tok + "?format=xray-json", v2rayNGUA}, "sing-box": {"/" + tok, sfaUA}} {
		rec := fetch(h, c.path, c.ua)
		if rec.Code != 200 || unb64(t, rec.Header().Get("Announce")) != none || !strings.Contains(rec.Body.String(), none) || rec.Header().Get("Profile-Update-Interval") != "12" {
			t.Errorf("%s: %d %v\n%s", name, rec.Code, rec.Header(), rec.Body.String())
		}
	}
	if len([]rune(none)) >= 200 {
		t.Error("the note does not fit the announcement")
	}
	// The link list of the same person stays what it was (empty: nothing for Happ either).
	if rec := fetch(h, "/"+tok, happUA); decode(t, rec.Body.String()) != "" || rec.Header().Get("Announce") != "" {
		t.Errorf("list: %q %v", decode(t, rec.Body.String()), rec.Header())
	}

	// Access ended: the same entry as the list gets, in both formats.
	id, tok2 := m.newUser("carol", nil)
	m.st.W.Exec(`UPDATE user SET disabled = 1 WHERE id = ?`, id)
	if err := m.svc.Recompute(m.ctx, []string{id}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/" + tok2 + "?format=xray-json", "/" + tok2} {
		ua := v2rayNGUA
		if !strings.Contains(path, "?") {
			ua = sfaUA
		}
		rec := fetch(h, path, ua)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Access paused") || unb64(t, rec.Header().Get("Announce")) != "Access paused" || rec.Header().Get("Profile-Update-Interval") != "1" {
			t.Errorf("%s: %d %v\n%s", path, rec.Code, rec.Header(), rec.Body.String())
		}
	}
}

// A person with the Hysteria2 server and the AWG key gets the server alone in either format.
func TestJSONFormatsLeaveOutTheKey(t *testing.T) {
	m := newM3Rig(t)
	h, _ := m.handler(func(c *subs.Config) { c.MinInterval = -1 })
	_, tok := m.newUser("alice", nil)
	var creds int
	x := parseXray(t, fetch(h, "/"+tok+"?format=xray-json", v2rayNGUA).Body.Bytes())
	s := parseSingbox(t, fetch(h, "/"+tok, sfaUA).Body.Bytes())
	m.st.R.QueryRow(`SELECT count(*) FROM device_credential WHERE protocol = 'awg'`).Scan(&creds)
	if len(x) != 1 || len(s.Outbounds) != 3 || creds != 0 {
		t.Errorf("xray %d configs, sing-box %d outbounds, %d AWG credentials issued (the JSON formats issue none)", len(x), len(s.Outbounds), creds)
	}
}

// The DNS preset of the person reaches both documents (the adblock preset has no split).
func TestJSONFormatsCarryTheDNSOfThePreset(t *testing.T) {
	m := newM3Rig(t)
	h, _ := m.handler(nil)
	_, tok := m.newUser("alice", func(c *adminv1.CreateUserRequest) { c.DnsPresetId = "dns_builtin_adblock" })
	x := fetch(h, "/"+tok+"?format=xray-json", v2rayNGUA).Body.String()
	s := fetch(h, "/"+tok, sfaUA).Body.String()
	if !strings.Contains(x, `"address":"94.140.14.14"`) || !strings.Contains(x, `"dns-out"`) || strings.Contains(x, `"domain:`) {
		t.Errorf("xray: %s", x)
	}
	if !strings.Contains(s, `"server":"94.140.14.14"`) || !strings.Contains(s, `"hijack-dns"`) || strings.Contains(s, `"domain_suffix"`) {
		t.Errorf("sing-box: %s", s)
	}
	// Without a DNS module there is no dns section in Xray JSON and only the system resolver in the sing-box profile.
	h2, _ := m.rig.handler(nil)
	x = fetch(h2, "/"+tok+"?format=xray-json", v2rayNGUA).Body.String()
	s = fetch(h2, "/"+tok, sfaUA).Body.String()
	if parseXray(t, []byte(x))[0].DNS != nil || strings.Contains(x, "dns-out") || !strings.Contains(s, `"servers":[{"type":"local","tag":"local"}]`) {
		t.Errorf("no dns module:\n%s\n%s", x, s)
	}
}

// Names are hostile input through the whole handler: node, profile and brand reach neither document as structure.
func TestJSONFormatsHostileNames(t *testing.T) {
	m := newM3Rig(t)
	m.st.W.Exec(`UPDATE node SET name = ? WHERE id = 'nod_1'`, "de1\"},{\"protocol\":\"freedom\",\"tag\":\"evil\"}\n</script>")
	m.st.W.Exec(`UPDATE profile SET name = ? WHERE id = ?`, "\"},{\"type\":\"direct\",\"tag\":\"evil\"}  ", m.profile)
	h, cache := m.handler(nil)
	set := subsettings.Defaults()
	set.Title = "Brand, \"Inc\" #1 & co=</script>"
	set.ServerNameTemplate = "{flag} {node} {country} {profile}"
	if _, err := cache.Update(m.ctx, set); err != nil {
		t.Fatal(err)
	}
	_, tok := m.newUser("al\"ice", nil)
	x := fetch(h, "/"+tok+"?format=xray-json", happUA)
	s := fetch(h, "/"+tok, sfaUA)
	xd := parseXray(t, x.Body.Bytes())
	sd := parseSingbox(t, s.Body.Bytes())
	if len(xd) != 1 || len(xd[0].Outbounds) != 3 || len(sd.Outbounds) != 3 || len(sd.Inbounds) != 1 {
		t.Fatalf("a hostile name changed the structure:\n%s\n%s", x.Body.String(), s.Body.String())
	}
	for name, body := range map[string]string{"xray": x.Body.String(), "sing-box": s.Body.String()} {
		if strings.ContainsAny(body, "\n ") || strings.Contains(body, "</script>") {
			t.Errorf("%s: a raw newline, U+2028 or </script> in the document", name)
		}
	}
	// The names are the list's (Happ's limit of 30 characters): data in a string, nothing else.
	if strings.ContainsAny(xd[0].Remarks, "\n\u2028") || len(sd.Outbounds[1]["tag"].(string)) == 0 {
		t.Errorf("remarks = %q, tag = %q", xd[0].Remarks, sd.Outbounds[1]["tag"])
	}
}

// A Source that cannot render formats serves the JSON rules the base64 list instead of failing.
func TestJSONRulesNeedAFormatSource(t *testing.T) {
	fh := subs.Handler(&fakeFormatless{}, decoy, subs.Config{Title: "T"})
	for _, ua := range []string{sfaUA, v2rayNGUA + "?"} {
		if rec := fetch(fh, "/"+strings.Repeat("a", 43)+"?format=xray-json", ua); rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
			t.Errorf("%s: %d %v", ua, rec.Code, rec.Header())
		}
	}
}
