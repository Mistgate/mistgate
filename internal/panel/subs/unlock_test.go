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

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/pagepass"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"google.golang.org/protobuf/proto"
)

// The page password: the HTML page and the calls it makes are gated, the subscription itself never is.

// pageKey is what the panel derives from its master key; the rig's vault has the all-zero master key.
func pageKey(t *testing.T) []byte {
	t.Helper()
	v, err := vault.New(make([]byte, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return v.Derive(pagepass.KeyLabel)
}

type gate struct {
	*m3rig
	h     http.Handler
	cache *subsettings.Cache
	now   time.Time
	ip    string // the client address of the next request ("" = unknown)
}

func newGate(t *testing.T, mut func(*subs.Config)) *gate {
	t.Helper()
	g := &gate{m3rig: newM3Rig(t), now: time.Unix(1_800_000_000, 0), ip: "198.51.100.7"}
	g.h, g.cache = g.handler(func(c *subs.Config) {
		c.PageKey = pageKey(t)
		c.Events = g.st
		c.Now = func() time.Time { return g.now }
		c.ClientIP = func(*http.Request) netip.Addr { a, _ := netip.ParseAddr(g.ip); return a }
		if mut != nil {
			mut(c)
		}
	})
	return g
}

func (g *gate) password(userID string) (pw string) {
	return must(g.svc.GetSubscriptionLink(g.ctx, connect.NewRequest(&adminv1.GetSubscriptionLinkRequest{UserId: userID}))).Msg.PagePassword
}

func (g *gate) unlock(t *testing.T, token, password string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"password": password})
	return call{g.h, t, token}.post("/unlock", string(b), hdr...)
}

func (g *gate) page(token string, hdr ...string) *httptest.ResponseRecorder {
	return fetch(g.h, "/"+token, chrome, hdr...)
}

func cookieOf(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == "mg_page" {
			return c
		}
	}
	t.Fatalf("no cookie in %v", rec.Header())
	return nil
}

// ck is the Cookie header a browser would send back.
func ck(c *http.Cookie) string { return c.Name + "=" + c.Value }

func dataOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	d, _ := pageData(t, rec.Body.String())
	return d
}

func TestPasswordShapeOnTheRPC(t *testing.T) {
	g := newGate(t, nil)
	uid, tok := g.newUser("alice", nil)
	pw := g.password(uid)
	if !regexp.MustCompile(`^[2-9a-km-np-z]{4}-[2-9a-km-np-z]{4}$`).MatchString(pw) {
		t.Fatalf("password %q is not xxxx-xxxx of the unambiguous alphabet", pw)
	}
	if pw != pagepass.Password(pageKey(t), tok) {
		t.Error("the RPC's password is not the one the page checks")
	}
	if again := g.password(uid); again != pw {
		t.Error("the password changed between two reads")
	}
	// the user-created dialog gets it too
	c := must(g.svc.CreateUser(g.ctx, connect.NewRequest(&adminv1.CreateUserRequest{Name: "bob", GroupId: g.group2})))
	bobTok := strings.TrimPrefix(c.Msg.SubscriptionUrl, "https://sub.example.com/k3xq8/")
	if c.Msg.PagePassword == "" || c.Msg.PagePassword != pagepass.Password(pageKey(t), bobTok) {
		t.Errorf("CreateUser page password %q", c.Msg.PagePassword)
	}
}

func TestPageIsLockedWithoutTheCookie(t *testing.T) {
	g := newGate(t, nil)
	_, tok := g.newUser("alice-the-secret-name", nil)

	rec := g.page(tok)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("locked page: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	d := dataOf(t, rec)
	if d["locked"] != true || d["unlock_url"] != "https://sub.example.com/k3xq8/"+tok+"/unlock" {
		t.Errorf("locked data: %v %v", d["locked"], d["unlock_url"])
	}
	body := rec.Body.String()
	for _, leak := range []string{"alice-the-secret-name", "hysteria2://", "profile_name", "my devices"} {
		if strings.Contains(body, leak) {
			t.Errorf("the locked page holds %q", leak)
		}
	}
	if d["subscription_url"] != "" {
		t.Errorf("the locked page carries the link: %v", d["subscription_url"])
	}
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("headers: %v", rec.Header())
	}
}

func TestAppsAreNeverAskedForThePassword(t *testing.T) {
	g := newGate(t, nil)
	_, tok := g.newUser("alice", nil)
	for name, ua := range map[string]string{"happ": happUA, "mihomo-style": mihomoUA, "clash": "clash.meta/1.18", "curl": curlUA} {
		rec := fetch(g.h, "/"+tok, ua)
		if rec.Code != 200 || strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || strings.Contains(rec.Body.String(), "mg-data") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Header().Get("Content-Type"))
		}
		if rec.Header().Get("Subscription-Userinfo") == "" {
			t.Errorf("%s: not the subscription: %v", name, rec.Header())
		}
	}
	if rec := fetch(g.h, "/"+tok, mihomoUA); !strings.Contains(rec.Body.String(), "proxies") {
		t.Error("the Mihomo profile is not served")
	}
}

func TestRightPasswordSetsTheCookieThatUnlocksThePage(t *testing.T) {
	g := newGate(t, nil)
	uid, tok := g.newUser("alice", nil)
	pw := g.password(uid)

	// a phone keyboard capitalises and people type blanks: still the same password
	rec := g.unlock(t, tok, " "+strings.ToUpper(strings.ReplaceAll(pw, "-", " ")))
	if rec.Code != 200 {
		t.Fatalf("unlock: %d %s", rec.Code, rec.Body.String())
	}
	c := cookieOf(t, rec)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/k3xq8/"+tok || c.MaxAge != 180*24*3600 {
		t.Errorf("cookie attributes: %+v", c)
	}
	if strings.Contains(c.Value, pw) || strings.Contains(c.Value, tok) {
		t.Error("the cookie value reveals the password or the token")
	}

	rec = g.page(tok, "Cookie", ck(c))
	d := dataOf(t, rec)
	if d["locked"] == true || d["subscription_url"] == "" {
		t.Errorf("the page stayed locked with the cookie: %v", d["locked"])
	}
	if u, _ := d["user"].(map[string]any); u["name"] != "alice" {
		t.Errorf("user: %v", d["user"])
	}
	// a cookie of another user's link does not open this one
	bobID, other := g.newUser("bob", nil)
	oc := cookieOf(t, g.unlock(t, other, g.password(bobID)))
	if d := dataOf(t, g.page(tok, "Cookie", ck(oc))); d["locked"] != true {
		t.Error("another link's cookie unlocked this page")
	}
}

func TestDeviceCallsNeedTheCookie(t *testing.T) {
	g := newGate(t, nil)
	uid, tok := g.newUser("alice", nil)
	c := call{g.h, t, tok}

	rec := c.post("/devices", g.addBody("phone"))
	if rec.Code != 401 || decodeAnswer(t, rec).Error != "locked" {
		t.Fatalf("without the cookie: %d %s", rec.Code, rec.Body.String())
	}
	for _, p := range []string{"/devices/dev_abcde/configs", "/devices/dev_abcde/rotate", "/devices/dev_abcde/revoke", "/devices/dev_abcde/rename"} {
		if rec := c.post(p, ""); rec.Code != 401 {
			t.Errorf("%s without the cookie: %d", p, rec.Code)
		}
	}
	ck0 := cookieOf(t, g.unlock(t, tok, g.password(uid)))
	if rec := c.post("/devices", g.addBody("phone"), "Cookie", ck(ck0)); rec.Code != 200 || len(decodeAnswer(t, rec).Configs) == 0 {
		t.Fatalf("with the cookie: %d %s", rec.Code, rec.Body.String())
	}
}

func TestWrongPasswordsAreCountedPerTokenAndLimited(t *testing.T) {
	g := newGate(t, nil)
	uid, tok := g.newUser("alice", nil)
	pw := g.password(uid)

	for i := 0; i < 5; i++ {
		rec := g.unlock(t, tok, "aaaa-bbbb")
		var a struct {
			Error string `json:"error"`
			Left  int    `json:"left"`
		}
		json.Unmarshal(rec.Body.Bytes(), &a)
		if rec.Code != 401 || a.Error != "wrong_password" || a.Left != 4-i {
			t.Fatalf("try %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Set-Cookie") != "" {
			t.Fatal("a wrong password got a cookie")
		}
	}
	// locked: even the right password waits
	rec := g.unlock(t, tok, pw)
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" || decodeAnswer(t, rec).Error != "locked" {
		t.Fatalf("lockout: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	g.ip = "203.0.113.50" // another network, same token: the token's count still holds
	if rec := g.unlock(t, tok, pw); rec.Code != 429 {
		t.Fatalf("another network could pass the token's lockout: %d", rec.Code)
	}
	g.now = g.now.Add(10*time.Minute + time.Second)
	if rec := g.unlock(t, tok, pw); rec.Code != 200 {
		t.Fatalf("after the window: %d %s", rec.Code, rec.Body.String())
	}
}

// Once a counter has run out, the answer comes before any lookup of the token: even a link that was rotated away since
// (identify would say "unknown": the decoy) is told to wait, and the locked device calls need no lookup either.
func TestALockedOutEntryIsRefusedBeforeTheTokenIsLookedUp(t *testing.T) {
	g := newGate(t, nil)
	uid, tok := g.newUser("alice", nil)
	for i := 0; i < 6; i++ { // five entries and the one that starts (and audits) the lockout
		g.unlock(t, tok, "aaaa-bbbb")
	}
	must(g.svc.GetSubscriptionLink(g.ctx, connect.NewRequest(&adminv1.GetSubscriptionLinkRequest{UserId: uid, Rotate: true})))
	if rec := g.unlock(t, tok, "aaaa-bbbb"); rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("the lockout was not answered up front: %d %s", rec.Code, rec.Body.String())
	}
	g.ip = "203.0.113.50" // an unknown link from a clean network is still the decoy, not a 429
	if rec := g.unlock(t, "nosuchtoken0123456789", "aaaa-bbbb"); rec.Code == 429 || rec.Code == 200 {
		t.Errorf("unknown token: %d", rec.Code)
	}
	if rec := (call{g.h, t, tok}).post("/devices", g.addBody("phone")); rec.Code != 401 || decodeAnswer(t, rec).Error != "locked" {
		t.Errorf("devices without the cookie: %d %s", rec.Code, rec.Body.String())
	}
}

// The cookie is Secure only when the instance's public URL is https: from an http page a browser would drop it, and
// the page could never be unlocked.
func TestCookieOfAPlainHTTPInstanceIsNotSecure(t *testing.T) {
	g := newGate(t, func(c *subs.Config) { c.BaseURL = "http://sub.example.com/k3xq8" })
	uid, tok := g.newUser("alice", nil)
	rec := g.unlock(t, tok, g.password(uid))
	if rec.Code != 200 {
		t.Fatalf("unlock: %d %s", rec.Code, rec.Body.String())
	}
	if c := cookieOf(t, rec); c.Secure || !c.HttpOnly || c.Path != "/k3xq8/"+tok {
		t.Errorf("cookie attributes: %+v", c)
	}
}

func TestWrongPasswordsAreCountedPerClientNetworkToo(t *testing.T) {
	g := newGate(t, nil)
	uidA, a := g.newUser("alice", nil)
	_, b := g.newUser("bob", nil)
	for i := 0; i < 5; i++ { // one client network guessing at two links
		g.unlock(t, []string{a, b}[i%2], "aaaa-bbbb")
	}
	if rec := g.unlock(t, a, g.password(uidA)); rec.Code != 429 {
		t.Fatalf("the client network is not limited across tokens: %d", rec.Code)
	}
	g.ip = "203.0.113.50"
	if rec := g.unlock(t, a, g.password(uidA)); rec.Code != 200 {
		t.Fatalf("another network was caught by someone else's guesses: %d", rec.Code)
	}
}

func TestAnUnlockLockoutIsAuditedWithoutTokenOrAddress(t *testing.T) {
	g := newGate(t, nil)
	_, tok := g.newUser("alice", nil)
	for i := 0; i < 7; i++ {
		g.unlock(t, tok, "aaaa-bbbb")
	}
	var n int
	var params, ip string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		g.st.R.QueryRow(`SELECT count(*), coalesce(max(params), ''), coalesce(max(ip), '') FROM audit WHERE action = 'page_unlock_lockout'`).Scan(&n, &params, &ip)
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n < 1 {
		t.Fatal("the lockout was not audited")
	}
	if strings.Contains(params, tok) || ip != "" || strings.Contains(params, "198.51.100") {
		t.Errorf("the audit row has a token or an address: %s %s", params, ip)
	}
	// and the failed tries before it are not audited one by one
	var tries int
	g.st.R.QueryRow(`SELECT count(*) FROM audit WHERE action LIKE '%unlock%'`).Scan(&tries)
	if tries > 2 { // one per counter that ran out: the token's and the client network's
		t.Errorf("%d audit rows for 7 tries", tries)
	}
}

func TestRotatingTheLinkChangesThePasswordAndKillsTheCookie(t *testing.T) {
	g := newGate(t, nil)
	uid, tok := g.newUser("alice", nil)
	oldPW := g.password(uid)
	ck0 := cookieOf(t, g.unlock(t, tok, oldPW))

	rot := must(g.svc.GetSubscriptionLink(g.ctx, connect.NewRequest(&adminv1.GetSubscriptionLinkRequest{UserId: uid, Rotate: true}))).Msg
	newTok := strings.TrimPrefix(rot.Url, "https://sub.example.com/k3xq8/")
	if rot.PagePassword == oldPW || rot.PagePassword == "" {
		t.Fatalf("the password did not change with the link: %q -> %q", oldPW, rot.PagePassword)
	}
	// the old link is gone (the read-only page view may be served from the 10 s cache, the unlock never is)
	g.now = g.now.Add(11 * time.Second)
	if rec := g.page(tok, "Cookie", ck(ck0)); rec.Code == 200 && strings.Contains(rec.Body.String(), "mg-data") {
		t.Error("the old link still serves the page")
	}
	if rec := g.unlock(t, tok, oldPW); rec.Code == 200 {
		t.Error("the old link still unlocks")
	}
	// the new link does not honour the old cookie or the old password
	if d := dataOf(t, g.page(newTok, "Cookie", ck(ck0))); d["locked"] != true {
		t.Error("the old cookie opened the new link")
	}
	if rec := g.unlock(t, newTok, oldPW); rec.Code != 401 {
		t.Errorf("the old password on the new link: %d", rec.Code)
	}
	if rec := g.unlock(t, newTok, rot.PagePassword); rec.Code != 200 {
		t.Errorf("the new password: %d", rec.Code)
	}
}

func TestSwitchedOffThereIsNoGate(t *testing.T) {
	g := newGate(t, nil)
	uid, tok := g.newUser("alice", nil)
	set := subsettings.Defaults()
	set.UserPage.RequirePagePassword = proto.Bool(false)
	if _, err := g.cache.Update(g.ctx, set); err != nil {
		t.Fatal(err)
	}
	if d := dataOf(t, g.page(tok)); d["locked"] == true {
		t.Error("the page is locked although the setting is off")
	}
	if rec := (call{g.h, t, tok}).post("/devices", g.addBody("phone")); rec.Code != 200 {
		t.Errorf("device call with the setting off: %d %s", rec.Code, rec.Body.String())
	}
	if pw := g.password(uid); pw != "" {
		t.Errorf("the RPC shows a password (%q) that nothing asks for", pw)
	}
	if rec := g.unlock(t, tok, "aaaa-bbbb"); rec.Code != 200 {
		t.Errorf("unlock with the setting off: %d", rec.Code)
	}
}

func TestAbsentSettingMeansOnAndFreshInstallsStartOn(t *testing.T) {
	if !subsettings.PagePassword(nil) || !subsettings.PagePassword(&adminv1.SubscriptionSettings{UserPage: &adminv1.UserPageOptions{}}) {
		t.Error("a document without the key must ask for the password: that is the upgrade")
	}
	if !subsettings.PagePassword(subsettings.Defaults()) {
		t.Error("a fresh install does not ask for the password")
	}
	off := &adminv1.SubscriptionSettings{UserPage: &adminv1.UserPageOptions{RequirePagePassword: proto.Bool(false)}}
	if subsettings.PagePassword(off) {
		t.Error("an explicit false is ignored")
	}
}

func TestNoKeyMeansNoGate(t *testing.T) { // a handler built without the panel's key (tests, tools) never locks
	m := newM3Rig(t)
	h, _ := m.handler(nil)
	_, tok := m.newUser("alice", nil)
	if d, _ := pageData(t, fetch(h, "/"+tok, chrome).Body.String()); d["locked"] == true {
		t.Error("locked without a key")
	}
}

func TestUnlockOnlyForPostCrossOriginAndKnownTokens(t *testing.T) {
	g := newGate(t, func(c *subs.Config) { c.BaseURL = "https://sub.example.com/k3xq8" })
	uid, tok := g.newUser("alice", nil)
	pw := g.password(uid)
	if rec := fetch(g.h, "/"+tok+"/unlock", chrome); rec.Code != 404 {
		t.Errorf("GET /unlock: %d", rec.Code)
	}
	if rec := g.unlock(t, tok, pw, "Sec-Fetch-Site", "cross-site"); rec.Code != 403 || decodeAnswer(t, rec).Error != "cross_origin" {
		t.Errorf("cross-site unlock: %d %s", rec.Code, rec.Body.String())
	}
	if rec := g.unlock(t, unknown, pw); rec.Code != 404 { // the decoy: a wrong link cannot be told from any other path
		t.Errorf("unknown token: %d", rec.Code)
	}
	// the body rules of the other calls hold here too
	if rec := (call{g.h, t, tok}).post("/unlock", `{"password":"x","extra":1}`); rec.Code != 400 {
		t.Errorf("unknown field: %d", rec.Code)
	}
}

// ---- found by review: self-service must not authorize from the page-view cache ----

func TestSelfServiceRefusesADisabledUserEvenWithAFreshPageCache(t *testing.T) {
	m := newM3Rig(t)
	h, _ := m.handler(nil)
	userID, token := m.newUser("alice", nil)
	c := call{h: h, t: t, token: token}

	created := decodeAnswer(t, c.post("/devices", m.addBody("phone")))
	deviceID, _ := created.Device["id"].(string)
	if deviceID == "" || len(created.Configs) == 0 {
		t.Fatalf("device creation failed: %+v", created)
	}
	// the page view fills the cache while the account is active
	if rec := fetch(h, "/"+token, "Mozilla/5.0"); rec.Code != 200 {
		t.Fatalf("subscription fetch: %d", rec.Code)
	}
	m.st.W.Exec(`UPDATE user SET disabled = 1 WHERE id = ?`, userID)
	if err := m.svc.Recompute(m.ctx, []string{userID}); err != nil {
		t.Fatal(err)
	}
	rec := c.post("/devices/"+deviceID+"/configs", "")
	if rec.Code != 409 {
		got := decodeAnswer(t, rec)
		t.Fatalf("a disabled user must get user_inactive, got HTTP %d with %d config(s), error %q", rec.Code, len(got.Configs), got.Error)
	}
}

func TestSelfServiceRejectsARotatedLinkEvenWithAFreshPageCache(t *testing.T) {
	m := newM3Rig(t)
	h, _ := m.handler(func(c *subs.Config) {
		c.MissLimit, c.BlockFor = 2, time.Hour
		c.ClientIP = func(*http.Request) netip.Addr { return netip.MustParseAddr("198.51.100.17") }
	})
	userID, token := m.newUser("alice", nil)
	_, otherToken := m.newUser("bob", nil)
	c := call{h: h, t: t, token: token}
	created := decodeAnswer(t, c.post("/devices", m.addBody("phone")))
	deviceID, _ := created.Device["id"].(string)
	if deviceID == "" || len(created.Configs) == 0 {
		t.Fatalf("device creation failed: %+v", created)
	}
	if rec := fetch(h, "/"+token, "Mozilla/5.0"); rec.Code != 200 {
		t.Fatalf("subscription fetch: %d", rec.Code)
	}
	must(m.svc.GetSubscriptionLink(m.ctx, connect.NewRequest(&adminv1.GetSubscriptionLinkRequest{UserId: userID, Rotate: true})))
	cross := []string{"Sec-Fetch-Site", "cross-site"}
	rec := c.post("/devices/"+deviceID+"/configs", "", cross...)
	unknown := (call{h: h, t: t, token: strings.Repeat("z", 43)}).post("/devices/"+deviceID+"/configs", "", cross...)
	if rec.Code != http.StatusNotFound || rec.Code != unknown.Code || rec.Body.String() != unknown.Body.String() {
		t.Fatalf("rotated link: %d %q; unknown token: %d %q", rec.Code, rec.Body.String(), unknown.Code, unknown.Body.String())
	}
	if rec := (call{h: h, t: t, token: otherToken}).post("/devices", m.addBody("phone")); rec.Code != unknown.Code || rec.Body.String() != unknown.Body.String() {
		t.Errorf("rotated link did not count as a miss: %d %q", rec.Code, rec.Body.String())
	}
}
