package subs_test

import (
	"bytes"
	"context"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/subs"
)

// What a delivery of logs may cost the panel: unpacking and cleaning 4 MiB is seconds of CPU, so a device brings one at a time, the
// process takes one at a time, and a person brings a few an hour to the reading of the body.

// gateBody is a body that is not given out until released, and counts what was read of it.
type gateBody struct {
	r       io.Reader
	started chan struct{}
	release chan struct{}
	once    sync.Once
	n       atomic.Int64
}

func newGateBody(body []byte) *gateBody {
	return &gateBody{r: bytes.NewReader(body), started: make(chan struct{}), release: make(chan struct{})}
}

func (g *gateBody) Read(p []byte) (int, error) {
	g.once.Do(func() { close(g.started) })
	<-g.release
	n, err := g.r.Read(p)
	g.n.Add(int64(n))
	return n, err
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// Twelve deliveries at once from one device: one body is read in full, the other eleven are refused before theirs is touched.
func TestParallelDeliveriesOfOneDeviceReadOneBody(t *testing.T) {
	e := newDebugEnv(t, true)
	_, tok := e.person("alice-liddell", nil)
	a := e.enrolDevice(tok, hwidA)
	first := newGateBody(gzipped(t, e.logLine("one")+"\n"))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- e.deliver(a.Key, first) }()
	waitFor(t, first.started, "the first body to be read")

	var wg sync.WaitGroup
	var mu sync.Mutex
	var refused int
	for range 11 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := e.deliver(a.Key, &readFails{t: t})
			mu.Lock()
			defer mu.Unlock()
			if rec.Code == 429 && errorCode(t, rec) == "too_many_requests" && rec.Header().Get("Retry-After") == "30" {
				refused++
			} else {
				t.Errorf("parallel delivery = %d %q Retry-After %q", rec.Code, rec.Body.String(), rec.Header().Get("Retry-After"))
			}
		}()
	}
	wg.Wait()
	if refused != 11 {
		t.Fatalf("%d refused", refused)
	}
	close(first.release)
	rec := <-done
	if got := decodeDebug(t, rec); got.Lines != 1 {
		t.Errorf("the one delivery = %+v", got)
	}
	// the device is free again
	e.clock.Advance(11 * time.Minute)
	if rec := e.answer(a.Key, "", e.logLine("two")); rec.Code != 200 {
		t.Errorf("delivery after the others = %d %s", rec.Code, rec.Body.String())
	}
}

// A slow sender holds its own device and one reader place, nothing else: another device gets its answer while the first body is
// still on the wire.
func TestASlowBodyOfOneDeviceDoesNotHoldUpAnother(t *testing.T) {
	e := newDebugEnv(t, true)
	_, tok := e.person("alice-liddell", nil)
	_, tok2 := e.person("bob-the-builder", nil)
	a, b := e.enrolDevice(tok, hwidA), e.enrolDevice(tok2, hwidB2)
	slow := newGateBody(gzipped(t, e.logLine("one")+"\n"))
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- e.deliver(a.Key, slow) }()
	waitFor(t, slow.started, "the slow body to be read")

	start := time.Now()
	if rec := e.answer(b.Key, "", e.logLine("two")); rec.Code != 200 {
		t.Fatalf("another device while a body is slow = %d %s", rec.Code, rec.Body.String())
	}
	if time.Since(start) > 250*time.Millisecond { // the slot wait of these tests is 300 ms: a wait for the slot would show
		t.Errorf("the other device was held up %v", time.Since(start))
	}
	close(slow.release)
	if got := decodeDebug(t, <-done); got.Lines != 1 {
		t.Errorf("slow delivery = %+v", got)
	}
}

// holdReceive keeps the processing of a delivery (unpacking, cleaning, storing: what the slot of the process is for) until released.
type holdReceive struct {
	subs.ClientDebug
	started, release chan struct{}
	once             sync.Once
}

func (h *holdReceive) Receive(ctx context.Context, in subs.ClientDebugIn) (subs.ClientDebugResult, error) {
	h.once.Do(func() { close(h.started) })
	<-h.release
	return h.ClientDebug.Receive(ctx, in)
}

// The one slot of the process is for the processing only: a second delivery whose body is in waits for it a few seconds, then is told to
// come back (its body was read, and is dropped).
func TestOneDeliveryIsProcessedAtATimeInTheProcess(t *testing.T) {
	hold := &holdReceive{started: make(chan struct{}), release: make(chan struct{})}
	e := newDebugEnvWith(t, true, func(c *subs.Config) {
		hold.ClientDebug = c.ClientDebug
		c.ClientDebug = hold
	})
	_, tok := e.person("alice-liddell", nil)
	_, tok2 := e.person("bob-the-builder", nil)
	a, b := e.enrolDevice(tok, hwidA), e.enrolDevice(tok2, hwidB2)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- e.answer(a.Key, "", e.logLine("one")) }()
	waitFor(t, hold.started, "the first delivery to be processed")

	start := time.Now()
	rec := e.answer(b.Key, "", e.logLine("two"))
	assertJSONError(t, "second delivery while the slot is taken", rec, 503, "retry")
	if rec.Header().Get("Retry-After") != "30" {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	if waited := time.Since(start); waited < 250*time.Millisecond || waited > 5*time.Second {
		t.Errorf("the second delivery waited %v for the slot", waited)
	}
	close(hold.release)
	if got := decodeDebug(t, <-done); got.Lines != 1 {
		t.Errorf("first delivery = %+v", got)
	}
	if rec := e.answer(b.Key, "", e.logLine("two")); rec.Code != 200 {
		t.Errorf("delivery after the first = %d %s", rec.Code, rec.Body.String())
	}
}

// Only a few bodies are read at once (each is held in memory): one more is told to come back, its body untouched, and is not counted
// against its person's attempts.
func TestOnlyAFewBodiesAreReadAtOnce(t *testing.T) {
	e := newDebugEnvWith(t, true, func(c *subs.Config) {
		c.ClientDebugReaders = 2
		c.ClientDebugAttemptsPerHour = 0
	})
	var keys []string
	for i, hwid := range []string{hwidA, hwidB2, "ffffffff-ffff-4fff-8fff-ffffffffffff"} {
		_, tok := e.person([]string{"alice-liddell", "bob-the-builder", "carol-of-the-bells"}[i], nil)
		keys = append(keys, e.enrolDevice(tok, hwid).Key)
	}
	bodies := []*gateBody{newGateBody(gzipped(t, e.logLine("one")+"\n")), newGateBody(gzipped(t, e.logLine("two")+"\n"))}
	done := make(chan *httptest.ResponseRecorder, 2)
	for i, b := range bodies {
		go func() { done <- e.deliver(keys[i], b) }()
		waitFor(t, b.started, "a body to be read")
	}
	for range 7 { // seven refusals do not use up the six attempts of the person
		rec := e.deliver(keys[2], &readFails{t: t})
		assertJSONError(t, "third body", rec, 503, "retry")
		if rec.Header().Get("Retry-After") != "30" {
			t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
		}
	}
	for _, b := range bodies {
		close(b.release)
	}
	for range 2 {
		if rec := <-done; rec.Code != 200 {
			t.Errorf("a delivery being read = %d %s", rec.Code, rec.Body.String())
		}
	}
	// the places are free again
	if rec := e.answer(keys[2], "", e.logLine("three")); rec.Code != 200 {
		t.Errorf("third delivery after the others = %d %s", rec.Code, rec.Body.String())
	}
}

// A person brings six deliveries an hour to the reading of the body, a broken one too; the seventh is 429 with Retry-After, from any
// of their devices, and another person is not touched. Refusals before the body (no request, the ten minutes) do not count.
func TestAPersonsAttemptsAreSixAnHour(t *testing.T) {
	e := newDebugEnvWith(t, true, func(c *subs.Config) { c.ClientDebugAttemptsPerHour = 0 })
	_, tok := e.person("alice-liddell", nil)
	_, tok2 := e.person("bob-the-builder", nil)
	a, a2, b := e.enrolDevice(tok, hwidA), e.enrolDevice(tok, hwidB2), e.enrolDevice(tok2, "ffffffff-ffff-4fff-8fff-ffffffffffff")
	broken := func(key string) *httptest.ResponseRecorder {
		return e.deliver(key, bytes.NewReader(gzipped(t, "not json\n")))
	}
	// a request that is not open is refused before the body: it is no attempt
	for range 3 {
		assertJSONError(t, "no request", e.deliver(a.Key, &readFails{t: t}, "X-Mistgate-Debug-Request", "dbr_none"), 409, "no_request")
	}
	for i := 1; i <= 6; i++ {
		assertJSONError(t, "broken delivery "+strconv.Itoa(i), broken(a.Key), 400, "bad_request")
	}
	rec := e.deliver(a.Key, &readFails{t: t})
	assertJSONError(t, "seventh delivery", rec, 429, "too_many_requests")
	if secs, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || secs < 1 || secs > 3600 {
		t.Errorf("Retry-After = %q", rec.Header().Get("Retry-After"))
	}
	assertJSONError(t, "seventh, from another device of the person", e.deliver(a2.Key, &readFails{t: t}), 429, "too_many_requests")
	assertJSONError(t, "another person", broken(b.Key), 400, "bad_request")
	// the failed deliveries did not use up the ten minutes of the device: a good one is only held by the hour's window
	if rec := e.answer(a.Key, "", e.logLine("good")); rec.Code != 429 {
		t.Errorf("good delivery in the same hour = %d", rec.Code)
	}
}

// A delivery that failed does not spend the ten minutes between two deliveries a device sends on its own: that is the store's, and it
// counts stored bundles only. The hour's window counts the attempts.
func TestABrokenDeliveryKeepsTheTenMinutes(t *testing.T) {
	e := newDebugEnvWith(t, true, func(c *subs.Config) { c.ClientDebugAttemptsPerHour = 0 })
	_, tok := e.person("alice-liddell", nil)
	a := e.enrolDevice(tok, hwidA)
	assertJSONError(t, "broken", e.deliver(a.Key, bytes.NewReader(gzipped(t, "not json\n"))), 400, "bad_request")
	if rec := e.answer(a.Key, "", e.logLine("good")); rec.Code != 200 {
		t.Errorf("good delivery right after a broken one = %d %s", rec.Code, rec.Body.String())
	}
}

// A person who is not active, or for whom the app is off, sends nothing on their own and sync does not offer it; the owner's request is
// answered whatever the person's state.
func TestUnaskedDeliveryNeedsAnActivePersonWithTheAppOn(t *testing.T) {
	e := newDebugEnv(t, true)
	uid, tok := e.person("alice-liddell", nil)
	a := e.enrolDevice(tok, hwidA)
	if debug, _, _ := e.syncDebug(a.Key); debug["unasked"] != true {
		t.Fatalf("an active person: %v", debug)
	}
	req := e.ask(a.DeviceID, "info", "")

	// the app switched off for the person
	if _, err := e.st.W.Exec(`UPDATE user SET app_mistgate_off = 1 WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	e.cache.Invalidate()
	debug, _, _ := e.syncDebug(a.Key)
	if debug["intake"] != true || debug["unasked"] != nil {
		t.Errorf("app off, sync debug = %v", debug)
	}
	rec := e.deliver(a.Key, &readFails{t: t})
	assertJSONError(t, "app off", rec, 409, "app_disabled")
	// a request is answered all the same
	if rec := e.answer(a.Key, req.ID, e.logLine("asked")); rec.Code != 200 {
		t.Fatalf("answer to a request with the app off = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := e.st.W.Exec(`UPDATE user SET app_mistgate_off = 0 WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	e.cache.Invalidate()
	if debug, _, _ := e.syncDebug(a.Key); debug["unasked"] != true {
		t.Errorf("app on again: %v", debug)
	}

	// A person who is not active: the status is the one sync computes (disabled, expired, limited at this instant), not the stored
	// column, which the sweep updates later: here the column still says "active" in every case.
	req = e.ask(a.DeviceID, "info", "")
	reset := `UPDATE user SET disabled = 0, expires_at = NULL, quota_bytes = 0, used_bytes = 0, status = 'active' WHERE id = ?`
	past := e.clock.Now().Add(-time.Hour).Unix()
	for _, c := range []struct{ status, set string }{
		{"disabled", `disabled = 1`},
		{"expired", `expires_at = ` + strconv.FormatInt(past, 10)},
		{"limited", `quota_bytes = 100, used_bytes = 100`},
	} {
		if _, err := e.st.W.Exec(reset, uid); err != nil {
			t.Fatal(err)
		}
		if _, err := e.st.W.Exec(`UPDATE user SET `+c.set+` WHERE id = ?`, uid); err != nil {
			t.Fatal(err)
		}
		rec := e.deliver(a.Key, &readFails{t: t})
		assertJSONError(t, c.status, rec, 409, "user_inactive")
		if !strings.Contains(rec.Body.String(), `"message":"`+c.status+`"`) {
			t.Errorf("%s: body %s", c.status, rec.Body.String())
		}
		if debug, _, _ := e.syncDebug(a.Key); debug["intake"] != true || debug["unasked"] != nil {
			t.Errorf("%s, sync debug = %v", c.status, debug)
		}
	}
	if rec := e.answer(a.Key, req.ID, e.logLine("asked")); rec.Code != 200 {
		t.Fatalf("answer to a request by a person who is not active = %d %s", rec.Code, rec.Body.String())
	}

	// the other way round: the column says expired, the term was extended: sync and the delivery both say active
	if _, err := e.st.W.Exec(reset, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.W.Exec(`UPDATE user SET status = 'expired' WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	if debug, _, _ := e.syncDebug(a.Key); debug["unasked"] != true {
		t.Errorf("a stale 'expired' column, sync debug = %v", debug)
	}
	if rec := e.answer(a.Key, "", e.logLine("own")); rec.Code != 200 {
		t.Errorf("unasked delivery with a stale 'expired' column = %d %s", rec.Code, rec.Body.String())
	}
}

// recorderWithDeadlines is a writer that can set the deadlines of its connection, as the server's can, and keeps them.
type recorderWithDeadlines struct {
	*httptest.ResponseRecorder
	read, write time.Time
}

func (r *recorderWithDeadlines) SetReadDeadline(t time.Time) error  { r.read = t; return nil }
func (r *recorderWithDeadlines) SetWriteDeadline(t time.Time) error { r.write = t; return nil }

// The listener sets its write timeout when the headers are read, and a body that takes the whole of the longer read window would be stored
// and never answered: the write deadline goes out with the read one, by 30 seconds.
func TestDeliveryExtendsTheWriteDeadlineWithTheReadOne(t *testing.T) {
	e := newDebugEnv(t, true)
	_, tok := e.person("alice-liddell", nil)
	a := e.enrolDevice(tok, hwidA)
	req := httptest.NewRequest("POST", "/client/debug", bytes.NewReader(gzipped(t, e.logLine("x")+"\n")))
	req.Header.Set("Authorization", bearer(a.Key))
	req.Header.Set("Content-Type", "application/x-ndjson")
	req.Header.Set("Content-Encoding", "gzip")
	rec := &recorderWithDeadlines{ResponseRecorder: httptest.NewRecorder()}
	before := time.Now()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("delivery = %d %s", rec.Code, rec.Body.String())
	}
	if rec.read.Before(before.Add(2*time.Minute-time.Second)) || rec.read.After(time.Now().Add(2*time.Minute+time.Second)) {
		t.Errorf("read deadline %v is not two minutes ahead", rec.read.Sub(before))
	}
	if gap := rec.write.Sub(rec.read); gap != 30*time.Second {
		t.Errorf("the write deadline is %v after the read one, want 30s", gap)
	}
}
