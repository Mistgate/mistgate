package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/release"
)

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeFleet is the fleet as the updates module sees it: the test plays the nodes.
type fakeFleet struct {
	mu       sync.Mutex
	live     map[string]bool
	liveErr  error
	caps     map[string][]string
	drift    map[string]bool
	online   map[string]int
	updates  []string // node ids UpdateAgent was sent to, in order
	rollback []string
	// onUpdate / onRollback decide the answer; nil = ok. They run in the command goroutine.
	onUpdate   func(node string) (*agentv1.CommandResult, error)
	onRollback func(node string) (*agentv1.CommandResult, error)
}

func newFakeFleet() *fakeFleet {
	return &fakeFleet{live: map[string]bool{}, caps: map[string][]string{}, drift: map[string]bool{}, online: map[string]int{}}
}

func (f *fakeFleet) Live(context.Context) ([]store.NodeLiveRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.liveErr != nil {
		return nil, f.liveErr
	}
	ids := map[string]bool{}
	for id := range f.live {
		ids[id] = true
	}
	for id := range f.caps {
		ids[id] = true
	}
	for id := range f.drift {
		ids[id] = true
	}
	for id := range f.online {
		ids[id] = true
	}
	rows := make([]store.NodeLiveRow, 0, len(ids))
	for id := range ids {
		users := make(map[string]int64, f.online[id])
		for i := range f.online[id] {
			users[fmt.Sprintf("usr_%d", i)] = 0
		}
		encoded, _ := json.Marshal(users)
		rows = append(rows, store.NodeLiveRow{NodeID: id, State: "active", Exists: true, Connected: f.live[id],
			AgentCaps: f.caps[id], Drift: f.drift[id], UsersJSON: string(encoded)})
	}
	return rows, nil
}

func (f *fakeFleet) NodeLive(_ context.Context, nodeID string) (store.NodeLiveRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.liveErr != nil {
		return store.NodeLiveRow{}, f.liveErr
	}
	return store.NodeLiveRow{NodeID: nodeID, State: "active", Exists: true, Connected: f.live[nodeID], AgentCaps: f.caps[nodeID], Drift: f.drift[nodeID]}, nil
}

func (f *fakeFleet) setLiveError(err error) {
	f.mu.Lock()
	f.liveErr = err
	f.mu.Unlock()
}

// precondition is what the real fleet answers when nothing can be sent.
func (f *fakeFleet) precondition(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.live[id] {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("node is offline"))
	}
	for _, c := range f.caps[id] {
		if c == capUpdate {
			return nil
		}
	}
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("node cannot update itself"))
}

func okResult() *agentv1.CommandResult { return &agentv1.CommandResult{Ok: true, Affected: 1} }

func (f *fakeFleet) UpdateAgent(_ context.Context, id string, _, _ []byte, _ time.Duration) (*agentv1.CommandResult, error) {
	if err := f.precondition(id); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.updates = append(f.updates, id)
	fn := f.onUpdate
	f.mu.Unlock()
	if fn != nil {
		return fn(id)
	}
	return okResult(), nil
}

func (f *fakeFleet) RollbackAgent(_ context.Context, id string, _ time.Duration) (*agentv1.CommandResult, error) {
	if err := f.precondition(id); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.rollback = append(f.rollback, id)
	fn := f.onRollback
	f.mu.Unlock()
	if fn != nil {
		return fn(id)
	}
	return okResult(), nil
}

func (f *fakeFleet) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.updates...)
}

func (f *fakeFleet) rolledBack() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.rollback...)
}

// fakeHealth answers the gate's questions per node.
type fakeHealth struct {
	mu        sync.Mutex
	probeable map[string]int
	ok        map[string]int
	failed    map[string]int
	runs      map[string]int
	since     map[string]time.Time
	errors    map[string]error
}

func newFakeHealth() *fakeHealth {
	return &fakeHealth{probeable: map[string]int{}, ok: map[string]int{}, failed: map[string]int{}, runs: map[string]int{}, since: map[string]time.Time{}, errors: map[string]error{}}
}

func (h *fakeHealth) RunChecksNow(_ context.Context, id string) (int, int, time.Duration, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runs[id]++
	return 1, 0, 0, nil
}

func (h *fakeHealth) NodeChecksSince(_ context.Context, id string, since time.Time) (int, int, int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.since[id] = since
	return h.probeable[id], h.ok[id], h.failed[id], h.errors[id]
}

func (h *fakeHealth) set(id string, probeable, ok, failed int) {
	h.mu.Lock()
	h.probeable[id], h.ok[id], h.failed[id] = probeable, ok, failed
	h.mu.Unlock()
}

func (h *fakeHealth) setError(id string, err error) {
	h.mu.Lock()
	h.errors[id] = err
	h.mu.Unlock()
}

// env is a Service on a real store with a fake fleet, fake health and a fake clock.
type env struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	fl     *fakeFleet
	hl     *fakeHealth
	clk    *clock
	s      *Service
	dir    string
	pub    ed25519.PublicKey
	priv   ed25519.PrivateKey
	stepUp func(context.Context) error
	built  map[string]int64 // node name -> built it was added with
}

const (
	oldBuilt = 1_790_000_000
	newBuilt = 1_791_000_000
)

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	pub, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ctx: ctx, st: st, fl: newFakeFleet(), hl: newFakeHealth(), dir: t.TempDir(), pub: pub, priv: priv,
		clk: &clock{t: time.Unix(1_791_100_000, 0)}, built: map[string]int64{}}
	e.stepUp = func(context.Context) error { return nil }
	e.s = e.newService(pub)
	return e
}

// newService builds a Service like cmd/mistgate does, on this env's store.
func (e *env) newService(key ed25519.PublicKey) *Service {
	e.t.Helper()
	s, err := New(e.st, e.fl, e.hl, Config{
		DataDir: e.dir, Key: key, PanelVersion: "0.2.0-test", PanelBuilt: newBuilt,
		StepUp: func(ctx context.Context) error { return e.stepUp(ctx) },
		Actor:  func(context.Context) string { return "adm_test" },
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)), Now: e.clk.Now,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// restart replaces the Service with a new one on the same database, the way a panel restart does.
func (e *env) restart() {
	e.t.Helper()
	e.s = e.newService(e.pub)
	e.s.recover(e.ctx)
}

// bundle writes a signed bundle into dist/ and rescans. files: name -> content.
func (e *env) bundle(version string, built int64, files map[string][]byte) {
	e.t.Helper()
	writeBundle(e.t, filepath.Join(e.dir, "dist"), e.priv, version, built, e.clk.Now().Add(30*24*time.Hour).Unix(), files)
	e.s.rescan()
}

func writeBundle(t *testing.T, dist string, priv ed25519.PrivateKey, version string, built, expires int64, files map[string][]byte) {
	t.Helper()
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	m := &release.Manifest{Schema: release.Schema, Version: version, Built: built, Expires: expires}
	for name, data := range files {
		path := filepath.Join(dist, name)
		if err := os.WriteFile(path, data, 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := release.FileFromPath(path)
		if err != nil {
			t.Fatal(err)
		}
		m.Files = append(m.Files, f)
	}
	body, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, release.ManifestName), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, release.SignatureName), release.Sign(priv, body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// defaultBundle is the release the rollout tests ship: one binary per platform.
func (e *env) defaultBundle() {
	e.bundle("0.2.0-new", newBuilt, map[string][]byte{
		"mistgate-node-linux-amd64": []byte("amd64 binary of the new build"),
		"mistgate-node-linux-arm64": []byte("arm64 binary of the new build"),
	})
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

type nodeOpts struct {
	caps     []string
	built    int64
	inbounds int
	online   int
	offline  bool
}

// addNode inserts an active node, plays its Hello and registers it with the fake fleet.
func (e *env) addNode(name string, o nodeOpts) string {
	e.t.Helper()
	id := "nod_" + name
	if _, err := e.st.W.ExecContext(e.ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, 'active', 1)`, id, name, name+".example.com"); err != nil {
		e.t.Fatal(err)
	}
	if o.caps == nil {
		o.caps = []string{"doctor/1", capUpdate, capGuard}
	}
	if o.built == 0 {
		o.built = oldBuilt
	}
	e.hello(id, "0.1.0-old", o.built, o.caps, "")
	for i := 0; i < o.inbounds; i++ {
		pid, iid := fmt.Sprintf("prf_%s_%d", name, i), fmt.Sprintf("inb_%s_%d", name, i)
		e.exec(`INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES (?, 'hysteria2', ?, '{}', 1, 1)`, pid, pid)
		e.exec(`INSERT INTO inbound (id, profile_id, node_id, state, created_at, updated_at) VALUES (?, ?, ?, 'active', 1, 1)`, iid, pid, id)
	}
	e.fl.mu.Lock()
	e.fl.live[id], e.fl.caps[id], e.fl.online[id] = !o.offline, o.caps, o.online
	e.fl.mu.Unlock()
	e.built[name] = o.built
	return id
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.W.ExecContext(e.ctx, q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

// hello records a Hello of the node (what fleet.Connect does) and marks it connected.
func (e *env) hello(id, version string, built int64, caps []string, lastUpdateJSON string) {
	e.t.Helper()
	if _, _, err := e.st.NodeHello(e.ctx, id, 1, store.HelloInfo{AgentVersion: version, Instance: "i-" + version, Built: built, Caps: caps, LastUpdateJSON: lastUpdateJSON}, e.clk.Now()); err != nil {
		e.t.Fatal(err)
	}
	e.fl.mu.Lock()
	e.fl.live[id], e.fl.caps[id] = true, caps
	e.fl.mu.Unlock()
}

// upgrade plays what the agent does after a successful UpdateAgent: it re-executes and reconnects with the new build.
func (e *env) upgrade(id string) {
	e.t.Helper()
	e.hello(id, "0.2.0-new", newBuilt, []string{"doctor/1", capUpdate, capGuard}, "")
}

// commit plays the agent raising update_committed.
func (e *env) commit(id string) {
	e.t.Helper()
	e.exec(`INSERT INTO event (ts, severity, code, source, node_id, params_json) VALUES (?, 1, 'update_committed', 'agent', ?, '{}')`, e.clk.Now().Unix(), id)
}

// drop plays the node going away (re-exec in flight).
func (e *env) drop(id string) {
	e.fl.mu.Lock()
	e.fl.live[id] = false
	e.fl.mu.Unlock()
}

// tick runs one worker pass and waits for the command goroutines it started.
func (e *env) tick() {
	e.t.Helper()
	e.s.tick(e.ctx)
	e.s.wg.Wait()
}

func (e *env) startRollout(ids ...string) store.RolloutRow {
	e.t.Helper()
	ro, err := e.s.start(e.ctx, ids, 0)
	if err != nil {
		e.t.Fatalf("start rollout: %v", err)
	}
	return ro
}

func (e *env) rollout() store.RolloutRow {
	e.t.Helper()
	ro, err := e.st.LatestRollout(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return ro
}

// step returns the step of a node in the newest rollout.
func (e *env) step(id string) store.StepRow {
	e.t.Helper()
	steps, err := e.st.RolloutSteps(e.ctx, e.rollout().ID)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, x := range steps {
		if x.NodeID == id {
			return x
		}
	}
	e.t.Fatalf("no step for %s", id)
	return store.StepRow{}
}

func (e *env) wantStep(id, state, errKey string) store.StepRow {
	e.t.Helper()
	x := e.step(id)
	if x.State != state || x.ErrorKey != errKey {
		e.t.Fatalf("step of %s: state %s error %q, want %s %q (%+v)", id, x.State, x.ErrorKey, state, errKey, x)
	}
	return x
}

// wantEvent finds the newest event of the node with this code (the node page reads them).
func (e *env) wantEvent(nodeID, code string) store.EventRow {
	e.t.Helper()
	rows, _, err := e.st.Events(e.ctx, store.EventFilter{NodeID: nodeID, Limit: 50})
	if err != nil {
		e.t.Fatal(err)
	}
	for _, r := range rows {
		if r.Code == code {
			return r
		}
	}
	e.t.Fatalf("no %s event of %s in %+v", code, nodeID, rows)
	return store.EventRow{}
}

func (e *env) wantRollout(status, pauseKey string) store.RolloutRow {
	e.t.Helper()
	ro := e.rollout()
	if ro.Status != status || ro.PauseKey != pauseKey {
		e.t.Fatalf("rollout: status %s pause %q, want %s %q", ro.Status, ro.PauseKey, status, pauseKey)
	}
	return ro
}

func (e *env) get() *adminv1.GetUpdatesResponse {
	e.t.Helper()
	resp, err := rpc{e.s}.GetUpdates(e.ctx, connect.NewRequest(&adminv1.GetUpdatesRequest{}))
	if err != nil {
		e.t.Fatal(err)
	}
	return resp.Msg
}

func (e *env) nodeState(id string) adminv1.NodeUpdateState {
	e.t.Helper()
	for _, n := range e.get().Nodes {
		if n.NodeId == id {
			return n.State
		}
	}
	e.t.Fatalf("node %s is not listed", id)
	return 0
}
