package subs_test

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/clientdebug"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subs"
)

// POST <base>/debug and <base>/debug/decline, and the debug object of sync, against the real access module, the real intake
// (internal/panel/clientdebug) and the database. The clock of the handler is the test's.

type tick struct {
	mu sync.Mutex
	t  time.Time
}

func (c *tick) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *tick) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type debugEnv struct {
	*syncEnv
	clock *tick
}

const (
	debugSecret = "agent-sni-label-0123456789.example.net"
	debugMarker = "zz-marker-content-of-a-log-line"
)

// newDebugEnv is a panel of the VPS edition (intake true) or of the edge edition (nil ClientDebug).
func newDebugEnv(t *testing.T, intake bool) *debugEnv {
	t.Helper()
	return newDebugEnvWith(t, intake, nil)
}

// newDebugEnvWith is newDebugEnv with a change to the configuration. The hourly window of attempts is off and the slot is waited for
// a short while, unless mut says otherwise.
func newDebugEnvWith(t *testing.T, intake bool, mut func(*subs.Config)) *debugEnv {
	t.Helper()
	clk := &tick{t: time.Now().UTC().Truncate(time.Second)}
	e := newSyncEnvWith(t, func(c *subs.Config, st *store.Store) {
		c.Now = clk.Now
		c.ClientDebugAttemptsPerHour = -1
		c.ClientDebugSlotWait = 300 * time.Millisecond
		if intake {
			c.ClientDebug = clientdebug.New(clientdebug.Config{Store: st, Secrets: []string{debugSecret}})
		}
		if mut != nil {
			mut(c)
		}
	})
	return &debugEnv{syncEnv: e, clock: clk}
}

// ask is the owner's request for the device's logs (what RequestClientDebug does).
func (e *debugEnv) ask(device, level, note string) store.ClientDebugRow {
	e.t.Helper()
	row, err := e.st.CreateClientDebug(e.ctx, store.ClientDebugRow{ID: store.NewClientDebugID(), DeviceID: device, Actor: "adm_1", Note: note, Level: level}, e.clock.Now())
	if err != nil {
		e.t.Fatalf("CreateClientDebug: %v", err)
	}
	return row
}

func (e *debugEnv) logLine(msg string) string {
	return fmt.Sprintf(`{"ts":%d,"lvl":"info","src":"service","msg":%q,"kv":{"code":"x"}}`, e.clock.Now().Add(-time.Minute).UnixMilli(), msg)
}

func gzipped(t *testing.T, text string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(text)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// deliver is POST /client/debug with what a client sends; hdr are name, value pairs that replace the defaults (an empty value removes one).
func (e *debugEnv) deliver(key string, body io.Reader, hdr ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.post("/client/debug", key, body, hdr...)
}

func (e *debugEnv) post(path, key string, body io.Reader, hdr ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest("POST", path, body)
	req.Header.Set("User-Agent", syncUA)
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("Content-Encoding", "gzip")
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
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *debugEnv) answer(key, request string, lines ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	hdr := []string{}
	if request != "" {
		hdr = append(hdr, "X-Mistgate-Debug-Request", request)
	}
	return e.deliver(key, bytes.NewReader(gzipped(e.t, strings.Join(lines, "\n")+"\n")), hdr...)
}

func (e *debugEnv) decline(key, request string) *httptest.ResponseRecorder {
	e.t.Helper()
	hdr := []string{}
	if request != "" {
		hdr = append(hdr, "X-Mistgate-Debug-Request", request)
	}
	return e.post("/client/debug/decline", key, nil, hdr...)
}

type debugAnswer struct {
	ID        string `json:"id"`
	Lines     int    `json:"lines"`
	DroppedKV int    `json:"dropped_kv"`
	Declined  bool   `json:"declined"`
}

func decodeDebug(t *testing.T, rec *httptest.ResponseRecorder) debugAnswer {
	t.Helper()
	var a debugAnswer
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &a) != nil {
		t.Fatalf("answer = %d %q", rec.Code, rec.Body.String())
	}
	return a
}

type debugRequest struct {
	ID          string `json:"id"`
	Level       string `json:"level"`
	Note        string `json:"note"`
	ExpiresUnix int64  `json:"expires_unix"`
}

func (e *debugEnv) syncDebug(key string) (debug map[string]any, request *debugRequest, etag string) {
	e.t.Helper()
	b := e.mustSync(key)
	if raw, ok := b.Debug["request"]; ok {
		enc, _ := json.Marshal(raw)
		request = &debugRequest{}
		if err := json.Unmarshal(enc, request); err != nil {
			e.t.Fatal(err)
		}
	}
	return b.Debug, request, b.etg
}

func (e *debugEnv) state(id string) string {
	e.t.Helper()
	row, err := e.st.ClientDebug(e.ctx, id, e.clock.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	return row.State
}

func (e *debugEnv) lines(id string) int {
	e.t.Helper()
	return e.count(`SELECT COUNT(*) FROM debug_capture_line WHERE capture_id = ?`, id)
}

// ---- sync ----

func TestSyncDebugObjectFollowsTheEdition(t *testing.T) {
	vps := newDebugEnv(t, true)
	_, tok := vps.person("alice-liddell", nil)
	a := vps.enrolDevice(tok, hwidA)
	b := vps.mustSync(a.Key)
	if !strings.Contains(b.raw, `"debug":{"intake":true,"unasked":true},`) {
		t.Errorf("VPS debug object: %s", b.raw)
	}
	edge := newDebugEnv(t, false)
	_, tok = edge.person("alice-liddell", nil)
	a = edge.enrolDevice(tok, hwidA)
	if b := edge.mustSync(a.Key); !strings.Contains(b.raw, `"debug":{"intake":false},`) {
		t.Errorf("edge debug object: %s", b.raw)
	}
}

func TestSyncCarriesTheOpenRequestAndItsETag(t *testing.T) {
	e := newDebugEnv(t, true)
	_, tok := e.person("alice-liddell", nil)
	a, other := e.enrolDevice(tok, hwidA), e.enrolDevice(tok, hwidB2)
	_, _, base := e.syncDebug(a.Key)
	if _, _, again := e.syncDebug(a.Key); again != base {
		t.Fatal("the ETag moved between two syncs")
	}
	_, _, otherBase := e.syncDebug(other.Key)

	note := "Connection drops in the evenings, please send the logs"
	req := e.ask(a.DeviceID, "debug", note)
	debug, got, withRequest := e.syncDebug(a.Key)
	if got == nil || got.ID != req.ID || got.Level != "debug" || got.Note != note || got.ExpiresUnix != e.clock.Now().Add(72*time.Hour).Unix() {
		t.Fatalf("request in sync: %+v", got)
	}
	if debug["intake"] != true || debug["unasked"] != true {
		t.Errorf("debug object: %v", debug)
	}
	if withRequest == base {
		t.Error("the ETag did not change when the request was made")
	}
	if _, _, again := e.syncDebug(a.Key); again != withRequest {
		t.Error("the ETag moved between two syncs with a request open")
	}
	if rec := e.sync(a.Key, "If-None-Match", withRequest); rec.Code != 304 {
		t.Errorf("sync with the current ETag = %d", rec.Code)
	}
	if rec := e.sync(a.Key, "If-None-Match", base); rec.Code != 200 {
		t.Errorf("sync with the old ETag = %d", rec.Code)
	}
	if _, r, tagOther := e.syncDebug(other.Key); r != nil || tagOther != otherBase {
		t.Error("another device saw the request")
	}
	if !strings.Contains(e.mustSync(a.Key).raw, `"request":{"id":"`+req.ID+`","level":"debug","note":"`+note+`","expires_unix":`) {
		t.Error("the request has another shape or key order")
	}

	if _, err := e.st.CancelClientDebug(e.ctx, req.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	// it goes: withdrawn, answered, refused, run out; every time the ETag is the one of no request
	for name, close := range map[string]func(r store.ClientDebugRow){
		"withdrawn": func(r store.ClientDebugRow) {
			if _, err := e.st.CancelClientDebug(e.ctx, r.ID, e.clock.Now()); err != nil {
				t.Fatal(err)
			}
		},
		"answered": func(r store.ClientDebugRow) {
			if rec := e.answer(a.Key, r.ID, e.logLine("hello")); rec.Code != 200 {
				t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
			}
		},
		"refused": func(r store.ClientDebugRow) {
			if rec := e.decline(a.Key, r.ID); rec.Code != 200 {
				t.Fatalf("decline = %d %s", rec.Code, rec.Body.String())
			}
		},
		"run out": func(store.ClientDebugRow) { e.clock.Advance(72 * time.Hour) },
	} {
		e.clock.Advance(time.Minute)
		r := e.ask(a.DeviceID, "info", "")
		if _, got, tag := e.syncDebug(a.Key); got == nil || got.ID != r.ID || got.Level != "info" || tag == base {
			t.Fatalf("%s: the new request is not in sync", name)
		}
		close(r)
		if _, got, tag := e.syncDebug(a.Key); got != nil || tag != base {
			t.Errorf("%s: sync after = %+v, ETag same as no request: %v", name, got, tag == base)
		}
	}
	// the last second of a request
	e.clock.Advance(time.Minute)
	r := e.ask(a.DeviceID, "info", "")
	e.clock.Advance(72*time.Hour - time.Second)
	if _, got, _ := e.syncDebug(a.Key); got == nil {
		t.Error("the request ran out a second early")
	}
	e.clock.Advance(time.Second)
	if _, got, _ := e.syncDebug(a.Key); got != nil || e.state(r.ID) != "expired" {
		t.Error("the request did not run out at its time")
	}
}

// ---- the delivery ----

func TestDeliveryAnswersARequestAndIsIdempotent(t *testing.T) {
	e := newDebugEnv(t, true)
	uid, tok := e.person("alice-liddell", nil)
	a := e.enrolDevice(tok, hwidA)
	req := e.ask(a.DeviceID, "debug", "please")
	win := `{"ts":` + strconv.FormatInt(e.clock.Now().Add(-time.Minute).UnixMilli(), 10) + `,"lvl":"error","src":"service","msg":"cannot write C:\\Users\\SomeUser\\AppData\\Roaming\\Mistgate\\state.json ` + debugMarker + ` via 203.0.113.9:443","kv":{"code":"state_write_failed","ssid":"HomeNet"}}`
	rec := e.answer(a.Key, req.ID, e.logLine("connecting "+debugMarker), win, e.logLine("secret "+strings.ToUpper(debugSecret)))
	got := decodeDebug(t, rec)
	if got.ID != req.ID || got.Lines != 3 || got.DroppedKV != 1 {
		t.Fatalf("answer = %+v", got)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
	if e.state(req.ID) != "received" || e.lines(req.ID) != 3 {
		t.Fatalf("state %s, lines %d", e.state(req.ID), e.lines(req.ID))
	}
	var msg string
	if err := e.st.R.QueryRow(`SELECT message FROM debug_capture_line WHERE capture_id = ? AND seq = 2`, req.ID).Scan(&msg); err != nil {
		t.Fatal(err)
	}
	if want := `cannot write C:\Users\<user>\AppData\Roaming\Mistgate\state.json ` + debugMarker + ` via <ip>:port`; msg != want {
		t.Errorf("stored message %q, want %q", msg, want)
	}
	var secret string
	if err := e.st.R.QueryRow(`SELECT message FROM debug_capture_line WHERE capture_id = ? AND seq = 3`, req.ID).Scan(&secret); err != nil || secret != "secret <secret>" {
		t.Errorf("secret name: %q %v", secret, err)
	}
	row, err := e.st.ClientDebug(e.ctx, req.ID, e.clock.Now())
	if err != nil || row.Records != 3 || row.App != "Mistgate/0.3.1" || row.UserID != uid || row.DeviceID != a.DeviceID || row.DroppedKV != 1 || row.Unasked {
		t.Errorf("row: %+v %v", row, err)
	}
	// a delivery that came twice (the answer was lost) is answered the same way and stored once
	again := decodeDebug(t, e.answer(a.Key, req.ID, e.logLine("a different line")))
	if again != got {
		t.Errorf("repeat = %+v, want %+v", again, got)
	}
	if e.lines(req.ID) != 3 {
		t.Error("a repeat wrote lines")
	}
	if _, r, _ := e.syncDebug(a.Key); r != nil {
		t.Error("an answered request is still in sync")
	}
	// the node-capture read finds none of it
	if _, err := e.st.DebugCapture(e.ctx, req.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("DebugCapture of a bundle = %v", err)
	}
	// nothing of the content in the logs of the panel, and the audit and the event say that logs came, not what
	if strings.Contains(e.logs.String(), debugMarker) || strings.Contains(e.logs.String(), "SomeUser") {
		t.Error("the panel's log holds a word of the logs")
	}
	var params, actor string
	if err := e.st.R.QueryRow(`SELECT actor, params FROM audit WHERE action = 'client_debug_received'`).Scan(&actor, &params); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if actor != "user:"+uid || !strings.Contains(params, req.ID) || !strings.Contains(params, a.DeviceID) || strings.Contains(params, debugMarker) {
		t.Errorf("audit: %s %s", actor, params)
	}
	if e.count(`SELECT COUNT(*) FROM audit WHERE action = 'client_debug_received'`) != 1 {
		t.Error("a repeat was audited")
	}
	var sev int
	var evParams, evUser, evDevice string
	if err := e.st.R.QueryRow(`SELECT severity, params_json, user_id, device_id FROM event WHERE code = 'client_logs_received'`).Scan(&sev, &evParams, &evUser, &evDevice); err != nil {
		t.Fatalf("event row: %v", err)
	}
	if sev != 1 || evUser != uid || evDevice != a.DeviceID || strings.Contains(evParams, debugMarker) || !strings.Contains(evParams, `"lines":"3"`) {
		t.Errorf("event: %d %s %s %s", sev, evParams, evUser, evDevice)
	}
}

func TestDeliveryAnswersBeforeAnyWork(t *testing.T) {
	e := newDebugEnv(t, true)
	_, tok := e.person("alice-liddell", nil)
	a := e.enrolDevice(tok, hwidA)
	// strangers get the decoy and a miss: no key, an unknown key, and a method that is not POST
	for _, c := range []struct {
		name, method, path, key string
	}{
		{"no key", "POST", "/client/debug", ""},
		{"unknown key", "POST", "/client/debug", newKey()},
		{"unknown key, decline", "POST", "/client/debug/decline", newKey()},
		{"GET debug", "GET", "/client/debug", a.Key},
		{"PUT debug", "PUT", "/client/debug", a.Key},
		{"GET decline", "GET", "/client/debug/decline", a.Key},
	} {
		before := e.limiter.count("subscription-miss")
		rec := e.request(c.method, c.path, c.key)
		assertDecoy(t, c.name, rec)
		if got := e.limiter.count("subscription-miss") - before; got != 1 {
			t.Errorf("%s: counted %d times as a miss, want 1", c.name, got)
		}
	}
	// a removed, a blocked and a moved device
	removed, blocked, moved := e.enrolDevice(tok, "ffffffff-ffff-4fff-8fff-ffffffffffff"), e.enrolDevice(tok, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"), e.enrolDevice(tok, "dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	if _, err := e.st.Access().RevokeDevice(e.ctx, removed.DeviceID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Access().BlockDevice(e.ctx, blocked.DeviceID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.Exec(`UPDATE device SET enrol_token_hash = x'0000000000000000000000000000000000000000000000000000000000000000' WHERE id = ?`, moved.DeviceID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/client/debug", "/client/debug/decline"} {
		assertJSONError(t, path+" removed", e.post(path, removed.Key, nil), 401, "device_removed")
		assertJSONError(t, path+" blocked", e.post(path, blocked.Key, nil), 401, "device_blocked")
		assertJSONError(t, path+" link moved", e.post(path, moved.Key, nil), 401, "link_changed")
		for _, hdr := range [][]string{{"Sec-Fetch-Site", "cross-site"}, {"Origin", "https://evil.example"}} {
			assertJSONError(t, path+" with "+hdr[0], e.post(path, a.Key, nil, hdr...), 403, "cross_origin")
		}
	}
}

func TestDeliveryBudgetIsTheDevicesAndSharedWithSync(t *testing.T) {
	e := newDebugEnv(t, true)
	_, tok := e.person("alice-liddell", nil)
	a := e.enrolDevice(tok, hwidA)
	for i := 1; i <= 29; i++ {
		if i%2 == 0 {
			e.sync(a.Key)
		} else {
			assertJSONError(t, "decline "+strconv.Itoa(i), e.decline(a.Key, "dbr_none"), 409, "no_request")
		}
	}
	e.sync(a.Key) // the 30th
	rec := e.decline(a.Key, "dbr_none")
	assertJSONError(t, "31st request", rec, 429, "too_many_requests")
	if secs, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || secs < 1 || secs > 3600 {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	assertJSONError(t, "delivery over the budget", e.answer(a.Key, ""), 429, "too_many_requests")
}

func TestDeliveryWithoutAnOpenRequestIsNoRequestAndTheBodyIsNotRead(t *testing.T) {
	e := newDebugEnv(t, true)
	_, tok := e.person("alice-liddell", nil)
	a, b := e.enrolDevice(tok, hwidA), e.enrolDevice(tok, hwidB2)
	failing := func() io.Reader {
		return &readFails{t: t}
	}
	mine := e.ask(a.DeviceID, "info", "")
	foreign := e.ask(b.DeviceID, "info", "")
	if _, err := e.st.CancelClientDebug(e.ctx, mine.ID, e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	declined := e.ask(a.DeviceID, "info", "")
	if rec := e.decline(a.Key, declined.ID); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	expiring := e.ask(a.DeviceID, "info", "")
	e.clock.Advance(72 * time.Hour)
	for name, id := range map[string]string{"unknown id": "dbr_unknownunknownunknown", "another device's request": foreign.ID, "cancelled": mine.ID,
		"declined": declined.ID, "run out": expiring.ID, "an id that is far too long": strings.Repeat("x", 500)} {
		assertJSONError(t, name, e.deliver(a.Key, failing(), "X-Mistgate-Debug-Request", id), 409, "no_request")
		assertJSONError(t, name+" (decline)", e.decline(a.Key, id), 409, "no_request")
	}
	assertJSONError(t, "decline with no header", e.decline(a.Key, ""), 409, "no_request")
	if e.lines(foreign.ID) != 0 || e.count(`SELECT COUNT(*) FROM client_debug WHERE id = ? AND state = 'open'`, foreign.ID) != 1 {
		t.Error("a delivery touched another device's request")
	}
	// a repeat of an answered request is answered without reading the body too
	open := e.ask(a.DeviceID, "info", "")
	first := decodeDebug(t, e.answer(a.Key, open.ID, e.logLine("one"), e.logLine("two")))
	if rec := e.deliver(a.Key, failing(), "X-Mistgate-Debug-Request", open.ID); decodeDebug(t, rec) != first {
		t.Errorf("repeat without a body read = %d %s", rec.Code, rec.Body.String())
	}
}

// readFails is a body that fails the test when it is read.
type readFails struct{ t *testing.T }

func (r *readFails) Read([]byte) (int, error) {
	r.t.Error("the body was read")
	return 0, errors.New("read")
}

func TestDeliveryMediaTypeSizeAndSchema(t *testing.T) {
	e := newDebugEnv(t, true)
	_, tok := e.person("alice-liddell", nil)
	a := e.enrolDevice(tok, hwidA)
	req := e.ask(a.DeviceID, "info", "")
	hdr := []string{"X-Mistgate-Debug-Request", req.ID}
	body := func(text string) io.Reader { return bytes.NewReader(gzipped(t, text)) }
	ok := e.logLine("fine")

	assertJSONError(t, "json content type", e.deliver(a.Key, body(ok+"\n"), append(hdr, "Content-Type", "application/json")...), 415, "unsupported_media_type")
	assertJSONError(t, "no content type", e.deliver(a.Key, body(ok+"\n"), append(hdr, "Content-Type", "")...), 415, "unsupported_media_type")
	assertJSONError(t, "no gzip", e.deliver(a.Key, strings.NewReader(ok+"\n"), append(hdr, "Content-Encoding", "")...), 415, "unsupported_media_type")
	assertJSONError(t, "deflate", e.deliver(a.Key, body(ok+"\n"), append(hdr, "Content-Encoding", "deflate")...), 415, "unsupported_media_type")
	if rec := e.deliver(a.Key, body(ok+"\n"), append(hdr, "Content-Type", "application/x-ndjson; charset=utf-8", "Content-Encoding", "GZIP")...); rec.Code != 200 {
		t.Fatalf("media type with a charset, encoding in capitals = %d %s", rec.Code, rec.Body.String())
	}
	req = e.reask(a, req)
	hdr = []string{"X-Mistgate-Debug-Request", req.ID}

	// 413: announced, past the wire's limit, past 4 MiB unpacked, past 20000 lines; nothing is stored
	big := httptest.NewRequest("POST", "/client/debug", body(ok+"\n"))
	big.ContentLength = subs.ClientDebugMaxWire + 1
	rec := httptest.NewRecorder()
	big.Header.Set("Authorization", bearer(a.Key))
	big.Header.Set("Content-Type", "application/x-ndjson")
	big.Header.Set("Content-Encoding", "gzip")
	big.Header.Set("X-Mistgate-Debug-Request", req.ID)
	e.h.ServeHTTP(rec, big)
	assertJSONError(t, "announced 1 MiB + 1", rec, 413, "too_large")

	noise := make([]byte, 1<<20)
	if _, err := rand.Read(noise); err != nil {
		t.Fatal(err)
	}
	assertJSONError(t, "wire past 1 MiB", e.deliver(a.Key, bytes.NewReader(append(gzipped(t, ok+"\n")[:10], noise...)), hdr...), 413, "too_large")

	lineOf := func(n int) string { return e.logLine(strings.Repeat("y", n)) }
	var past strings.Builder
	for past.Len() <= clientdebug.MaxText {
		past.WriteString(lineOf(4000) + "\n")
	}
	assertJSONError(t, "past 4 MiB unpacked", e.deliver(a.Key, body(past.String()), hdr...), 413, "too_large")
	assertJSONError(t, "past 20000 lines", e.deliver(a.Key, body(strings.Repeat(ok+"\n", clientdebug.MaxLines+1)), hdr...), 413, "too_large")
	if e.lines(req.ID) != 0 || e.state(req.ID) != "open" {
		t.Fatal("a refused delivery stored lines or closed the request")
	}

	// 400: the first bad line and the part of it, never the value
	bad := func(what string, text string, message string) {
		t.Helper()
		rec := e.deliver(a.Key, body(text), hdr...)
		assertJSONError(t, what, rec, 400, "bad_request")
		var m struct{ Message string }
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		if m.Message != message {
			t.Errorf("%s: message %q, want %q", what, m.Message, message)
		}
	}
	lines := make([]string, 17)
	for i := range lines {
		lines[i] = ok
	}
	lines[16] = strings.Replace(ok, `"lvl":"info"`, `"lvl":"trace-alice-liddell"`, 1)
	bad("bad level on line 17", strings.Join(lines, "\n")+"\n", "line 17: lvl")
	bad("unknown field", ok+"\n"+`{"ts":1,"user":"alice-liddell"}`+"\n", "line 2: unknown_field")
	bad("not json", "connecting to alice-liddell\n", "line 1: json")
	bad("empty body", "", "line 1: empty")
	assertJSONError(t, "garbage that claims to be gzip", e.deliver(a.Key, strings.NewReader("this is not gzip data at all"), hdr...), 400, "bad_request")
	assertJSONError(t, "empty body that claims to be gzip", e.deliver(a.Key, strings.NewReader(""), hdr...), 400, "bad_request")
	var cut bytes.Buffer
	cutGz := gzip.NewWriter(&cut)
	_, _ = cutGz.Write([]byte(strings.Repeat(ok+"\n", 200)))
	_ = cutGz.Close()
	assertJSONError(t, "gzip cut in the middle", e.deliver(a.Key, bytes.NewReader(cut.Bytes()[:cut.Len()/2]), hdr...), 400, "bad_request")
	if e.lines(req.ID) != 0 || e.state(req.ID) != "open" {
		t.Fatal("a refused delivery stored lines or closed the request")
	}
	// and a good one still goes
	if rec := e.deliver(a.Key, body(ok+"\n"), hdr...); rec.Code != 200 {
		t.Fatalf("delivery after the refusals = %d %s", rec.Code, rec.Body.String())
	}
}

// reask answers the device's request and makes it a new one, for a test that needs several deliveries.
func (e *debugEnv) reask(a enrolled, old store.ClientDebugRow) store.ClientDebugRow {
	e.t.Helper()
	if e.state(old.ID) == "open" {
		if _, err := e.st.CancelClientDebug(e.ctx, old.ID, e.clock.Now()); err != nil {
			e.t.Fatal(err)
		}
	}
	return e.ask(a.DeviceID, "info", "")
}

func TestDeclineClosesTheRequestAndTheOwnerSeesIt(t *testing.T) {
	e := newDebugEnv(t, true)
	uid, tok := e.person("alice-liddell", nil)
	a := e.enrolDevice(tok, hwidA)
	req := e.ask(a.DeviceID, "info", "")
	got := decodeDebug(t, e.decline(a.Key, req.ID))
	if got.ID != req.ID || !got.Declined {
		t.Fatalf("decline = %+v", got)
	}
	if rec := e.decline(a.Key, req.ID); rec.Code != 409 {
		t.Errorf("second decline = %d", rec.Code)
	}
	assertJSONError(t, "delivery after the refusal", e.answer(a.Key, req.ID, e.logLine("x")), 409, "no_request")
	if e.state(req.ID) != "declined" {
		t.Errorf("state = %s", e.state(req.ID))
	}
	if _, r, _ := e.syncDebug(a.Key); r != nil {
		t.Error("a refused request is still in sync")
	}
	var actor, params string
	if err := e.st.R.QueryRow(`SELECT actor, params FROM audit WHERE action = 'client_debug_declined'`).Scan(&actor, &params); err != nil || actor != "user:"+uid || !strings.Contains(params, req.ID) {
		t.Errorf("audit: %q %q %v", actor, params, err)
	}
	// the person can send on their own all the same
	if rec := e.answer(a.Key, "", e.logLine("own")); rec.Code != 200 {
		t.Errorf("unasked delivery after a refusal = %d %s", rec.Code, rec.Body.String())
	}
}

// ---- sent on the person's own ----

func TestDeliveryWithoutARequestIsTheirOwnAndOncePerTenMinutes(t *testing.T) {
	e := newDebugEnv(t, true)
	uid, tok := e.person("alice-liddell", nil)
	a, b := e.enrolDevice(tok, hwidA), e.enrolDevice(tok, hwidB2)
	got := decodeDebug(t, e.answer(a.Key, "", e.logLine("one"), e.logLine("two")))
	if !strings.HasPrefix(got.ID, "dbr_") || got.Lines != 2 {
		t.Fatalf("answer = %+v", got)
	}
	row, err := e.st.ClientDebug(e.ctx, got.ID, e.clock.Now())
	if err != nil || !row.Unasked || row.State != "received" || row.Actor != "" || row.UserID != uid || row.DeviceID != a.DeviceID || row.Records != 2 {
		t.Fatalf("row: %+v %v", row, err)
	}
	if e.lines(got.ID) != 2 {
		t.Errorf("lines = %d", e.lines(got.ID))
	}
	if _, r, _ := e.syncDebug(a.Key); r != nil {
		t.Error("a bundle sent unasked made a request")
	}
	var params string
	if err := e.st.R.QueryRow(`SELECT params_json FROM event WHERE code = 'client_logs_received'`).Scan(&params); err != nil || !strings.Contains(params, `"unasked":"true"`) {
		t.Errorf("event: %q %v", params, err)
	}

	e.clock.Advance(9*time.Minute + 59*time.Second)
	rec := e.answer(a.Key, "", e.logLine("three"))
	assertJSONError(t, "second within ten minutes", rec, 429, "too_many_requests")
	if rec.Header().Get("Retry-After") != "1" {
		t.Errorf("Retry-After = %q, want 1", rec.Header().Get("Retry-After"))
	}
	if rec := e.answer(b.Key, "", e.logLine("other device")); rec.Code != 200 {
		t.Errorf("another device = %d %s", rec.Code, rec.Body.String())
	}
	// the refusal came before the body: a failing reader proves it
	assertJSONError(t, "second, body unread", e.deliver(a.Key, &readFails{t: t}), 429, "too_many_requests")
	e.clock.Advance(time.Second)
	if rec := e.answer(a.Key, "", e.logLine("three")); rec.Code != 200 {
		t.Fatalf("after ten minutes = %d %s", rec.Code, rec.Body.String())
	}
	// a refused or failed delivery does not use the time up
	e.clock.Advance(11 * time.Minute)
	assertJSONError(t, "bad line", e.deliver(a.Key, bytes.NewReader(gzipped(t, "not json\n"))), 400, "bad_request")
	if rec := e.answer(a.Key, "", e.logLine("four")); rec.Code != 200 {
		t.Errorf("after a bad delivery = %d %s", rec.Code, rec.Body.String())
	}
	// and an open request stays open while the person sends on their own
	req := e.ask(a.DeviceID, "info", "")
	e.clock.Advance(11 * time.Minute)
	if rec := e.answer(a.Key, "", e.logLine("five")); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if e.state(req.ID) != "open" {
		t.Error("an unasked bundle closed the request")
	}
	if n := e.count(`SELECT COUNT(*) FROM client_debug WHERE device_id = ? AND state = 'received'`, a.DeviceID); n != 3 {
		t.Errorf("bundles kept for the device = %d, want 3", n)
	}
}

func TestPanelCeilingRefusesUnaskedOnlyWithDebugFull(t *testing.T) {
	e := newDebugEnv(t, true)
	_, tok := e.person("alice-liddell", nil)
	a, b := e.enrolDevice(tok, hwidA), e.enrolDevice(tok, hwidB2)
	if _, err := e.st.W.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
		INSERT INTO client_debug (id, user_id, device_id, requested_at, expires_at, state, closed_at) SELECT 'dbr_fill' || i, 'usr_x', 'dev_fill' || i, 1, 1, 'received', 1 FROM n`, store.ClientDebugMaxBundles); err != nil {
		t.Fatal(err)
	}
	rec := e.answer(a.Key, "", e.logLine("x"))
	assertJSONError(t, "past 200 bundles", rec, 503, "debug_full")
	if e.count(`SELECT COUNT(*) FROM client_debug WHERE device_id = ?`, a.DeviceID) != 0 {
		t.Error("a refused bundle left a row")
	}
	assertJSONError(t, "body unread", e.deliver(a.Key, &readFails{t: t}), 503, "debug_full")
	// the owner's request is answered all the same, and counts
	req := e.ask(b.DeviceID, "info", "")
	if rec := e.answer(b.Key, req.ID, e.logLine("x")); rec.Code != 200 {
		t.Fatalf("a requested bundle past the ceiling = %d %s", rec.Code, rec.Body.String())
	}
	if n := e.count(`SELECT COUNT(*) FROM client_debug WHERE state = 'received'`); n != store.ClientDebugMaxBundles+1 {
		t.Errorf("bundles = %d", n)
	}
}

// ---- the edge edition ----

func TestEdgeDebugRoutesAreOffAfterTheKeyIsChecked(t *testing.T) {
	e := newDebugEnv(t, false)
	_, tok := e.person("alice-liddell", nil)
	a := e.enrolDevice(tok, hwidA)
	assertJSONError(t, "delivery", e.deliver(a.Key, &readFails{t: t}), 403, "debug_off")
	assertJSONError(t, "decline", e.decline(a.Key, "dbr_x"), 403, "debug_off")
	// a stranger gets the decoy, not debug_off
	for _, path := range []string{"/client/debug", "/client/debug/decline"} {
		assertDecoy(t, "stranger "+path, e.post(path, newKey(), nil))
		assertDecoy(t, "no key "+path, e.post(path, "", nil))
	}
	// the key is checked first: a removed device is told so
	if _, err := e.st.Access().RevokeDevice(e.ctx, a.DeviceID, time.Now()); err != nil {
		t.Fatal(err)
	}
	assertJSONError(t, "removed device", e.deliver(a.Key, nil), 401, "device_removed")
}

func TestDebugRouteDoesNotLeakIntoOtherRoutes(t *testing.T) {
	e := newDebugEnv(t, true)
	_, tok := e.person("alice-liddell", nil)
	a := e.enrolDevice(tok, hwidA)
	// a longer or shorter path is no debug route
	for _, path := range []string{"/client/debug/", "/client/debug/decline/x", "/client/debugs", "/client/debug/other"} {
		assertDecoy(t, path, e.post(path, a.Key, nil))
	}
	if rec := e.sync(a.Key); rec.Code != http.StatusOK {
		t.Fatalf("sync = %d", rec.Code)
	}
}
