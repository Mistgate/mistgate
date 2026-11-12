package subs_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subs"
)

// GET <base>/sync and POST <base>/leave against the real access module and database: one node with a Hysteria2 server and an
// AmneziaWG profile. The node is named so that its name can be looked for in the answers.

const (
	syncCaps  = "v=1; hysteria2=2; gecko=1; awg=2.0,3.1; probe=1"
	syncUA    = "Mistgate/0.3.1 (windows)"
	nodeAlias = "frankfurt-edge-7"
	hwidB2    = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
)

type syncEnv struct {
	*enrolEnv
	awg, group2 string
}

func newSyncEnv(t *testing.T, mut func(*subs.Config)) *syncEnv {
	t.Helper()
	e := newEnrolEnvWith(t, func(c *subs.Config, st *store.Store) {
		c.Routing = subs.HappRouting(dns.New(st))
		if mut != nil {
			mut(c)
		}
	})
	e.switchAPI(true)
	if _, err := e.st.W.Exec(`UPDATE node SET name = ?, country_code = 'DE', location = 'Frankfurt' WHERE id = 'nod_1'`, nodeAlias); err != nil {
		t.Fatal(err)
	}
	p := must(e.svc.CreateProfile(e.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "awg", Name: "awg-profile"}))).Msg.Profile
	must(e.svc.CreateInbound(e.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: p.Id, NodeId: "nod_1"})))
	g := must(e.svc.CreateGroup(e.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "g2", ProfileIds: []string{e.profile, p.Id}}))).Msg.Group.Id
	return &syncEnv{enrolEnv: e, awg: p.Id, group2: g}
}

// person makes a user with both profiles.
func (s *syncEnv) person(name string, mut func(*adminv1.CreateUserRequest)) (id, tok string) {
	return s.user(name, func(m *adminv1.CreateUserRequest) {
		m.GroupId = s.group2
		if mut != nil {
			mut(m)
		}
	})
}

// request runs a request with the headers a client sends; hdr are name, value pairs that replace the defaults (an empty value
// removes one).
func (s *syncEnv) request(method, path, key string, hdr ...string) *httptest.ResponseRecorder {
	s.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("User-Agent", syncUA)
	req.Header.Set("X-Mistgate-Caps", syncCaps)
	if key != "" {
		req.Header.Set("Authorization", bearer(key))
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i+1] == "" {
			req.Header.Del(hdr[i])
		} else {
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, req)
	return rec
}

func (s *syncEnv) sync(key string, hdr ...string) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.request("GET", "/client/sync", key, hdr...)
}

func (s *syncEnv) leave(key string, hdr ...string) *httptest.ResponseRecorder {
	s.t.Helper()
	return s.request("POST", "/client/leave", key, hdr...)
}

// enrolled enrols a device of the person and returns it.
func (s *syncEnv) enrolDevice(tok, hwid string) enrolled { return s.enrolOK(tok, hwid) }

func (s *syncEnv) mustSync(key string, hdr ...string) syncBody {
	s.t.Helper()
	rec := s.sync(key, hdr...)
	if rec.Code != 200 {
		s.t.Fatalf("sync = %d %s", rec.Code, rec.Body.String())
	}
	return decodeSync(s.t, rec)
}

type syncPath struct {
	ID       string
	Protocol string
	Via      string
	IPv6     bool
	Settings map[string]any
}

type syncBody struct {
	V          int
	DeviceID   string `json:"device_id"`
	Status     string
	SyncEveryS int `json:"sync_every_s"`
	Brand      struct {
		Parts   []string
		Accent  string
		LogoSVG string `json:"logo_svg"`
	}
	Reason     string
	SupportURL string `json:"support_url"`
	Notice     *struct{ ID, Text string }
	Plan       map[string]any
	Probe      map[string]any
	Debug      map[string]any
	ASN        *uint32
	DNS        map[string]any
	Servers    []struct {
		ID, Country, City, Load string
		Paths                   []syncPath
	}
	raw string
	etg string
}

func decodeSync(t *testing.T, rec *httptest.ResponseRecorder) syncBody {
	t.Helper()
	var b syncBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("not a sync body (%d): %v %q", rec.Code, err, rec.Body.String())
	}
	b.raw, b.etg = rec.Body.String(), rec.Header().Get("ETag")
	return b
}

func (b syncBody) paths(protocol string) []syncPath {
	var out []syncPath
	for _, s := range b.Servers {
		for _, p := range s.Paths {
			if p.Protocol == protocol {
				out = append(out, p)
			}
		}
	}
	return out
}

func (s *syncEnv) lastSeen(deviceID string) int64 {
	s.t.Helper()
	var n int64
	if err := s.st.R.QueryRow(`SELECT last_seen_at FROM device WHERE id = ?`, deviceID).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func (s *syncEnv) setLastSeen(deviceID string, ago time.Duration) {
	s.t.Helper()
	if _, err := s.st.W.Exec(`UPDATE device SET last_seen_at = ? WHERE id = ?`, time.Now().Add(-ago).Unix(), deviceID); err != nil {
		s.t.Fatal(err)
	}
}

func (s *syncEnv) setSettings(mut func(*adminv1.SubscriptionSettings)) {
	s.t.Helper()
	cur := proto.Clone(s.cache.Get(s.ctx)).(*adminv1.SubscriptionSettings)
	mut(cur)
	if _, err := s.cache.Update(s.ctx, cur); err != nil {
		s.t.Fatal(err)
	}
}

func assertDecoy(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != 404 || rec.Body.String() != decoyBody {
		t.Errorf("%s = %d %q, want the decoy", what, rec.Code, rec.Body.String())
	}
}

func assertJSONError(t *testing.T, what string, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status || errorCode(t, rec) != code {
		t.Errorf("%s = %d %q, want %d %s", what, rec.Code, rec.Body.String(), status, code)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("%s: Cache-Control %q", what, rec.Header().Get("Cache-Control"))
	}
}

// ---- the answers before any work ----

func TestSyncStrangersGetTheDecoyAndAMiss(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	creds := e.liveCredIDs(a.DeviceID)
	otherKey := newKey()
	for _, c := range []struct {
		name, method, path, key string
		hdr                     []string
	}{
		{"no key", "GET", "/client/sync", "", nil},
		{"unknown key", "GET", "/client/sync", otherKey, nil},
		{"unknown key, leave", "POST", "/client/leave", otherKey, nil},
		{"malformed key", "GET", "/client/sync", "not-a-key", []string{"Authorization", "Bearer not-a-key"}},
		{"31 bytes", "GET", "/client/sync", "", []string{"Authorization", "Bearer " + otherKey[:len(otherKey)-2]}},
		{"basic scheme", "GET", "/client/sync", "", []string{"Authorization", "Basic " + a.Key}},
		{"the key as a token", "GET", "/client/sync", "", []string{"Authorization", a.Key}},
		{"POST sync", "POST", "/client/sync", a.Key, nil},
		{"GET leave", "GET", "/client/leave", a.Key, nil},
		{"PUT sync", "PUT", "/client/sync", a.Key, nil},
	} {
		before := e.limiter.count("subscription-miss")
		rec := e.request(c.method, c.path, c.key, c.hdr...)
		assertDecoy(t, c.name, rec)
		if got := e.limiter.count("subscription-miss") - before; got != 1 {
			t.Errorf("%s: counted %d times as a miss, want 1", c.name, got)
		}
	}
	// The real device was not touched by any of it.
	if e.liveCredIDs(a.DeviceID) != creds {
		t.Error("a refused request changed the device's credentials")
	}
	// A key of another person of this panel is just as unknown to a stranger who guesses; a valid one is not a miss.
	before := e.limiter.count("subscription-miss")
	if rec := e.sync(a.Key); rec.Code != 200 {
		t.Fatalf("valid key = %d", rec.Code)
	}
	if e.limiter.count("subscription-miss") != before {
		t.Error("a valid key was counted as a miss")
	}
}

func TestSyncRemovedBlockedAndMovedLink(t *testing.T) {
	e := newSyncEnv(t, nil)
	uid, tok := e.person("alice", nil)
	removed, blocked, moved := e.enrolDevice(tok, hwidA), e.enrolDevice(tok, hwidB2), e.enrolDevice(tok, "ffffffff-ffff-4fff-8fff-ffffffffffff")
	leaver := e.enrolDevice(tok, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")
	if _, err := e.st.Access().RevokeDevice(e.ctx, removed.DeviceID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Access().BlockDevice(e.ctx, blocked.DeviceID, time.Now()); err != nil {
		t.Fatal(err)
	}
	before := e.limiter.count("subscription-miss")
	assertJSONError(t, "removed device", e.sync(removed.Key), 401, "device_removed")
	assertJSONError(t, "removed device, leave", e.leave(removed.Key), 401, "device_removed")
	assertJSONError(t, "blocked device", e.sync(blocked.Key), 401, "device_blocked")
	assertJSONError(t, "blocked device, leave", e.leave(blocked.Key), 401, "device_blocked")
	if e.limiter.count("subscription-miss") != before {
		t.Error("the key of a removed or blocked device was counted as a miss")
	}

	// The link was rotated since the enrolment: link_changed, and re-enrolling with the new link and the old key fixes it.
	if rec := e.sync(moved.Key); rec.Code != 200 {
		t.Fatalf("before the rotation: %d", rec.Code)
	}
	link := must(e.svc.GetSubscriptionLink(e.ctx, connect.NewRequest(&adminv1.GetSubscriptionLinkRequest{UserId: uid, Rotate: true}))).Msg.Url
	newTok := link[strings.LastIndex(link, "/")+1:]
	assertJSONError(t, "after a rotation", e.sync(moved.Key), 401, "link_changed")
	// A device of an old link can still remove itself: the key proves it is that device, and the rotation took no credentials.
	if rec := e.leave(leaver.Key); rec.Code != 200 {
		t.Fatalf("leave after a rotation = %d %s", rec.Code, rec.Body.String())
	}
	assertJSONError(t, "sync after leave", e.sync(leaver.Key), 401, "device_removed")
	again := e.enrolOK(newTok, "ffffffff-ffff-4fff-8fff-ffffffffffff", "Authorization", bearer(moved.Key))
	if again.DeviceID != moved.DeviceID {
		t.Fatalf("re-enrolled as another device: %s", again.DeviceID)
	}
	if rec := e.sync(again.Key); rec.Code != 200 {
		t.Fatalf("after re-enrolling: %d %s", rec.Code, rec.Body.String())
	}
	assertDecoy(t, "the spent key", e.sync(moved.Key)) // the old key is nobody's now
}

func TestSyncMissLockoutLetsAValidKeyThrough(t *testing.T) {
	e := newSyncEnv(t, func(c *subs.Config) { c.MissLimit, c.MissWindow, c.BlockFor = 4, time.Minute, time.Hour })
	_, tok := e.person("alice", nil)
	a, a2 := e.enrolDevice(tok, hwidA), e.enrolDevice(tok, hwidB2)
	for i := 0; i < 4; i++ {
		assertDecoy(t, "stranger", e.sync(newKey()))
	}
	// The network is locked out now: the unknown token of a stranger is the decoy ...
	assertDecoy(t, "unknown token of a locked network", fetch(e.h, "/"+unknown, happUA))
	// ... a stranger's key is the decoy too, and is counted in the cold-check window, not the one that locks ...
	before := e.limiter.count("subscription-miss")
	assertDecoy(t, "stranger key, locked", e.sync(newKey()))
	assertDecoy(t, "no key, locked", e.sync(""))
	if e.limiter.count("subscription-miss") != before {
		t.Error("a locked network's miss was counted in the window that locks it")
	}
	// ... and the holder of a valid key gets through, as the holder of a valid link does, as often as it likes: a hit spends
	// nothing of the network's allowance.
	for i := 0; i < 3; i++ {
		if rec := e.sync(a.Key); rec.Code != 200 {
			t.Fatalf("valid key from a locked network = %d %s", rec.Code, rec.Body.String())
		}
	}
	if rec := fetch(e.h, "/"+tok, happUA); rec.Code != 200 {
		t.Fatalf("valid link from a locked network = %d", rec.Code)
	}
	if rec := e.leave(a.Key); rec.Code != 200 {
		t.Fatalf("leave from a locked network = %d", rec.Code)
	}
	// A network that has spent the allowance of the cold lookups gets the decoy for a stranger without a lookup ...
	for i := 0; i < 10; i++ {
		assertDecoy(t, "stranger of a spent network", e.sync(newKey()))
	}
	// ... and the holder of a valid key still gets through: a key of the right shape is always looked up, so a neighbour behind
	// the same exit address (a node, a CGNAT, WARP) cannot switch sync off for the others by missing.
	b := e.mustSync(a2.Key)
	if b.DeviceID != a2.DeviceID {
		t.Fatalf("sync of a network that spent its allowance: %s", b.raw)
	}
	if rec := e.leave(a2.Key); rec.Code != 200 {
		t.Fatalf("leave of a network that spent its allowance = %d", rec.Code)
	}
}

func TestSyncCrossOriginAndNoStore(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	for _, hdr := range [][]string{{"Sec-Fetch-Site", "cross-site"}, {"Origin", "https://evil.example"}} {
		assertJSONError(t, "leave with "+hdr[0], e.leave(a.Key, hdr...), 403, "cross_origin")
	}
	if e.count(`SELECT count(*) FROM device WHERE id = ? AND revoked_at IS NULL`, a.DeviceID) != 1 {
		t.Fatal("a cross-origin leave removed the device")
	}
	for _, hdr := range [][]string{{"Sec-Fetch-Site", "cross-site"}, {"Origin", "https://evil.example"}} {
		assertJSONError(t, "sync with "+hdr[0], e.sync(a.Key, hdr...), 403, "cross_origin")
	}
	// A request of the panel's own site, and the plain one of a native client, pass.
	for _, hdr := range [][]string{{"Sec-Fetch-Site", "none"}, {"Sec-Fetch-Site", "same-origin"}, nil} {
		if rec := e.sync(a.Key, hdr...); rec.Code != 200 {
			t.Errorf("sync with %v = %d", hdr, rec.Code)
		}
	}
	// A stranger from another origin learns nothing: the decoy, not the 403 of the panel.
	assertDecoy(t, "cross-origin stranger", e.leave(newKey(), "Origin", "https://evil.example"))
	// Everything carries no-store: the answer, the 304, the refusals.
	rec := e.sync(a.Key)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Errorf("sync headers: %d %v", rec.Code, rec.Header())
	}
	rec304 := e.sync(a.Key, "If-None-Match", rec.Header().Get("ETag"))
	if rec304.Code != 304 || rec304.Header().Get("Cache-Control") != "no-store" || rec304.Header().Get("ETag") != rec.Header().Get("ETag") {
		t.Errorf("304 headers: %d %v", rec304.Code, rec304.Header())
	}
	if lv := e.leave(a.Key); lv.Code != 200 || lv.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("leave headers: %d %v", lv.Code, lv.Header())
	}
}

// The owner's switch closes discovery and enrolment only.
func TestSyncAndLeaveSurviveTheOwnersSwitch(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice", nil)
	a, b := e.enrolDevice(tok, hwidA), e.enrolDevice(tok, hwidB2)
	e.switchAPI(false)
	if rec := e.post(tok, enrolBody(hwidA, a.Key)); rec.Code != 403 || errorCode(t, rec) != "client_api_off" {
		t.Fatalf("enrol with the switch off = %d %q", rec.Code, rec.Body.String())
	}
	if rec := e.sync(a.Key); rec.Code != 200 {
		t.Fatalf("sync with the switch off = %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.leave(b.Key); rec.Code != 200 {
		t.Fatalf("leave with the switch off = %d %s", rec.Code, rec.Body.String())
	}
}

// ---- the budget ----

func TestSyncBudgetIsPerDeviceAndSharedWithLeave(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice", nil)
	a, b := e.enrolDevice(tok, hwidA), e.enrolDevice(tok, hwidB2)
	first := e.sync(a.Key)
	etag := first.Header().Get("ETag")
	for i := 2; i <= 30; i++ { // 304s count too
		if rec := e.sync(a.Key, "If-None-Match", etag); rec.Code != 304 {
			t.Fatalf("request %d = %d", i, rec.Code)
		}
	}
	rec := e.sync(a.Key)
	assertJSONError(t, "31st request", rec, 429, "too_many_requests")
	if secs, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || secs < 1 || secs > 3600 {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	assertJSONError(t, "leave over the budget", e.leave(a.Key), 429, "too_many_requests")
	if e.count(`SELECT count(*) FROM device WHERE id = ? AND revoked_at IS NULL`, a.DeviceID) != 1 {
		t.Fatal("a leave over the budget removed the device")
	}
	// Another device of the same person has a budget of its own.
	if rec := e.sync(b.Key); rec.Code != 200 {
		t.Fatalf("another device = %d", rec.Code)
	}
	// Refusals before the budget do not spend it.
	if n := e.limiter.count("subscription-client-sync"); n != 33 {
		t.Errorf("%d budget records, want 33 (31 + leave + the other device)", n)
	}
}

// ---- the body ----

func TestSyncActiveBodyAndWhatIsNeverInIt(t *testing.T) {
	e := newSyncEnv(t, nil)
	uid, tok := e.person("alice", func(m *adminv1.CreateUserRequest) { m.QuotaBytes = 5 << 30; m.TermDays = 30 })
	e.setSettings(func(s *adminv1.SubscriptionSettings) {
		s.Announcement, s.SupportUrl = "Maintenance on Friday.", "https://support.example.com"
	})
	a := e.enrolDevice(tok, hwidA)
	_, bobTok := e.person("bob", nil)
	e.enrolDevice(bobTok, hwidB2)

	rec := e.sync(a.Key)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("ETag"), `"`) || len(rec.Header().Get("ETag")) < 10 {
		t.Fatalf("sync = %d %v", rec.Code, rec.Header())
	}
	b := decodeSync(t, rec)
	if b.V != 1 || b.DeviceID != a.DeviceID || b.Status != "active" || b.SyncEveryS != 3600 || b.SupportURL != "https://support.example.com" ||
		b.Notice == nil || b.Notice.Text != "Maintenance on Friday." || b.Probe["intake"] != false || b.Probe["v"] != float64(1) || b.Debug["intake"] != false || b.ASN != nil {
		t.Fatalf("body: %s", b.raw)
	}
	if len(b.Brand.Parts) == 0 || b.Brand.Accent == "" {
		t.Errorf("brand: %+v", b.Brand)
	}
	if b.Plan["quota_bytes"] != float64(5<<30) || b.Plan["used_bytes"] != float64(0) || b.Plan["expires_unix"].(float64) < float64(time.Now().Unix()) || b.Plan["quota_reset"] == nil {
		t.Errorf("plan: %v", b.Plan)
	}
	if len(b.Servers) != 1 || b.Servers[0].Country != "DE" || b.Servers[0].City != "Frankfurt" || len(b.Servers[0].Paths) != 2 {
		t.Fatalf("servers: %s", b.raw)
	}
	hy, aw := b.paths("hysteria2"), b.paths("awg")
	if len(hy) != 1 || len(aw) != 1 || hy[0].Via != "direct" || aw[0].Via != "direct" || hy[0].IPv6 || aw[0].IPv6 {
		t.Fatalf("paths: %+v %+v", hy, aw)
	}
	if hy[0].Settings["server"] != "de1.example.com" || hy[0].Settings["auth"] == "" || hy[0].Settings["tls"] == nil {
		t.Errorf("hysteria2 settings: %v", hy[0].Settings)
	}
	if aw[0].Settings["private_key"] == "" || aw[0].Settings["public_key"] == "" || aw[0].Settings["address_v4"] == nil || aw[0].Settings["params"] == nil {
		t.Errorf("awg settings: %v", aw[0].Settings)
	}
	if b.DNS == nil || b.DNS["servers"] == nil || b.DNS["transport"] == nil {
		t.Errorf("dns: %v", b.DNS)
	}

	// Never in the body: the node's name, the link or its token, the person, other people.
	for _, forbidden := range []string{nodeAlias, tok, bobTok, "k3xq8", "sub.example.com", "alice", "bob", uid, "nod_1", "inb_", "prf_", a.Key, hwidA, hwidB2, strings.ReplaceAll(hwidA, "-", "")} {
		if strings.Contains(b.raw, forbidden) {
			t.Errorf("the body holds %q", forbidden)
		}
	}
	if strings.Contains(b.raw, "page_url") {
		t.Error("the body holds the page address")
	}
	// The ids are opaque, stable and the same for the same path on a second call.
	b2 := e.mustSync(a.Key)
	if b2.Servers[0].ID != b.Servers[0].ID || b2.Servers[0].Paths[0].ID != b.Servers[0].Paths[0].ID || b2.raw != b.raw {
		t.Errorf("two calls differ:\n%s\n%s", b.raw, b2.raw)
	}
}

func TestSyncInactiveStatusesHaveNoServers(t *testing.T) {
	e := newSyncEnv(t, nil)
	uid, tok := e.person("alice", func(m *adminv1.CreateUserRequest) { m.QuotaBytes = 1000 })
	a := e.enrolDevice(tok, hwidA)
	if b := e.mustSync(a.Key); b.Status != "active" || len(b.Servers) == 0 {
		t.Fatalf("active: %s", b.raw)
	}
	credsBefore := e.liveCredIDs(a.DeviceID)
	for _, c := range []struct {
		status string
		set    string
	}{
		{"disabled", `UPDATE user SET disabled = 1 WHERE id = ?`},
		{"limited", `UPDATE user SET disabled = 0, used_bytes = quota_bytes WHERE id = ?`},
		{"expired", `UPDATE user SET used_bytes = 0, expires_at = 1 WHERE id = ?`},
	} {
		if _, err := e.st.W.Exec(c.set, uid); err != nil {
			t.Fatalf("%s: %v", c.status, err)
		}
		rec := e.sync(a.Key)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.status, rec.Code, rec.Body.String())
		}
		b := decodeSync(t, rec)
		if b.Status != c.status || b.Servers == nil || len(b.Servers) != 0 || b.DNS != nil || b.Plan == nil || b.Plan["quota_bytes"] != float64(1000) {
			t.Errorf("%s: %s", c.status, b.raw)
		}
		if strings.Contains(b.raw, `"servers":null`) {
			t.Errorf("%s: servers is null", c.status)
		}
	}
	if e.liveCredIDs(a.DeviceID) != credsBefore {
		t.Error("a person without access changed the device's credentials")
	}
}

// The status is computed at the instant of the request, not read from a stored column.
func TestSyncStatusIsComputed(t *testing.T) {
	e := newSyncEnv(t, nil)
	uid, tok := e.person("alice", func(m *adminv1.CreateUserRequest) { m.QuotaBytes = 1000 })
	a := e.enrolDevice(tok, hwidA)
	// Stored status says active; the term ended a second ago and the sweep has not run.
	if _, err := e.st.W.Exec(`UPDATE user SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Second).Unix(), uid); err != nil {
		t.Fatal(err)
	}
	if b := e.mustSync(a.Key); b.Status != "expired" || len(b.Servers) != 0 {
		t.Fatalf("expired by the clock: %s", b.raw)
	}
}

// ---- credentials: issued to the enrolled device by key only ----

func TestSyncIssuesAWGToTheEnrolledDeviceAndTheBasicPathStaysShut(t *testing.T) {
	e := newSyncEnv(t, nil)
	uid, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	if n := e.count(`SELECT count(*) FROM device_credential WHERE device_id = ? AND protocol = 'awg' AND revoked_at IS NULL`, a.DeviceID); n != 0 {
		t.Fatalf("%d AWG credentials before the first sync", n)
	}
	notified := e.state.n.Load()

	b := e.mustSync(a.Key)
	if len(b.paths("awg")) != 1 {
		t.Fatalf("no AWG path after the first sync: %s", b.raw)
	}
	if n := e.count(`SELECT count(*) FROM device_credential WHERE device_id = ? AND protocol = 'awg' AND revoked_at IS NULL`, a.DeviceID); n != 1 {
		t.Fatalf("%d AWG credentials after the first sync, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM awg_peer p JOIN device_credential c ON c.id = p.credential_id WHERE c.device_id = ? AND p.released_at = 0`, a.DeviceID); n != 1 {
		t.Fatalf("%d live AWG peers, want 1", n)
	}
	if e.state.n.Load() == notified {
		t.Error("the nodes were not told about the new peer")
	}
	// The second sync issues nothing and tells nobody.
	notified = e.state.n.Load()
	credIDs := e.liveCredIDs(a.DeviceID)
	if again := e.mustSync(a.Key); again.raw != b.raw || e.liveCredIDs(a.DeviceID) != credIDs || e.state.n.Load() != notified {
		t.Error("the second sync changed credentials or the body")
	}
	// The person's limits are untouched: one device, as many places as before.
	if n := e.count(`SELECT count(*) FROM device WHERE user_id = ? AND revoked_at IS NULL`, uid); n != 1 {
		t.Errorf("%d devices, want 1", n)
	}

	// A basic fetch with the same HWID is still refused: the device holds a key, and the HWID is no secret. It gets the
	// one-entry placeholder, no server, and nothing is issued, rewritten or revoked on the device.
	before := e.liveCredIDs(a.DeviceID)
	for _, ua := range []string{happUA, mihomoUA} {
		rec := fetch(e.h, "/"+tok, ua, "X-HWID", hwidA)
		if rec.Code != 200 {
			t.Fatalf("basic fetch (%s) = %d", ua, rec.Code)
		}
		body := rec.Body.String()
		if ua == happUA {
			body = decode(t, body)
		}
		if strings.Contains(body, "de1.example.com") || strings.Contains(body, "private-key") || strings.Contains(body, "PrivateKey") || strings.Contains(body, "wireguard") {
			t.Errorf("a basic fetch (%s) with an enrolled HWID got servers:\n%s", ua, body)
		}
	}
	if e.liveCredIDs(a.DeviceID) != before {
		t.Error("a basic fetch changed the enrolled device's credentials")
	}
	if n := e.count(`SELECT count(*) FROM device WHERE user_id = ? AND revoked_at IS NULL`, uid); n != 1 {
		t.Errorf("a basic fetch made a device: %d", n)
	}
}

// A new profile added after the enrolment reaches the device with its next sync, as a new peer; a profile the client cannot use
// (no capability) is not issued at all.
func TestSyncIssuesOnlyWhatTheClientAnnounced(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	b := e.mustSync(a.Key, "X-Mistgate-Caps", "v=1; hysteria2=2")
	if len(b.paths("awg")) != 0 || len(b.paths("hysteria2")) != 1 {
		t.Fatalf("a client without the awg capability: %s", b.raw)
	}
	if n := e.count(`SELECT count(*) FROM device_credential WHERE device_id = ? AND protocol = 'awg'`, a.DeviceID); n != 0 {
		t.Errorf("%d AWG credentials issued to a client that does not announce AWG", n)
	}
	if b := e.mustSync(a.Key, "X-Mistgate-Caps", "v=1; awg=1.5"); len(b.paths("awg")) != 0 {
		t.Errorf("an AWG version that nobody announced was given: %s", b.raw)
	}
	b = e.mustSync(a.Key)
	if len(b.paths("awg")) != 1 {
		t.Fatalf("the capability was announced and no AWG path came: %s", b.raw)
	}
	// And without the capability again the path is not listed; the peer stays.
	if b := e.mustSync(a.Key, "X-Mistgate-Caps", "v=1"); len(b.paths("awg")) != 0 {
		t.Errorf("AWG listed to a client that does not announce it: %s", b.raw)
	}
}

// The owner's switch "AmneziaWG in apps on the link" is for the other apps of the link. The Mistgate app gets AmneziaWG whatever it
// says (its own switch, its caps, the profiles of the person decide), and the Mihomo fetch of another device of the same person,
// which is told no AmneziaWG and drops the peers of its own device, never takes the peer of a device that holds a key.
func TestSyncAWGDoesNotFollowTheOwnersLinkSwitch(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	livePeers := func(deviceID string) int {
		return e.count(`SELECT count(*) FROM awg_peer p JOIN device_credential c ON c.id = p.credential_id WHERE c.device_id = ? AND p.released_at = 0`, deviceID)
	}
	if b := e.mustSync(a.Key); len(b.paths("awg")) != 1 {
		t.Fatalf("no AWG path: %s", b.raw)
	}
	// another app of the person, on the plain link with a device ID of its own: a Mihomo fetch makes its AWG peer too
	if rec := fetch(e.h, "/"+tok, mihomoUA, "X-HWID", hwidB); rec.Code != 200 || !strings.Contains(rec.Body.String(), "type: wireguard") {
		t.Fatalf("the Mihomo fetch with AWG on = %d, no wireguard proxy:\n%s", rec.Code, rec.Body.String())
	}
	var otherID string
	if err := e.st.R.QueryRow(`SELECT id FROM device WHERE key_hash IS NULL AND hwid_hash IS NOT NULL AND revoked_at IS NULL`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	if livePeers(a.DeviceID) != 1 || livePeers(otherID) != 1 {
		t.Fatalf("peers: app %d, other app %d, want one each", livePeers(a.DeviceID), livePeers(otherID))
	}

	e.setSettings(func(s *adminv1.SubscriptionSettings) { s.LinkAwg = proto.Bool(false) })
	// the other app gets no AmneziaWG now, and its own peer goes; the Mistgate app keeps its peer
	rec := fetch(e.h, "/"+tok, mihomoUA, "X-HWID", hwidB)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "wireguard") {
		t.Fatalf("the Mihomo fetch with the switch off = %d:\n%s", rec.Code, rec.Body.String())
	}
	if livePeers(otherID) != 0 {
		t.Errorf("the other app kept %d peers with the switch off", livePeers(otherID))
	}
	if livePeers(a.DeviceID) != 1 {
		t.Fatalf("the Mihomo fetch took the peer of the Mistgate app: %d live", livePeers(a.DeviceID))
	}
	credsBefore := e.liveCredIDs(a.DeviceID)
	b := e.mustSync(a.Key)
	if len(b.paths("awg")) != 1 || len(b.paths("hysteria2")) != 1 {
		t.Fatalf("sync with the link switch off: %s", b.raw)
	}
	if e.liveCredIDs(a.DeviceID) != credsBefore || livePeers(a.DeviceID) != 1 {
		t.Error("sync with the link switch off changed the device's AmneziaWG")
	}
	// a fetch with the HWID of the enrolled device is refused, and touches nothing
	fetch(e.h, "/"+tok, mihomoUA, "X-HWID", hwidA)
	if livePeers(a.DeviceID) != 1 {
		t.Error("a refused fetch with the HWID of the Mistgate app took its peer")
	}
}

// What the person and the client decide stays in force with the link switch off: the app switched off for the person, and a client
// that did not announce an AmneziaWG version, get no AmneziaWG.
func TestSyncAWGWithTheLinkSwitchOffStillFollowsThePersonAndTheCaps(t *testing.T) {
	e := newSyncEnv(t, nil)
	uid, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	e.setSettings(func(s *adminv1.SubscriptionSettings) { s.LinkAwg = proto.Bool(false) })

	if b := e.mustSync(a.Key, "X-Mistgate-Caps", "v=1; hysteria2=2"); len(b.paths("awg")) != 0 || len(b.paths("hysteria2")) != 1 {
		t.Fatalf("a client without the awg capability: %s", b.raw)
	}
	if n := e.count(`SELECT count(*) FROM device_credential WHERE device_id = ? AND protocol = 'awg' AND revoked_at IS NULL`, a.DeviceID); n != 0 {
		t.Fatalf("%d AWG credentials issued to a client that did not ask", n)
	}
	if b := e.mustSync(a.Key); len(b.paths("awg")) != 1 {
		t.Fatalf("with the capability: %s", b.raw)
	}
	e.setApps(uid, &adminv1.AppToggles{Happ: true, Amnezia: true, Mistgate: new(false)})
	if b := e.mustSync(a.Key); len(b.paths("awg")) != 0 || b.Reason != "app_disabled" {
		t.Fatalf("the app off for the person: %s", b.raw)
	}
}

// A plain Mihomo fetch and the sync of the same person at the same moment, with the link switch off: the sync keeps its peer
// whatever the order, and the plain app has none.
func TestSyncAWGAgainstParallelMihomoFetchesWithTheLinkSwitchOff(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	e.mustSync(a.Key)
	fetch(e.h, "/"+tok, mihomoUA, "X-HWID", hwidB)
	e.setSettings(func(s *adminv1.SubscriptionSettings) { s.LinkAwg = proto.Bool(false) })
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); fetch(e.h, "/"+tok, mihomoUA, "X-HWID", hwidB) }()
		go func() { defer wg.Done(); e.sync(a.Key) }()
	}
	wg.Wait()
	if n := e.count(`SELECT count(*) FROM awg_peer p JOIN device_credential c ON c.id = p.credential_id WHERE c.device_id = ? AND p.released_at = 0`, a.DeviceID); n != 1 {
		t.Errorf("the Mistgate app has %d live peers after the race, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM awg_peer p JOIN device_credential c ON c.id = p.credential_id JOIN device d ON d.id = c.device_id WHERE d.key_hash IS NULL AND p.released_at = 0`); n != 0 {
		t.Errorf("the plain app has %d live peers with the switch off", n)
	}
	if b := e.mustSync(a.Key); len(b.paths("awg")) != 1 {
		t.Fatalf("sync after the race: %s", b.raw)
	}
}

// The profile of a Gecko server is given to a client that announced gecko=1 and to nobody else.
func TestSyncGeckoByCapability(t *testing.T) {
	e := newSyncEnv(t, nil)
	gk := must(e.svc.CreateProfile(e.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "hysteria2", Name: "gecko", SettingsJson: `{"obfs":{"type":"gecko"},"port":8444}`}))).Msg.Profile
	must(e.svc.CreateInbound(e.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: gk.Id, NodeId: "nod_1"})))
	g := must(e.svc.CreateGroup(e.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "gk", ProfileIds: []string{gk.Id}}))).Msg.Group.Id
	_, tok := e.user("alice", func(m *adminv1.CreateUserRequest) { m.GroupId = g })
	a := e.enrolDevice(tok, hwidA)
	b := e.mustSync(a.Key, "X-Mistgate-Caps", "v=1; hysteria2=2; awg=2.0")
	if len(b.Servers) != 0 {
		t.Fatalf("Gecko listed to a client that does not announce it: %s", b.raw)
	}
	b = e.mustSync(a.Key)
	hy := b.paths("hysteria2")
	if len(hy) != 1 || hy[0].Settings["obfs"].(map[string]any)["type"] != "gecko" {
		t.Fatalf("Gecko not given to a client that announced it: %s", b.raw)
	}
}

// ---- ETag ----

func TestSyncETagFollowsTheBodyAndNothingElse(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	first := e.sync(a.Key)
	etag := first.Header().Get("ETag")
	if first.Code != 200 || etag == "" {
		t.Fatalf("first sync: %d %v", first.Code, first.Header())
	}
	again := e.sync(a.Key)
	if again.Header().Get("ETag") != etag || again.Body.String() != first.Body.String() {
		t.Fatal("the ETag or the body changed between two calls with nothing changed")
	}
	// A different time, user agent, OS and caps spelling change nothing (a request is not data).
	other := e.sync(a.Key, "User-Agent", "Mistgate/9.9.9 (windows)", "X-Ver-OS", "11 24H2", "X-Mistgate-Caps", "gecko=1;awg=3.1,2.0;v=1;hysteria2=2")
	if other.Header().Get("ETag") != etag {
		t.Error("the ETag follows the request headers")
	}

	// 304: no body; any of the forms of the validator; a stale one gets the body.
	for _, inm := range []string{etag, "W/" + etag, `"nope", ` + etag, "*"} {
		rec := e.sync(a.Key, "If-None-Match", inm)
		if rec.Code != 304 || rec.Body.Len() != 0 || rec.Header().Get("ETag") != etag {
			t.Errorf("If-None-Match %q = %d %q", inm, rec.Code, rec.Body.String())
		}
	}
	if rec := e.sync(a.Key, "If-None-Match", `"stale"`); rec.Code != 200 || rec.Body.Len() == 0 {
		t.Errorf("a stale validator = %d", rec.Code)
	}

	changed := func(what string, before string) string {
		t.Helper()
		rec := e.sync(a.Key)
		if rec.Code != 200 || rec.Header().Get("ETag") == before {
			t.Fatalf("%s did not change the ETag (%d)", what, rec.Code)
		}
		if st := e.sync(a.Key, "If-None-Match", before); st.Code != 200 {
			t.Fatalf("%s: the old validator still gets %d", what, st.Code)
		}
		return rec.Header().Get("ETag")
	}
	// A notice.
	e.setSettings(func(s *adminv1.SubscriptionSettings) { s.Announcement = "Maintenance on Friday." })
	etag = changed("a notice", etag)
	e.setSettings(func(s *adminv1.SubscriptionSettings) { s.Announcement = "Maintenance on Saturday." })
	etag = changed("another notice text", etag)
	// A credential: the device's AmneziaWG peer is revoked, a new one is issued by the next sync.
	if _, err := e.st.W.Exec(`UPDATE awg_peer SET released_at = ? WHERE released_at = 0`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.Exec(`UPDATE device_credential SET revoked_at = ? WHERE device_id = ? AND protocol = 'awg' AND revoked_at IS NULL`, time.Now().Unix(), a.DeviceID); err != nil {
		t.Fatal(err)
	}
	etag = changed("a credential", etag)
	// A profile.
	admin := auth.WithAdmin(e.ctx, store.Admin{ID: "adm_1"})
	prof := must(e.svc.GetProfile(admin, connect.NewRequest(&adminv1.GetProfileRequest{ProfileId: e.profile}))).Msg.Profile
	settings := `{"obfs":{"type":"none"}}`
	must(e.svc.UpdateProfile(admin, connect.NewRequest(&adminv1.UpdateProfileRequest{ProfileId: e.profile, ExpectedVersion: prof.Version, SettingsJson: &settings})))
	etag = changed("a profile", etag)
	// A status.
	if _, err := e.st.W.Exec(`UPDATE user SET disabled = 1`); err != nil {
		t.Fatal(err)
	}
	etag = changed("a status", etag)
	if again := e.sync(a.Key); again.Header().Get("ETag") != etag {
		t.Error("the ETag moved with nothing changed")
	}
}

// The ASN describes the caller, not the configuration: it is outside the ETag and absent from a 304.
func TestSyncASN(t *testing.T) {
	var asked []netip.Addr
	e := newSyncEnv(t, func(c *subs.Config) {
		c.ClientASN = func(ip netip.Addr) uint32 { asked = append(asked, ip); return 64500 }
	})
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)

	plain := e.sync(a.Key)
	if len(asked) != 0 || decodeSync(t, plain).ASN != nil {
		t.Fatalf("an ASN without the header: %v %s", asked, plain.Body.String())
	}
	for _, v := range []string{"0", "true", "yes", ""} {
		if rec := e.sync(a.Key, "X-Mistgate-Direct", v); decodeSync(t, rec).ASN != nil {
			t.Errorf("X-Mistgate-Direct: %q gave an ASN", v)
		}
	}
	direct := e.sync(a.Key, "X-Mistgate-Direct", "1")
	b := decodeSync(t, direct)
	if b.ASN == nil || *b.ASN != 64500 || len(asked) != 1 || asked[0] != netip.MustParseAddr(enrolIP) {
		t.Fatalf("direct: asn %v asked %v", b.ASN, asked)
	}
	if direct.Header().Get("ETag") != plain.Header().Get("ETag") {
		t.Error("the ASN is in the ETag")
	}
	// The body of a direct call is the plain body plus the field.
	var withASN, without map[string]any
	json.Unmarshal(direct.Body.Bytes(), &withASN)
	json.Unmarshal(plain.Body.Bytes(), &without)
	delete(withASN, "asn")
	jw, _ := json.Marshal(withASN)
	jo, _ := json.Marshal(without)
	if string(jw) != string(jo) {
		t.Error("a direct call differs from a plain one beyond the asn")
	}
	// On a 304 the ASN is not sent (there is no body), and the lookup is not even made.
	asked = nil
	rec := e.sync(a.Key, "X-Mistgate-Direct", "1", "If-None-Match", plain.Header().Get("ETag"))
	if rec.Code != 304 || rec.Body.Len() != 0 || len(asked) != 0 {
		t.Errorf("304 with the direct header: %d %q, %d lookups", rec.Code, rec.Body.String(), len(asked))
	}
	// Unknown (0) is absent; only a person with access has servers but the ASN does not depend on it.
	e2 := newSyncEnv(t, func(c *subs.Config) { c.ClientASN = func(netip.Addr) uint32 { return 0 } })
	_, tok2 := e2.person("alice", nil)
	a2 := e2.enrolDevice(tok2, hwidA)
	if rec := e2.sync(a2.Key, "X-Mistgate-Direct", "1"); decodeSync(t, rec).ASN != nil {
		t.Error("an unknown ASN is present")
	}
	// The edge edition has no table: no lookup function, no field.
	e3 := newSyncEnv(t, nil)
	_, tok3 := e3.person("alice", nil)
	a3 := e3.enrolDevice(tok3, hwidA)
	if rec := e3.sync(a3.Key, "X-Mistgate-Direct", "1"); decodeSync(t, rec).ASN != nil {
		t.Error("a panel without a networks table gave an ASN")
	}
	for _, l := range []string{e.logs.String(), e2.logs.String()} {
		if strings.Contains(l, "64500") || strings.Contains(l, enrolIP) {
			t.Error("the ASN or the address is in the log")
		}
	}
}

// ---- touch ----

func TestSyncTouchesTheDeviceAtMostEveryTenMinutes(t *testing.T) {
	e := newSyncEnv(t, nil)
	touches := make(chan bool, 16)
	e.svc.SetTouchHookForTest(func(started bool) { touches <- started })
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	touched := func() {
		t.Helper()
		waitTouchEvent(t, touches, true)
		waitTouchEvent(t, touches, false)
	}

	e.setLastSeen(a.DeviceID, 2*time.Hour)
	stale := e.lastSeen(a.DeviceID)
	e.mustSync(a.Key, "X-Ver-OS", "11 24H2")
	touched()
	if first := e.lastSeen(a.DeviceID); first <= stale {
		t.Fatalf("a stale device was not touched: %d -> %d", stale, first)
	}
	var osVersion, client string
	if err := e.st.R.QueryRow(`SELECT coalesce(os_version, ''), coalesce(client, '') FROM device WHERE id = ?`, a.DeviceID).Scan(&osVersion, &client); err != nil {
		t.Fatal(err)
	}
	if osVersion != "11 24H2" || client != "Mistgate/0.3.1" {
		t.Errorf("os_version %q client %q", osVersion, client)
	}

	// Five minutes ago is fresh enough: no write, however many calls come (a 304 included).
	e.setLastSeen(a.DeviceID, 5*time.Minute)
	fresh := e.lastSeen(a.DeviceID)
	etag := e.sync(a.Key).Header().Get("ETag")
	for i := 0; i < 5; i++ {
		e.mustSync(a.Key, "X-Ver-OS", "12")
	}
	e.sync(a.Key, "If-None-Match", etag)
	assertNoTouchStarted(t, touches)
	if e.lastSeen(a.DeviceID) != fresh {
		t.Error("a device seen five minutes ago was written again")
	}
	if err := e.st.R.QueryRow(`SELECT coalesce(os_version, '') FROM device WHERE id = ?`, a.DeviceID).Scan(&osVersion); err != nil || osVersion != "11 24H2" {
		t.Errorf("os_version moved without a touch: %q (%v)", osVersion, err)
	}
	// Eleven minutes ago is not.
	e.setLastSeen(a.DeviceID, 11*time.Minute)
	old := e.lastSeen(a.DeviceID)
	e.mustSync(a.Key, "X-Ver-OS", "12")
	touched()
	if e.lastSeen(a.DeviceID) <= old {
		t.Error("a device silent for eleven minutes was not touched")
	}
	// A person without access is touched as well (the panel sees a device that is trying), under the same throttle.
	e.setLastSeen(a.DeviceID, 2*time.Hour)
	old = e.lastSeen(a.DeviceID)
	if _, err := e.st.W.Exec(`UPDATE user SET disabled = 1`); err != nil {
		t.Fatal(err)
	}
	if b := e.mustSync(a.Key); b.Status != "disabled" {
		t.Fatalf("not disabled: %s", b.raw)
	}
	touched()
	if e.lastSeen(a.DeviceID) <= old {
		t.Error("a disabled person's device was not touched")
	}
	e.mustSync(a.Key)
	assertNoTouchStarted(t, touches)
}

// ---- leave ----

func TestLeaveRemovesExactlyThisDevice(t *testing.T) {
	e := newSyncEnv(t, nil)
	uid, tok := e.person("alice", nil)
	a, b := e.enrolDevice(tok, hwidA), e.enrolDevice(tok, hwidB2)
	e.mustSync(a.Key) // AWG peers are issued
	e.mustSync(b.Key)
	otherCreds := e.liveCredIDs(b.DeviceID)
	if n := e.count(`SELECT count(*) FROM awg_peer WHERE released_at = 0`); n != 2 {
		t.Fatalf("%d live peers, want 2", n)
	}
	notified := e.state.n.Load()

	rec := e.leave(a.Key, "Content-Type", "application/json")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"ok":true}` {
		t.Fatalf("leave = %d %q", rec.Code, rec.Body.String())
	}
	if e.state.n.Load() == notified {
		t.Error("the nodes were not told that credentials left")
	}
	if n := e.count(`SELECT count(*) FROM device_credential WHERE device_id = ? AND revoked_at IS NULL`, a.DeviceID); n != 0 {
		t.Errorf("%d live credentials on the removed device", n)
	}
	if n := e.count(`SELECT count(*) FROM awg_peer p JOIN device_credential c ON c.id = p.credential_id WHERE c.device_id = ? AND p.released_at = 0`, a.DeviceID); n != 0 {
		t.Errorf("%d live peers of the removed device (they are quarantined, not kept)", n)
	}
	if n := e.count(`SELECT count(*) FROM awg_peer p JOIN device_credential c ON c.id = p.credential_id WHERE c.device_id = ? AND p.released_at > 0`, a.DeviceID); n != 1 {
		t.Errorf("%d quarantined peers, want 1", n)
	}
	// Another device of the person is whole.
	if e.liveCredIDs(b.DeviceID) != otherCreds {
		t.Error("the other device lost credentials")
	}
	if rec := e.sync(b.Key); rec.Code != 200 {
		t.Errorf("the other device: %d", rec.Code)
	}
	// Afterwards the key answers device_removed, on sync and on a second leave (idempotent in effect).
	assertJSONError(t, "sync after leave", e.sync(a.Key), 401, "device_removed")
	assertJSONError(t, "second leave", e.leave(a.Key), 401, "device_removed")
	if n := e.count(`SELECT count(*) FROM device WHERE user_id = ? AND revoked_at IS NULL`, uid); n != 1 {
		t.Errorf("%d live devices, want 1", n)
	}

	// The audit row: the user and the device, nothing else.
	var actor, params, ip string
	if err := e.st.R.QueryRow(`SELECT actor, params, ip FROM audit WHERE action = 'client_device_left'`).Scan(&actor, &params, &ip); err != nil {
		t.Fatal(err)
	}
	if actor != "user:"+uid || ip != "" || !strings.Contains(params, a.DeviceID) {
		t.Errorf("audit: actor %q ip %q params %q", actor, ip, params)
	}
	for _, secret := range []string{tok, hwidA, strings.ReplaceAll(hwidA, "-", ""), a.Key, enrolIP, "Bearer"} {
		if strings.Contains(params, secret) || strings.Contains(actor, secret) {
			t.Errorf("a secret is in the audit row: %q", secret)
		}
	}
	// The HWID may enrol again, as a new device in the freed place (the page's remove does the same).
	if again := e.enrolDevice(tok, hwidA); again.DeviceID == a.DeviceID {
		t.Error("the removed device came back")
	}
}

// A leave changes what the nodes are told to carry: the credential of the device is gone from the desired state.
func TestLeaveChangesTheDesiredStateOfTheNodes(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	e.mustSync(a.Key)
	if n := e.count(`SELECT count(*) FROM device_credential c JOIN device d ON d.id = c.device_id WHERE c.revoked_at IS NULL AND d.revoked_at IS NULL`); n < 2 {
		t.Fatalf("%d live credentials, want the link protocols and AWG", n)
	}
	notified := e.state.n.Load()
	if rec := e.leave(a.Key); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if e.state.n.Load() != notified+1 {
		t.Errorf("the nodes were told %d times, want once", e.state.n.Load()-notified)
	}
	if n := e.count(`SELECT count(*) FROM device_credential c JOIN device d ON d.id = c.device_id WHERE c.revoked_at IS NULL AND d.revoked_at IS NULL`); n != 0 {
		t.Errorf("%d credentials are still live for the nodes", n)
	}
	// A refused leave tells nobody.
	notified = e.state.n.Load()
	e.leave(a.Key)
	e.leave(newKey())
	if e.state.n.Load() != notified {
		t.Error("a refused leave told the nodes")
	}
}

// ---- the limits of the person ----

func TestSyncDoesNotExceedThePersonsLimits(t *testing.T) {
	e := newSyncEnv(t, nil)
	uid, tok := e.person("alice", func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 2 })
	a, b := e.enrolDevice(tok, hwidA), e.enrolDevice(tok, hwidB2)
	if rec := e.post(tok, enrolBody("ffffffff-ffff-4fff-8fff-ffffffffffff")); rec.Code != 409 || errorCode(t, rec) != "device_limit" {
		t.Fatalf("the third device = %d %q", rec.Code, rec.Body.String())
	}
	for i := 0; i < 3; i++ {
		e.mustSync(a.Key)
		e.mustSync(b.Key)
	}
	if n := e.count(`SELECT count(*) FROM device WHERE user_id = ? AND revoked_at IS NULL`, uid); n != 2 {
		t.Errorf("%d devices after the syncs, the limit is 2", n)
	}
	if n := e.count(`SELECT count(*) FROM device_credential WHERE user_id = ? AND protocol = 'awg' AND revoked_at IS NULL`, uid); n != 2 {
		t.Errorf("%d AWG credentials, want one per device", n)
	}
	// A sync never makes a device: a key of nobody is the decoy and nothing is written.
	rows := e.count(`SELECT (SELECT count(*) FROM device) + (SELECT count(*) FROM device_credential) + (SELECT count(*) FROM awg_peer)`)
	assertDecoy(t, "unknown key", e.sync(newKey()))
	if e.count(`SELECT (SELECT count(*) FROM device) + (SELECT count(*) FROM device_credential) + (SELECT count(*) FROM awg_peer)`) != rows {
		t.Error("a stranger's sync wrote rows")
	}
}

// ---- DNS ----

func TestSyncCarriesTheDNSOfThePerson(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	b := e.mustSync(a.Key)
	if b.DNS == nil {
		t.Fatalf("no dns in the body: %s", b.raw)
	}
	servers := b.DNS["servers"].([]any)
	if len(servers) == 0 {
		t.Fatalf("dns servers: %v", b.DNS)
	}
	first := servers[0].(map[string]any)
	if first["transport"] == nil || first["address"] == nil {
		t.Errorf("dns server: %v", first)
	}
	for _, k := range []string{"transport", "split", "split_direct", "block_ads", "ipv4_only"} {
		if _, ok := b.DNS[k]; !ok {
			t.Errorf("dns has no %q", k)
		}
	}
}

// ---- nothing secret in logs ----

func TestSyncAndLeaveLogNothingSecret(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	b := e.mustSync(a.Key, "X-Mistgate-Direct", "1")
	e.sync(newKey())
	e.sync(a.Key, "Authorization", "Bearer not-a-key")
	e.leave(a.Key)
	e.sync(a.Key)
	canon := strings.ReplaceAll(hwidA, "-", "")
	logs := e.logs.String()
	secrets := map[string]string{"link token": tok, "device key": a.Key, "HWID": hwidA, "canonical HWID": canon, "client address": enrolIP, "Bearer": "Bearer"}
	for _, p := range b.paths("awg") {
		secrets["AWG private key"] = p.Settings["private_key"].(string)
		secrets["AWG preshared key"] = p.Settings["preshared_key"].(string)
		if hpk, ok := p.Settings["params"].(map[string]any)["header_protection_key"].(string); ok {
			secrets["AWG header protection key"] = hpk
		} else {
			t.Fatalf("the test profile has no header protection key (not 3.1?): %v", p.Settings["params"])
		}
	}
	for _, p := range b.paths("hysteria2") {
		secrets["Hysteria2 credential"] = p.Settings["auth"].(string)
		secrets["Hysteria2 obfuscation password"] = p.Settings["obfs"].(map[string]any)["password"].(string)
	}
	for name, secret := range secrets {
		if secret == "" {
			t.Fatalf("no value for %s", name)
		}
		if strings.Contains(logs, secret) {
			t.Errorf("%s is in the log", name)
		}
		for _, q := range []string{`SELECT count(*) FROM audit WHERE params LIKE ? OR actor LIKE ? OR action LIKE ?`, `SELECT count(*) FROM event WHERE params_json LIKE ?`} {
			args := []any{"%" + secret + "%"}
			if strings.Contains(q, "audit") {
				args = append(args, args[0], args[0])
			}
			if e.count(q, args...) != 0 {
				t.Errorf("%s is in %q", name, q)
			}
		}
	}
	// Every warning and error the panel logged carries a code.
	for _, line := range strings.Split(logs, "\n") {
		if (strings.Contains(line, "level=WARN") || strings.Contains(line, "level=ERROR")) && !strings.Contains(line, "code=") {
			t.Errorf("a log line without a code: %s", line)
		}
	}
}

// A source that cannot serve the client API leaves the endpoints to the decoy, whatever the key.
func TestSyncOnASourceWithoutTheClientAPIIsTheDecoy(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/client/sync", nil)
	req.Header.Set("Authorization", bearer(newKey()))
	subs.Handler(bareSource{}, decoy, subs.Config{ClientIP: func(*http.Request) netip.Addr { return netip.MustParseAddr(enrolIP) }}).ServeHTTP(rec, req)
	assertDecoy(t, "a source without the client API", rec)
}

type bareSource struct{}

func (bareSource) Subscription(context.Context, string) (access.SubView, error) {
	return access.SubView{}, nil
}

// A server is one exit node; its IPv6 entry and each relay of it are more paths of the same server, and an inbound the owner keeps
// out of the subscriptions gives no direct path (the twins stay), as in the Mihomo profile.
func TestSyncPathsOfOneServer(t *testing.T) {
	e := newSyncEnv(t, nil)
	const relayName = "relay-helsinki-3"
	for _, q := range []string{
		`UPDATE node SET ipv6_entry = '2001:db8::5' WHERE id = 'nod_1'`,
		`INSERT INTO node (id, name, address, country_code, state, agent_caps, created_at) VALUES ('nod_2', '` + relayName + `', 'fi1.example.com', 'FI', 'active', 'relay-forward/1', 2)`,
	} {
		if _, err := e.st.W.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	var inbound string
	if err := e.st.R.QueryRow(`SELECT id FROM inbound WHERE profile_id = ?`, e.profile).Scan(&inbound); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.Exec(`INSERT INTO relay (id, inbound_id, via_node_id, network, port, enabled, state, check_json, created_at, updated_at)
		VALUES ('rly_1', ?, 'nod_2', 'udp', 8443, 1, 'active', ?, 1, 1)`, inbound, `{"at":1,"leg":{"status":"ok"},"path":{"status":"ok"},"ok":true}`); err != nil {
		t.Fatal(err)
	}
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	b := e.mustSync(a.Key)
	if len(b.Servers) != 1 {
		t.Fatalf("the relay's node became a server of its own: %s", b.raw)
	}
	if strings.Contains(b.raw, relayName) {
		t.Errorf("the relay node's name is in the body")
	}
	hy := b.paths("hysteria2")
	var got []string
	ids := map[string]bool{}
	for _, p := range hy {
		got = append(got, p.Via+"/"+strconv.FormatBool(p.IPv6)+"/"+p.Settings["server"].(string))
		ids[p.ID] = true
	}
	if want := "direct/false/de1.example.com direct/true/2001:db8::5 relay/false/fi1.example.com"; strings.Join(got, " ") != want || len(ids) != 3 {
		t.Errorf("hysteria2 paths: %v, want %s", got, want)
	}
	if hy[2].Settings["port"] != float64(8443) {
		t.Errorf("the relay path keeps the forwarded port: %v", hy[2].Settings["port"])
	}
	if _, ok := hy[2].Settings["ports"]; ok {
		t.Error("the relay path has a hop range (the forward is one port)")
	}

	if _, err := e.st.W.Exec(`UPDATE inbound SET direct_in_subscriptions = 0 WHERE id = ?`, inbound); err != nil {
		t.Fatal(err)
	}
	b = e.mustSync(a.Key)
	got = nil
	for _, p := range b.paths("hysteria2") {
		got = append(got, p.Via+"/"+strconv.FormatBool(p.IPv6))
	}
	if strings.Join(got, " ") != "direct/true relay/false" {
		t.Errorf("with the direct path off: %v", got)
	}
}

// Syncs of one device that race for the same missing credential make it once, and every one of them answers with the credential
// that won.
func TestSyncRacingFirstSyncsIssueOnce(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	const racers = 8
	bodies := make([]string, racers)
	codes := make([]int, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := e.sync(a.Key)
			codes[i], bodies[i] = rec.Code, rec.Body.String()
		}()
	}
	close(start)
	wg.Wait()
	for i := range racers {
		if codes[i] != 200 || bodies[i] != bodies[0] {
			t.Fatalf("racer %d: %d, a body that differs from the first:\n%s\n%s", i, codes[i], bodies[i], bodies[0])
		}
	}
	if n := e.count(`SELECT count(*) FROM device_credential WHERE device_id = ? AND protocol = 'awg' AND revoked_at IS NULL`, a.DeviceID); n != 1 {
		t.Errorf("%d AWG credentials, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM awg_peer WHERE released_at = 0`); n != 1 {
		t.Errorf("%d live peers, want 1", n)
	}
}

// ---- review: the decoy is the decoy, a live key is never a stranger, budgets of every key ----

func sortedHeaders(h http.Header) []string {
	var out []string
	for k := range h {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// view is everything a client can compare in an answer: status, headers and body.
func answerView(rec *httptest.ResponseRecorder) string {
	var hdr []string
	for _, k := range sortedHeaders(rec.Header()) {
		hdr = append(hdr, k+"="+strings.Join(rec.Header().Values(k), "|"))
	}
	return strconv.Itoa(rec.Code) + "\n" + strings.Join(hdr, "\n") + "\n" + rec.Body.String()
}

// The decoy of the client endpoints is the decoy of a path that does not exist: the same bytes and the same headers, for no key, a
// key of the wrong shape and a key of nobody, whatever the method. (A valid key with the right method is the one exception: it
// is answered.)
func TestSyncDecoyIsIndistinguishableFromAnyOtherPath(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	if neighbour := answerView(e.request("GET", "/client/syncx", "")); !strings.HasPrefix(neighbour, "404\n") || !strings.Contains(neighbour, decoyBody) {
		t.Fatalf("the neighbouring path is not the decoy: %q", neighbour)
	}
	for _, path := range []string{"/client/sync", "/client/leave"} {
		for _, method := range []string{"GET", "POST", "PUT", "DELETE", "HEAD", "OPTIONS"} {
			want := answerView(e.request(method, path+"x", ""))
			for name, key := range map[string]string{"no key": "", "unknown key": newKey()} {
				if got := answerView(e.request(method, path, key)); got != want {
					t.Errorf("%s %s (%s) differs from the decoy of a neighbouring path:\n%s\n--- want ---\n%s", method, path, name, got, want)
				}
			}
			for _, bad := range []string{"Bearer", "Bearer ", "Bearer not-a-key", "Basic " + a.Key, a.Key} {
				req := httptest.NewRequest(method, path, nil)
				req.Header.Set("Authorization", bad)
				req.Header.Set("User-Agent", syncUA)
				rec := httptest.NewRecorder()
				e.h.ServeHTTP(rec, req)
				if got := answerView(rec); got != want {
					t.Errorf("%s %s with %q differs from the decoy of a neighbouring path:\n%s\n--- want ---\n%s", method, path, bad, got, want)
				}
			}
			// a valid key with the wrong method is a stranger too
			if (path == "/client/sync" && method != "GET") || (path == "/client/leave" && method != "POST") {
				if got := answerView(e.request(method, path, a.Key)); got != want {
					t.Errorf("%s %s with a valid key differs from the decoy:\n%s\n--- want ---\n%s", method, path, got, want)
				}
			}
		}
	}
}

// A key that is still a live device's is never answered with the decoy and never counted as a miss: when the sync fails on
// something else (here a profile deleted between the snapshot and the issue), the answer is 503 retry.
func TestSyncValidKeyOnAnUnrelatedNotFoundGetsRetry(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	fired := false
	e.svc.SetSnapshotHookForTest(func() {
		if fired {
			return
		}
		fired = true
		for _, q := range []string{`DELETE FROM inbound WHERE profile_id = ?`, `DELETE FROM profile WHERE id = ?`} {
			if _, err := e.st.W.Exec(q, e.awg); err != nil {
				t.Errorf("%s: %v", q, err)
			}
		}
	})
	before := e.limiter.count("subscription-miss")
	rec := e.sync(a.Key)
	assertJSONError(t, "sync with a profile deleted mid-request", rec, 503, "retry")
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	if e.limiter.count("subscription-miss") != before {
		t.Error("a live key was counted as a miss")
	}
	e.svc.SetSnapshotHookForTest(nil)
	if rec := e.sync(a.Key); rec.Code != 200 {
		t.Errorf("the next sync = %d", rec.Code)
	}
}

// A link rotated between the snapshot of a sync and its write: nothing is issued under the old link, and the answer is the
// device's own, link_changed.
func TestSyncRotationRacingTheIssue(t *testing.T) {
	e := newSyncEnv(t, nil)
	uid, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	fired := false
	e.svc.SetSnapshotHookForTest(func() {
		if fired {
			return
		}
		fired = true
		must(e.svc.GetSubscriptionLink(e.ctx, connect.NewRequest(&adminv1.GetSubscriptionLinkRequest{UserId: uid, Rotate: true})))
	})
	before := e.limiter.count("subscription-miss")
	assertJSONError(t, "sync racing a rotation", e.sync(a.Key), 401, "link_changed")
	if e.limiter.count("subscription-miss") != before {
		t.Error("the race was counted as a miss")
	}
	if n := e.count(`SELECT count(*) FROM device_credential WHERE device_id = ? AND protocol = 'awg'`, a.DeviceID); n != 0 {
		t.Errorf("%d AWG credentials issued under a link that was rotated", n)
	}
}

// Every key of a device spends the budget of that device, the keys of removed, blocked and moved ones too; they are answered
// after the budget, so the read that finds a refused key is not free.
func TestSyncBudgetAppliesToRefusedKeys(t *testing.T) {
	e := newSyncEnv(t, nil)
	uid, tok := e.person("alice", nil)
	removed, blocked, moved := e.enrolDevice(tok, hwidA), e.enrolDevice(tok, hwidB2), e.enrolDevice(tok, "ffffffff-ffff-4fff-8fff-ffffffffffff")
	if _, err := e.st.Access().RevokeDevice(e.ctx, removed.DeviceID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Access().BlockDevice(e.ctx, blocked.DeviceID, time.Now()); err != nil {
		t.Fatal(err)
	}
	must(e.svc.GetSubscriptionLink(e.ctx, connect.NewRequest(&adminv1.GetSubscriptionLinkRequest{UserId: uid, Rotate: true})))
	for name, c := range map[string]struct{ key, code string }{
		"removed": {removed.Key, "device_removed"}, "blocked": {blocked.Key, "device_blocked"}, "moved": {moved.Key, "link_changed"},
	} {
		for i := 1; i <= 30; i++ {
			if rec := e.sync(c.key); rec.Code != 401 || errorCode(t, rec) != c.code {
				t.Fatalf("%s, request %d = %d %q", name, i, rec.Code, rec.Body.String())
			}
		}
		rec := e.sync(c.key)
		assertJSONError(t, name+", 31st request", rec, 429, "too_many_requests")
		if rec.Header().Get("Retry-After") == "" {
			t.Errorf("%s: no Retry-After", name)
		}
	}
	// A stranger's key is a miss, not a budget.
	before := e.limiter.count("subscription-client-sync")
	assertDecoy(t, "stranger", e.sync(newKey()))
	if e.limiter.count("subscription-client-sync") != before {
		t.Error("a stranger spent a budget")
	}
}

// The values of X-Mistgate-Caps are trimmed: "awg=2.0, 3.1" announces 3.1.
func TestSyncCapsAreTrimmed(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	for _, caps := range []string{"v=1; awg=2.0, 3.1; gecko=1", "v=1 ;  awg = 2.0 ,3.1", "awg=3.1 , 2.0"} {
		if b := e.mustSync(a.Key, "X-Mistgate-Caps", caps); len(b.paths("awg")) != 1 {
			t.Errorf("caps %q: no AWG path: %s", caps, b.raw)
		}
	}
}
