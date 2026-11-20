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
		changed: make(chan struct{}, 1),
	}
}

func (c *manualRelayClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualRelayClock) NewTimer(d time.Duration) RelayTimer {
	c.mu.Lock()
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
	r, handler, capability := newRelayFixture(t, echoBackend, nil, nil)
	bootstrap, token := rawSession(t, handler, capability)
	_ = bootstrap
	upLost := &lostResponseTransport{base: relayTransport{handler: handler}, path: "/api/v1/up"}
	upClient := &http.Client{Transport: upLost}
	open, _ := AppendFrame(nil, Frame{Type: FrameOpen, StreamID: 1})
	open, _ = AppendFrame(open, Frame{Type: FrameData, StreamID: 1, Payload: []byte("de1-first")})
	if err := postUpRaw(upClient, token, 1, open); err == nil {
		t.Fatal("uplink response was not dropped")
	}
	ack, err := postUpRawResponse(upClient, token, 1, open)
	if err != nil || ack != 1 {
		t.Fatalf("repeated uplink ack = %d, err=%v", ack, err)
	}

	var session *transportSession
	r.mu.Lock()
	session = r.sessions[token]
	r.mu.Unlock()
	waitForOutput(t, session)

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
	frames, err := readFrameBatch(gotBatch)
	if err != nil {
		t.Fatal(err)
	}
	var echoed int
	for _, frame := range frames {
		if frame.Type == FrameData && bytes.Equal(frame.Payload, []byte("de1-first")) {
			echoed++
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
	hello, _ := AppendFrame(nil, Frame{Type: FrameHello, Payload: AppendHelloPayload(nil)})
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
	open, _ := AppendFrame(nil, Frame{Type: FrameOpen, StreamID: 7})
	if _, err := postUpRawResponse(client, token, 1, open); err != nil {
		t.Fatal(err)
	}
	pressure := int64(MaxPendingBytes) - r.pendingBytes.Load()
	if pressure <= 0 || !r.reservePending(pressure) {
		t.Fatal("could not create test pressure")
	}
	data, _ := AppendFrame(nil, Frame{Type: FrameData, StreamID: 7, Payload: []byte("a")})
	status, headers, err := postUpStatus(client, token, 2, data)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusServiceUnavailable || headers.Get("Retry-After") != "1" {
		t.Fatalf("pressure response = %d, Retry-After %q", status, headers.Get("Retry-After"))
	}
	r.releasePending(pressure)
	ack, err := postUpRawResponse(client, token, 2, data)
	if err != nil || ack != 2 {
		t.Fatalf("retried uplink ack = %d, err=%v", ack, err)
	}
}

func TestRelayPassThroughPreservesRequestAndBody(t *testing.T) {
	r, _, _ := newRelayFixture(t, echoBackend, nil, nil)
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

func TestRelayExpiredBootstrapPassesThroughAndIdleExpiryClosesSession(t *testing.T) {
	clock := newManualRelayClock()
	r, handler, capability := newRelayFixture(t, echoBackend, clock, nil)
	bootstrapReq := httptest.NewRequest(http.MethodGet, "http://example.com/?bridge="+capability, nil)
	bootstrapRec := httptest.NewRecorder()
	handler.ServeHTTP(bootstrapRec, bootstrapReq)
	bootstrapToken := bootstrapRec.Header().Get(headerBootstrap)
	clock.waitForTimer(t)
	clock.Advance(BootstrapTokenTTL)
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
	clock2.Advance(RelayIdleTimeout)
	select {
	case <-session.done:
	case <-time.After(testWait):
		t.Fatal("idle session did not close")
	}
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

func TestRelayStreamAndSessionLimits(t *testing.T) {
	backend := func(ctx context.Context, conn net.Conn, credentialID string) error {
		<-ctx.Done()
		return nil
	}
	r, handler, capability := newRelayFixture(t, backend, nil, nil)
	var firstSession *transportSession
	for i := 0; i < MaxSessionsPerCredential+1; i++ {
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
	if owned != MaxSessionsPerCredential {
		t.Fatalf("credential has %d sessions, want %d", owned, MaxSessionsPerCredential)
	}

	_, token := rawSession(t, handler, capability)
	streamFrames := make([]byte, 0, (MaxStreamsPerSession+1)*FrameHeaderSize)
	for id := uint32(1); id <= MaxStreamsPerSession+1; id++ {
		streamFrames, _ = AppendFrame(streamFrames, Frame{Type: FrameOpen, StreamID: id})
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
	_, excessClosed := session.tombstones[MaxStreamsPerSession+1]
	session.mu.Unlock()
	if count != MaxStreamsPerSession || !excessClosed {
		t.Fatalf("active streams=%d, excess stream tombstoned=%t", count, excessClosed)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if got := r.ActiveGoroutines(); got != 0 {
		t.Fatalf("relay left %d owned goroutines", got)
	}
}

func TestRelayGlobalSessionLimitAndCloseWaits(t *testing.T) {
	backend := func(ctx context.Context, conn net.Conn, credentialID string) error {
		<-ctx.Done()
		return nil
	}
	r, handler, _ := newRelayFixture(t, backend, nil, nil)
	credentials := make([]Credential, MaxSessionsPerRelay/MaxSessionsPerCredential+1)
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
	for i := 0; i < MaxSessionsPerRelay; i++ {
		capIndex := i / MaxSessionsPerCredential
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
	if got := r.ActiveGoroutines(); got != 0 {
		t.Fatalf("relay left %d owned goroutines after Close", got)
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

func newRelayFixture(t *testing.T, backend Backend, clock RelayClock, onAuth func(*http.Request)) (*Relay, http.Handler, string) {
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
		if got := r.ActiveGoroutines(); got != 0 {
			t.Errorf("relay left %d owned goroutines", got)
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
	if pageRec.Code != http.StatusOK || !validTokenText(bootstrap) {
		t.Fatalf("bridge returned %d without bootstrap token", pageRec.Code)
	}
	hello, _ := AppendFrame(nil, Frame{Type: FrameHello, Payload: AppendHelloPayload(nil)})
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

func waitForOutput(t *testing.T, session *transportSession) {
	t.Helper()
	timer := time.NewTimer(testWait)
	defer timer.Stop()
	for {
		session.mu.Lock()
		hasData := false
		for _, queued := range session.outQueue {
			if queued.typeID == FrameData {
				hasData = true
				break
			}
		}
		if hasData {
			session.mu.Unlock()
			return
		}
		wake := session.outSignal
		session.mu.Unlock()
		select {
		case <-wake:
		case <-timer.C:
			t.Fatal("backend produced no downlink data")
		}
	}
}
