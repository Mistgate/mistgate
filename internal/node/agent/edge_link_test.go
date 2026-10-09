package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
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

// The edge edition's agent link with TODAY's agent: the real agent talks WebSocket to nodeLinkEmu (the Worker and the
// NodeLink objects in Go), which drives the real Fleet.Link on SQLite. Design: design/cloudflare-edition/AGENT-LINK.md §6.3.
// The agent enrols over the link route (step 5c): it is link-only, and renews over its session.

const edgeNodeName = "node-a"

type edgeOpts struct {
	liveness   int           // node.liveness_timeout_s, 0 = the default
	statsEvery time.Duration // 0 = 20 ms
	backoff    time.Duration // the reconnect delay (min = max), 0 = 20 ms
	noEnroll   bool          // the test enrols the agent itself
}

type edgeRig struct {
	t        *testing.T
	o        edgeOpts
	st       *store.Store
	f        *fleet.Fleet
	em       *nodeLinkEmu
	ts       *httptest.Server
	pool     *x509.CertPool
	host     string
	prefix   string
	nodeID   string
	stateDir string
	logs     *lockedLogBuffer

	mu    sync.Mutex
	creds []string // credential ids of the one inbound; the first is the seeded one (it is the one that carries traffic)
}

func newEdgeRig(t *testing.T, o edgeOpts) *edgeRig {
	t.Helper()
	ctx := context.Background()
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
	r := &edgeRig{t: t, o: o, st: st, logs: &lockedLogBuffer{}, creds: []string{"crd_alice"},
		prefix: "/" + strings.Repeat("e", 24) + "/", nodeID: store.NewID("nod_")}
	r.em = newNodeLinkEmu(t)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("agent log:\n%s", r.logs.String())
			t.Logf("steps: %+v", r.em.steps(r.nodeID))
		}
	})
	r.f, err = fleet.New(st, v, nil, fleet.Config{AgentSNI: testSNI, PanelAddr: "127.0.0.1:443", LinkServed: true,
		Remote: r.em, Now: r.em.now, Desired: r.desired, Log: slog.New(slog.NewTextHandler(r.logs, nil)).With("side", "panel")})
	if err != nil {
		t.Fatal(err)
	}
	r.em.bind(r.f)
	r.seed()

	publicCert, pool := testLinkTLSCert(t)
	r.pool = pool
	// The Worker under the secret prefix. The edge has no mTLS endpoint: anything outside the prefix is a 404.
	root := http.NewServeMux()
	root.Handle(r.prefix, r.em.Worker(r.prefix))
	ts := httptest.NewUnstartedServer(root)
	ts.EnableHTTP2 = true
	ts.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{publicCert}, NextProtos: []string{"h2", "http/1.1"}}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	r.ts = ts
	r.host = strings.TrimPrefix(ts.URL, "https://")

	r.stateDir = filepath.Join(t.TempDir(), "state")
	if !o.noEnroll {
		if _, err := r.enrol(r.f.CAFingerprint()); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func (r *edgeRig) linkURL() string { return "wss://" + r.host + r.prefix }

// publicClient is a client of the Worker's public TLS: the test listener's certificate is its only root.
func (r *edgeRig) publicClient() *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: r.pool, ServerName: "127.0.0.1"}}}
}

// enrol is `mistgate-node enroll --link-url …` with the seeded one-time token.
func (r *edgeRig) enrol(pin string) (Meta, error) {
	return Enroll(context.Background(), EnrollConfig{StateDir: r.stateDir, LinkURL: r.linkURL(), CASHA256: pin,
		Token: "edge-link-enrollment-token", httpClient: r.publicClient()})
}

// seed creates the node (one enrolment), one inbound and the user whose credential carries the traffic.
func (r *edgeRig) seed() {
	r.t.Helper()
	ctx := context.Background()
	tokenHash := sha256.Sum256([]byte("edge-link-enrollment-token"))
	now := time.Now().UTC()
	if _, err := r.st.CreateEnrollment(ctx, &store.NodeRow{ID: r.nodeID, Name: edgeNodeName, Address: "de1.example.com"}, "", tokenHash[:], "test", now, now.Add(time.Hour)); err != nil {
		r.t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		r.t.Helper()
		if _, err := r.st.W.ExecContext(ctx, q, args...); err != nil {
			r.t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`UPDATE node SET bandwidth_mbps = 100 WHERE id = ?`, r.nodeID) // no automatic bandwidth test
	if r.o.liveness != 0 {
		exec(`UPDATE node SET liveness_timeout_s = ? WHERE id = ?`, r.o.liveness, r.nodeID)
	}
	unix := now.Unix()
	exec(`INSERT INTO user_group (id, name, created_at) VALUES ('grp_edge', 'g1', ?)`, unix)
	exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at) VALUES ('prf_edge', 'fake', 'p1', '{}', 1, ?, ?)`, unix, unix)
	exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, created_at, updated_at) VALUES ('inb_edge', 'prf_edge', ?, 1, ?, ?)`, r.nodeID, unix, unix)
	h := sha256.Sum256([]byte("alice"))
	exec(`INSERT INTO user (id, name, group_id, disabled, status, app_happ, app_amnezia, all_nodes, quota_bytes, period_start,
		expires_at, speed_limit_bps, sub_token_hash, sub_token_enc, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"usr_alice", "alice", "grp_edge", 0, "active", 1, 1, 1, 0, unix, nil, 0, h[:], []byte("x"), unix)
	exec(`INSERT INTO device (id, user_id, first_seen_at, last_seen_at, created_at, platform, model) VALUES (?,?,?,?,?,?,?)`,
		"dev_alice", "usr_alice", unix, unix, unix, "ios", "iPhone")
	exec(`INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at) VALUES (?,?,?,?,?,?,?)`,
		"crd_alice", "dev_alice", "usr_alice", "fake", []byte("x"), `{"key":"value"}`, unix)
}

// desired is Config.Desired: one inbound with the current credentials.
func (r *edgeRig) desired(context.Context, string) ([]statehash.Inbound, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	creds := make([]plugin.UserCred, 0, len(r.creds))
	for _, id := range r.creds {
		who := strings.TrimPrefix(id, "crd_")
		creds = append(creds, plugin.UserCred{CredID: id, UserID: "usr_" + who, DeviceID: "dev_" + who, Data: []byte(`{"key":"value"}`)})
	}
	return []statehash.Inbound{{Spec: plugin.InboundSpec{ID: "inb_edge", Protocol: "fake", ProfileID: "prf_edge", Version: 1, Enabled: true,
		Listen: plugin.Listen{Network: "udp", Port: 443}, TLS: plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: "de1.example.com"},
		Egress: "direct", Settings: []byte(`{}`)}, Creds: creds}}, nil
}

func (r *edgeRig) setCreds(ids ...string) {
	r.mu.Lock()
	r.creds = ids
	r.mu.Unlock()
}

// poke is what step 6 does through waitUntil when desired state changes.
func (r *edgeRig) poke() {
	r.t.Helper()
	if err := r.em.poke(context.Background(), r.nodeID); err != nil {
		r.t.Fatal(err)
	}
}

type edgeAgent struct {
	a        *Agent
	eng      *fakeEngine
	finished chan struct{}
	err      error // Run's result, valid once finished is closed
	stop     func() error
}

// start runs the real agent against the emulator. It enrolled over the link, so it is link-only: it dials the link at
// once, never mTLS, whatever the panel advertises.
func (r *edgeRig) start(tune func(*Agent)) *edgeAgent {
	r.t.Helper()
	eng := newFakeEngine("fake")
	a, err := New(Config{StateDir: r.stateDir, DoctorEnv: testDoctorEnv(r.t),
		Log: slog.New(slog.NewTextHandler(r.logs, nil))}, map[string]engine.Factory{"fake": eng.factory()}, &fakeHost{})
	if err != nil {
		r.t.Fatal(err)
	}
	a.linkHTTPClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: r.pool,
		ServerName: "127.0.0.1", NextProtos: []string{"http/1.1"}}}}
	backoff, stats := r.o.backoff, r.o.statsEvery
	if backoff == 0 {
		backoff = 20 * time.Millisecond
	}
	if stats == 0 {
		stats = 20 * time.Millisecond
	}
	a.backoffMin, a.backoffMax, a.statsEvery, a.sweepEvery = backoff, backoff, stats, time.Hour
	if tune != nil {
		tune(a)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ea := &edgeAgent{a: a, eng: eng, finished: make(chan struct{})}
	go func() { ea.err = a.Run(ctx); close(ea.finished) }()
	var once sync.Once
	var stopped error // Run's result; nil when Run did not return in time (ea.err is then still being written)
	ea.stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case <-ea.finished:
				stopped = ea.err
			case <-time.After(10 * time.Second):
				r.t.Error("agent did not stop")
			}
		})
		return stopped
	}
	r.t.Cleanup(func() {
		if err := ea.stop(); err != nil && !errors.Is(err, ErrRetired) {
			r.t.Errorf("agent Run: %v", err)
		}
	})
	return ea
}

// What the panel's database says.

func (r *edgeRig) liveRow() (session, sampleAt int64, ok bool) {
	r.t.Helper()
	err := r.st.R.QueryRowContext(context.Background(), `SELECT session, sample_at FROM node_live WHERE node_id = ?`, r.nodeID).Scan(&session, &sampleAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false
	}
	if err != nil {
		r.t.Errorf("node_live: %v", err)
	}
	return session, sampleAt, err == nil
}

func (r *edgeRig) session() int64 {
	s, _, _ := r.liveRow()
	return s
}

func (r *edgeRig) applied() (revision uint64, hash string) {
	r.t.Helper()
	if err := r.st.R.QueryRowContext(context.Background(), `SELECT applied_revision, applied_hash FROM node WHERE id = ?`, r.nodeID).Scan(&revision, &hash); err != nil {
		r.t.Errorf("node: %v", err)
	}
	return revision, hash
}

func (r *edgeRig) traffic() (up, down uint64) {
	r.t.Helper()
	if err := r.st.R.QueryRowContext(context.Background(),
		`SELECT coalesce(sum(bytes_up), 0), coalesce(sum(bytes_down), 0) FROM traffic_bucket WHERE node_id = ?`, r.nodeID).Scan(&up, &down); err != nil {
		r.t.Errorf("traffic_bucket: %v", err)
	}
	return up, down
}

func (r *edgeRig) inboundState() string {
	r.t.Helper()
	var s string
	if err := r.st.R.QueryRowContext(context.Background(), `SELECT state FROM inbound WHERE id = 'inb_edge'`).Scan(&s); err != nil {
		r.t.Errorf("inbound: %v", err)
	}
	return s
}

// desiredFrames are the DesiredState messages the emulator sent to the agent in one generation.
func (r *edgeRig) desiredFrames(gen uint64) []*agentv1.DesiredState {
	var out []*agentv1.DesiredState
	for _, s := range r.em.sent(r.nodeID) {
		if s.Gen == gen && s.Frame.GetDesiredState() != nil {
			out = append(out, s.Frame.GetDesiredState())
		}
	}
	return out
}

func (r *edgeRig) admin() adminv1connect.NodeServiceClient {
	r.t.Helper()
	path, h := r.f.NodeHandler()
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	r.t.Cleanup(srv.Close)
	return adminv1connect.NewNodeServiceClient(srv.Client(), srv.URL)
}

func (r *edgeRig) retire() *adminv1.RetireNodeResponse {
	r.t.Helper()
	resp, err := r.admin().RetireNode(context.Background(), connect.NewRequest(&adminv1.RetireNodeRequest{NodeId: r.nodeID, ConfirmName: edgeNodeName}))
	if err != nil {
		r.t.Fatalf("RetireNode: %v", err)
	}
	return resp.Msg
}

func stepIs(gen uint64, kind fleet.LinkKind, msg string) func(emuStep) bool {
	return func(s emuStep) bool { return s.Gen == gen && s.Kind == kind && (msg == "" || s.Msg == msg) }
}

func countSteps(steps []emuStep, pred func(emuStep) bool) int {
	n := 0
	for _, s := range steps {
		if pred(s) {
			n++
		}
	}
	return n
}

func noFailedSteps(t *testing.T, em *nodeLinkEmu, nodeID string) {
	t.Helper()
	for _, s := range em.steps(nodeID) {
		if s.Err {
			t.Errorf("a step failed: %+v", s)
		}
	}
}

// ---------------------------------------------------------------------------------------------------------------------
// A raw link client: the agent's side of the handshake, signed with a key the test holds.

type rawLink struct {
	t  *testing.T
	ws *websocket.Conn
}

func (r *edgeRig) agentKey() *ecdsa.PrivateKey {
	r.t.Helper()
	id, err := loadIdentity(r.stateDir)
	if err != nil {
		r.t.Fatal(err)
	}
	key, ok := id.cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		r.t.Fatal("agent key is not ECDSA")
	}
	return key
}

// dialChallenge connects and reads the panel's challenge.
func (r *edgeRig) dialChallenge() (*websocket.Conn, *agentv1.LinkChallenge) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: r.pool, ServerName: "127.0.0.1", NextProtos: []string{"http/1.1"}}}}
	r.t.Cleanup(client.CloseIdleConnections)
	ws, _, err := websocket.Dial(ctx, "wss://"+r.host+r.prefix+"link/"+r.nodeID, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		r.t.Fatalf("raw dial: %v", err)
	}
	ws.SetReadLimit(agentlink.MaxFrameSize)
	r.t.Cleanup(func() { _ = ws.CloseNow() })
	typ, frame, err := agentlink.ReadFrame(ctx, ws)
	var challenge agentv1.LinkChallenge
	if err != nil || typ != websocket.MessageBinary || proto.Unmarshal(frame, &challenge) != nil || len(challenge.Nonce) != 32 {
		r.t.Fatalf("raw challenge: %v", err)
	}
	return ws, &challenge
}

// sendAuth answers the challenge with a LinkAuth signed by key.
func (r *edgeRig) sendAuth(ws *websocket.Conn, challenge *agentv1.LinkChallenge, key *ecdsa.PrivateKey) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		r.t.Fatal(err)
	}
	sig, err := agentlink.Sign(key, agentlink.AgentDigest(r.nodeID, challenge.Audience, challenge.Nonce))
	if err != nil {
		r.t.Fatal(err)
	}
	if err := agentlink.WriteFrame(ctx, ws, &agentv1.LinkAuth{NodeId: r.nodeID, Nonce: nonce, Signature: sig}); err != nil {
		r.t.Fatalf("raw LinkAuth: %v", err)
	}
}

// dialRaw connects, answers the challenge with key and returns once LinkAccept came.
func (r *edgeRig) dialRaw(key *ecdsa.PrivateKey) *rawLink {
	r.t.Helper()
	ws, challenge := r.dialChallenge()
	r.sendAuth(ws, challenge, key)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	typ, frame, err := agentlink.ReadFrame(ctx, ws)
	var accepted agentv1.LinkAccept
	if err != nil || typ != websocket.MessageBinary || proto.Unmarshal(frame, &accepted) != nil || len(accepted.Signature) == 0 {
		r.t.Fatalf("raw LinkAccept: %v", err)
	}
	return &rawLink{t: r.t, ws: ws}
}

func (c *rawLink) send(msg *agentv1.ConnectRequest) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := agentlink.WriteFrame(ctx, c.ws, msg); err != nil {
		c.t.Fatalf("raw send: %v", err)
	}
}

// next reads one frame; the error is the close the server sent, if any.
func (c *rawLink) next() (*agentv1.ConnectResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	typ, b, err := agentlink.ReadFrame(ctx, c.ws)
	if err != nil {
		return nil, err
	}
	var m agentv1.ConnectResponse
	if typ != websocket.MessageBinary || proto.Unmarshal(b, &m) != nil {
		c.t.Fatalf("raw: not a ConnectResponse frame")
	}
	return &m, nil
}

// until reads frames until pred matches one.
func (c *rawLink) until(pred func(*agentv1.ConnectResponse) bool) *agentv1.ConnectResponse {
	c.t.Helper()
	for {
		m, err := c.next()
		if err != nil {
			c.t.Fatalf("raw read: %v", err)
		}
		if pred(m) {
			return m
		}
	}
}

func (c *rawLink) hello(instance string) {
	c.t.Helper()
	c.send(&agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{
		AgentVersion: "0.0.1", ApiVersion: fleet.APIVersion, InstanceId: instance, NextSeq: 1,
		Engines: []*agentv1.EngineInfo{{Protocol: "fake", Version: "v1"}},
		Facts:   &agentv1.HostFacts{Hostname: "de1", Os: "linux", CpuCount: 2, BootUnix: 1000},
	}}})
	c.until(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
}

// ---------------------------------------------------------------------------------------------------------------------
// Scenarios (design §6.3)

// 1. Enrol, Hello, desired state, stats, and an admin request answered through Replies.
func TestEdgeLinkSessionDesiredStatsAndAsk(t *testing.T) {
	r := newEdgeRig(t, edgeOpts{})
	ag := r.start(nil)
	eventually(t, func() bool { return ag.eng.has("inb_edge") && r.inboundState() == "active" }, "desired state applied over the emulated link")
	eventually(t, func() bool { return r.session() == 1 }, "node_live.session is the generation of the first session")

	sent := r.em.sent(r.nodeID)
	if len(sent) < 2 || sent[0].Frame.GetHelloAck() == nil || sent[1].Frame.GetDesiredState() == nil {
		t.Fatalf("the first frames to the agent = %+v, want HelloAck then DesiredState", sent)
	}
	first := sent[1].Frame.GetDesiredState()
	if first.BaseRevision != 0 || len(first.Inbounds) != 1 || sent[0].Gen != 1 {
		t.Errorf("first desired state = %+v (gen %d), want a full state of generation 1", first, sent[0].Gen)
	}
	eventually(t, func() bool { rev, hash := r.applied(); return rev == first.Revision && hash == first.StateHash }, "the applied revision and hash are stored")

	ag.eng.setEmit(true)
	eventually(t, func() bool { _, sample, _ := r.liveRow(); up, _ := r.traffic(); return sample > 0 && up > 0 }, "a host sample and traffic rows from stats")

	// The doctor report travels as a frame step; its EffectReply is the call's reply.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	report, err := r.f.RunDoctor(ctx, r.nodeID, nil, 10*time.Second)
	if err != nil || report == nil || report.RequestId == "" || len(report.Results) == 0 {
		t.Fatalf("RunDoctor = %v, %v", report, err)
	}
	asked := false
	for _, s := range r.em.sent(r.nodeID) {
		if rd := s.Frame.GetRunDoctor(); rd != nil && rd.RequestId == report.RequestId {
			asked = true
		}
	}
	if !asked {
		t.Errorf("no RunDoctor frame with request id %q reached the agent", report.RequestId)
	}

	ag.eng.setEmit(false)
	up, down := ag.eng.emitted()
	eventually(t, func() bool { u, d := r.traffic(); return u == up && d == down && up > 0 }, "every emitted byte counted")

	steps := r.em.steps(r.nodeID)
	if len(steps) < 3 || steps[0] != (emuStep{Gen: 1, Kind: fleet.LinkOpen}) || steps[1] != (emuStep{Gen: 1, Kind: fleet.LinkFrame, Msg: "hello"}) {
		t.Fatalf("first steps = %+v, want open then the Hello frame", steps)
	}
	for _, want := range []func(emuStep) bool{stepIs(1, fleet.LinkFrame, "apply_result"), stepIs(1, fleet.LinkFrame, "stats"),
		stepIs(1, fleet.LinkRequest, "run_doctor"), stepIs(1, fleet.LinkFrame, "doctor_report")} {
		if countSteps(steps, want) == 0 {
			t.Errorf("a step is missing from the log: %+v", steps)
		}
	}
	if n := countSteps(steps, func(s emuStep) bool { return s.Kind == fleet.LinkClosed }); n != 0 {
		t.Errorf("the session was closed %d times", n)
	}
	noFailedSteps(t, r.em, r.nodeID)
	if st := r.em.storage(r.nodeID); st.Gen != 1 || st.Live != 1 || !st.HasState || st.Sockets != 1 {
		t.Errorf("storage = %+v", st)
	}
}

// 2. A reconnect continues with deltas. A step that fails after its database writes committed (the real way to a stale
// state: on workerd no state write means no frames) ends in one full resend after the reconnect, and so does a step whose
// state alone was lost (a harsher case the platform cannot produce). The agent converges each time.
func TestEdgeLinkReconnectDeltaBaseAndStaleState(t *testing.T) {
	// A quiet agent (no stats), so that the alarm the object armed is its liveness deadline and nothing else.
	r := newEdgeRig(t, edgeOpts{backoff: time.Second, statsEvery: time.Hour})
	ag := r.start(nil)
	eventually(t, func() bool { return ag.eng.has("inb_edge") && r.session() == 1 }, "first session")
	first := r.desiredFrames(1)
	if len(first) != 1 {
		t.Fatalf("desired states in generation 1 = %d", len(first))
	}
	eventually(t, func() bool {
		rev, _ := r.applied()
		return rev == first[0].Revision && agentFirstUnacked(ag.a) == 0
	}, "the first state is applied and reported, the agent has nothing more to say")

	// The platform loses the socket without a close event; at the armed time the alarm finds the session without a
	// socket and ends it.
	r.em.reset(r.nodeID)
	r.em.advanceToAlarm(r.nodeID)
	if n := countSteps(r.em.steps(r.nodeID), stepIs(1, fleet.LinkClosed, "")); n != 1 {
		t.Fatalf("closed steps of generation 1 = %d, want 1", n)
	}
	if _, _, ok := r.liveRow(); ok {
		t.Fatal("node_live still has a row after the session ended")
	}

	// The agent comes back as generation 2; its Hello matches the digest, so no state is sent (the desired step is part of
	// the Hello call, so the HelloAck is the last frame there will be).
	eventually(t, func() bool { return r.session() == 2 }, "second session")
	eventually(t, func() bool {
		for _, s := range r.em.sent(r.nodeID) {
			if s.Gen == 2 && s.Frame.GetHelloAck() != nil {
				return true
			}
		}
		return false
	}, "HelloAck of generation 2")
	if got := r.desiredFrames(2); len(got) != 0 {
		t.Fatalf("generation 2 got desired states %+v, want none: the Hello matched the digest", got)
	}

	// A new credential and a poke give a delta on top of what the agent holds.
	r.setCreds("crd_alice", "crd_bob")
	r.poke()
	eventually(t, func() bool { return slices.Equal(ag.eng.credIDs("inb_edge"), []string{"crd_alice", "crd_bob"}) }, "credential added")
	got := r.desiredFrames(2)
	if len(got) != 1 || got[0].BaseRevision != first[0].Revision || got[0].Revision <= got[0].BaseRevision {
		t.Fatalf("generation 2 desired states = %+v, want one delta on revision %d", got, first[0].Revision)
	}
	second := got[0]

	// A desired step whose state is lost: its frame goes out and the database moves on, the object still has the old state.
	r.em.loseState(r.nodeID, fleet.LinkDesired)
	r.setCreds("crd_alice", "crd_bob", "crd_carol")
	r.poke()
	eventually(t, func() bool { return len(ag.eng.credIDs("inb_edge")) == 3 }, "second credential change applied")
	got = r.desiredFrames(2)
	if len(got) != 2 || got[1].BaseRevision != second.Revision {
		t.Fatalf("desired states = %+v, want a delta on revision %d", got, second.Revision)
	}

	// The next step starts from the lost state: the digest is ahead of it, so the whole state is sent again.
	r.setCreds("crd_alice", "crd_bob", "crd_carol", "crd_dave")
	r.poke()
	want := []string{"crd_alice", "crd_bob", "crd_carol", "crd_dave"}
	eventually(t, func() bool { return slices.Equal(ag.eng.credIDs("inb_edge"), want) }, "agent converged on the full state")
	got = r.desiredFrames(2)
	if len(got) != 3 || got[2].BaseRevision != 0 || len(got[2].Inbounds) != 1 || got[2].Revision <= got[1].Revision {
		t.Fatalf("desired states = %+v, want a full resend as the third", got)
	}
	eventually(t, func() bool { rev, hash := r.applied(); return rev == got[2].Revision && hash == got[2].StateHash }, "the full state is the applied one")
	eventually(t, func() bool { return agentFirstUnacked(ag.a) == 0 }, "quiet again")

	// The real path: the desired step fails after its writes committed. Nothing reaches the agent, the socket closes 1011.
	r.setCreds("crd_alice", "crd_bob", "crd_carol", "crd_dave", "crd_erin")
	r.em.failNext(r.nodeID, fleet.LinkDesired)
	r.poke()
	eventually(t, func() bool { return slices.ContainsFunc(r.em.steps(r.nodeID), func(s emuStep) bool { return s.Err }) }, "the injected failure")
	eventually(t, func() bool { return r.session() == 3 }, "the agent reconnects as generation 3")
	want = append(want, "crd_erin")
	eventually(t, func() bool { return slices.Equal(ag.eng.credIDs("inb_edge"), want) }, "agent converged after the failed step")
	if n := len(r.desiredFrames(2)); n != 3 {
		t.Errorf("generation 2 sent %d desired states, want 3: the failed step must send nothing", n)
	}
	// Its Hello (applied revision and hash of the old state) does not match the digest the failed step wrote: one full state.
	full := r.desiredFrames(3)
	if len(full) != 1 || full[0].BaseRevision != 0 || len(full[0].Inbounds) != 1 {
		t.Fatalf("generation 3 desired states = %+v, want exactly one full state", full)
	}
	eventually(t, func() bool { rev, hash := r.applied(); return rev == full[0].Revision && hash == full[0].StateHash }, "the agent's hash matches")
	if n := len(r.desiredFrames(3)); n != 1 {
		t.Errorf("generation 3 sent %d desired states, want 1", n)
	}
	var failed []emuStep
	for _, s := range r.em.steps(r.nodeID) {
		if s.Err {
			failed = append(failed, s)
		}
	}
	if len(failed) != 1 || failed[0].Gen != 2 || failed[0].Kind != fleet.LinkDesired {
		t.Errorf("failed steps = %+v, want only the injected desired step of generation 2", failed)
	}
}

// 3. The liveness deadline closes the session from the alarm; the agent reconnects as the next generation.
func TestEdgeLinkLivenessCloseByAlarm(t *testing.T) {
	r := newEdgeRig(t, edgeOpts{liveness: 15, statsEvery: time.Hour})
	ag := r.start(nil)
	eventually(t, func() bool {
		rev, _ := r.applied()
		return ag.eng.has("inb_edge") && rev > 0 && r.session() == 1 && agentFirstUnacked(ag.a) == 0
	}, "connected, applied and acknowledged: the agent has nothing more to say")

	r.em.advance(16 * time.Second)
	r.em.runAlarm(r.nodeID)

	steps := r.em.steps(r.nodeID)
	alarm, closed := slices.IndexFunc(steps, func(s emuStep) bool { return s.Kind == fleet.LinkAlarm }), slices.IndexFunc(steps, func(s emuStep) bool { return s.Kind == fleet.LinkClosed })
	if alarm < 0 || closed < alarm || steps[alarm].Gen != 1 || steps[closed].Gen != 1 {
		t.Fatalf("steps = %+v, want the alarm of generation 1 and then its closed step", steps)
	}
	closes := r.em.closes(r.nodeID)
	if len(closes) != 1 || closes[0].Gen != 1 || closes[0].Code != 1008 || !strings.HasPrefix(closes[0].Reason, "no message from the agent") {
		t.Fatalf("closes = %+v, want one 1008 for the liveness deadline", closes)
	}
	if _, _, ok := r.liveRow(); ok && r.session() == 1 {
		t.Error("the node_live row of the ended session is still there")
	}
	eventually(t, func() bool { return r.session() == 2 }, "the agent reconnects as generation 2")
	noFailedSteps(t, r.em, r.nodeID)
}

// 4a. Retire with the real agent: it wipes its state and Run returns ErrRetired; the object forgets the node.
func TestEdgeLinkRetireWithAgent(t *testing.T) {
	r := newEdgeRig(t, edgeOpts{})
	ag := r.start(nil)
	eventually(t, func() bool { return ag.eng.has("inb_edge") && r.session() == 1 }, "connected")

	if !r.retire().AgentNotified {
		t.Error("AgentNotified = false: the link did not take the Retire request")
	}
	select {
	case <-ag.finished:
		if !errors.Is(ag.err, ErrRetired) {
			t.Fatalf("Run returned %v, want ErrRetired", ag.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the agent did not stop after Retire")
	}
	if _, err := loadIdentity(r.stateDir); !errors.Is(err, ErrNotEnrolled) {
		t.Errorf("the agent kept its identity: %v", err)
	}
	eventually(t, func() bool { return r.em.storage(r.nodeID).empty() }, "Forget: no storage and no alarm left")
	steps := r.em.steps(r.nodeID)
	if countSteps(steps, stepIs(1, fleet.LinkRequest, "retire")) != 1 || countSteps(steps, stepIs(1, fleet.LinkClosed, "")) != 1 {
		t.Errorf("steps = %+v, want the Retire request and one closed step", steps)
	}
	if _, _, ok := r.liveRow(); ok {
		t.Error("the node_live row of a retired node is still there")
	}
	time.Sleep(50 * time.Millisecond) // nothing may re-arm the alarm afterwards
	if st := r.em.storage(r.nodeID); !st.empty() {
		t.Errorf("storage after Forget = %+v", st)
	}
	noFailedSteps(t, r.em, r.nodeID)
}

// 4b. Retire with a client that never closes: the 5 s alarm closes the socket with 1008 and the object forgets the node.
func TestEdgeLinkRetireByAlarmWithSilentClient(t *testing.T) {
	r := newEdgeRig(t, edgeOpts{})
	c := r.dialRaw(r.agentKey())
	c.hello("inst-raw")
	eventually(t, func() bool { return r.session() == 1 }, "the raw client's session is online")
	pending, _ := r.dialChallenge() // a socket that has not authenticated: Forget must not take its handshake deadline

	if !r.retire().AgentNotified {
		t.Error("AgentNotified = false")
	}
	c.until(func(m *agentv1.ConnectResponse) bool { return m.GetRetire() != nil })
	if st := r.em.storage(r.nodeID); st.empty() || st.Live != 1 || st.AlarmAt == 0 {
		t.Fatalf("storage before the alarm = %+v, want a live session with an alarm", st)
	}

	// The client says nothing and does not close. Five seconds later the core closes the session.
	r.em.advance(6 * time.Second)
	r.em.runAlarm(r.nodeID)
	closes := r.em.closes(r.nodeID)
	if len(closes) != 1 || closes[0].Code != 1008 || closes[0].Reason != "node retired" {
		t.Fatalf("closes = %+v, want one 1008 \"node retired\"", closes)
	}
	if st := r.em.storage(r.nodeID); !st.kvEmpty() || !st.AlarmSet {
		t.Errorf("storage after Forget = %+v, want nothing stored and only the pending socket's handshake alarm", st)
	}
	steps := r.em.steps(r.nodeID)
	if countSteps(steps, stepIs(1, fleet.LinkAlarm, "")) == 0 || countSteps(steps, stepIs(1, fleet.LinkClosed, "")) != 1 {
		t.Errorf("steps = %+v, want the alarm step and one closed step", steps)
	}
	if _, _, ok := r.liveRow(); ok {
		t.Error("the node_live row of a retired node is still there")
	}
	for { // what the client reads next is the close
		_, err := c.next()
		if err == nil {
			continue
		}
		if code := websocket.CloseStatus(err); code != 1008 {
			t.Fatalf("the client was closed with %v (%v), want 1008", code, err)
		}
		break
	}
	// The pending socket still has its deadline: it is closed by the alarm, and then nothing is left at all.
	r.em.advance(5 * time.Second)
	r.em.runAlarm(r.nodeID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, err := pending.Read(ctx)
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Code != 1008 || ce.Reason != "handshake timeout" {
		t.Errorf("the pending socket was closed with %v, want 1008 \"handshake timeout\"", err)
	}
	eventually(t, func() bool { return r.em.storage(r.nodeID).empty() }, "nothing left")
	noFailedSteps(t, r.em, r.nodeID)
}

// 5. A second socket with the agent's key takes the link over: the agent's socket gets 4000, its session ends, its later
// frames are never stepped (and there were some: the accept is slow, so batches queue behind the LinkAuth), and the agent
// comes back as the next generation, taking the link back.
func TestEdgeLinkSupersededSocket(t *testing.T) {
	r := newEdgeRig(t, edgeOpts{backoff: 3 * time.Second})
	ag := r.start(nil)
	eventually(t, func() bool { return ag.eng.has("inb_edge") && r.session() == 1 }, "connected as generation 1")
	ag.eng.setEmit(true)
	eventually(t, func() bool { return countSteps(r.em.steps(r.nodeID), stepIs(1, fleet.LinkFrame, "stats")) >= 3 }, "stats flowing")
	// The raw client's accept takes a while; the agent goes on sending behind its LinkAuth in the object's queue.
	var slow atomic.Bool
	slow.Store(true)
	r.em.setBefore(func(ctx context.Context, in fleet.LinkIn) {
		if in.Kind == emuAcceptKind && slow.CompareAndSwap(true, false) {
			select {
			case <-time.After(300 * time.Millisecond):
			case <-ctx.Done():
			}
		}
	})

	c := r.dialRaw(r.agentKey())
	c.hello("inst-raw")
	if got := r.session(); got != 2 {
		t.Fatalf("node_live.session = %d after the raw client's Hello, want 2", got)
	}
	if closes := r.em.closes(r.nodeID); len(closes) != 1 || closes[0] != (emuClose{Gen: 1, Code: 4000, Reason: "superseded"}) {
		t.Fatalf("closes = %+v, want the agent's socket closed 4000", closes)
	}
	// The agent answers the close; the old socket's close event runs no step (the session it belonged to is already
	// closed, inside the takeover: the session guard of NodeDisconnected is covered by the 5a driver tests).
	eventually(t, func() bool {
		return slices.ContainsFunc(r.em.closeEvents(r.nodeID), func(e emuCloseEvent) bool { return e.Gen == 1 })
	}, "the old socket's close event")
	if got := r.session(); got != 2 {
		t.Errorf("node_live.session = %d after the old socket closed, want 2", got)
	}
	if n := r.em.dropped(r.nodeID); n == 0 {
		t.Error("no frame of the superseded socket reached the object after the takeover: the test did not exercise the drop")
	}

	// The agent reconnects and supersedes the raw client in turn.
	eventually(t, func() bool { return r.session() == 3 }, "the agent comes back as generation 3")
	for {
		_, err := c.next()
		if err == nil {
			continue
		}
		if code := websocket.CloseStatus(err); code != 4000 {
			t.Fatalf("the raw client was closed with %v (%v), want 4000", code, err)
		}
		break
	}

	steps := r.em.steps(r.nodeID)
	open2 := slices.IndexFunc(steps, stepIs(2, fleet.LinkOpen, ""))
	if open2 < 0 {
		t.Fatalf("no open step of generation 2: %+v", steps)
	}
	for i, s := range steps {
		if i > open2 && s.Gen == 1 {
			t.Errorf("a step of the superseded generation ran after generation 2 opened: %+v", s)
		}
	}
	if n := countSteps(steps[:open2], stepIs(1, fleet.LinkClosed, "")); n != 1 || countSteps(steps, stepIs(1, fleet.LinkClosed, "")) != 1 {
		t.Errorf("the superseded session must be closed exactly once, before generation 2 opens: %+v", steps)
	}
	if countSteps(steps, stepIs(2, fleet.LinkClosed, "")) != 1 {
		t.Errorf("generation 2 must be closed once when the agent takes the link back: %+v", steps)
	}
	noFailedSteps(t, r.em, r.nodeID)
}

// 6. A failed step closes the socket 1011 and writes nothing; frames queued behind it are dropped; after the reconnect every
// stats batch is counted exactly once, also the one whose step failed after its database write committed.
func TestEdgeLinkFailedStepCountsEveryBatchOnce(t *testing.T) {
	r := newEdgeRig(t, edgeOpts{})
	ag := r.start(nil)
	eventually(t, func() bool { return ag.eng.has("inb_edge") && r.session() == 1 }, "connected")
	ag.eng.setEmit(true)
	eventually(t, func() bool {
		up, _ := r.traffic()
		return up > 0 && countSteps(r.em.steps(r.nodeID), stepIs(1, fleet.LinkFrame, "stats")) >= 3
	}, "stats counted")

	// The failing step is slow, so that frames queue behind it (the agent sends a batch every 20 ms).
	var slow atomic.Bool
	r.em.setBefore(func(ctx context.Context, in fleet.LinkIn) {
		if in.Kind == fleet.LinkFrame && slow.CompareAndSwap(true, false) {
			select {
			case <-time.After(200 * time.Millisecond):
			case <-ctx.Done():
			}
		}
	})
	slow.Store(true)
	r.em.failNext(r.nodeID, fleet.LinkFrame)
	eventually(t, func() bool { return slices.IndexFunc(r.em.steps(r.nodeID), func(s emuStep) bool { return s.Err }) >= 0 }, "the injected failure")
	eventually(t, func() bool { return r.session() == 2 }, "the agent reconnects as generation 2")
	eventually(t, func() bool { return countSteps(r.em.steps(r.nodeID), stepIs(2, fleet.LinkFrame, "stats")) >= 3 }, "stats of generation 2")

	steps := r.em.steps(r.nodeID)
	failed := slices.IndexFunc(steps, func(s emuStep) bool { return s.Err })
	if steps[failed].Gen != 1 || steps[failed].Kind != fleet.LinkFrame || steps[failed].Msg != "stats" {
		t.Fatalf("failed step = %+v, want a stats frame of generation 1", steps[failed])
	}
	if got := r.em.closes(r.nodeID); len(got) == 0 || got[0] != (emuClose{Gen: 1, Code: 1011, Reason: "link error"}) {
		t.Fatalf("closes = %+v, want the agent's socket closed 1011 first", got)
	}
	for i, s := range steps {
		if i > failed && s.Gen == 1 && s.Kind != fleet.LinkClosed {
			t.Errorf("a step of generation 1 ran after the failed one: %+v (queued frames of a closed socket must be dropped)", s)
		}
	}
	if n := countSteps(steps, stepIs(1, fleet.LinkClosed, "")); n != 1 {
		t.Errorf("closed steps of generation 1 = %d, want 1 (from the socket's close event)", n)
	}
	if n := r.em.dropped(r.nodeID); n == 0 {
		t.Error("no frame was queued behind the failed step: the test did not exercise the drop")
	}

	ag.eng.setEmit(false)
	up, down := ag.eng.emitted()
	eventually(t, func() bool { u, d := r.traffic(); return u == up && d == down }, "every batch counted")
	time.Sleep(100 * time.Millisecond) // a batch counted twice would show up late
	if u, d := r.traffic(); u != up || d != down || up == 0 {
		t.Fatalf("panel counted up=%d down=%d, the engine emitted up=%d down=%d", u, d, up, down)
	}
}

// Not in the design list: the block budget. A step that outlives its budget is a failed step (1011, waiters rejected "link
// lost", nothing written, the late result thrown away), and the next connection works.
func TestEdgeLinkSpentBudgetFailsTheStepAndItsWaiters(t *testing.T) {
	// A quiet agent and a slow reconnect: only the hung step runs while the limits are short.
	r := newEdgeRig(t, edgeOpts{statsEvery: time.Hour, backoff: time.Second})
	ag := r.start(nil)
	eventually(t, func() bool {
		rev, _ := r.applied()
		return ag.eng.has("inb_edge") && rev > 0 && r.session() == 1 && agentFirstUnacked(ag.a) == 0
	}, "connected, applied and acknowledged")

	var hung atomic.Int32
	r.em.setBefore(func(ctx context.Context, in fleet.LinkIn) {
		if in.Kind == fleet.LinkRequest && hung.Add(1) == 1 {
			<-ctx.Done() // the step takes longer than the object will wait
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r.em.setLimits(600*time.Millisecond, 300*time.Millisecond)
	_, err := r.f.RunDoctor(ctx, r.nodeID, nil, 5*time.Second)
	r.em.setLimits(25*time.Second, 20*time.Second)
	if err == nil || connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("RunDoctor over a step that ran out of budget = %v, want the link lost error", err)
	}
	steps := r.em.steps(r.nodeID)
	if i := slices.IndexFunc(steps, func(s emuStep) bool { return s.Err }); i < 0 || steps[i].Kind != fleet.LinkRequest {
		t.Fatalf("steps = %+v, want the request step to have failed", steps)
	}
	if got := r.em.closes(r.nodeID); len(got) == 0 || got[0].Code != 1011 {
		t.Fatalf("closes = %+v, want 1011", got)
	}

	// The agent reconnects and the next request is answered.
	eventually(t, func() bool { return r.session() == 2 }, "the agent reconnects as generation 2")
	report, err := r.f.RunDoctor(ctx, r.nodeID, nil, 5*time.Second)
	if err != nil || report == nil {
		t.Fatalf("RunDoctor after the reconnect = %v, %v", report, err)
	}
}

// The panel closes the session (re-enrolment): NodeLink.close(4000), the closed step runs, the agent reconnects.
func TestEdgeLinkCloseFromThePanel(t *testing.T) {
	r := newEdgeRig(t, edgeOpts{})
	ag := r.start(nil)
	eventually(t, func() bool { return ag.eng.has("inb_edge") && r.session() == 1 }, "connected")
	if err := r.em.Close(context.Background(), r.nodeID, "re-enrolled"); err != nil {
		t.Fatal(err)
	}
	if got := r.em.closes(r.nodeID); len(got) != 1 || got[0] != (emuClose{Gen: 1, Code: 4000, Reason: "re-enrolled"}) {
		t.Fatalf("closes = %+v", got)
	}
	if n := countSteps(r.em.steps(r.nodeID), stepIs(1, fleet.LinkClosed, "")); n != 1 {
		t.Errorf("closed steps of generation 1 = %d, want 1", n)
	}
	eventually(t, func() bool { return r.session() == 2 }, "the agent reconnects as generation 2")
	noFailedSteps(t, r.em, r.nodeID)
}

// A socket that does not authenticate leaves nothing behind: a wrong signature and a text frame are refused, a silent one is
// closed by the alarm, and none of them touches the generation or the storage or reaches a Go step.
func TestEdgeLinkHandshakeRefusalsStoreNothing(t *testing.T) {
	r := newEdgeRig(t, edgeOpts{})
	closedWith := func(ws *websocket.Conn) websocket.CloseError {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, err := ws.Read(ctx)
		var ce websocket.CloseError
		if !errors.As(err, &ce) {
			t.Fatalf("read = %v, want a close", err)
		}
		return ce
	}

	forged, challenge := r.dialChallenge()
	other, err := ecdsa.GenerateKey(r.agentKey().Curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r.sendAuth(forged, challenge, other)
	if ce := closedWith(forged); ce.Code != 1008 || ce.Reason != "unauthorized" {
		t.Errorf("forged LinkAuth closed with %v %q, want 1008 \"unauthorized\"", ce.Code, ce.Reason)
	}

	text, _ := r.dialChallenge()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := text.Write(ctx, websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if ce := closedWith(text); ce.Code != 1008 || ce.Reason != "handshake" {
		t.Errorf("text frame closed with %v %q, want 1008 \"handshake\"", ce.Code, ce.Reason)
	}

	silent, _ := r.dialChallenge()
	if st := r.em.storage(r.nodeID); st.Sockets != 1 || st.Gen != 0 || st.Live != 0 || st.HasState || st.AlarmAt != 0 || !st.AlarmSet {
		t.Errorf("storage with a silent socket = %+v, want only the handshake alarm", st)
	}
	r.em.advance(11 * time.Second)
	r.em.runAlarm(r.nodeID)
	if ce := closedWith(silent); ce.Code != 1008 || ce.Reason != "handshake timeout" {
		t.Errorf("silent socket closed with %v %q, want 1008 \"handshake timeout\"", ce.Code, ce.Reason)
	}

	eventually(t, func() bool { return r.em.storage(r.nodeID).empty() }, "no storage and no alarm after unauthenticated sockets")
	if steps := r.em.steps(r.nodeID); len(steps) != 0 {
		t.Errorf("steps = %+v, want none: no Go step before a socket authenticates", steps)
	}
}

// A session that never says Hello: a desired-state poke before Hello sends nothing and must not lose the Hello deadline;
// the deadline then closes the session from the alarm.
func TestEdgeLinkNoHelloKeepsItsDeadlineAndIsClosedByAlarm(t *testing.T) {
	r := newEdgeRig(t, edgeOpts{})
	c := r.dialRaw(r.agentKey())
	before := r.em.storage(r.nodeID)
	if before.Live != 1 || before.AlarmAt == 0 {
		t.Fatalf("storage after LinkAuth = %+v, want a live session with the Hello deadline as its alarm", before)
	}

	r.poke()
	eventually(t, func() bool { return countSteps(r.em.steps(r.nodeID), stepIs(1, fleet.LinkDesired, "")) == 1 }, "the desired step ran")
	if got := r.em.sent(r.nodeID); len(got) != 0 {
		t.Fatalf("frames before Hello: %+v, want none", got)
	}
	if after := r.em.storage(r.nodeID); after.AlarmAt != before.AlarmAt {
		t.Errorf("alarm moved from %d to %d: a step that does nothing must keep the Hello deadline", before.AlarmAt, after.AlarmAt)
	}

	r.em.advance(16 * time.Second)
	r.em.runAlarm(r.nodeID)
	if closes := r.em.closes(r.nodeID); len(closes) != 1 || closes[0].Code != 1008 || closes[0].Reason != "no Hello" {
		t.Fatalf("closes = %+v, want one 1008 \"no Hello\"", closes)
	}
	if st := r.em.storage(r.nodeID); st.Live != 0 || st.AlarmAt != 0 || st.AlarmSet {
		t.Errorf("storage after the close = %+v, want no live session and no alarm", st)
	}
	if _, _, ok := r.liveRow(); ok {
		t.Error("a node_live row exists for a session that never said Hello")
	}
	_, err := c.next()
	if code := websocket.CloseStatus(err); code != 1008 {
		t.Errorf("the client was closed with %v (%v), want 1008", code, err)
	}
	noFailedSteps(t, r.em, r.nodeID)
}

// The own-node guard fires: a step that calls Ask, Close or poke for its own node with the step's context (or one derived
// from it, even by WithoutCancel, which keeps the tag) is reported and refused; the step itself goes on.
func TestEdgeLinkOwnNodeGuardFires(t *testing.T) {
	r := newEdgeRig(t, edgeOpts{statsEvery: time.Hour})
	var mu sync.Mutex
	var reports []string
	r.em.onViolation(func(msg string) {
		mu.Lock()
		reports = append(reports, msg)
		mu.Unlock()
	})
	ag := r.start(nil)
	eventually(t, func() bool {
		rev, _ := r.applied()
		return ag.eng.has("inb_edge") && rev > 0 && r.session() == 1 && agentFirstUnacked(ag.a) == 0
	}, "connected and quiet")

	errs := make(chan []error, 1)
	var once atomic.Bool
	r.em.setBefore(func(ctx context.Context, in fleet.LinkIn) {
		if in.Kind != fleet.LinkRequest || !once.CompareAndSwap(false, true) {
			return
		}
		frame := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RunDoctor{RunDoctor: &agentv1.RunDoctor{RequestId: "req_own"}}}
		_, e1 := r.em.Ask(ctx, in.NodeID, "req_own", frame, time.Now().Add(time.Second))
		e2 := r.em.Close(ctx, in.NodeID, "own")
		e3 := r.em.poke(ctx, in.NodeID)
		_, e4 := r.em.Ask(context.WithoutCancel(ctx), in.NodeID, "req_own2", frame, time.Now().Add(time.Second))
		errs <- []error{e1, e2, e3, e4}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if report, err := r.f.RunDoctor(ctx, r.nodeID, nil, 5*time.Second); err != nil || report == nil {
		t.Fatalf("RunDoctor = %v, %v: the guard must refuse the inner calls and let the step go on", report, err)
	}
	for i, err := range <-errs {
		if err == nil {
			t.Errorf("own-node call %d was not refused", i+1)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 4 {
		t.Fatalf("reports = %q, want 4", reports)
	}
	for i, what := range []string{"Ask(", "Close(", "Poke(", "Ask("} {
		if !strings.Contains(reports[i], what) || !strings.Contains(reports[i], "own link step") {
			t.Errorf("report %d = %q", i+1, reports[i])
		}
	}
}

// 7. Renew over the link (5c). A node enrolled 25 days ago has a certificate five days from its end: the renewal loop
// asks for a new one on the session, the panel records it, and the session keeps running on the old serial. Once the
// old certificate's grace is over, the recheck of the next frame ends the old session, and the agent reconnects with
// the new certificate. The loop starts before the first session exists, so its first tries find no session and fail.
func TestEdgeLinkRenewOverTheLink(t *testing.T) {
	r := newEdgeRig(t, edgeOpts{liveness: 3600, noEnroll: true})
	const age = 25 * 24 * time.Hour
	r.em.advance(-age)
	_, err := r.enrol(r.f.CAFingerprint())
	r.em.advance(age)
	if err != nil {
		t.Fatal(err)
	}
	certSerial := func() string {
		var s string
		if err := r.st.R.QueryRowContext(context.Background(), `SELECT coalesce(cert_serial, '') FROM node WHERE id = ?`, r.nodeID).Scan(&s); err != nil {
			t.Errorf("node: %v", err)
		}
		return s
	}
	oldSerial := certSerial()
	if oldSerial == "" {
		t.Fatal("the enrolment left no certificate serial")
	}

	ag := r.start(func(a *Agent) { a.renewEvery = 20 * time.Millisecond })
	eventually(t, func() bool { return ag.a.id.Load().cert.Leaf.SerialNumber.Text(16) != oldSerial }, "the agent holds a renewed certificate")
	newSerial := certSerial()
	if newSerial == oldSerial || ag.a.id.Load().cert.Leaf.SerialNumber.Text(16) != newSerial {
		t.Fatalf("serials: panel %q, agent %q, old %q: the panel and the agent must hold the same new one", newSerial, ag.a.id.Load().cert.Leaf.SerialNumber.Text(16), oldSerial)
	}
	if time.Until(ag.a.id.Load().notAfter()) < 29*24*time.Hour {
		t.Errorf("the renewed certificate ends in %v, want about 30 days", time.Until(ag.a.id.Load().notAfter()))
	}
	renews := func() int {
		return countSteps(r.em.steps(r.nodeID), func(s emuStep) bool { return s.Kind == fleet.LinkFrame && s.Msg == "renew" })
	}
	if renews() != 1 {
		t.Errorf("renew frames stepped = %d, want 1 (the loop stops once the certificate is fresh)", renews())
	}
	// The session carries on with the old serial: nothing was closed by the renewal itself.
	if n := countSteps(r.em.steps(r.nodeID), func(s emuStep) bool { return s.Kind == fleet.LinkClosed }); n != 0 || r.session() != 1 {
		t.Fatalf("the session ended at renewal (closed steps %d, node_live.session %d)", n, r.session())
	}
	eventually(t, func() bool { return ag.eng.has("inb_edge") && r.session() == 1 }, "the first session works")
	if sent := r.em.sent(r.nodeID); !slices.ContainsFunc(sent, func(s emuSent) bool { return s.Gen == 1 && s.Frame.GetRenew() != nil }) {
		t.Error("no RenewResponse reached the agent")
	}

	// Past the old certificate's grace (three hours) the next frame's recheck refuses the session.
	r.em.advance(3*time.Hour + time.Minute)
	eventually(t, func() bool { return r.session() == 2 }, "the agent reconnects as generation 2 with the new certificate")
	closes := r.em.closes(r.nodeID)
	if len(closes) == 0 || closes[0].Gen != 1 || closes[0].Code != 1008 || closes[0].Reason != "client certificate revoked" {
		t.Errorf("closes = %+v, want the recheck to end generation 1 with 1008", closes)
	}
	ag.eng.setEmit(true)
	eventually(t, func() bool { up, _ := r.traffic(); return up > 0 }, "the new session carries traffic")
	if got := ag.a.id.Load().cert.Leaf.SerialNumber.Text(16); got != newSerial {
		t.Errorf("the agent's certificate moved to %q", got)
	}
	if renews() != 1 {
		t.Errorf("renew frames stepped = %d, want still 1", renews())
	}
	noFailedSteps(t, r.em, r.nodeID)
}

// 7b. The answer to a renewal is lost: the panel committed the new certificate, the socket closed before the agent saw
// it. The agent still holds the old key, whose certificate stays valid for the grace, so it reconnects, renews again on
// it and is not locked out; when the grace is over the certificate it holds is the newest one.
func TestEdgeLinkLostRenewAnswerDoesNotLockTheAgentOut(t *testing.T) {
	// A quiet agent (no stats, no automatic renewal: its certificate is fresh), so that the one frame step the failure
	// meets is the renew frame.
	r := newEdgeRig(t, edgeOpts{liveness: 3600, statsEvery: time.Hour})
	ag := r.start(nil)
	eventually(t, func() bool {
		rev, _ := r.applied()
		return ag.eng.has("inb_edge") && rev > 0 && r.session() == 1 && agentFirstUnacked(ag.a) == 0
	}, "connected, applied and acknowledged: the agent has nothing more to say")
	agentSerial := func() string { return ag.a.id.Load().cert.Leaf.SerialNumber.Text(16) }
	nodeSerial := func() string {
		var s string
		if err := r.st.R.QueryRowContext(context.Background(), `SELECT coalesce(cert_serial, '') FROM node WHERE id = ?`, r.nodeID).Scan(&s); err != nil {
			t.Errorf("node: %v", err)
		}
		return s
	}
	enrolled := agentSerial()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	r.em.failNext(r.nodeID, fleet.LinkFrame)
	if err := ag.a.renew(ctx); err == nil {
		t.Fatal("the renewal succeeded although its answer was dropped")
	}
	lost := nodeSerial()
	if lost == enrolled || agentSerial() != enrolled {
		t.Fatalf("serials: panel %q, agent %q, enrolled %q: the panel must have committed a certificate the agent never got", lost, agentSerial(), enrolled)
	}
	eventually(t, func() bool { return r.session() == 2 && ag.a.cur.Load() != nil }, "the agent reconnects as generation 2 on its old certificate")

	// The retry (the next renewLoop tick) runs on the old certificate, inside the grace.
	if err := ag.a.renew(ctx); err != nil {
		t.Fatalf("the retry on the old certificate: %v", err)
	}
	if got := agentSerial(); got == enrolled || got == lost || got != nodeSerial() {
		t.Fatalf("after the retry: agent %q, panel %q, lost one %q, enrolled %q", got, nodeSerial(), lost, enrolled)
	}
	newest := agentSerial()

	// Past the grace both older certificates are revoked; the session on the old one is ended by the next step's
	// recheck, and the agent comes back on the newest certificate.
	r.em.advance(3*time.Hour + time.Minute)
	_, _ = r.f.RunDoctor(ctx, r.nodeID, nil, 5*time.Second) // any step does: this one meets the recheck
	eventually(t, func() bool { return r.session() == 3 }, "the agent reconnects as generation 3")
	if got := agentSerial(); got != newest {
		t.Errorf("the agent's certificate moved to %q", got)
	}
	closes := r.em.closes(r.nodeID)
	if !slices.ContainsFunc(closes, func(c emuClose) bool { return c.Gen == 2 && c.Reason == "client certificate revoked" }) {
		t.Errorf("closes = %+v, want generation 2 refused by the recheck", closes)
	}
	if n := countSteps(r.em.steps(r.nodeID), func(st emuStep) bool { return st.Err }); n != 1 {
		t.Errorf("failed steps = %d, want only the injected one", n)
	}
	eventually(t, func() bool { return slices.ContainsFunc(r.em.steps(r.nodeID), stepIs(3, fleet.LinkFrame, "hello")) }, "generation 3 says Hello: the agent is not locked out")
}
