package subs_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// POST <link>/client/enrol against the real access module and database (one node with a Hysteria2 server).

const enrolIP = "198.51.100.88"

// lockedBuffer is a log sink that handlers on several goroutines may write to.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// stateCounter counts the "credentials changed" notifications the nodes are told about.
type stateCounter struct{ n atomic.Int32 }

func (c *stateCounter) StateChanged() { c.n.Add(1) }

// recordCounter counts the limiter records per window name, to see what a request was counted against.
type recordCounter struct {
	securitylimit.Limiter
	mu     sync.Mutex
	byName map[string]int
}

func (l *recordCounter) Record(ctx context.Context, w securitylimit.Window, key string) (securitylimit.Decision, error) {
	l.mu.Lock()
	if l.byName == nil {
		l.byName = map[string]int{}
	}
	l.byName[w.Name]++
	l.mu.Unlock()
	return l.Limiter.Record(ctx, w, key)
}

func (l *recordCounter) count(name string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.byName[name]
}

type enrolEnv struct {
	*rig
	h       http.Handler
	cache   *subsettings.Cache
	logs    *lockedBuffer
	state   *stateCounter
	limiter *recordCounter
}

// newEnrolEnv builds the panel and a handler that caches views for ten minutes (to see the cache dropped), has no fetch budget
// and no budget of self-service writes (the tests that look at the budgets say so), and logs everything to e.logs.
func newEnrolEnv(t *testing.T, mut func(*subs.Config)) *enrolEnv {
	t.Helper()
	ctx := context.Background()
	logs := &lockedBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prev := slog.Default()
	slog.SetDefault(log)
	t.Cleanup(func() { slog.SetDefault(prev) })

	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v, _ := vault.New(make([]byte, vault.KeySize))
	state := &stateCounter{}
	svc, err := access.New(st, v, builtin.Registry(), state, nil, access.Config{SubscriptionBaseURL: "https://sub.example.com/k3xq8", Log: log})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, ctx: ctx, st: st, svc: svc, clock: time.Now()}
	if _, err := st.W.Exec(`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_1', 'de1', 'de1.example.com', 'active', 1)`); err != nil {
		t.Fatal(err)
	}
	prof := must(svc.CreateProfile(ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "hysteria2", Name: "p"}))).Msg.Profile
	must(svc.CreateInbound(ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: prof.Id, NodeId: "nod_1"})))
	r.profile = prof.Id
	r.group = must(svc.CreateGroup(ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "g", ProfileIds: []string{prof.Id}}))).Msg.Group.Id

	limiter := &recordCounter{Limiter: securitylimit.NewMemory(time.Now, 0)}
	h, cache := r.handler(func(c *subs.Config) {
		c.Log = log
		c.Limiter = limiter
		c.MinInterval = 10 * time.Minute
		c.MaxPerHour = -1
		c.MaxWritesPerHour = -1
		c.MaxEnrolsPerHour = -1
		c.ClientIP = func(*http.Request) netip.Addr { return netip.MustParseAddr(enrolIP) }
		c.AfterResponse = func(f func()) { f() }
		if mut != nil {
			mut(c)
		}
	})
	r.h = h
	return &enrolEnv{rig: r, h: h, cache: cache, logs: logs, state: state, limiter: limiter}
}

func (e *enrolEnv) switchAPI(on bool) {
	e.t.Helper()
	v := "0"
	if on {
		v = "1"
	}
	if err := e.st.SetSettings(e.ctx, map[string]string{store.SettingClientAPI: v}); err != nil {
		e.t.Fatal(err)
	}
	e.cache.Invalidate()
}

// post is POST /<token>/client/enrol; headers are name, value pairs that replace the defaults (an empty value removes one).
func (e *enrolEnv) post(token, body string, hdr ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest("POST", "/"+token+"/client/enrol", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mistgate/0.1.0 (windows)")
	for i := 0; i+1 < len(hdr); i += 2 {
		switch {
		case hdr[i] == "Host":
			req.Host = hdr[i+1]
		case hdr[i+1] == "":
			req.Header.Del(hdr[i])
		default:
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// newKey is a device key as the client makes it: 32 random bytes, base64url without padding.
func newKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// enrolBody is a valid body for the HWID; the key is the given one, or a new one.
func enrolBody(hwid string, key ...string) string {
	k := newKey()
	if len(key) > 0 {
		k = key[0]
	}
	return fmt.Sprintf(`{"hwid":%q,"model":"Pixel 9","platform":"android","os_version":"15","device_key":%q}`, hwid, k)
}

func bearer(key string) string { return "Bearer " + key }

type enrolled struct {
	DeviceID   string `json:"device_id"`
	Base       string `json:"base"`
	SyncEveryS int    `json:"sync_every_s"`
	Key        string `json:"-"` // the key the client sent: the answer holds none
}

// enrolOK enrols with a new key and returns the answer; the request must succeed.
func (e *enrolEnv) enrolOK(token, hwid string, hdr ...string) enrolled {
	e.t.Helper()
	return e.enrolWith(token, hwid, newKey(), hdr...)
}

// enrolWith is enrolOK with the key of the client's choice.
func (e *enrolEnv) enrolWith(token, hwid, key string, hdr ...string) enrolled {
	e.t.Helper()
	rec := e.post(token, enrolBody(hwid, key), hdr...)
	if rec.Code != 200 {
		e.t.Fatalf("enrol of %s = %d %s", hwid, rec.Code, rec.Body.String())
	}
	var a enrolled
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil || a.DeviceID == "" {
		e.t.Fatalf("answer %q: %v", rec.Body.String(), err)
	}
	a.Key = key
	return a
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("not an error body (status %d): %q", rec.Code, rec.Body.String())
	}
	return body.Error
}

func keyHashOf(t *testing.T, key string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 {
		t.Fatalf("key %q is not 32 bytes of base64url without padding: %v", key, err)
	}
	sum := sha256.Sum256(raw)
	return sum[:]
}

func (e *enrolEnv) liveCredIDs(deviceID string) string {
	e.t.Helper()
	rows, err := e.st.R.Query(`SELECT id FROM device_credential WHERE device_id = ? AND revoked_at IS NULL ORDER BY id`, deviceID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			e.t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return strings.Join(ids, ",")
}

func (e *enrolEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.st.R.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestEnrolIsDecoyForUnknownTokenAndCountsAsAMiss(t *testing.T) {
	e := newEnrolEnv(t, nil)
	e.switchAPI(false) // off: an unknown token is still the decoy, never "client_api_off"
	_, tok := e.user("alice", nil)
	for _, token := range []string{unknown, "short"} {
		before := e.limiter.count("subscription-miss")
		rec := e.post(token, enrolBody(hwidA))
		if rec.Code != 404 || rec.Body.String() != decoyBody {
			t.Fatalf("enrol under %q = %d %q, want the decoy", token, rec.Code, rec.Body.String())
		}
		if got := e.limiter.count("subscription-miss") - before; got != 1 {
			t.Fatalf("enrol under %q was counted %d times as a miss, want 1", token, got)
		}
	}
	// A valid token with the switch off is the 403, and no miss.
	before := e.limiter.count("subscription-miss")
	if rec := e.post(tok, enrolBody(hwidA)); rec.Code != 403 || errorCode(t, rec) != "client_api_off" {
		t.Fatalf("switch off = %d %q", rec.Code, rec.Body.String())
	}
	if e.limiter.count("subscription-miss") != before {
		t.Fatal("a valid link was counted as a miss")
	}
	// Anything but POST is no route: the decoy and a miss, as for any other path of the link.
	before = e.limiter.count("subscription-miss")
	if rec := fetch(e.h, "/"+tok+"/client/enrol", happUA); rec.Code != 404 || rec.Body.String() != decoyBody {
		t.Fatalf("GET = %d %q, want the decoy", rec.Code, rec.Body.String())
	}
	if e.limiter.count("subscription-miss") != before+1 {
		t.Fatal("a wrong method was not counted as a miss")
	}
	if e.count(`SELECT count(*) FROM device`) != 0 {
		t.Fatal("a refused enrolment made a device")
	}
}

func TestEnrolRefusesBeforeItWrites(t *testing.T) {
	// The default budget of enrols (five): the refused bodies below must not spend any of it.
	e := newEnrolEnv(t, func(c *subs.Config) { c.MaxEnrolsPerHour = 0 })
	e.switchAPI(true)
	_, tok := e.user("alice", nil)
	notified := e.state.n.Load()
	k := newKey()
	body := func(fields string) string { return `{"hwid":"` + hwidA + `","device_key":"` + k + `"` + fields + `}` }
	big := body(`,"model":"` + strings.Repeat("x", 5<<10) + `"`)
	pad := base64.URLEncoding.EncodeToString(make([]byte, 32)) // with padding
	std := strings.Repeat("ûÿþ", 11)[:32]                      // "+/" in the standard alphabet, not in the URL one
	std = base64.RawStdEncoding.EncodeToString([]byte(std))
	for _, c := range []struct {
		name   string
		body   string
		hdr    []string
		status int
		code   string
	}{
		{"cross-origin", enrolBody(hwidA), []string{"Sec-Fetch-Site", "cross-site"}, 403, "cross_origin"},
		{"cross-origin by Origin", enrolBody(hwidA), []string{"Origin", "https://evil.example"}, 403, "cross_origin"},
		{"not JSON", enrolBody(hwidA), []string{"Content-Type", "text/plain"}, 415, "unsupported_media_type"},
		{"no content type", enrolBody(hwidA), []string{"Content-Type", ""}, 415, "unsupported_media_type"},
		{"too large", big, nil, 413, "too_large"},
		{"unknown field", body(`,"name":"x"`), nil, 400, "bad_request"},
		{"caps are a header, not a field", body(`,"caps":"v=1"`), nil, 400, "bad_request"},
		{"not an object", `["a"]`, nil, 400, "bad_request"},
		{"a number for a string", `{"hwid":12345678901234}`, nil, 400, "bad_request"},
		{"trailing data", enrolBody(hwidA) + ` {}`, nil, 400, "bad_request"},
		{"empty body", ``, nil, 400, "bad_request"},
		{"no hwid", `{"platform":"android","device_key":"` + k + `"}`, nil, 400, "bad_request"},
		{"short hwid", enrolBody("abc"), nil, 400, "bad_request"},
		{"hwid with bad characters", enrolBody("0f8fad5b d9cb 469f a165 7086"), nil, 400, "bad_request"},
		{"unknown platform", body(`,"platform":"plan9"`), nil, 400, "bad_request"},
		{"model with a control character", body(`,"model":"Pixel"`), nil, 400, "bad_request"},
		{"model too long", body(`,"model":"` + strings.Repeat("m", 41) + `"`), nil, 400, "bad_request"},
		{"no device key", `{"hwid":"` + hwidA + `"}`, nil, 400, "bad_request"},
		{"device key not base64url", `{"hwid":"` + hwidA + `","device_key":"not a key!"}`, nil, 400, "bad_request"},
		{"device key of 31 bytes", `{"hwid":"` + hwidA + `","device_key":"` + base64.RawURLEncoding.EncodeToString(make([]byte, 31)) + `"}`, nil, 400, "bad_request"},
		{"device key of 33 bytes", `{"hwid":"` + hwidA + `","device_key":"` + base64.RawURLEncoding.EncodeToString(make([]byte, 33)) + `"}`, nil, 400, "bad_request"},
		{"device key with padding", `{"hwid":"` + hwidA + `","device_key":"` + pad + `"}`, nil, 400, "bad_request"},
		{"device key in standard base64", `{"hwid":"` + hwidA + `","device_key":"` + std + `"}`, nil, 400, "bad_request"},
	} {
		rec := e.post(tok, c.body, c.hdr...)
		if rec.Code != c.status || errorCode(t, rec) != c.code {
			t.Errorf("%s: %d %q, want %d %s", c.name, rec.Code, rec.Body.String(), c.status, c.code)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: Cache-Control %q", c.name, rec.Header().Get("Cache-Control"))
		}
	}
	if n := e.count(`SELECT count(*) FROM device`); n != 0 {
		t.Fatalf("%d devices made by refused requests", n)
	}
	if e.state.n.Load() != notified {
		t.Fatal("the nodes were told about a refused request")
	}
	// A request that failed validation did not spend the budget of enrols (the limit is the default one here): the five that
	// are allowed all go through afterwards, and the sixth is the one refused.
	if n := e.limiter.count("subscription-client-enrol"); n != 0 {
		t.Fatalf("refused bodies cost %d enrol budget units", n)
	}
	for n := 1; n <= 5; n++ {
		e.enrolOK(tok, fmt.Sprintf("c0ffee00-c0ff-ee00-c0ff-ee00c0ffee%02d", n))
	}
	if rec := e.post(tok, enrolBody("c0ffee00-c0ff-ee00-c0ff-ee00c0ffee06")); rec.Code != 429 {
		t.Fatalf("sixth enrol = %d %q, want 429", rec.Code, rec.Body.String())
	}
}

func TestEnrolAnswersOnceWithAKeyOfItsOwn(t *testing.T) {
	e := newEnrolEnv(t, nil)
	e.switchAPI(true)
	uid, tok := e.user("alice", nil)

	// A basic fetch with the HWID made a device already: the enrolment takes its slot and replaces its credentials.
	basic := fetch(e.h, "/"+tok, happUA, "X-HWID", hwidA, "X-Device-OS", "Windows")
	if !strings.HasPrefix(decode(t, basic.Body.String()), "hysteria2://") {
		t.Fatalf("basic fetch: %q", basic.Body.String())
	}
	var basicDevice string
	if err := e.st.R.QueryRow(`SELECT id FROM device WHERE user_id = ?`, uid).Scan(&basicDevice); err != nil {
		t.Fatal(err)
	}
	basicCreds := e.liveCredIDs(basicDevice)
	notified := e.state.n.Load()

	key := newKey()
	rec := e.post(tok, enrolBody(hwidA, key), "Host", "evil.example") // not the Host decides the address
	if rec.Code != 200 {
		t.Fatalf("enrol = %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", rec.Header())
	}
	var a enrolled
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &fields); err != nil || len(fields) != 3 || fields["device_id"] == nil || fields["base"] == nil || fields["sync_every_s"] == nil {
		t.Fatalf("the answer has fields %v, want device_id, base, sync_every_s", fields)
	}
	if strings.Contains(rec.Body.String(), key) {
		t.Fatal("the answer shows the key")
	}
	a.Key = key
	if a.DeviceID != basicDevice {
		t.Errorf("device %s, want the slot of the basic device %s", a.DeviceID, basicDevice)
	}
	if a.Base != "https://sub.example.com/k3xq8/client" || strings.Contains(a.Base, tok) {
		t.Errorf("base = %q", a.Base)
	}
	if a.SyncEveryS != 3600 {
		t.Errorf("sync_every_s = %d", a.SyncEveryS)
	}
	hash := keyHashOf(t, a.Key)

	dev, owner, err := e.st.Access().LinkDeviceByKeyHash(e.ctx, hash)
	tokenHash := sha256.Sum256([]byte(tok))
	if err != nil || dev.ID != a.DeviceID || owner.UserID != uid || !bytes.Equal(dev.EnrolTokenHash, tokenHash[:]) || !bytes.Equal(owner.SubTokenHash, tokenHash[:]) {
		t.Fatalf("lookup of the key = %+v %+v %v", dev, owner, err)
	}
	if dev.Platform != "android" || dev.Model != "Pixel 9" || dev.Client != "Mistgate/0.1.0" {
		t.Errorf("device recorded as platform %q model %q client %q", dev.Platform, dev.Model, dev.Client)
	}
	if got := e.liveCredIDs(a.DeviceID); got == "" || got == basicCreds {
		t.Errorf("credentials after the enrolment %q, before %q: want new ones", got, basicCreds)
	}
	if e.state.n.Load() == notified {
		t.Error("the nodes were not told about the changed credentials")
	}
	if n := e.count(`SELECT count(*) FROM device WHERE user_id = ?`, uid); n != 1 {
		t.Errorf("%d devices, want the one slot", n)
	}

	// Only the hash is stored: the key is in no column, and not in the audit, the events or the log.
	raw, _ := base64.RawURLEncoding.DecodeString(a.Key)
	if e.count(`SELECT count(*) FROM device WHERE key_hash = ?`, raw) != 0 || e.count(`SELECT count(*) FROM device WHERE key_hash = ?`, hash) != 1 {
		t.Error("the key itself is stored, or its hash is not")
	}
	for _, q := range []string{`SELECT count(*) FROM audit WHERE params LIKE ? OR actor LIKE ? OR action LIKE ?`, `SELECT count(*) FROM event WHERE params_json LIKE ?`} {
		args := []any{"%" + a.Key + "%"}
		if strings.Contains(q, "audit") {
			args = append(args, args[0], args[0])
		}
		if e.count(q, args...) != 0 {
			t.Errorf("the key is in %q", q)
		}
	}
	// Audit: one row, the user as the actor, the device in the params; no address, no HWID, no key, no link.
	var actor, action, params, ip string
	if e.count(`SELECT count(*) FROM audit WHERE action LIKE 'client_device_%'`) != 1 {
		t.Fatal("want exactly one audit row of the enrolment")
	}
	if err := e.st.R.QueryRow(`SELECT actor, action, params, ip FROM audit WHERE action LIKE 'client_device_%'`).Scan(&actor, &action, &params, &ip); err != nil {
		t.Fatal(err)
	}
	if actor != "user:"+uid || action != "client_device_enrolled" || ip != "" || !strings.Contains(params, a.DeviceID) {
		t.Errorf("audit row: actor %q action %q ip %q params %q", actor, action, ip, params)
	}
	canon := strings.ReplaceAll(hwidA, "-", "")
	for _, secret := range []string{tok, hwidA, canon, a.Key, enrolIP, "Bearer"} {
		if strings.Contains(params, secret) || strings.Contains(actor, secret) || strings.Contains(e.logs.String(), secret) {
			t.Errorf("a secret is in the audit row or the log: %q", secret)
		}
	}

	// Another key for the enrolled HWID, without the old one, is refused.
	if rec := e.post(tok, enrolBody(hwidA)); rec.Code != 409 || errorCode(t, rec) != "device_exists" {
		t.Errorf("second enrol with another key = %d %q", rec.Code, rec.Body.String())
	}
}

// base must not come from a Host header: without a configured public address enrol is not available.
func TestEnrolNeedsAConfiguredPublicAddress(t *testing.T) {
	e := newEnrolEnv(t, func(c *subs.Config) { c.BaseURL = ""; c.Prefix = "/k3xq8" })
	e.switchAPI(true)
	_, tok := e.user("alice", nil)
	req := httptest.NewRequest("POST", "/k3xq8/"+tok+"/client/enrol", strings.NewReader(enrolBody(hwidA)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 503 || errorCode(t, rec) != "not_configured" {
		t.Fatalf("enrol without a public address = %d %q, want 503 not_configured", rec.Code, rec.Body.String())
	}
	if e.count(`SELECT count(*) FROM device`) != 0 {
		t.Fatal("a device was made")
	}
	// An unknown link is still the decoy, and the switch still comes first.
	if rec := e.post(unknown, enrolBody(hwidA)); rec.Code != 404 {
		t.Fatalf("unknown link = %d", rec.Code)
	}
}

func TestReenrolKeepsTheDeviceAndFollowsARotatedLink(t *testing.T) {
	e := newEnrolEnv(t, nil)
	e.switchAPI(true)
	uid, tok := e.user("alice", nil)
	first := e.enrolOK(tok, hwidA)
	creds := e.liveCredIDs(first.DeviceID)
	notified := e.state.n.Load()

	second := e.enrolOK(tok, hwidA, "Authorization", bearer(first.Key))
	if second.DeviceID != first.DeviceID || second.Key == first.Key {
		t.Fatalf("re-enrol gave device %s key-changed=%v; want the same device and a new key", second.DeviceID, second.Key != first.Key)
	}
	if got := e.liveCredIDs(first.DeviceID); got != creds {
		t.Errorf("credentials changed by a re-enrolment: %q -> %q", creds, got)
	}
	if e.state.n.Load() != notified {
		t.Error("a re-enrolment told the nodes about credentials that did not change")
	}
	if _, _, err := e.st.Access().LinkDeviceByKeyHash(e.ctx, keyHashOf(t, first.Key)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the old key still resolves: %v", err)
	}
	if dev, _, err := e.st.Access().LinkDeviceByKeyHash(e.ctx, keyHashOf(t, second.Key)); err != nil || dev.ID != first.DeviceID {
		t.Fatalf("the new key = %+v, %v", dev, err)
	}
	// The old key is nobody's now: with the HWID that is enrolled this is "device exists".
	if rec := e.post(tok, enrolBody(hwidA), "Authorization", bearer(first.Key)); rec.Code != 409 || errorCode(t, rec) != "device_exists" {
		t.Fatalf("enrol with the spent key = %d %q", rec.Code, rec.Body.String())
	}

	// The link is rotated: the old token is dead, the device comes back with the new one and its old key, and keeps its slot.
	link := must(e.svc.GetSubscriptionLink(e.ctx, connect.NewRequest(&adminv1.GetSubscriptionLinkRequest{UserId: uid, Rotate: true}))).Msg.Url
	newTok := link[strings.LastIndex(link, "/")+1:]
	if rec := e.post(tok, enrolBody(hwidA), "Authorization", bearer(second.Key)); rec.Code != 404 {
		t.Fatalf("enrol under the rotated token = %d, want the decoy", rec.Code)
	}
	third := e.enrolOK(newTok, hwidA, "Authorization", bearer(second.Key))
	if third.DeviceID != first.DeviceID {
		t.Fatalf("device after the rotation: %s, want %s", third.DeviceID, first.DeviceID)
	}
	dev, _, err := e.st.Access().LinkDeviceByKeyHash(e.ctx, keyHashOf(t, third.Key))
	newHash := sha256.Sum256([]byte(newTok))
	if err != nil || !bytes.Equal(dev.EnrolTokenHash, newHash[:]) {
		t.Fatalf("enrol_token_hash was not moved to the new link: %v", err)
	}
	if strings.Contains(third.Base, newTok) || third.Base != "https://sub.example.com/k3xq8/client" {
		t.Errorf("base = %q", third.Base)
	}
	if e.count(`SELECT count(*) FROM audit WHERE action = 'client_device_reenrolled'`) != 2 || e.count(`SELECT count(*) FROM audit WHERE action = 'client_device_enrolled'`) != 1 {
		t.Error("audit rows are not one enrolment and two re-enrolments")
	}
}

func TestEnrolWithKeysThatAreNotTheDevicesOwn(t *testing.T) {
	e := newEnrolEnv(t, nil)
	e.switchAPI(true)
	_, alice := e.user("alice", nil)
	_, bob := e.user("bob", nil)
	a := e.enrolOK(alice, hwidA)
	b := e.enrolOK(alice, hwidB)
	bobs := e.enrolOK(bob, hwidA) // the same HWID value under another person is another device

	random := make([]byte, 32)
	for _, c := range []struct {
		name, auth string
	}{
		{"a random key", bearer(base64.RawURLEncoding.EncodeToString(random))},
		{"not base64", bearer("not a key!")},
		{"a short key", bearer("AAAA")},
		{"another scheme", "Basic " + a.Key},
		{"a foreign person's key", bearer(bobs.Key)},
		{"the key of this person's other device", bearer(b.Key)},
	} {
		rec := e.post(alice, enrolBody(hwidA), "Authorization", c.auth)
		if rec.Code != 409 || errorCode(t, rec) != "device_exists" {
			t.Errorf("%s: %d %q, want 409 device_exists (treated as no key)", c.name, rec.Code, rec.Body.String())
		}
	}
	if dev, _, err := e.st.Access().LinkDeviceByKeyHash(e.ctx, keyHashOf(t, a.Key)); err != nil || dev.ID != a.DeviceID {
		t.Fatalf("a refused request changed the device's key: %+v, %v", dev, err)
	}

	// A device of this person that was removed, or blocked, says so to the one who holds its key, whatever HWID it brings.
	if _, err := e.st.Access().RevokeDevice(e.ctx, a.DeviceID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if rec := e.post(alice, enrolBody(hwidA), "Authorization", bearer(a.Key)); rec.Code != 401 || errorCode(t, rec) != "device_removed" {
		t.Errorf("removed device's key = %d %q", rec.Code, rec.Body.String())
	}
	if rec := e.post(alice, enrolBody(hwidB), "Authorization", bearer(a.Key)); rec.Code != 401 || errorCode(t, rec) != "device_removed" {
		t.Errorf("removed device's key with another HWID = %d %q", rec.Code, rec.Body.String())
	}
	if _, err := e.st.Access().BlockDevice(e.ctx, b.DeviceID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if rec := e.post(alice, enrolBody(hwidB), "Authorization", bearer(b.Key)); rec.Code != 401 || errorCode(t, rec) != "device_blocked" {
		t.Errorf("blocked device's key = %d %q", rec.Code, rec.Body.String())
	}
	// The key of a removed device of another person is no key to this one.
	if _, err := e.st.Access().RevokeDevice(e.ctx, bobs.DeviceID, time.Now()); err != nil {
		t.Fatal(err)
	}
	c := e.enrolOK(alice, "c0ffee00-c0ff-ee00-c0ff-ee00c0ffee00", "Authorization", bearer(bobs.Key))
	if c.DeviceID == bobs.DeviceID {
		t.Fatal("a foreign removed key re-enrolled a device")
	}
	// A removed HWID is a new device (rule 3 of the draft); a blocked one is refused.
	again := e.enrolOK(alice, hwidA)
	if again.DeviceID == a.DeviceID {
		t.Fatal("the removed device came back")
	}
	if rec := e.post(alice, enrolBody(hwidB)); rec.Code != 403 || errorCode(t, rec) != "device_blocked" {
		t.Errorf("blocked HWID = %d %q", rec.Code, rec.Body.String())
	}
}

func TestEnrolRefusalsOfThePersonAndTheLimit(t *testing.T) {
	e := newEnrolEnv(t, nil)
	e.switchAPI(true)
	e.setLinkMode(store.LinkModeEnforce)
	uid, tok := e.user("alice", func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 1 })
	first := e.enrolOK(tok, hwidA)
	rec := e.post(tok, enrolBody(hwidB))
	if rec.Code != 409 || errorCode(t, rec) != "device_limit" {
		t.Fatalf("second device at the limit = %d %q", rec.Code, rec.Body.String())
	}
	if e.count(`SELECT count(*) FROM device WHERE user_id = ? AND revoked_at IS NULL`, uid) != 1 {
		t.Fatal("a device was made over the limit")
	}

	// A person who cannot use the servers gets no new device, and the status is the message; a device they have follows the link.
	if _, err := e.st.W.Exec(`UPDATE user SET disabled = 1 WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	rec = e.post(tok, enrolBody(hwidB))
	var body struct{ Error, Message string }
	json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != 409 || body.Error != "user_inactive" || body.Message != access.StatusDisabled {
		t.Fatalf("disabled person = %d %q", rec.Code, rec.Body.String())
	}
	e.enrolOK(tok, hwidA, "Authorization", bearer(first.Key))

	// A person none of whose apps takes a link device has nothing to enrol with.
	_, amnezia := e.user("bob", func(m *adminv1.CreateUserRequest) { m.Apps = &adminv1.AppToggles{Amnezia: true} })
	if rec := e.post(amnezia, enrolBody(hwidA)); rec.Code != 409 || errorCode(t, rec) != "app_disabled" {
		t.Fatalf("a person without a link app = %d %q, want 409 app_disabled", rec.Code, rec.Body.String())
	}
}

// The answer of an enrolment can be lost on the way. The client sends the same key again and gets the same answer, once more,
// with nothing changed: no new credentials, no second audit row, no notification of the nodes and no budget spent.
func TestEnrolRepeatOfALostAnswerIsIdempotent(t *testing.T) {
	e := newEnrolEnv(t, func(c *subs.Config) { c.MaxEnrolsPerHour = 0 })
	e.switchAPI(true)
	_, tok := e.user("alice", nil)
	key := newKey()
	first := e.enrolWith(tok, hwidA, key)
	creds := e.liveCredIDs(first.DeviceID)
	notified := e.state.n.Load()
	audits := e.count(`SELECT count(*) FROM audit WHERE action LIKE 'client_device_%'`)
	spent := e.limiter.count("subscription-client-enrol")
	for range 8 { // more than the budget of five
		again := e.enrolWith(tok, hwidA, key)
		if again.DeviceID != first.DeviceID || again.Base != first.Base || again.SyncEveryS != first.SyncEveryS {
			t.Fatalf("repeat = %+v, want %+v", again, first)
		}
	}
	if e.liveCredIDs(first.DeviceID) != creds || e.state.n.Load() != notified ||
		e.count(`SELECT count(*) FROM audit WHERE action LIKE 'client_device_%'`) != audits || e.limiter.count("subscription-client-enrol") != spent {
		t.Fatal("a repeat changed the credentials, notified the nodes, wrote the audit or spent the budget")
	}
	// Another key is not a repeat.
	if rec := e.post(tok, enrolBody(hwidA)); rec.Code != 409 || errorCode(t, rec) != "device_exists" {
		t.Fatalf("another key without the Bearer = %d %q", rec.Code, rec.Body.String())
	}
	// The same key for another HWID is no repeat either (the key is one device's).
	if rec := e.post(tok, enrolBody(hwidB, key)); rec.Code == 200 {
		t.Fatalf("the key of a device enrolled another HWID: %q", rec.Body.String())
	}
	// A repeat after the device was removed is not an answer: the key died with it.
	if _, err := e.st.Access().RevokeDevice(e.ctx, first.DeviceID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if rec := e.post(tok, enrolBody(hwidA, key)); rec.Code == 200 {
		t.Fatalf("a removed device's key enrolled again: %q", rec.Body.String())
	}
}

// The same for a re-enrolment: the new key is in place, the answer was lost, and the client asks again, with the old key as
// Bearer (which is gone now) or with the new one. Both are 200 with the device, and nothing else happens.
func TestReenrolRepeatOfALostAnswerIsIdempotent(t *testing.T) {
	e := newEnrolEnv(t, nil)
	e.switchAPI(true)
	_, tok := e.user("alice", nil)
	first := e.enrolOK(tok, hwidA)
	creds := e.liveCredIDs(first.DeviceID)
	next := newKey()
	second := e.enrolWith(tok, hwidA, next, "Authorization", bearer(first.Key))
	notified := e.state.n.Load()
	audits := e.count(`SELECT count(*) FROM audit WHERE action LIKE 'client_device_%'`)
	for name, hdr := range map[string][]string{
		"the old key as Bearer, erased by now": {"Authorization", bearer(first.Key)},
		"the new key as Bearer":                {"Authorization", bearer(next)},
		"no Bearer":                            nil,
	} {
		again := e.enrolWith(tok, hwidA, next, hdr...)
		if again.DeviceID != second.DeviceID {
			t.Errorf("%s: device %s, want %s", name, again.DeviceID, second.DeviceID)
		}
	}
	if e.liveCredIDs(first.DeviceID) != creds || e.state.n.Load() != notified || e.count(`SELECT count(*) FROM audit WHERE action LIKE 'client_device_%'`) != audits {
		t.Fatal("a repeat changed the credentials, notified the nodes or wrote the audit")
	}
	if dev, _, err := e.st.Access().LinkDeviceByKeyHash(e.ctx, keyHashOf(t, next)); err != nil || dev.ID != first.DeviceID {
		t.Fatalf("the new key = %+v, %v", dev, err)
	}
	if _, _, err := e.st.Access().LinkDeviceByKeyHash(e.ctx, keyHashOf(t, first.Key)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the old key still resolves: %v", err)
	}
}

func TestEnrolBudgets(t *testing.T) {
	t.Run("five enrols that make credentials a link an hour; re-enrolments and repeats are free", func(t *testing.T) {
		e := newEnrolEnv(t, func(c *subs.Config) { c.MaxEnrolsPerHour = 0 }) // the default
		e.switchAPI(true)
		_, tok := e.user("alice", nil)
		_, other := e.user("bob", nil)
		hwid := func(n int) string { return fmt.Sprintf("c0ffee00-c0ff-ee00-c0ff-ee00c0ffee%02d", n) }
		first := e.enrolOK(tok, hwid(1))
		// Re-enrolments (any number) and a repeat of a lost answer cost nothing.
		key := first.Key
		for range 7 {
			key = e.enrolOK(tok, hwid(1), "Authorization", bearer(key)).Key
		}
		e.enrolWith(tok, hwid(1), key)
		for n := 2; n <= 5; n++ {
			e.enrolOK(tok, hwid(n))
		}
		rec := e.post(tok, enrolBody(hwid(6)))
		if rec.Code != 429 || errorCode(t, rec) != "too_many_requests" || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("sixth enrol that makes credentials = %d %q, Retry-After %q", rec.Code, rec.Body.String(), rec.Header().Get("Retry-After"))
		}
		if got := e.limiter.count("subscription-client-enrol"); got != 6 {
			t.Errorf("the enrol budget was taken %d times, want 6 (five enrols and the refused sixth)", got)
		}
		// A re-enrolment and a repeat still go through when the budget is gone.
		key = e.enrolOK(tok, hwid(1), "Authorization", bearer(key)).Key
		e.enrolWith(tok, hwid(1), key)
		// The budget is the link's own.
		e.enrolOK(other, hwidA)
	})
	t.Run("the self-service write budget is taken too", func(t *testing.T) {
		e := newEnrolEnv(t, func(c *subs.Config) { c.MaxWritesPerHour = 2 })
		e.switchAPI(true)
		_, tok := e.user("alice", nil)
		e.enrolOK(tok, hwidA)
		e.enrolOK(tok, hwidB)
		rec := e.post(tok, enrolBody("c0ffee00-c0ff-ee00-c0ff-ee00c0ffee00"))
		if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
			t.Fatalf("third write = %d %q", rec.Code, rec.Body.String())
		}
		if got := e.limiter.count("subscription-writes"); got != 3 {
			t.Errorf("writes recorded %d times, want 3", got)
		}
	})
}

// Two enrolments at the limit make one device; one HWID twice makes one.
func TestEnrolInParallelMakesOneDevice(t *testing.T) {
	run := func(t *testing.T, hwids []string, limit uint32, key ...string) (ok int, codes map[string]int, devices int) {
		e := newEnrolEnv(t, nil)
		e.switchAPI(true)
		e.setLinkMode(store.LinkModeEnforce)
		uid, tok := e.user("alice", func(m *adminv1.CreateUserRequest) { m.DeviceLimit = limit })
		start := make(chan struct{})
		var wg sync.WaitGroup
		var mu sync.Mutex
		codes = map[string]int{}
		for _, hwid := range hwids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				rec := e.post(tok, enrolBody(hwid, key...))
				mu.Lock()
				defer mu.Unlock()
				if rec.Code == 200 {
					ok++
				} else {
					var b struct{ Error string }
					json.Unmarshal(rec.Body.Bytes(), &b)
					codes[b.Error]++
				}
			}()
		}
		close(start)
		wg.Wait()
		return ok, codes, e.count(`SELECT count(*) FROM device WHERE user_id = ? AND revoked_at IS NULL`, uid)
	}
	t.Run("two HWIDs at a limit of one", func(t *testing.T) {
		if ok, codes, devices := run(t, []string{hwidA, hwidB}, 1); ok != 1 || codes["device_limit"] != 1 || devices != 1 {
			t.Fatalf("%d enrolled, refusals %v; want one and one device_limit", ok, codes)
		}
	})
	t.Run("one HWID and one key twice: the twins both get the answer", func(t *testing.T) {
		k := newKey()
		if ok, codes, devices := run(t, []string{hwidA, hwidA, hwidA}, 5, k); ok != 3 || len(codes) != 0 || devices != 1 {
			t.Fatalf("%d answered, refusals %v, %d devices; want all three to get the one device", ok, codes, devices)
		}
	})
	t.Run("one HWID twice", func(t *testing.T) {
		if ok, codes, devices := run(t, []string{hwidA, hwidA}, 5); ok != 1 || codes["device_exists"] != 1 || devices != 1 {
			t.Fatalf("%d enrolled, refusals %v; want one and one device_exists", ok, codes)
		}
	})
}

// A basic fetch that carries the HWID of an enrolled device gets the one-entry refusal in every format, the same HWIDs of the
// person that are not enrolled are served as before, and nothing is written for the refused request.
func TestBasicFetchOfAnEnrolledHWIDIsRefusedInEveryFormat(t *testing.T) {
	e := newEnrolEnv(t, nil)
	e.switchAPI(true)
	uid, tok := e.user("alice", nil)
	formats := []struct {
		name, path, ua string
		raw            bool // the body is not base64
	}{
		{"base64 list", "/" + tok, happUA, false},
		{"plain URI list", "/" + tok, curlUA, false},
		{"Mihomo YAML", "/" + tok, mihomoUA, true},
		{"Xray JSON", "/" + tok + "?format=xray-json", happUA, true},
		{"sing-box JSON", "/" + tok, sfaUA, true},
	}
	body := func(f struct {
		name, path, ua string
		raw            bool
	}, rec *httptest.ResponseRecorder) string {
		if f.raw {
			return rec.Body.String()
		}
		return decode(t, rec.Body.String())
	}
	// Served before the enrolment, from the cache too: a view cached for the HWID must not outlive it.
	for _, f := range formats {
		rec := fetch(e.h, f.path, f.ua, "X-HWID", hwidA)
		if rec.Code != 200 || !strings.Contains(body(f, rec), "de1.example.com") {
			t.Fatalf("%s before the enrolment: %d %q", f.name, rec.Code, rec.Body.String())
		}
	}
	a := e.enrolOK(tok, hwidA)
	enrolledCreds := e.liveCredIDs(a.DeviceID)
	for _, f := range formats {
		rec := fetch(e.h, f.path, f.ua, "X-HWID", hwidA)
		got := body(f, rec)
		if rec.Code != 200 || strings.Contains(got, "de1.example.com") || !(strings.Contains(got, "its own app") || strings.Contains(got, "its%20own%20app")) {
			t.Errorf("%s with the enrolled HWID: %d %q; want the refusal entry and no server", f.name, rec.Code, got)
		}
		// The same HWID, spelled as the draft allows, is the same device.
		if got := body(f, fetch(e.h, f.path, f.ua, "X-HWID", strings.ToUpper(strings.ReplaceAll(hwidA, "-", "")))); strings.Contains(got, "de1.example.com") {
			t.Errorf("%s with the HWID in upper case was served: %q", f.name, got)
		}
		// Others of the same person are served as ever.
		if got := body(f, fetch(e.h, f.path, f.ua, "X-HWID", hwidB)); !strings.Contains(got, "de1.example.com") {
			t.Errorf("%s with another HWID is not served: %q", f.name, got)
		}
		if got := body(f, fetch(e.h, f.path, f.ua)); !strings.Contains(got, "de1.example.com") {
			t.Errorf("%s without a HWID is not served: %q", f.name, got)
		}
	}
	// Refused requests write nothing: no device, no credential, no peer (the other HWIDs' devices exist by now).
	rows := func() int {
		return e.count(`SELECT (SELECT count(*) FROM device) + (SELECT count(*) FROM device_credential) + (SELECT count(*) FROM awg_peer)`)
	}
	before := rows()
	for _, f := range formats {
		fetch(e.h, f.path, f.ua, "X-HWID", hwidA)
	}
	if after := rows(); after != before {
		t.Errorf("rows %d -> %d: the refused requests wrote something", before, after)
	}
	// The refusal of the URI list, in the form the other refusals have, and the announcement of the apps that show one.
	rec := fetch(e.h, "/"+tok, happUA, "X-HWID", hwidA)
	if lines := strings.Split(decode(t, rec.Body.String()), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], "hysteria2://off@0.0.0.0:1/#") {
		t.Errorf("refusal entry = %q", lines)
	}
	if got := unb64(t, rec.Header().Get("Announce")); got != "This device gets its servers in its own app with its key, not through the link" {
		t.Errorf("Announce = %q", got)
	}
	if rec.Header().Get("X-Hwid-Max-Devices-Reached") != "" || rec.Header().Get("Profile-Update-Interval") != "1" {
		t.Errorf("headers = %v", rec.Header())
	}
	if rec.Header().Get("Mistgate-Client-Api") != "" {
		t.Error("the discovery header went to a request without caps")
	}
	if got := e.liveCredIDs(a.DeviceID); got == "" || got != enrolledCreds {
		t.Errorf("the credentials of the enrolled device changed by basic fetches: %q -> %q", enrolledCreds, got)
	}
	// A basic fetch does not take the enrolled device's key slot either: the key still works.
	if dev, _, err := e.st.Access().LinkDeviceByKeyHash(e.ctx, keyHashOf(t, a.Key)); err != nil || dev.ID != a.DeviceID {
		t.Errorf("key after the refused fetches: %+v, %v", dev, err)
	}
	_ = uid
}

func TestEnrolLogsNothingSecret(t *testing.T) {
	e := newEnrolEnv(t, nil)
	e.switchAPI(true)
	_, tok := e.user("alice", nil)
	a := e.enrolOK(tok, hwidA)
	b := e.enrolOK(tok, hwidA, "Authorization", bearer(a.Key))
	e.post(tok, enrolBody(hwidA), "Authorization", bearer("garbage"))
	e.post(unknown, enrolBody(hwidB), "Authorization", bearer(b.Key))
	logs := e.logs.String()
	for name, secret := range map[string]string{
		"token": tok, "first key": a.Key, "second key": b.Key, "hwid": hwidA, "canonical hwid": strings.ReplaceAll(hwidA, "-", ""),
		"address": enrolIP, "unknown token": unknown, "Authorization": "Bearer",
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("the log holds the %s", name)
		}
	}
}
