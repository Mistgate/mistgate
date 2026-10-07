package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/agentlink"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
	"google.golang.org/protobuf/proto"
)

type agentLinkAttempt struct {
	conn *websocket.Conn
	hit  int64
	err  error
}

func TestAgentLinkSessionReconnectsAndResumesReliableSequence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	key := make([]byte, vault.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := store.NewID("nod_")
	token := "link-integration-enrollment-token"
	tokenHash := sha256.Sum256([]byte(token))
	now := time.Now().UTC()
	if _, err := st.CreateEnrollment(ctx, &store.NodeRow{ID: nodeID, Name: "Link node", Address: "example.com"}, "", tokenHash[:], "test", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `UPDATE node SET bandwidth_mbps = 100 WHERE id = ?`, nodeID); err != nil {
		t.Fatal(err)
	}
	desired := []statehash.Inbound{{Spec: plugin.InboundSpec{ID: "inb_link", Protocol: "fake", ProfileID: "prf_link", Version: 1, Enabled: true,
		Listen: plugin.Listen{Network: "udp", Port: 443}, TLS: plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: "example.com"}, Egress: "direct", Settings: []byte(`{}`)},
		Creds: []plugin.UserCred{{CredID: "crd_link", UserID: "usr_link", DeviceID: "dev_link", Data: []byte(`{"key":"value"}`)}}}}
	f, err := fleet.New(st, v, nil, fleet.Config{AgentSNI: testSNI, PanelAddr: "127.0.0.1:443", LinkServed: true, Desired: func(context.Context, string) ([]statehash.Inbound, error) {
		return desired, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	linkPrefix := "/" + strings.Repeat("l", 24) + "/"
	links := make(chan int64, 8)
	var linkHits atomic.Int64
	blockReconnect := atomic.Bool{}
	releaseReconnect := make(chan struct{})
	linkHandler := f.LinkHandler()
	root := http.NewServeMux()
	root.Handle("/", f.AgentHandler())
	root.HandleFunc(linkPrefix, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, linkPrefix+"link/") {
			http.NotFound(w, r)
			return
		}
		hit := linkHits.Add(1)
		select {
		case links <- hit:
		default:
		}
		if hit > 1 && blockReconnect.Load() {
			select {
			case <-releaseReconnect:
			case <-r.Context().Done():
				return
			}
		}
		r.URL.Path = strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(linkPrefix, "/"))
		linkHandler.ServeHTTP(w, r)
	})
	publicCert, publicPool := testLinkTLSCert(t)
	ts := httptest.NewUnstartedServer(root)
	ts.EnableHTTP2 = true
	ts.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		if hello.ServerName == testSNI {
			return f.AgentTLSConfig(hello)
		}
		return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{publicCert}, NextProtos: []string{"h2", "http/1.1"}}, nil
	}}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	host := strings.TrimPrefix(ts.URL, "https://")
	stateDir := filepath.Join(t.TempDir(), "state")
	meta, err := Enroll(ctx, EnrollConfig{StateDir: stateDir, Panel: host, SNI: testSNI, CASHA256: f.CAFingerprint(), Token: token})
	if err != nil {
		t.Fatal(err)
	}
	linkURL := "wss://" + host + linkPrefix
	eng := newFakeEngine("fake")
	a, err := New(Config{StateDir: stateDir, LinkURL: linkURL, DoctorEnv: testDoctorEnv(t)},
		map[string]engine.Factory{"fake": eng.factory()}, &fakeHost{})
	if err != nil {
		t.Fatal(err)
	}
	if a.meta.NodeID != meta.NodeID {
		t.Fatalf("agent node id %q differs from enrolled id %q", a.meta.NodeID, meta.NodeID)
	}
	a.linkHTTPClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: publicPool,
		ServerName: "127.0.0.1", NextProtos: []string{"http/1.1"}}}}
	a.backoffMin, a.backoffMax, a.statsEvery, a.sweepEvery = 20*time.Millisecond, 80*time.Millisecond, time.Hour, time.Hour
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- a.Run(runCtx) }()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("agent Run: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("agent did not stop")
		}
	})
	// Link requests may arrive only after the initial mTLS HelloAck advertises link support.
	if hit := waitLinkHit(t, links); hit != 1 {
		t.Fatalf("first signed link request number = %d", hit)
	}
	eventually(t, func() bool { return eng.has("inb_link") }, "desired state applied over WebSocket link")
	if got := eng.Observed(); len(got) != 1 || got[0].Spec.ID != "inb_link" {
		t.Fatalf("engine observed state = %+v", got)
	}

	eventually(t, func() bool { return agentFirstUnacked(a) == 0 }, "initial reliable event acknowledged")
	next, _ := a.out.hello()
	a.event(agentv1.Severity_SEVERITY_INFO, "link_first_event", "", nil)
	firstSeq := next
	eventually(t, func() bool {
		return linkEventCount(t, st, nodeID, "link_first_event") == 1 && agentFirstUnacked(a) == 0
	}, "reliable event acknowledged over link")

	blockReconnect.Store(true)
	current := a.cur.Load()
	if current == nil {
		t.Fatal("agent has no active WebSocket session")
	}
	current.cancel()
	eventually(t, func() bool { return a.cur.Load() == nil }, "link session disconnected")
	if hit := waitLinkHit(t, links); hit != 2 {
		t.Fatalf("reconnect link request number = %d", hit)
	}
	eventually(t, func() bool { return a.cur.Load() == nil }, "reconnect paused before WebSocket upgrade")
	next, _ = a.out.hello()
	a.event(agentv1.Severity_SEVERITY_INFO, "link_resumed_event", "", nil)
	resumeSeq := next
	if _, pending := a.out.hello(); pending != resumeSeq {
		t.Fatalf("pending seq before reconnect = %d, want %d", pending, resumeSeq)
	}
	close(releaseReconnect)
	eventually(t, func() bool {
		return linkEventCount(t, st, nodeID, "link_resumed_event") == 1 && agentFirstUnacked(a) == 0
	}, "reliable event resumed and acknowledged")
	if got := linkEventSeq(t, st, nodeID, "link_first_event"); got != firstSeq {
		t.Errorf("first reliable event seq = %d, want %d", got, firstSeq)
	}
	if got := linkEventSeq(t, st, nodeID, "link_resumed_event"); got != resumeSeq {
		t.Errorf("resumed reliable event seq = %d, want %d", got, resumeSeq)
	}
}

func TestAgentLinkHandshakeFailureFallsBackToMTLS(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true, cfg: func(c *Config) {
		c.LinkURL = "wss://example.com/" + strings.Repeat("x", 24) + "/"
	}})
	linkPrefix := "/" + strings.Repeat("p", 24) + "/"
	var linkHits atomic.Int64
	linkServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		linkHits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(linkServer.Close)
	h.a.cfg.LinkURL = "wss" + strings.TrimPrefix(linkServer.URL, "https") + linkPrefix
	h.a.linkHTTPClient = linkServer.Client()
	h.a.backoffMin, h.a.backoffMax = 10*time.Millisecond, 20*time.Millisecond
	h.a.linkAdvertised.Store(true) // the previous HelloAck advertised a link that is no longer served
	logSink := &lockedLogBuffer{}
	h.a.log = slog.New(slog.NewTextHandler(logSink, nil))
	h.start()

	select {
	case <-h.panel.connects:
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not reconnect over mTLS after the link handshake failed")
	}
	eventually(t, func() bool { return h.a.cur.Load() != nil && !h.a.linkAdvertised.Load() }, "active mTLS session after link failure")
	if linkHits.Load() != 1 {
		t.Fatalf("link handshake attempts = %d, want one before mTLS fallback", linkHits.Load())
	}
	logs := logSink.String()
	if !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "agent link failed; falling back to mTLS") {
		t.Fatalf("fallback warning missing: %s", logs)
	}
	if !strings.Contains(logs, `err="`) || !strings.Contains(logs, "404") {
		t.Fatalf("fallback warning lost the link dial error: %s", logs)
	}
	if strings.Contains(logs, linkPrefix) {
		t.Fatalf("agent log contains the link path prefix: %s", logs)
	}
}

func TestAgentLinkFailureHoldsMTLSSessionOnce(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true, cfg: func(c *Config) {
		c.LinkURL = "wss://example.com/" + strings.Repeat("x", 24) + "/"
	}})
	h.panel.linkSupported.Store(true)
	linkPrefix := "/" + strings.Repeat("w", 24) + "/"
	var linkHits atomic.Int64
	linkServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		linkHits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(linkServer.Close)
	h.a.cfg.LinkURL = "wss" + strings.TrimPrefix(linkServer.URL, "https") + linkPrefix
	h.a.linkHTTPClient = linkServer.Client()
	h.a.backoffMin, h.a.backoffMax = 10*time.Millisecond, 20*time.Millisecond
	h.start()

	deadline := time.NewTimer(2 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for h.a.cur.Load() == nil {
		if count := h.panel.connCount(); count > 2 {
			t.Fatalf("mTLS sessions during link fallback = %d, want the discovery session and one held session", count)
		}
		if hits := linkHits.Load(); hits > 1 {
			t.Fatalf("link attempts before an active mTLS session = %d, want at most one", hits)
		}
		select {
		case <-deadline.C:
			t.Fatal("agent did not establish an mTLS session after the failed advertised link")
		case <-ticker.C:
		}
	}
	active := h.a.cur.Load()
	if count := h.panel.connCount(); count != 2 {
		t.Fatalf("mTLS sessions after link fallback = %d, want 2", count)
	}
	if !h.a.linkAdvertised.Load() {
		t.Fatal("active mTLS HelloAck advertisement was cleared")
	}
	if hits := linkHits.Load(); hits != 1 {
		t.Fatalf("link attempts before the mTLS session ended = %d, want 1", hits)
	}

	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if h.a.cur.Load() != active {
		t.Fatal("active mTLS session did not stay connected")
	}
	if count := h.panel.connCount(); count != 2 {
		t.Fatalf("mTLS sessions while the held session was active = %d, want 2", count)
	}
	if hits := linkHits.Load(); hits != 1 {
		t.Fatalf("link attempts while the held mTLS session was active = %d, want 1", hits)
	}
}

func TestAgentEstablishedLinkDropReconnectsWithoutMTLS(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true, cfg: func(c *Config) {
		c.LinkURL = "wss://example.com/" + strings.Repeat("x", 24) + "/"
	}})
	h.panel.linkSupported.Store(true)
	linkPrefix := "/" + strings.Repeat("e", 24) + "/"
	linkReady := make(chan agentLinkAttempt, 4)
	var linkHits atomic.Int64
	linkServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit := linkHits.Add(1)
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			linkReady <- agentLinkAttempt{hit: hit, err: err}
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "")
		panelNonce := bytes.Repeat([]byte{7}, 32)
		if err := agentlink.WriteFrame(r.Context(), ws, &agentv1.LinkChallenge{Nonce: panelNonce, Audience: r.Host}); err != nil {
			linkReady <- agentLinkAttempt{hit: hit, err: err}
			return
		}
		typ, frame, err := agentlink.ReadFrame(r.Context(), ws)
		if err != nil || typ != websocket.MessageBinary {
			linkReady <- agentLinkAttempt{hit: hit, err: errors.New("agent did not send binary LinkAuth")}
			return
		}
		var auth agentv1.LinkAuth
		if err := proto.Unmarshal(frame, &auth); err != nil {
			linkReady <- agentLinkAttempt{hit: hit, err: err}
			return
		}
		panelSignature, err := agentlink.Sign(h.panel.caKey, agentlink.PanelDigest(auth.NodeId, r.Host, panelNonce, auth.Nonce))
		if err != nil {
			linkReady <- agentLinkAttempt{hit: hit, err: err}
			return
		}
		if err := agentlink.WriteFrame(r.Context(), ws, &agentv1.LinkAccept{Signature: panelSignature}); err != nil {
			linkReady <- agentLinkAttempt{hit: hit, err: err}
			return
		}
		typ, frame, err = agentlink.ReadFrame(r.Context(), ws)
		if err != nil || typ != websocket.MessageBinary {
			linkReady <- agentLinkAttempt{hit: hit, err: errors.New("agent did not send binary Hello")}
			return
		}
		var hello agentv1.ConnectRequest
		if err := proto.Unmarshal(frame, &hello); err != nil || hello.GetHello() == nil {
			linkReady <- agentLinkAttempt{hit: hit, err: errors.New("agent did not send Hello")}
			return
		}
		if err := agentlink.WriteFrame(r.Context(), ws, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_HelloAck{
			HelloAck: &agentv1.HelloAck{LinkSupported: true},
		}}); err != nil {
			linkReady <- agentLinkAttempt{hit: hit, err: err}
			return
		}
		linkReady <- agentLinkAttempt{conn: ws, hit: hit}
		for {
			if _, _, err := agentlink.ReadFrame(r.Context(), ws); err != nil {
				return
			}
		}
	}))
	t.Cleanup(linkServer.Close)
	h.a.cfg.LinkURL = "wss" + strings.TrimPrefix(linkServer.URL, "https") + linkPrefix
	h.a.linkHTTPClient = linkServer.Client()
	h.a.backoffMin, h.a.backoffMax = 10*time.Millisecond, 20*time.Millisecond
	h.start()

	first := waitForLinkAttempt(t, linkReady)
	if first.hit != 1 {
		t.Fatalf("first link attempt = %d, want 1", first.hit)
	}
	eventually(t, func() bool { return h.a.cur.Load() != nil }, "active established link session")
	mtlsCount := h.panel.connCount()
	if mtlsCount != 1 {
		t.Fatalf("initial mTLS discovery sessions = %d, want 1", mtlsCount)
	}
	if err := first.conn.Close(websocket.StatusCode(4000), "superseded"); err != nil {
		t.Fatalf("close established link: %v", err)
	}

	second := waitForLinkAttempt(t, linkReady)
	if second.hit != 2 {
		t.Fatalf("reconnected link attempt = %d, want 2", second.hit)
	}
	eventually(t, func() bool { return h.a.cur.Load() != nil }, "reconnected established link session")
	if got := h.panel.connCount(); got != mtlsCount {
		t.Fatalf("mTLS sessions after established link dropped = %d, want unchanged count %d", got, mtlsCount)
	}
}

// holdLinkServer is a fake link endpoint for the hold-expiry tests: the first `fail` requests get a 404 (the link is
// unreachable before it is established), every later one completes the signed handshake and is held open as an
// established link. It records the time of each request.
type holdLinkServer struct {
	srv  *httptest.Server
	mu   sync.Mutex
	at   []time.Time
	fail int
}

func newHoldLinkServer(t *testing.T, h *harness, fail int) *holdLinkServer {
	t.Helper()
	s := &holdLinkServer{fail: fail}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.at = append(s.at, time.Now())
		n := len(s.at)
		s.mu.Unlock()
		if n <= s.fail {
			http.NotFound(w, r)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "")
		panelNonce := bytes.Repeat([]byte{7}, 32)
		if agentlink.WriteFrame(r.Context(), ws, &agentv1.LinkChallenge{Nonce: panelNonce, Audience: r.Host}) != nil {
			return
		}
		_, frame, err := agentlink.ReadFrame(r.Context(), ws)
		var auth agentv1.LinkAuth
		if err != nil || proto.Unmarshal(frame, &auth) != nil {
			return
		}
		sig, err := agentlink.Sign(h.panel.caKey, agentlink.PanelDigest(auth.NodeId, r.Host, panelNonce, auth.Nonce))
		if err != nil || agentlink.WriteFrame(r.Context(), ws, &agentv1.LinkAccept{Signature: sig}) != nil {
			return
		}
		if _, _, err := agentlink.ReadFrame(r.Context(), ws); err != nil { // Hello
			return
		}
		if agentlink.WriteFrame(r.Context(), ws, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_HelloAck{
			HelloAck: &agentv1.HelloAck{LinkSupported: true},
		}}) != nil {
			return
		}
		for {
			if _, _, err := agentlink.ReadFrame(r.Context(), ws); err != nil {
				return
			}
		}
	}))
	t.Cleanup(s.srv.Close)
	h.a.cfg.LinkURL = "wss" + strings.TrimPrefix(s.srv.URL, "https") + "/" + strings.Repeat("h", 24) + "/"
	h.a.linkHTTPClient = s.srv.Client()
	return s
}

func (s *holdLinkServer) attempts() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.at...)
}

func sessionOnLink(a *Agent) bool {
	s := a.cur.Load()
	if s == nil {
		return false
	}
	_, ok := s.stream.(*websocketAgentTransport)
	return ok
}

func sessionOnMTLS(a *Agent) bool {
	s := a.cur.Load()
	if s == nil {
		return false
	}
	_, ok := s.stream.(*connectAgentTransport)
	return ok
}

// A link attempt that fails before it is established holds the next mTLS session; once the hold period is over
// that session ends on its own, the agent dials the link again without a backoff wait and, this time, stays on it.
func TestAgentHeldMTLSSessionEndsAfterHoldAndRetriesLink(t *testing.T) {
	const hold = 300 * time.Millisecond
	h := newHarness(t, harnessOpts{noStart: true, cfg: func(c *Config) {
		c.LinkURL = "wss://example.com/" + strings.Repeat("x", 24) + "/"
	}})
	h.panel.linkSupported.Store(true)
	ls := newHoldLinkServer(t, h, 1)
	h.a.backoffMin, h.a.backoffMax = 5*time.Second, 5*time.Second // a backoff wait anywhere would show as a timeout
	h.a.linkHoldFor = hold
	h.start()

	eventually(t, func() bool { return len(ls.attempts()) == 1 && sessionOnMTLS(h.a) }, "held mTLS session after the failed link attempt")
	if got := h.panel.connCount(); got != 2 {
		t.Fatalf("mTLS sessions while holding = %d, want the discovery session and one held session", got)
	}
	eventually(t, func() bool { return sessionOnLink(h.a) }, "link session after the hold period")
	at := ls.attempts()
	if len(at) != 2 {
		t.Fatalf("link attempts = %d, want 2 (one failed, one established)", len(at))
	}
	if gap := at[1].Sub(at[0]); gap < hold {
		t.Fatalf("link retried %v after the failure, before the %v hold period was over", gap, hold)
	}
	time.Sleep(2 * hold)
	if !sessionOnLink(h.a) || len(ls.attempts()) != 2 || h.panel.connCount() != 2 {
		t.Fatalf("agent left the established link: onLink=%v attempts=%d mtls=%d", sessionOnLink(h.a), len(ls.attempts()), h.panel.connCount())
	}
}

// A link that keeps failing is tried at most once per hold period, and an mTLS session is up between the attempts.
func TestAgentFailingLinkIsRetriedOncePerHoldPeriod(t *testing.T) {
	const hold = 200 * time.Millisecond
	h := newHarness(t, harnessOpts{noStart: true, cfg: func(c *Config) {
		c.LinkURL = "wss://example.com/" + strings.Repeat("x", 24) + "/"
	}})
	h.panel.linkSupported.Store(true)
	ls := newHoldLinkServer(t, h, 1<<30)
	h.a.linkHoldFor = hold
	h.start()

	var mu sync.Mutex
	var mtls []time.Time // when an mTLS session was seen active
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
			if sessionOnMTLS(h.a) {
				mu.Lock()
				mtls = append(mtls, time.Now())
				mu.Unlock()
			}
		}
	}()

	const want = 5
	eventually(t, func() bool { return len(ls.attempts()) >= want }, "repeated link attempts")
	at := ls.attempts()[:want]
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < want; i++ {
		if gap := at[i].Sub(at[i-1]); gap < hold {
			t.Errorf("link attempts %d and %d are %v apart, want at least the %v hold period", i, i+1, gap, hold)
		}
		active := false
		for _, m := range mtls {
			if m.After(at[i-1]) && m.Before(at[i]) {
				active = true
				break
			}
		}
		if !active {
			t.Errorf("no active mTLS session between link attempts %d and %d", i, i+1)
		}
	}
	conns := h.panel.connCount() // read before the attempts: both only grow
	if n := len(ls.attempts()); conns > n+1 {
		t.Errorf("mTLS sessions = %d for %d link attempts, want at most one discovery and one held session per attempt", conns, n)
	}
}

func waitForLinkAttempt(t *testing.T, attempts <-chan agentLinkAttempt) agentLinkAttempt {
	t.Helper()
	select {
	case attempt := <-attempts:
		if attempt.err != nil {
			t.Fatalf("link handshake failed: %v", attempt.err)
		}
		return attempt
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for link session")
		return agentLinkAttempt{}
	}
}

func TestAgentSwitchesToAdvertisedLinkWithoutBackoff(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true, cfg: func(c *Config) {
		c.LinkURL = "wss://example.com/" + strings.Repeat("x", 24) + "/"
	}})
	h.panel.linkSupported.Store(true)
	linkPrefix := "/" + strings.Repeat("q", 24) + "/"
	linkReady := make(chan struct{}, 1)
	linkErrors := make(chan error, 1)
	linkServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			linkErrors <- err
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "")
		panelNonce := bytes.Repeat([]byte{7}, 32)
		if err := agentlink.WriteFrame(r.Context(), ws, &agentv1.LinkChallenge{Nonce: panelNonce, Audience: r.Host}); err != nil {
			linkErrors <- err
			return
		}
		typ, frame, err := agentlink.ReadFrame(r.Context(), ws)
		if err != nil || typ != websocket.MessageBinary {
			linkErrors <- errors.New("agent did not send binary LinkAuth")
			return
		}
		var auth agentv1.LinkAuth
		if err := proto.Unmarshal(frame, &auth); err != nil {
			linkErrors <- err
			return
		}
		panelSignature, err := agentlink.Sign(h.panel.caKey, agentlink.PanelDigest(auth.NodeId, r.Host, panelNonce, auth.Nonce))
		if err != nil {
			linkErrors <- err
			return
		}
		if err := agentlink.WriteFrame(r.Context(), ws, &agentv1.LinkAccept{Signature: panelSignature}); err != nil {
			linkErrors <- err
			return
		}
		typ, frame, err = agentlink.ReadFrame(r.Context(), ws)
		if err != nil || typ != websocket.MessageBinary {
			linkErrors <- errors.New("agent did not send binary Hello")
			return
		}
		var hello agentv1.ConnectRequest
		if err := proto.Unmarshal(frame, &hello); err != nil || hello.GetHello() == nil {
			linkErrors <- errors.New("agent did not send Hello")
			return
		}
		if err := agentlink.WriteFrame(r.Context(), ws, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_HelloAck{
			HelloAck: &agentv1.HelloAck{LinkSupported: true},
		}}); err != nil {
			linkErrors <- err
			return
		}
		linkReady <- struct{}{}
		for {
			if _, _, err := agentlink.ReadFrame(r.Context(), ws); err != nil {
				return
			}
		}
	}))
	t.Cleanup(linkServer.Close)
	h.a.cfg.LinkURL = "wss" + strings.TrimPrefix(linkServer.URL, "https") + linkPrefix
	h.a.linkHTTPClient = linkServer.Client()
	h.a.backoffMin, h.a.backoffMax = 5*time.Second, 5*time.Second
	h.start()
	h.waitConnected() // the mTLS HelloAck advertises link support
	select {
	case <-linkReady:
	case err := <-linkErrors:
		t.Fatalf("link handshake failed: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("agent waited for reconnect backoff before switching to the advertised link")
	}
	eventually(t, func() bool { return h.a.cur.Load() != nil }, "active link session")
}

func TestAgentRejectsLinkChallengeWithWrongAudience(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	assertAgentLinkRefusal(t, h.a, func(ws *websocket.Conn, host string) error {
		return agentlink.WriteFrame(context.Background(), ws, &agentv1.LinkChallenge{Nonce: bytes.Repeat([]byte{7}, 32), Audience: host + ".wrong"})
	}, false)
}

func TestAgentRejectsTextFrameDuringLinkHandshake(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	assertAgentLinkRefusal(t, h.a, func(ws *websocket.Conn, _ string) error {
		return ws.Write(context.Background(), websocket.MessageText, []byte("not a protobuf frame"))
	}, false)
}

func TestAgentRejectsLinkAcceptFromDifferentPanelCA(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	otherCA, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	assertAgentLinkRefusal(t, h.a, func(ws *websocket.Conn, host string) error {
		panelNonce := bytes.Repeat([]byte{9}, 32)
		if err := agentlink.WriteFrame(context.Background(), ws, &agentv1.LinkChallenge{Nonce: panelNonce, Audience: host}); err != nil {
			return err
		}
		typ, frame, err := agentlink.ReadFrame(context.Background(), ws)
		if err != nil || typ != websocket.MessageBinary {
			return errors.New("agent did not send binary LinkAuth")
		}
		var auth agentv1.LinkAuth
		if err := proto.Unmarshal(frame, &auth); err != nil || auth.NodeId != h.a.meta.NodeID || len(auth.Nonce) != 32 {
			return errors.New("agent sent invalid LinkAuth")
		}
		sig, err := agentlink.Sign(otherCA, agentlink.PanelDigest(auth.NodeId, host, panelNonce, auth.Nonce))
		if err != nil {
			return err
		}
		return agentlink.WriteFrame(context.Background(), ws, &agentv1.LinkAccept{Signature: sig})
	}, true)
}

func assertAgentLinkRefusal(t *testing.T, a *Agent, sendChallenge func(*websocket.Conn, string) error, expectAuth bool) {
	t.Helper()
	type handlerResult struct {
		status websocket.StatusCode
		err    error
	}
	result := make(chan handlerResult, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			result <- handlerResult{err: err}
			return
		}
		defer ws.Close(websocket.StatusNormalClosure, "")
		err = sendChallenge(ws, r.Host)
		if err == nil && expectAuth {
			_, _, err = agentlink.ReadFrame(r.Context(), ws)
			if err != nil {
				result <- handlerResult{status: websocket.CloseStatus(err), err: err}
				return
			}
		}
		_, _, closeErr := ws.Read(r.Context())
		result <- handlerResult{status: websocket.CloseStatus(closeErr), err: err}
	}))
	defer server.Close()
	base := "wss://" + strings.TrimPrefix(server.URL, "https://") + "/" + strings.Repeat("a", 24) + "/"
	a.cfg.LinkURL = base
	a.linkHTTPClient = server.Client()
	if stream, cleanup, err := a.dialLink(context.Background()); err == nil {
		if stream != nil {
			_ = stream.Close()
		}
		if cleanup != nil {
			cleanup()
		}
		t.Fatal("agent accepted the untrusted link handshake")
	}
	select {
	case got := <-result:
		if got.status != websocket.StatusPolicyViolation {
			t.Fatalf("panel observed close status %v, err=%v (handler err=%v)", got.status, got.err, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("fake panel did not observe the agent close")
	}
}

func agentFirstUnacked(a *Agent) uint64 {
	_, first := a.out.hello()
	return first
}

func waitLinkHit(t *testing.T, links <-chan int64) int64 {
	t.Helper()
	select {
	case hit := <-links:
		return hit
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a link request")
		return 0
	}
}

func linkEventCount(t *testing.T, st *store.Store, nodeID, code string) int {
	t.Helper()
	var count int
	if err := st.R.QueryRowContext(context.Background(), `SELECT count(*) FROM event WHERE node_id = ? AND code = ?`, nodeID, code).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func linkEventSeq(t *testing.T, st *store.Store, nodeID, code string) uint64 {
	t.Helper()
	var seq uint64
	if err := st.R.QueryRowContext(context.Background(), `SELECT src_seq FROM event WHERE node_id = ? AND code = ?`, nodeID, code).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

type lockedLogBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func testLinkTLSCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "link test listener"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: cert}, pool
}
