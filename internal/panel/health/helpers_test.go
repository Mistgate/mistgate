package health

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
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

type liveState struct {
	up       bool
	caps     []string
	drift    bool
	lastSeen time.Time
}

type fixCall struct {
	node, fix string
	dry       bool
	params    map[string]string
}

// fakeFleet is the fleet as the health module sees it.
type fakeFleet struct {
	mu       sync.Mutex
	live     map[string]liveState
	liveErr  error
	doctor   func(ctx context.Context, node string, checks []string) (*agentv1.DoctorReport, error)
	fix      func(c fixCall) (*agentv1.CommandResult, error)
	fixCalls []fixCall
	online   map[string]int
}

func (f *fakeFleet) OnlineByInbound(context.Context) map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.online
}

func (f *fakeFleet) set(node string, l liveState) {
	f.mu.Lock()
	f.live[node] = l
	f.mu.Unlock()
}

func (f *fakeFleet) Live(context.Context) ([]store.NodeLiveRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.liveErr != nil {
		return nil, f.liveErr
	}
	rows := make([]store.NodeLiveRow, 0, len(f.live))
	for node, live := range f.live {
		rows = append(rows, store.NodeLiveRow{NodeID: node, State: "active", Exists: true, Connected: live.up,
			LastSeenAt: live.lastSeen, AgentCaps: live.caps, Drift: live.drift})
	}
	return rows, nil
}

func (f *fakeFleet) NodeLive(_ context.Context, nodeID string) (store.NodeLiveRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.liveErr != nil {
		return store.NodeLiveRow{}, f.liveErr
	}
	live, ok := f.live[nodeID]
	if !ok {
		return store.NodeLiveRow{NodeID: nodeID}, nil
	}
	return store.NodeLiveRow{NodeID: nodeID, State: "active", Exists: true, Connected: live.up,
		LastSeenAt: live.lastSeen, AgentCaps: live.caps, Drift: live.drift}, nil
}

func (f *fakeFleet) NodeStatusWithLive(_ context.Context, n store.NodeRow, live store.NodeLiveRow) adminv1.NodeStatus {
	if n.State == "retired" {
		return adminv1.NodeStatus_NODE_STATUS_RETIRED
	}
	if n.State == "pending" {
		return adminv1.NodeStatus_NODE_STATUS_PENDING
	}
	if live.Connected {
		return adminv1.NodeStatus_NODE_STATUS_ONLINE
	}
	return adminv1.NodeStatus_NODE_STATUS_DOWN
}

func (f *fakeFleet) RunDoctor(ctx context.Context, node string, checks []string, _ time.Duration) (*agentv1.DoctorReport, error) {
	f.mu.Lock()
	live := f.live[node]
	f.mu.Unlock()
	if !live.up || len(live.caps) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errTooOld)
	}
	if f.doctor == nil {
		return &agentv1.DoctorReport{}, nil
	}
	return f.doctor(ctx, node, checks)
}

func (f *fakeFleet) ApplyFix(_ context.Context, node, fix string, dry bool, params map[string]string) (*agentv1.CommandResult, error) {
	f.mu.Lock()
	live := f.live[node]
	f.mu.Unlock()
	if !live.up || len(live.caps) == 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errTooOld)
	}
	c := fixCall{node, fix, dry, params}
	f.mu.Lock()
	f.fixCalls = append(f.fixCalls, c)
	f.mu.Unlock()
	if f.fix == nil {
		return &agentv1.CommandResult{Ok: true}, nil
	}
	return f.fix(c)
}

var errTooOld = errString("agent too old")

type errString string

func (e errString) Error() string { return string(e) }

// env is a health module on a real store and vault, a real access module (to make profiles and inbounds exactly
// as production does) and a fake fleet and clock.
type env struct {
	t     *testing.T
	ctx   context.Context
	st    *store.Store
	v     *vault.Vault
	acc   *access.Service
	fl    *fakeFleet
	clock *clock
	s     *Service
	n     int // counter for names
}

func newEnv(t *testing.T, mut ...func(*Config)) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v, err := vault.New(make([]byte, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	reg := builtin.Registry()
	acc, err := access.New(st, v, reg, nil, nil, access.Config{SubscriptionBaseURL: "https://sub.example.com/k3xq/"})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ctx: ctx, st: st, v: v, acc: acc, fl: &fakeFleet{live: map[string]liveState{}},
		clock: &clock{t: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}}
	cfg := Config{Now: e.clock.Now, StartGrace: time.Nanosecond, RetryDelay: time.Millisecond, SnapshotTTL: time.Nanosecond,
		Actor: func(context.Context) string { return "adm_test" }}
	for _, m := range mut {
		m(&cfg)
	}
	e.s = New(st, v, reg, e.fl, cfg)
	e.clock.Advance(time.Minute) // past the start grace
	return e
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.W.ExecContext(e.ctx, q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

// node adds an active node; up makes the fake fleet report a stream with the doctor capability.
func (e *env) node(id, provider string, up bool) {
	e.t.Helper()
	now := e.clock.Now().Unix()
	e.exec(`INSERT INTO node (id, name, address, provider, state, created_at, last_seen_at, last_connected_at) VALUES (?, ?, ?, ?, 'active', 1, ?, ?)`,
		id, id, id+".example.com", provider, now, now)
	e.fl.set(id, liveState{up: up, caps: []string{capDoctor}, lastSeen: time.Unix(now, 0)})
}

// inbound deploys a new hy2 profile on a node on the given port and marks it active (as the node reports).
func (e *env) inbound(nodeID string, port uint32) string {
	e.t.Helper()
	e.n++
	p, err := e.acc.CreateProfile(e.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "hysteria2", Name: "hy2-" + nodeID + "-" + string(rune('a'+e.n))}))
	if err != nil {
		e.t.Fatal(err)
	}
	in, err := e.acc.CreateInbound(e.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: p.Msg.Profile.Id, NodeId: nodeID, PortOverride: port}))
	if err != nil {
		e.t.Fatal(err)
	}
	id := in.Msg.Inbound.Id
	e.exec(`UPDATE inbound SET state = 'active', cert_pin_sha256 = ?, cert_not_after = ? WHERE id = ?`,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", e.clock.Now().Add(60*24*time.Hour).Unix(), id)
	e.s.invalidateSnapshot()
	return id
}

// round records a finished round of an inbound and evaluates.
func (e *env) round(inbound string, status adminv1.CheckStatus, code string) {
	e.t.Helper()
	e.s.record(e.ctx, inbound, Result{Status: status, At: e.clock.Now(), ErrorCode: code, LatencyMS: 50})
	e.clock.Advance(time.Second)
	e.s.evaluate(e.ctx)
}

func (e *env) fail(inbound, code string) {
	e.round(inbound, adminv1.CheckStatus_CHECK_STATUS_FAILED, code)
}
func (e *env) pass(inbound string) { e.round(inbound, adminv1.CheckStatus_CHECK_STATUS_OK, "") }

func (e *env) evaluate() { e.t.Helper(); e.s.invalidateSnapshot(); e.s.evaluate(e.ctx) }

func (e *env) active() map[string]store.HealthAlert {
	e.t.Helper()
	list, err := e.st.ActiveAlerts(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]store.HealthAlert{}
	for _, a := range list {
		out[a.Kind+"/"+a.NodeID+"/"+a.Subject] = a
	}
	return out
}

func (e *env) history() []store.HealthAlert {
	e.t.Helper()
	h, err := e.st.AlertHistory(e.ctx, e.clock.Now().Add(-30*24*time.Hour), "", 100)
	if err != nil {
		e.t.Fatal(err)
	}
	return h
}

func (e *env) report(node string, partial bool, results ...*agentv1.DoctorResult) {
	e.t.Helper()
	e.clock.Advance(time.Second)
	e.s.DoctorReport(e.ctx, node, &agentv1.DoctorReport{Results: results, Partial: partial})
	e.s.evaluate(e.ctx)
}

func res(id string, st agentv1.DoctorStatus, fix string, params ...string) *agentv1.DoctorResult {
	r := &agentv1.DoctorResult{Id: id, Status: st, FixId: fix, Detail: id + " detail", Params: map[string]string{}}
	for i := 0; i+1 < len(params); i += 2 {
		r.Params[params[i]] = params[i+1]
	}
	return r
}

const (
	dOK   = agentv1.DoctorStatus_DOCTOR_STATUS_OK
	dWarn = agentv1.DoctorStatus_DOCTOR_STATUS_WARN
	dFail = agentv1.DoctorStatus_DOCTOR_STATUS_FAIL
	dSkip = agentv1.DoctorStatus_DOCTOR_STATUS_SKIP
)

func want(t *testing.T, got map[string]store.HealthAlert, keys ...string) {
	t.Helper()
	if len(got) != len(keys) {
		var have []string
		for k := range got {
			have = append(have, k)
		}
		t.Fatalf("active alerts %v, want %v", have, keys)
	}
	for _, k := range keys {
		if _, ok := got[k]; !ok {
			var have []string
			for k := range got {
				have = append(have, k)
			}
			t.Fatalf("active alerts %v, want %v", have, keys)
		}
	}
}

func nodeRow(addr string) store.NodeRow { return store.NodeRow{ID: "nod_x", Name: "x", Address: addr} }
