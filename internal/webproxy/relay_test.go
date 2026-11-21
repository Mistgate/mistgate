package webproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testWait = 8 * time.Second

type relayTransport struct{ handler http.Handler }

func (t relayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, req)
	return recorder.Result(), nil
}

type lostResponseTransport struct {
	base      http.RoundTripper
	path      string
	mu        sync.Mutex
	dropped   bool
	captured  []byte
	capturedH http.Header
}

func (t *lostResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || req.URL.Path != t.path {
		return resp, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.dropped {
		return resp, nil
	}
	t.dropped = true
	t.capturedH = resp.Header.Clone()
	t.captured, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	return nil, errors.New("simulated lost HTTP response")
}

func (t *lostResponseTransport) saved() ([]byte, http.Header) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.captured...), t.capturedH.Clone()
}

type manualRelayClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  map[*manualRelayTimer]struct{}
	created map[time.Duration]int
	changed chan struct{}
}

type manualRelayTimer struct {
	clock    *manualRelayClock
	deadline time.Time
	ch       chan time.Time
	stopped  bool
	fired    bool
}

func newManualRelayClock() *manualRelayClock {
	return &manualRelayClock{
		now:     time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
		timers:  make(map[*manualRelayTimer]struct{}),
		created: make(map[time.Duration]int),
		changed: make(chan struct{}, 1),
	}
}

func (c *manualRelayClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualRelayClock) NewTimer(d time.Duration) relayTimer {
	c.mu.Lock()
	c.created[d]++
	timer := &manualRelayTimer{clock: c, deadline: c.now.Add(d), ch: make(chan time.Time, 1)}
	if d <= 0 {
		timer.fired = true
		timer.ch <- c.now
	} else {
		c.timers[timer] = struct{}{}
	}
	c.mu.Unlock()
	signal(c.changed)
	return timer
}

func (c *manualRelayClock) timerCalls(d time.Duration) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.created[d]
}

func (c *manualRelayClock) waitForTimerCalls(t *testing.T, d time.Duration, want int) {
	t.Helper()
	deadline := time.NewTimer(testWait)
	defer deadline.Stop()
	for {
		if c.timerCalls(d) >= want {
			return
		}
		select {
		case <-c.changed:
		case <-deadline.C:
			t.Fatalf("relay registered %d timers of %s, want %d", c.timerCalls(d), d, want)
		}
	}
}

func (t *manualRelayTimer) C() <-chan time.Time { return t.ch }

func (t *manualRelayTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.fired || t.stopped {
		return false
	}
	t.stopped = true
	delete(t.clock.timers, t)
	return true
}

func (c *manualRelayClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	for timer := range c.timers {
		if !c.now.Before(timer.deadline) {
			timer.fired = true
			delete(c.timers, timer)
			timer.ch <- c.now
		}
	}
	c.mu.Unlock()
	signal(c.changed)
}

func (c *manualRelayClock) waitForTimer(t *testing.T) {
	t.Helper()
	deadline := time.NewTimer(testWait)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		count := len(c.timers)
		c.mu.Unlock()
		if count != 0 {
			return
		}
		select {
		case <-c.changed:
		case <-deadline.C:
			t.Fatal("relay did not register its expiry timer")
		}
	}
}

func TestRelayClientEchoHashAndConcurrentStreams(t *testing.T) {
	r, _, capability := newRelayFixture(t, echoBackend, nil, nil)
	client := newTestClient(t, capability, r)
	if err := client.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	stream, err := client.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 16<<20)
	for i := range payload {
		payload[i] = byte((i*31 + 17) % 251)
	}
	written := make(chan error, 1)
	go func() {
		n, writeErr := stream.Write(payload)
		if writeErr == nil && n != len(payload) {
			writeErr = io.ErrShortWrite
		}
		written <- writeErr
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(payload)
	gotHash := sha256.Sum256(got)
	if gotHash != wantHash {
		t.Fatal("echo payload hash differs")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}

	const parallelStreams = 16
	streams := make([]*ClientStream, parallelStreams)
	for i := range streams {
		streams[i], err = client.OpenStream(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
	writes := make(chan error, parallelStreams)
	var group sync.WaitGroup
	for i, current := range streams {
		group.Add(1)
		go func(i int, current *ClientStream) {
			defer group.Done()
			data := []byte(fmt.Sprintf("alice-%02d-example.com", i))
			n, writeErr := current.Write(data)
			if writeErr == nil && n != len(data) {
				writeErr = io.ErrShortWrite
			}
			writes <- writeErr
		}(i, current)
	}
	group.Wait()
	close(writes)
	for writeErr := range writes {
		if writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	for i, current := range streams {
		want := []byte(fmt.Sprintf("alice-%02d-example.com", i))
		got := make([]byte, len(want))
		if _, err := io.ReadFull(current, got); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("stream %d returned %q, want %q", i, got, want)
		}
		_ = current.Close()
	}
	if err := client.DeleteSession(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRelayUpAndDownReplayAndLostResponses(t *testing.T) {
	backendEchoed := make(chan struct{}, 1)
	backend := func(_ context.Context, conn net.Conn, _ string) error {
		buffer := make([]byte, 4096)
		for {
			n, err := conn.Read(buffer)
			if n > 0 {
				if _, writeErr := conn.Write(buffer[:n]); writeErr != nil {
					return writeErr
				}
				select {
				case backendEchoed <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return err
			}
		}
	}
	_, handler, capability := newRelayFixture(t, backend, nil, nil)
	bootstrap, token := rawSession(t, handler, capability)
	_ = bootstrap
	upLost := &lostResponseTransport{base: relayTransport{handler: handler}, path: "/api/v1/up"}
	upClient := &http.Client{Transport: upLost}
	open, _ := appendFrame(nil, frame{Type: frameOpen, StreamID: 1})
	open, _ = appendFrame(open, frame{Type: frameData, StreamID: 1, Payload: []byte("de1-first")})
	if err := postUpRaw(upClient, token, 1, open); err == nil {
		t.Fatal("uplink response was not dropped")
	}
	ack, err := postUpRawResponse(upClient, token, 1, open)
	if err != nil || ack != 1 {
		t.Fatalf("repeated uplink ack = %d, err=%v", ack, err)
	}
	select {
	case <-backendEchoed:
	case <-time.After(testWait):
		t.Fatal("fake backend did not echo the applied uplink")
	}

	downLost := &lostResponseTransport{base: relayTransport{handler: handler}, path: "/api/v1/down"}
	downClient := &http.Client{Transport: downLost}
	if err := postDownLost(downClient, token, 0); err == nil {
		t.Fatal("downlink response was not dropped")
	}
	wantBatch, wantHeaders := downLost.saved()
	gotBatch, gotHeaders, err := postDownRaw(downClient, token, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBatch, wantBatch) || gotHeaders.Get(headerDownCursor) != wantHeaders.Get(headerDownCursor) {
		t.Fatal("repeated downlink cursor did not replay the same batch")
	}
	// The echo reaches the downlink queue a moment after the backend wrote it, so the first batch may not hold it yet.
	var echoed int
	for deadline := time.Now().Add(testWait); ; {
		frames, err := parseFrameBatch(gotBatch)
		if err != nil {
			t.Fatal(err)
		}
		for _, frame := range frames {
			if frame.Type == frameData && bytes.Equal(frame.Payload, []byte("de1-first")) {
				echoed++
			}
		}
		if echoed > 0 || time.Now().After(deadline) {
			break
		}
		cursor, err := strconv.ParseUint(gotHeaders.Get(headerDownCursor), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if gotBatch, gotHeaders, err = postDownRaw(downClient, token, cursor); err != nil {
			t.Fatal(err)
		}
	}
	if echoed != 1 {
		t.Fatalf("repeated uplink was applied %d times", echoed)
	}
}

func TestRelayBootstrapRepeatReturnsSameSession(t *testing.T) {
	var mu sync.Mutex
	authenticated := 0
	r, _, capability := newRelayFixture(t, echoBackend, nil, func(*http.Request) {
		mu.Lock()
		authenticated++
		mu.Unlock()
	})
	handler := r.Handler(http.NotFoundHandler())
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "http://example.com/?bridge="+capability, nil))
	bootstrap := page.Header().Get(headerBootstrap)
	hello, _ := appendFrame(nil, frame{Type: frameHello, Payload: appendHelloPayload(nil)})
	var firstBody []byte
	var firstHeaders http.Header
	for attempt := 0; attempt < 2; attempt++ {
		req := httptest.NewRequest(http.MethodPost, "http://example.com/api/v1/session", bytes.NewReader(hello))
		req.Header.Set("Authorization", "Bearer "+bootstrap)
		req.Header.Set("Content-Type", "application/octet-stream")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("bootstrap attempt %d returned %d", attempt, rec.Code)
		}
		if attempt == 0 {
			firstBody = append([]byte(nil), rec.Body.Bytes()...)
			firstHeaders = rec.Header().Clone()
		} else if !bytes.Equal(rec.Body.Bytes(), firstBody) || rec.Header().Get(headerSessionToken) != firstHeaders.Get(headerSessionToken) {
			t.Fatal("repeated bootstrap did not return the original session response")
		}
	}
	r.mu.Lock()
	count := len(r.sessions)
	r.mu.Unlock()
	mu.Lock()
	callbackCount := authenticated
	mu.Unlock()
	if count != 1 || callbackCount != 3 {
		t.Fatalf("sessions=%d, authenticated callback calls=%d", count, callbackCount)
	}
}

func TestRelayPendingPressureRetriesWithoutCommittingSequence(t *testing.T) {
	r, handler, capability := newRelayFixture(t, echoBackend, nil, nil)
	_, token := rawSession(t, handler, capability)
	client := &http.Client{Transport: relayTransport{handler: handler}}
	open, _ := appendFrame(nil, frame{Type: frameOpen, StreamID: 7})
	if _, err := postUpRawResponse(client, token, 1, open); err != nil {
		t.Fatal(err)
	}
	r.pendingMu.Lock()
	used := r.pendingByCredential["alice"]
	r.pendingMu.Unlock()
	pressure := maxPendingPerCredential - used
	if pressure <= 0 || !r.reservePending("alice", pendingUp, pressure) {
		t.Fatal("could not create test pressure")
	}
	data, _ := appendFrame(nil, frame{Type: frameData, StreamID: 7, Payload: []byte("a")})
	status, headers, err := postUpStatus(client, token, 2, data)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusServiceUnavailable || headers.Get("Retry-After") != "1" {
		t.Fatalf("pressure response = %d, Retry-After %q", status, headers.Get("Retry-After"))
	}
	r.releasePending("alice", pendingUp, pressure)
	ack, err := postUpRawResponse(client, token, 2, data)
	if err != nil || ack != 2 {
		t.Fatalf("retried uplink ack = %d, err=%v", ack, err)
	}
}

func TestRelayPendingBudgetsSeparateCredentialsAndDirections(t *testing.T) {
	r, _, _ := newRelayFixture(t, echoBackend, nil, nil)
	share := int64(maxPendingPerCredential)
	for _, id := range []string{"alice", "bob", "carol", "dave"} {
		if !r.reservePending(id, pendingUp, share) {
			t.Fatalf("could not reserve uplink share for %s", id)
		}
	}
	if r.reservePending("alice", pendingUp, 1) || r.reservePending("alice", pendingDown, 1) {
		t.Fatal("one credential exceeded its shared byte allowance")
	}
	if r.reservePending("eve", pendingUp, 1) {
		t.Fatal("uplink exceeded its directional allowance")
	}
	if !r.reservePending("eve", pendingDown, share) {
		t.Fatal("uplink pressure consumed the separate downlink allowance")
	}
	for _, id := range []string{"alice", "bob", "carol", "dave"} {
		r.releasePending(id, pendingUp, share)
	}
	r.releasePending("eve", pendingDown, share)
}

func TestRelayPendingShareBackpressureResumesAfterPollAcknowledgement(t *testing.T) {
	const streamCount = 5
	controls := make([]chan struct{}, streamCount)
	for i := range controls {
		controls[i] = make(chan struct{})
	}
	started := make(chan int, streamCount)
	attempted := make(chan int, 300)
	wrote := make(chan int, 300)
	var nextBackend atomic.Int32
	chunk := bytes.Repeat([]byte{'x'}, backendChunkSize)
	backend := func(ctx context.Context, conn net.Conn, _ string) error {
		id := int(nextBackend.Add(1) - 1)
		started <- id
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-controls[id]:
			}
			attempted <- id
			n, err := conn.Write(chunk)
			if err != nil {
				return err
			}
			if n != len(chunk) {
				return io.ErrShortWrite
			}
			wrote <- id
		}
	}
	r, handler, capability := newRelayFixture(t, backend, nil, nil)
	_, token := rawSession(t, handler, capability)
	client := &http.Client{Transport: relayTransport{handler: handler}}
	open := make([]byte, 0, streamCount*frameHeaderSize)
	for id := uint32(1); id <= streamCount; id++ {
		open, _ = appendFrame(open, frame{Type: frameOpen, StreamID: id})
	}
	if _, err := postUpRawResponse(client, token, 1, open); err != nil {
		t.Fatal(err)
	}
	seen := make(map[int]bool, streamCount)
	for len(seen) < streamCount {
		select {
		case id := <-started:
			seen[id] = true
		case <-time.After(testWait):
			t.Fatal("not all backend streams started")
		}
	}
	grant := func(id int) {
		t.Helper()
		select {
		case controls[id] <- struct{}{}:
		case <-time.After(testWait):
			t.Fatalf("backend %d did not accept a write grant", id)
		}
		select {
		case got := <-attempted:
			if got != id {
				t.Fatalf("backend write attempt = %d, want %d", got, id)
			}
		case <-time.After(testWait):
			t.Fatalf("backend %d did not attempt its granted write", id)
		}
		select {
		case got := <-wrote:
			if got != id {
				t.Fatalf("backend write completed on %d, want %d", got, id)
			}
		case <-time.After(testWait):
			t.Fatalf("backend %d write did not complete", id)
		}
	}
	for i := 0; i < 254; i++ {
		grant(i % streamCount)
	}
	grant(0) // This read cannot enter the budget; it leaves the backend blocked on its next write.
	select {
	case controls[0] <- struct{}{}:
	case <-time.After(testWait):
		t.Fatal("blocked backend did not accept its next write grant")
	}
	select {
	case id := <-attempted:
		if id != 0 {
			t.Fatalf("blocked write attempt = %d, want 0", id)
		}
	case <-time.After(testWait):
		t.Fatal("backend did not reach the write blocked by the pending budget")
	}
	r.pendingMu.Lock()
	pending := r.pendingByCredential["alice"]
	globalPending := r.pendingBytes.Load()
	upPending, downPending := r.pendingUpBytes, r.pendingDownBytes
	r.pendingMu.Unlock()
	if pending > maxPendingPerCredential || pending < maxPendingPerCredential-8*dataFrameCharge(backendChunkSize) {
		t.Fatalf("pending share = %d, want within %d and near its limit", pending, maxPendingPerCredential)
	}
	if globalPending > maxPendingBytes || upPending > maxPendingUpBytes || downPending > maxPendingDownBytes {
		t.Fatal("pending accounting exceeded a relay or directional limit")
	}

	body, headers, err := postDownRaw(client, token, 0)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := parseFrameBatch(body)
	if err != nil || len(frames) == 0 {
		t.Fatalf("first downlink batch has %d frames, err=%v", len(frames), err)
	}
	cursor, err := parseCanonicalUint(headers.Get(headerDownCursor))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := postDownRaw(client, token, cursor); err != nil {
		t.Fatalf("acknowledging the first downlink batch: %v", err)
	}
	select {
	case got := <-wrote:
		if got != 0 {
			t.Fatalf("resumed backend %d, want blocked backend 0", got)
		}
	case <-time.After(testWait):
		t.Fatal("backend did not resume after poll acknowledgement")
	}
}

func TestRelayPassThroughPreservesRequestAndBody(t *testing.T) {
	r, _, capability := newRelayFixture(t, echoBackend, nil, nil)
	wrongCapability := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	unknownToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x91}, tokenSize))
	type observed struct {
		request *http.Request
		body    string
	}
	var got []observed
	var requests []*http.Request
	next := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		got = append(got, observed{request: req, body: string(body)})
		w.WriteHeader(http.StatusAccepted)
	})
	handler := r.Handler(next)
	cases := []struct {
		method string
		path   string
		query  string
		header string
		body   string
	}{
		{method: http.MethodGet, path: "/", query: "bridge=" + wrongCapability, body: "bridge-body"},
		{method: http.MethodPost, path: "/api/v1/up", header: "Bearer " + unknownToken, body: "unknown-token-body"},
		{method: http.MethodHead, path: "/", query: "bridge=" + capability, body: "head-body"},
		{method: http.MethodGet, path: "/", query: "bridge=" + capability + "&bridge=" + capability, body: "duplicate-bridge-body"},
		{method: http.MethodGet, path: "/", query: "other=1&bridge=" + capability, body: "reordered-query-body"},
		{method: http.MethodPost, path: "/foreign", body: "bridge=" + capability},
		{method: http.MethodPost, path: "/foreign", body: "foreign-body"},
		{method: http.MethodGet, path: "/api/v1/up", body: "wrong-method-body"},
	}
	for i, test := range cases {
		req := httptest.NewRequest(test.method, "http://example.com"+test.path, strings.NewReader(test.body))
		requests = append(requests, req)
		if test.query != "" {
			req.URL.RawQuery = test.query
		}
		if test.header != "" {
			req.Header.Set("Authorization", test.header)
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusAccepted {
			t.Fatalf("case %d reached status %d", i, rr.Code)
		}
	}
	if len(got) != len(cases) {
		t.Fatalf("next received %d requests, want %d", len(got), len(cases))
	}
	for i, test := range cases {
		if got[i].request != requests[i] {
			t.Fatalf("case %d was passed as a different request object", i)
		}
		if got[i].body != test.body {
			t.Fatalf("case %d body = %q, want %q", i, got[i].body, test.body)
		}
	}
}

func TestRelayEvictsOldestUnusedBootstrapPerCredential(t *testing.T) {
	r, handler, capability := newRelayFixture(t, echoBackend, nil, nil)
	tokens := make([]string, maxUnusedBootstraps+1)
	for i := range tokens {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "http://example.com/?bridge="+capability, nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("bridge %d returned %d", i, rec.Code)
		}
		tokens[i] = rec.Header().Get(headerBootstrap)
	}
	r.mu.Lock()
	if len(r.unusedBootstrap["alice"]) != maxUnusedBootstraps {
		t.Fatalf("unused bootstraps = %d, want %d", len(r.unusedBootstrap["alice"]), maxUnusedBootstraps)
	}
	_, oldestRetained := r.bootstrap[tokens[0]]
	_, newestRetained := r.bootstrap[tokens[len(tokens)-1]]
	r.mu.Unlock()
	if oldestRetained || !newestRetained {
		t.Fatal("bootstrap eviction did not discard the oldest token")
	}

	hello, _ := appendFrame(nil, frame{Type: frameHello, Payload: appendHelloPayload(nil)})
	requestSession := func(token string) int {
		req := httptest.NewRequest(http.MethodPost, "http://example.com/api/v1/session", bytes.NewReader(hello))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/octet-stream")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := requestSession(tokens[0]); got != http.StatusNotFound {
		t.Fatalf("evicted bootstrap returned %d, want pass-through 404", got)
	}
	if got := requestSession(tokens[len(tokens)-1]); got != http.StatusOK {
		t.Fatalf("newest bootstrap returned %d, want 200", got)
	}
}

func TestRelayExpiredBootstrapPassesThroughAndIdleExpiryClosesSession(t *testing.T) {
	clock := newManualRelayClock()
	r, handler, capability := newRelayFixture(t, echoBackend, clock, nil)
	bootstrapReq := httptest.NewRequest(http.MethodGet, "http://example.com/?bridge="+capability, nil)
	bootstrapRec := httptest.NewRecorder()
	handler.ServeHTTP(bootstrapRec, bootstrapReq)
	bootstrapToken := bootstrapRec.Header().Get(headerBootstrap)
	clock.waitForTimer(t)
	clock.Advance(bootstrapTokenTTL)
	var observed *http.Request
	var body string
	next := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		observed = req
		bytesRead, _ := io.ReadAll(req.Body)
		body = string(bytesRead)
		w.WriteHeader(http.StatusAccepted)
	})
	request := httptest.NewRequest(http.MethodPost, "http://example.com/api/v1/session", strings.NewReader("untouched"))
	request.Header.Set("Authorization", "Bearer "+bootstrapToken)
	request.Header.Set("Content-Type", "application/octet-stream")
	r.Handler(next).ServeHTTP(httptest.NewRecorder(), request)
	if observed != request || body != "untouched" {
		t.Fatal("expired bootstrap request was not passed through untouched")
	}

	clock2 := newManualRelayClock()
	r2, handler2, capability2 := newRelayFixture(t, echoBackend, clock2, nil)
	_, token := rawSession(t, handler2, capability2)
	r2.mu.Lock()
	session := r2.sessions[token]
	r2.mu.Unlock()
	clock2.waitForTimer(t)
	clock2.Advance(relayIdleTimeout)
	select {
	case <-session.done:
	case <-time.After(testWait):
		t.Fatal("idle session did not close")
	}
}

func TestRelayPollTakeoverWakesReplacementPoll(t *testing.T) {
	clock := newManualRelayClock()
	backend := func(ctx context.Context, _ net.Conn, _ string) error {
		<-ctx.Done()
		return nil
	}
	r, handler, capability := newRelayFixture(t, backend, clock, nil)
	_, token := rawSession(t, handler, capability)
	client := &http.Client{Transport: relayTransport{handler: handler}}
	open, _ := appendFrame(nil, frame{Type: frameOpen, StreamID: 1})
	if _, err := postUpRawResponse(client, token, 1, open); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	session := r.sessions[token]
	r.mu.Unlock()

	baseline := clock.timerCalls(downLongPollTimeout)
	oldResponse, oldDone, cancelOld := startDownPoll(handler, token, 0)
	t.Cleanup(cancelOld)
	clock.waitForTimerCalls(t, downLongPollTimeout, baseline+1)
	newResponse, newDone, cancelNew := startDownPoll(handler, token, 0)
	t.Cleanup(cancelNew)
	clock.waitForTimerCalls(t, downLongPollTimeout, baseline+2)

	payload := []byte("wake")
	charge := dataFrameCharge(len(payload))
	if !r.reservePending("alice", pendingDown, charge) {
		t.Fatal("could not reserve test data")
	}
	if !session.enqueueReserved(frame{Type: frameData, StreamID: 1, Payload: payload}, charge) {
		t.Fatal("could not enqueue test data")
	}
	select {
	case <-oldDone:
	case <-time.After(testWait):
		t.Fatal("replaced poll did not return")
	}
	if oldResponse.Code != http.StatusNoContent || oldResponse.Header().Get(headerDownCursor) != "0" {
		t.Fatalf("replaced poll returned %d at cursor %q", oldResponse.Code, oldResponse.Header().Get(headerDownCursor))
	}
	select {
	case <-newDone:
	case <-time.After(testWait):
		t.Fatal("replacement poll did not receive queued data")
	}
	if newResponse.Code != http.StatusOK || newResponse.Header().Get(headerDownCursor) != "1" {
		t.Fatalf("replacement poll returned %d at cursor %q", newResponse.Code, newResponse.Header().Get(headerDownCursor))
	}
	frames, err := parseFrameBatch(newResponse.Body.Bytes())
	if err != nil || len(frames) != 1 || frames[0].Type != frameData || !bytes.Equal(frames[0].Payload, payload) {
		t.Fatalf("replacement poll data = %#v, err=%v", frames, err)
	}
}

func startDownPoll(handler http.Handler, token string, cursor uint64) (*httptest.ResponseRecorder, <-chan struct{}, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/v1/down", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(headerDownCursor, strconv.FormatUint(cursor, 10))
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, req)
		close(done)
	}()
	return response, done, cancel
}

func TestRelayCredentialRemovalClosesStreamsAndSession(t *testing.T) {
	backend := func(ctx context.Context, conn net.Conn, credentialID string) error {
		<-ctx.Done()
		return nil
	}
	r, _, capability := newRelayFixture(t, backend, nil, nil)
	client := newTestClient(t, capability, r)
	if err := client.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	stream, err := client.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	token := client.token
	client.mu.Unlock()
	r.mu.Lock()
	session := r.sessions[token]
	r.mu.Unlock()
	session.mu.Lock()
	serverStream := session.streams[stream.id]
	session.mu.Unlock()
	if err := r.SetCredentials(nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.done:
	case <-time.After(testWait):
		t.Fatal("removed credential session did not stop")
	}
	select {
	case <-serverStream.done:
	case <-time.After(testWait):
		t.Fatal("removed credential stream did not stop")
	}
	client.mu.Lock()
	if client.cancel != nil {
		client.cancel()
	}
	pollDone := client.pollDone
	client.mu.Unlock()
	if pollDone != nil {
		select {
		case <-pollDone:
		case <-time.After(testWait):
			t.Fatal("client poll did not stop after credential removal")
		}
	}
}

func TestRelayCredentialRotationRetiresSessionAndBootstrapWithoutWaiting(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	backend := func(context.Context, net.Conn, string) error {
		started <- struct{}{}
		<-release
		return nil
	}
	r, handler, oldCapability := newRelayFixture(t, backend, nil, nil)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	_, token := rawSession(t, handler, oldCapability)
	client := &http.Client{Transport: relayTransport{handler: handler}}
	open, _ := appendFrame(nil, frame{Type: frameOpen, StreamID: 1})
	if _, err := postUpRawResponse(client, token, 1, open); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(testWait):
		t.Fatal("backend did not start before credential rotation")
	}
	r.mu.Lock()
	session := r.sessions[token]
	r.mu.Unlock()
	unusedPage := httptest.NewRecorder()
	handler.ServeHTTP(unusedPage, httptest.NewRequest(http.MethodGet, "http://example.com/?bridge="+oldCapability, nil))
	unusedBootstrap := unusedPage.Header().Get(headerBootstrap)

	newSecret := []byte("bob1234567890123")
	newCapability, err := DeriveCapability("example.com", newSecret)
	if err != nil {
		t.Fatal(err)
	}
	rotated := make(chan error, 1)
	go func() {
		rotated <- r.SetCredentials([]Credential{{ID: "alice", Secret: newSecret}})
	}()
	select {
	case err := <-rotated:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(testWait):
		t.Fatal("SetCredentials waited for the backend worker")
	}
	if session.ctx.Err() == nil {
		t.Fatal("credential rotation did not cancel the old session")
	}
	r.mu.Lock()
	_, bootstrapExists := r.bootstrap[unusedBootstrap]
	_, sessionExists := r.sessions[token]
	r.mu.Unlock()
	if bootstrapExists {
		t.Fatal("credential rotation retained an unused bootstrap")
	}
	if sessionExists {
		t.Fatal("credential rotation retained the old transport session")
	}
	select {
	case <-session.done:
		t.Fatal("SetCredentials waited for the blocked backend worker")
	default:
	}
	oldRequest := httptest.NewRequest(http.MethodPost, "http://example.com/api/v1/session", strings.NewReader("untouched"))
	oldRequest.Header.Set("Authorization", "Bearer "+unusedBootstrap)
	oldRequest.Header.Set("Content-Type", "application/octet-stream")
	oldResponse := httptest.NewRecorder()
	var passedBody string
	oldHandler := r.Handler(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		data, _ := io.ReadAll(req.Body)
		passedBody = string(data)
		http.NotFound(w, req)
	}))
	oldHandler.ServeHTTP(oldResponse, oldRequest)
	if oldResponse.Code != http.StatusNotFound || passedBody != "untouched" {
		t.Fatalf("old bootstrap response = %d passed body %q", oldResponse.Code, passedBody)
	}
	newPage := httptest.NewRecorder()
	handler.ServeHTTP(newPage, httptest.NewRequest(http.MethodGet, "http://example.com/?bridge="+newCapability, nil))
	if newPage.Code != http.StatusOK {
		t.Fatalf("new capability returned HTTP %d", newPage.Code)
	}
	close(release)
	select {
	case <-session.done:
	case <-time.After(testWait):
		t.Fatal("rotated session did not finish after backend release")
	}
}

func TestRelayCloseCredentialReturnsBeforeBackendWorker(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	backend := func(context.Context, net.Conn, string) error {
		started <- struct{}{}
		<-release
		return nil
	}
	r, handler, capability := newRelayFixture(t, backend, nil, nil)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	_, token := rawSession(t, handler, capability)
	client := &http.Client{Transport: relayTransport{handler: handler}}
	open, _ := appendFrame(nil, frame{Type: frameOpen, StreamID: 1})
	if _, err := postUpRawResponse(client, token, 1, open); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(testWait):
		t.Fatal("backend did not start before credential close")
	}
	r.mu.Lock()
	session := r.sessions[token]
	r.mu.Unlock()
	closed := make(chan struct{})
	go func() {
		r.CloseCredential("alice")
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(testWait):
		t.Fatal("CloseCredential waited for the backend worker")
	}
	if session.ctx.Err() != nil {
		t.Fatal("CloseCredential cancelled the transport session")
	}
	close(release)
}

func TestRelayCloseCredentialKeepsTransportSession(t *testing.T) {
	r, _, capability := newRelayFixture(t, echoBackend, nil, nil)
	client := newTestClient(t, capability, r)
	if err := client.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	first, err := client.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	token := client.token
	client.mu.Unlock()
	r.mu.Lock()
	session := r.sessions[token]
	r.mu.Unlock()
	session.mu.Lock()
	firstServerStream := session.streams[first.id]
	session.mu.Unlock()
	r.CloseCredential("alice")
	select {
	case <-firstServerStream.done:
	case <-time.After(testWait):
		t.Fatal("credential kick did not close the first stream")
	}
	if session.ctx.Err() != nil {
		t.Fatal("credential kick ended the transport session")
	}
	second, err := client.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Write([]byte("bob-de1")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("bob-de1"))
	if _, err := io.ReadFull(second, got); err != nil || string(got) != "bob-de1" {
		t.Fatalf("new stream read %q, err=%v", got, err)
	}
	if err := client.DeleteSession(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRelayBackendReceivesOpenRequestRemoteAddress(t *testing.T) {
	remote := make(chan net.Addr, 1)
	backend := func(ctx context.Context, conn net.Conn, _ string) error {
		remote <- conn.RemoteAddr()
		<-ctx.Done()
		return nil
	}
	_, handler, capability := newRelayFixture(t, backend, nil, nil)
	_, token := rawSession(t, handler, capability)
	open, _ := appendFrame(nil, frame{Type: frameOpen, StreamID: 1})
	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/v1/up", bytes.NewReader(open))
	req.RemoteAddr = "203.0.113.44:3456"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(headerUpSequence, "1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("open returned HTTP %d", rec.Code)
	}
	select {
	case addr := <-remote:
		tcpAddr, ok := addr.(*net.TCPAddr)
		if !ok || tcpAddr.String() != req.RemoteAddr {
			t.Fatalf("backend RemoteAddr = %T %v, want *net.TCPAddr %s", addr, addr, req.RemoteAddr)
		}
	case <-time.After(testWait):
		t.Fatal("backend did not receive OPEN")
	}
}

func TestRelayStreamAndSessionLimits(t *testing.T) {
	backend := func(ctx context.Context, conn net.Conn, credentialID string) error {
		<-ctx.Done()
		return nil
	}
	r, handler, capability := newRelayFixture(t, backend, nil, nil)
	var firstSession *transportSession
	for i := 0; i < maxSessionsPerCredential+1; i++ {
		_, token := rawSession(t, handler, capability)
		if i == 0 {
			r.mu.Lock()
			firstSession = r.sessions[token]
			r.mu.Unlock()
		}
	}
	select {
	case <-firstSession.done:
	case <-time.After(testWait):
		t.Fatal("oldest transport session was not evicted")
	}
	r.mu.Lock()
	owned := len(r.byCredential["alice"])
	r.mu.Unlock()
	if owned != maxSessionsPerCredential {
		t.Fatalf("credential has %d sessions, want %d", owned, maxSessionsPerCredential)
	}

	_, token := rawSession(t, handler, capability)
	streamFrames := make([]byte, 0, (maxStreamsPerSession+1)*frameHeaderSize)
	for id := uint32(1); id <= maxStreamsPerSession+1; id++ {
		streamFrames, _ = appendFrame(streamFrames, frame{Type: frameOpen, StreamID: id})
	}
	client := &http.Client{Transport: relayTransport{handler: handler}}
	if _, err := postUpRawResponse(client, token, 1, streamFrames); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	session := r.sessions[token]
	r.mu.Unlock()
	session.mu.Lock()
	count := len(session.streams)
	_, excessClosed := session.tombstones[maxStreamsPerSession+1]
	session.mu.Unlock()
	if count != maxStreamsPerSession || !excessClosed {
		t.Fatalf("active streams=%d, excess stream tombstoned=%t", count, excessClosed)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRelayGlobalSessionLimitAndCloseWaits(t *testing.T) {
	backend := func(ctx context.Context, conn net.Conn, credentialID string) error {
		<-ctx.Done()
		return nil
	}
	r, handler, _ := newRelayFixture(t, backend, nil, nil)
	credentials := make([]Credential, maxSessionsPerRelay/maxSessionsPerCredential+1)
	capabilities := make([]string, len(credentials))
	for i := range credentials {
		id := fmt.Sprintf("alice-%02d", i)
		secret := make([]byte, 16)
		binaryValue := uint64(i + 1)
		for j := 0; j < 8; j++ {
			secret[j] = byte(binaryValue >> (8 * j))
		}
		copy(secret[8:], []byte("de1-node"))
		credentials[i] = Credential{ID: id, Secret: secret}
		capabilities[i], _ = DeriveCapability("example.com", secret)
	}
	if err := r.SetCredentials(credentials); err != nil {
		t.Fatal(err)
	}
	var finalStatus int
	for i := 0; i < maxSessionsPerRelay; i++ {
		capIndex := i / maxSessionsPerCredential
		_, _ = rawSession(t, handler, capabilities[capIndex])
	}
	status, headers, _ := rawSessionStatus(t, handler, capabilities[len(capabilities)-1])
	finalStatus = status
	if finalStatus != http.StatusServiceUnavailable || headers.Get("Retry-After") != "1" {
		t.Fatalf("global capacity response = %d, Retry-After %q", finalStatus, headers.Get("Retry-After"))
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRelaySilentStreamsDoNotReservePendingOrStarveNeighbor(t *testing.T) {
	const (
		quietSessionCount = maxSessionsPerCredential
		quietPerSession   = maxStreamsPerSession
		otherQuietCount   = 52
		quietTotal        = quietSessionCount*quietPerSession + otherQuietCount
	)
	started := make(chan struct{}, quietTotal)
	backend := func(ctx context.Context, conn net.Conn, credentialID string) error {
		if credentialID == "healthy" {
			_, err := io.Copy(conn, conn)
			return err
		}
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	r, handler, _ := newRelayFixture(t, backend, nil, nil)
	credentials := []Credential{
		{ID: "silent-a", Secret: []byte("silent-a12345678")},
		{ID: "silent-b", Secret: []byte("silent-b12345678")},
		{ID: "healthy", Secret: []byte("healthy123456789")},
	}
	if err := r.SetCredentials(credentials); err != nil {
		t.Fatal(err)
	}
	capabilities := make(map[string]string, len(credentials))
	for _, credential := range credentials {
		capabilities[credential.ID], _ = DeriveCapability("example.com", credential.Secret)
	}
	client := &http.Client{Transport: relayTransport{handler: handler}}
	openSessions := func(capability string, count, streamsPerSession int) {
		for i := 0; i < count; i++ {
			_, token := rawSession(t, handler, capability)
			frames := make([]byte, 0, streamsPerSession*frameHeaderSize)
			for id := uint32(1); id <= uint32(streamsPerSession); id++ {
				frames, _ = appendFrame(frames, frame{Type: frameOpen, StreamID: id})
			}
			if _, err := postUpRawResponse(client, token, 1, frames); err != nil {
				t.Fatalf("opening silent streams: %v", err)
			}
		}
	}
	openSessions(capabilities["silent-a"], quietSessionCount, quietPerSession)
	openSessions(capabilities["silent-b"], 1, otherQuietCount)
	timer := time.NewTimer(testWait)
	defer timer.Stop()
	for i := 0; i < quietTotal; i++ {
		select {
		case <-started:
		case <-timer.C:
			t.Fatalf("only %d of %d quiet backends started", i, quietTotal)
		}
	}
	if got := r.pendingBytes.Load(); got != 0 {
		t.Fatalf("silent streams reserved %d pending bytes", got)
	}

	clientConn := newTestClient(t, capabilities["healthy"], r)
	if err := clientConn.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	stream, err := clientConn.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte("neighbor")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("neighbor"))
	if _, err := io.ReadFull(stream, got); err != nil || string(got) != "neighbor" {
		t.Fatalf("neighbor echo = %q, err=%v", got, err)
	}
	if err := clientConn.DeleteSession(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRelayPanicOnlyClosesItsStream(t *testing.T) {
	backend := func(ctx context.Context, conn net.Conn, credentialID string) error {
		var first [1]byte
		n, err := conn.Read(first[:])
		if err != nil {
			return err
		}
		if n > 0 && first[0] == '!' {
			panic("stream-local test panic")
		}
		if n > 0 {
			if _, err := conn.Write(first[:n]); err != nil {
				return err
			}
		}
		_, err = io.Copy(conn, conn)
		return err
	}
	r, _, capability := newRelayFixture(t, backend, nil, nil)
	client := newTestClient(t, capability, r)
	if err := client.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	panics, err := client.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := client.OpenStream(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := panics.Write([]byte("!")); err != nil {
		t.Fatal(err)
	}
	if _, err := healthy.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	want := make([]byte, 2)
	if _, err := io.ReadFull(healthy, want); err != nil || string(want) != "ok" {
		t.Fatalf("healthy stream read %q, err=%v", want, err)
	}
	var one [1]byte
	if _, err := panics.Read(one[:]); err == nil {
		t.Fatal("panicking stream did not close")
	}
	if err := client.DeleteSession(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func echoBackend(_ context.Context, conn net.Conn, _ string) error {
	_, err := io.Copy(conn, conn)
	return err
}

func newRelayFixture(t *testing.T, backend Backend, clock relayClock, onAuth func(*http.Request)) (*Relay, http.Handler, string) {
	t.Helper()
	secret := []byte("alice01234567890")
	capability, err := DeriveCapability("example.com", secret)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRelay(RelayOptions{
		Host:    "example.com",
		Backend: backend,
		BridgePage: func(token string) []byte {
			return []byte("<meta name=\"tproxy-bootstrap-token\" content=\"" + token + "\">")
		},
		Clock:           clock,
		OnAuthenticated: onAuth,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetCredentials([]Credential{{ID: "alice", Secret: secret}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = r.Close()
		if got := r.pendingBytes.Load(); got != 0 {
			t.Errorf("relay retained %d pending bytes after Close", got)
		}
	})
	return r, r.Handler(http.NotFoundHandler()), capability
}

func newTestClient(t *testing.T, capability string, r *Relay) *Client {
	t.Helper()
	client, err := NewClient("http://example.com", capability, relayTransport{handler: r.Handler(http.NotFoundHandler())})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.mu.Lock()
		cancel := client.cancel
		done := client.pollDone
		client.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if done != nil {
			select {
			case <-done:
			case <-time.After(testWait):
				t.Errorf("client poll goroutine did not stop")
			}
		}
	})
	return client
}

func rawSession(t *testing.T, handler http.Handler, capability string) (string, string) {
	t.Helper()
	status, headers, _ := rawSessionStatus(t, handler, capability)
	if status != http.StatusOK {
		t.Fatalf("session creation returned %d", status)
	}
	return headers.Get(headerBootstrap), headers.Get(headerSessionToken)
}

func rawSessionStatus(t *testing.T, handler http.Handler, capability string) (int, http.Header, []byte) {
	t.Helper()
	pageReq := httptest.NewRequest(http.MethodGet, "http://example.com/?bridge="+capability, nil)
	pageRec := httptest.NewRecorder()
	handler.ServeHTTP(pageRec, pageReq)
	bootstrap := pageRec.Header().Get(headerBootstrap)
	if pageRec.Code != http.StatusOK || !canonicalBase64URL(bootstrap, tokenSize) {
		t.Fatalf("bridge returned %d without bootstrap token", pageRec.Code)
	}
	hello, _ := appendFrame(nil, frame{Type: frameHello, Payload: appendHelloPayload(nil)})
	req := httptest.NewRequest(http.MethodPost, "http://example.com/api/v1/session", bytes.NewReader(hello))
	req.Header.Set("Authorization", "Bearer "+bootstrap)
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, rec.Header().Clone(), append([]byte(nil), rec.Body.Bytes()...)
}

func postUpRaw(client *http.Client, token string, sequence uint64, body []byte) error {
	_, err := postUpRawResponse(client, token, sequence, body)
	return err
}

func postUpRawResponse(client *http.Client, token string, sequence uint64, body []byte) (uint64, error) {
	status, headers, err := postUpStatus(client, token, sequence, body)
	if err != nil {
		return 0, err
	}
	if status != http.StatusNoContent {
		return 0, fmt.Errorf("uplink returned HTTP %d", status)
	}
	return parseCanonicalUint(headers.Get(headerUpAck))
}

func postUpStatus(client *http.Client, token string, sequence uint64, body []byte) (int, http.Header, error) {
	req, _ := http.NewRequest(http.MethodPost, "http://example.com/api/v1/up", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(headerUpSequence, strconv.FormatUint(sequence, 10))
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, resp.Header.Clone(), nil
}

func postDownLost(client *http.Client, token string, cursor uint64) error {
	_, _, err := postDownRaw(client, token, cursor)
	return err
}

func postDownRaw(client *http.Client, token string, cursor uint64) ([]byte, http.Header, error) {
	req, _ := http.NewRequest(http.MethodPost, "http://example.com/api/v1/down", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(headerDownCursor, strconv.FormatUint(cursor, 10))
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, nil, readErr
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.Header.Clone(), fmt.Errorf("downlink returned HTTP %d", resp.StatusCode)
	}
	return body, resp.Header.Clone(), nil
}
