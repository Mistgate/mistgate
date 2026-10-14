package countryprobe

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// call is one command the fake fleet received.
type call struct {
	node string
	req  fleet.CountryProbeRequest
}

// fakeFleet answers CountryProbe from a script. The default answer is a run that placed the TSPU at hop 4, passed the control
// and found every pair open. Nothing here opens a socket: the tests never probe a real address.
type fakeFleet struct {
	mu     sync.Mutex
	calls  []call
	script func(n int, c call) (fleet.CountryProbeResult, error)
}

func (f *fakeFleet) CountryProbe(ctx context.Context, nodeID string, req fleet.CountryProbeRequest) (fleet.CountryProbeResult, error) {
	f.mu.Lock()
	n := len(f.calls)
	c := call{node: nodeID, req: req}
	f.calls = append(f.calls, c)
	script := f.script
	f.mu.Unlock()
	if script != nil {
		return script(n, c)
	}
	return openAll(c), nil
}

func openAll(c call) fleet.CountryProbeResult {
	res := fleet.CountryProbeResult{DPIHop: 4, ControlOK: true, Verdicts: map[string]string{}}
	for _, t := range c.req.Targets {
		for _, p := range t.Ports {
			res.Verdicts[t.Host+":"+strconv.Itoa(int(p))] = "open"
		}
	}
	return res
}

func (f *fakeFleet) sent() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

type env struct {
	t       *testing.T
	st      *store.Store
	ff      *fakeFleet
	svc     *Service
	logs    *bytes.Buffer
	steps   int
	stepErr error
	changed int
	mu      sync.Mutex
	now     time.Time
	country string
	edge    bool
	nodes   []Node
}

func newEnv(t *testing.T, opts ...func(*env)) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{t: t, st: st, ff: &fakeFleet{}, logs: &bytes.Buffer{}, country: "RU", now: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	// a fleet of invented nodes: probe1 inside the country (declares the capability), de1 and nl1 abroad, de2 offline
	e.nodes = []Node{
		{ID: "probe1", Name: "probe1", Address: "198.51.100.1", CountryCode: "RU", Caps: []string{Capability}, Online: true, Ports: []uint16{443}},
		{ID: "de1", Name: "de1", Address: "203.0.113.10", CountryCode: "DE", Caps: []string{"tcpcheck/1"}, Online: true, Ports: []uint16{8443, 443}},
		{ID: "nl1", Name: "nl1", Address: "203.0.113.20", CountryCode: "NL", Online: true, Ports: []uint16{443}},
		{ID: "de2", Name: "de2", Address: "203.0.113.30", CountryCode: "DE", Online: false, Ports: []uint16{443}},
	}
	for _, o := range opts {
		o(e)
	}
	e.svc = e.newService()
	return e
}

func (e *env) newService() *Service {
	svc, err := New(Config{Store: e.st, Fleet: e.ff, Edge: e.edge,
		StepUp: func(context.Context) error { e.steps++; return e.stepErr },
		Log:    slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:    func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now },
		Country: func(context.Context) string {
			e.mu.Lock()
			defer e.mu.Unlock()
			return e.country
		},
		Nodes: func(context.Context) ([]Node, error) {
			e.mu.Lock()
			defer e.mu.Unlock()
			return slices.Clone(e.nodes), nil
		},
		OnChange: func(context.Context) { e.changed++ }})
	if err != nil {
		e.t.Fatal(err)
	}
	return svc
}

func (e *env) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }

func (e *env) setNodes(f func([]Node) []Node) {
	e.mu.Lock()
	e.nodes = f(e.nodes)
	e.mu.Unlock()
}

func ownerCtx() context.Context {
	return auth.WithAdmin(context.Background(), store.Admin{ID: "adm_owner", Role: store.RoleOwner})
}

func ptr[T any](v T) *T { return &v }

func connectCode(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return 0
}

func (e *env) update(req *adminv1.UpdateTspuProbeRequest) (*adminv1.TspuProbeSettings, error) {
	r, err := (rpc{e.svc}).UpdateTspuProbe(ownerCtx(), connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return r.Msg.Settings, nil
}

// enable switches the source on with probe1 as the probe node.
func (e *env) enable() {
	e.t.Helper()
	if _, err := e.update(&adminv1.UpdateTspuProbeRequest{Enabled: ptr(true), ProbeNodeId: ptr("probe1")}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) get() *adminv1.TspuProbeSettings {
	e.t.Helper()
	r, err := (rpc{e.svc}).GetTspuProbe(ownerCtx(), connect.NewRequest(&adminv1.GetTspuProbeRequest{}))
	if err != nil {
		e.t.Fatal(err)
	}
	return r.Msg.Settings
}

func (e *env) test() (*adminv1.TestTspuProbeResponse, error) {
	r, err := (rpc{e.svc}).TestTspuProbe(ownerCtx(), connect.NewRequest(&adminv1.TestTspuProbeRequest{}))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

func (e *env) rows() map[string]store.TSPUCheck {
	e.t.Helper()
	rows, err := e.st.RecentTSPUChecks(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]store.TSPUCheck{}
	for _, r := range rows {
		out[r.NodeID] = r
	}
	return out
}

func (e *env) tick() bool { return e.svc.Tick(context.Background()) }

func (e *env) lastRun() lastRun {
	e.t.Helper()
	st, err := e.svc.load(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return st.last
}

// ---------------------------------------------------------------------------------------------------

func TestOffByDefaultNothingIsSent(t *testing.T) {
	e := newEnv(t)
	s := e.get()
	if s.Enabled || !s.Available || s.UnavailableReason != "" || s.ProbeNodeId != "" || s.IntervalMin != 120 || s.LastRun != nil {
		t.Fatalf("defaults = %+v", s)
	}
	if st := e.svc.State(context.Background()); st.On || st.Run != nil {
		t.Fatalf("State while off = %+v", st)
	}
	for range 3 {
		if e.tick() {
			t.Fatal("a run happened while off")
		}
		e.advance(3 * time.Hour)
	}
	if _, err := e.test(); connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "tspu_off") {
		t.Fatalf("Test while off = %v", err)
	}
	// on, but no probe node chosen: still nothing
	if _, err := e.update(&adminv1.UpdateTspuProbeRequest{Enabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if e.tick() {
		t.Fatal("a run happened without a probe node")
	}
	if _, err := e.test(); connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "tspu_no_probe_node") {
		t.Fatalf("Test without a probe node = %v", err)
	}
	if n := len(e.ff.sent()); n != 0 {
		t.Fatalf("%d commands were sent, want none", n)
	}
}

// The picker lists every active node, the ones in the selected country first, each with what it can do.
func TestGetListsTheProbeNodeCandidates(t *testing.T) {
	e := newEnv(t)
	e.setNodes(func(ns []Node) []Node {
		return append(ns, Node{ID: "ru2", Name: "aaa-ru2", CountryCode: "ru", Online: false})
	})
	s := e.get()
	var got []string
	for _, n := range s.Nodes {
		got = append(got, n.Name+":"+strconv.FormatBool(n.InCountry)+":"+strconv.FormatBool(n.Declares)+":"+strconv.FormatBool(n.Online))
	}
	want := []string{"aaa-ru2:true:false:false", "probe1:true:true:true", "de1:false:false:true", "de2:false:false:false", "nl1:false:false:true"}
	if !slices.Equal(got, want) {
		t.Fatalf("nodes = %v, want %v", got, want)
	}
}

func TestUpdateNeedsOwnerAndStepUp(t *testing.T) {
	e := newEnv(t)
	for name, ctx := range map[string]context.Context{
		"no admin": context.Background(),
		"helper":   auth.WithAdmin(context.Background(), store.Admin{ID: "adm_x", Role: store.RoleHelper}),
		"token":    auth.WithAdmin(context.Background(), store.Admin{ID: "token:abc", Role: store.RoleOwner}),
		"mcp":      auth.WithAdmin(context.Background(), store.Admin{ID: "mcp:abc", Role: store.RoleOwner}),
	} {
		r := rpc{e.svc}
		if _, err := r.GetTspuProbe(ctx, connect.NewRequest(&adminv1.GetTspuProbeRequest{})); connectCode(err) != connect.CodePermissionDenied {
			t.Errorf("%s: Get = %v", name, err)
		}
		if _, err := r.UpdateTspuProbe(ctx, connect.NewRequest(&adminv1.UpdateTspuProbeRequest{Enabled: ptr(true)})); connectCode(err) != connect.CodePermissionDenied {
			t.Errorf("%s: Update = %v", name, err)
		}
		if _, err := r.TestTspuProbe(ctx, connect.NewRequest(&adminv1.TestTspuProbeRequest{})); connectCode(err) != connect.CodePermissionDenied {
			t.Errorf("%s: Test = %v", name, err)
		}
		if _, err := r.DisconnectTspuProbe(ctx, connect.NewRequest(&adminv1.DisconnectTspuProbeRequest{})); connectCode(err) != connect.CodePermissionDenied {
			t.Errorf("%s: Disconnect = %v", name, err)
		}
	}
	if e.steps != 0 || e.get().Enabled {
		t.Fatalf("a refused caller changed something: steps %d", e.steps)
	}

	// Update and Disconnect ask for the step-up, Get and Test do not; a refused step-up changes nothing
	e.enable()
	if e.steps != 1 {
		t.Fatalf("Update asked for the step-up %d times, want once", e.steps)
	}
	e.stepErr = connect.NewError(connect.CodePermissionDenied, errors.New("step_up_required"))
	if _, err := e.update(&adminv1.UpdateTspuProbeRequest{Enabled: ptr(false)}); err == nil || !strings.Contains(err.Error(), "step_up_required") {
		t.Fatalf("Update without a step-up = %v", err)
	}
	if _, err := (rpc{e.svc}).DisconnectTspuProbe(ownerCtx(), connect.NewRequest(&adminv1.DisconnectTspuProbeRequest{})); err == nil || !strings.Contains(err.Error(), "step_up_required") {
		t.Fatalf("Disconnect without a step-up = %v", err)
	}
	e.stepErr = nil
	if s := e.get(); !s.Enabled || s.ProbeNodeId != "probe1" {
		t.Fatalf("a refused step-up changed the settings: %+v", s)
	}
	steps := e.steps
	e.get()
	if _, err := e.test(); err != nil {
		t.Fatal(err)
	}
	if e.steps != steps {
		t.Fatal("Get or Test asked for a step-up")
	}
	// Test never reads the probe from a token either: the policy table keeps tokens out, the handler checks again (above)
}

func TestUpdateValidatesTheSettings(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		name string
		req  *adminv1.UpdateTspuProbeRequest
		code connect.Code
		msg  string
	}{
		{"interval too short", &adminv1.UpdateTspuProbeRequest{IntervalMin: ptr(uint32(29))}, connect.CodeInvalidArgument, "interval_min"},
		{"interval too long", &adminv1.UpdateTspuProbeRequest{IntervalMin: ptr(uint32(1441))}, connect.CodeInvalidArgument, "interval_min"},
		{"unknown node", &adminv1.UpdateTspuProbeRequest{ProbeNodeId: ptr("nope")}, connect.CodeInvalidArgument, "probe_node_unknown"},
		{"a node without the capability", &adminv1.UpdateTspuProbeRequest{ProbeNodeId: ptr("de1")}, connect.CodeInvalidArgument, "probe_node_not_capable"},
	} {
		if _, err := e.update(tc.req); connectCode(err) != tc.code || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	e.setNodes(func(ns []Node) []Node { ns[0].Online = false; return ns })
	if _, err := e.update(&adminv1.UpdateTspuProbeRequest{ProbeNodeId: ptr("probe1")}); connectCode(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "probe_node_offline") {
		t.Errorf("an offline node: %v", err)
	}
	e.setNodes(func(ns []Node) []Node { ns[0].Online = true; return ns })
	s, err := e.update(&adminv1.UpdateTspuProbeRequest{ProbeNodeId: ptr("probe1"), IntervalMin: ptr(uint32(30)), Enabled: ptr(true)})
	if err != nil || s.ProbeNodeId != "probe1" || s.IntervalMin != 30 || !s.Enabled {
		t.Fatalf("a good update = %+v, %v", s, err)
	}
	// absent fields stay; "" clears the node
	s, err = e.update(&adminv1.UpdateTspuProbeRequest{Enabled: ptr(false)})
	if err != nil || s.ProbeNodeId != "probe1" || s.IntervalMin != 30 || s.Enabled {
		t.Fatalf("switching off = %+v, %v", s, err)
	}
	if s, err = e.update(&adminv1.UpdateTspuProbeRequest{ProbeNodeId: ptr("")}); err != nil || s.ProbeNodeId != "" {
		t.Fatalf("clearing the node = %+v, %v", s, err)
	}
	if e.changed == 0 {
		t.Error("the alerts were not told about the change")
	}
}

// A run is due when the newest row of the probe node is older than the interval; the clock is read from the store, so a restart
// does not fire one at once.
func TestScheduleDueLogicAndRestart(t *testing.T) {
	e := newEnv(t)
	e.enable()
	if !e.tick() {
		t.Fatal("the first run did not happen")
	}
	if n := len(e.ff.sent()); n != 1 {
		t.Fatalf("%d commands for the first run, want 1 (two nodes, three pairs)", n)
	}
	if e.tick() {
		t.Fatal("a second run right after the first")
	}
	e.advance(119 * time.Minute)
	if e.tick() {
		t.Fatal("a run before the interval ended")
	}
	// a restart: a new process on the same store does not fire either
	svc2 := e.newService()
	if svc2.Tick(context.Background()) {
		t.Fatal("a restart ran at once")
	}
	e.advance(2 * time.Minute)
	if !svc2.Tick(context.Background()) {
		t.Fatal("the run after the interval did not happen")
	}
	if e.changed < 2 {
		t.Errorf("the alerts were told %d times, want once per run", e.changed)
	}

	// the interval is the owner's, 30 minutes at the shortest
	if _, err := e.update(&adminv1.UpdateTspuProbeRequest{IntervalMin: ptr(uint32(30))}); err != nil {
		t.Fatal(err)
	}
	e.advance(29 * time.Minute)
	if e.tick() {
		t.Fatal("a run before the shorter interval ended")
	}
	e.advance(2 * time.Minute)
	if !e.tick() {
		t.Fatal("the run after the shorter interval did not happen")
	}
	// the probe node offline: skipped without a trace
	e.setNodes(func(ns []Node) []Node { ns[0].Online = false; return ns })
	before := len(e.ff.sent())
	e.advance(time.Hour)
	if e.tick() || len(e.ff.sent()) != before {
		t.Fatal("a run was sent to an offline probe node")
	}
	// a different probe node has no rows: due at once
	e.setNodes(func(ns []Node) []Node {
		ns[0].Online = true
		return append(ns, Node{ID: "probe2", Name: "probe2", Address: "198.51.100.2", CountryCode: "RU", Caps: []string{Capability}, Online: true, Ports: []uint16{443}})
	})
	if _, err := e.update(&adminv1.UpdateTspuProbeRequest{ProbeNodeId: ptr("probe2")}); err != nil {
		t.Fatal(err)
	}
	if !e.tick() {
		t.Fatal("a new probe node did not run at once")
	}
	if last := e.ff.sent()[len(e.ff.sent())-1]; last.node != "probe2" {
		t.Fatalf("the command went to %q", last.node)
	}
}

// The targets are the other online nodes: never the probe node, never an offline one; 443 first; a node's ports stay together;
// at most 8 pairs a command; each batch is stored as its answer arrives.
func TestBatchingAndTargets(t *testing.T) {
	e := newEnv(t)
	e.setNodes(func(ns []Node) []Node {
		ns[1].Ports = []uint16{9000, 8443, 443, 2053, 2083} // five ports: the agent takes four
		for i := range 4 {
			id := "x" + strconv.Itoa(i)
			ns = append(ns, Node{ID: id, Name: id, Address: "192.0.2." + strconv.Itoa(10+i), Online: true, Ports: []uint16{443, 8443}})
		}
		// the same machine as the probe node, and a node with no TCP port: nothing to test
		ns = append(ns, Node{ID: "twin", Name: "twin", Address: "198.51.100.1", Online: true, Ports: []uint16{443}},
			Node{ID: "udp1", Name: "udp1", Address: "192.0.2.99", Online: true})
		return ns
	})
	// by the time the second command arrives, the first batch must be in the store
	e.ff.script = func(n int, c call) (fleet.CountryProbeResult, error) {
		if n == 1 && len(e.rows()) == 0 {
			t.Error("the first batch was not stored when the second was sent")
		}
		return openAll(c), nil
	}
	e.enable()
	if !e.tick() {
		t.Fatal("no run")
	}
	calls := e.ff.sent()
	var seen []string
	for i, c := range calls {
		if c.node != "probe1" {
			t.Errorf("command %d went to %q", i, c.node)
		}
		if c.req.Control != "77.88.55.88" || c.req.ControlSNI != "www.yandex.ru" || c.req.BlockedRef != "31.13.72.36" {
			t.Errorf("command %d references: %+v", i, c.req)
		}
		pairs := 0
		for _, tg := range c.req.Targets {
			pairs += len(tg.Ports)
			if len(tg.Ports) > 4 || tg.Ports[0] != 443 {
				t.Errorf("command %d: %s ports %v, want 443 first and at most four", i, tg.Host, tg.Ports)
			}
			seen = append(seen, tg.Host)
		}
		if pairs > 8 {
			t.Errorf("command %d has %d pairs, want at most 8", i, pairs)
		}
	}
	// de1 (4 ports) + nl1 (1) + x0 (2) = 7; x1 (2) would make 9, so it starts the second command: x1 x2 x3
	want := []string{"203.0.113.10", "203.0.113.20", "192.0.2.10", "192.0.2.11", "192.0.2.12", "192.0.2.13"}
	if len(calls) != 2 || !slices.Equal(seen, want) {
		t.Fatalf("%d commands to %v, want 2 commands to %v", len(calls), seen, want)
	}
	if got := calls[0].req.Targets[0].Ports; !slices.Equal(got, []uint16{443, 2053, 2083, 8443}) {
		t.Errorf("the five ports of de1 became %v", got)
	}
	rows := e.rows()
	if len(rows) != 6 {
		t.Fatalf("%d rows, want 6 nodes: %v", len(rows), rows)
	}
	for _, id := range []string{"probe1", "de2", "twin", "udp1"} {
		if _, ok := rows[id]; ok {
			t.Errorf("a row for %s, which must not be probed", id)
		}
	}
	if r := rows["de1"]; r.Vantage != "probe1" || r.Verdict != "open" || r.DPIHop != 4 || !r.ControlOK || r.DetailJSON != `[{"port":443,"verdict":"open"},{"port":2053,"verdict":"open"},{"port":2083,"verdict":"open"},{"port":8443,"verdict":"open"}]` {
		t.Errorf("de1 row = %+v", r)
	}
}

// What the agent says is folded per node: all ports blocked is blocked, some is partial, a refused port is not blocked, and a
// pair the agent gave no word for counts for nothing.
func TestVerdictsAreFoldedPerNode(t *testing.T) {
	e := newEnv(t)
	e.setNodes(func(ns []Node) []Node {
		ns[1].Ports = []uint16{443, 8443} // de1
		ns[2].Ports = []uint16{443, 8443} // nl1
		return append(ns, Node{ID: "fi1", Name: "fi1", Address: "203.0.113.40", Online: true, Ports: []uint16{443}})
	})
	e.ff.script = func(_ int, c call) (fleet.CountryProbeResult, error) {
		return fleet.CountryProbeResult{DPIHop: 5, ControlOK: true, Verdicts: map[string]string{
			"203.0.113.10:443": "blocked", "203.0.113.10:8443": "blocked", // de1: blocked
			"203.0.113.20:443": "blocked", "203.0.113.20:8443": "open", // nl1: partial
			// fi1: no word
		}}, nil
	}
	e.enable()
	r, err := e.test()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, n := range r.Nodes {
		got[n.Name] = n.Verdict
	}
	if got["de1"] != "blocked" || got["nl1"] != "partial" || got["fi1"] != "unknown" || len(got) != 3 {
		t.Fatalf("verdicts = %v", got)
	}
	if r.Run.Status != "ok" || r.Run.DpiHop != 5 || r.Run.NodesChecked != 3 || r.Run.NodeId != "probe1" {
		t.Fatalf("run = %+v", r.Run)
	}
	var nl1 *adminv1.TspuNodeVerdict
	for _, n := range r.Nodes {
		if n.Name == "nl1" {
			nl1 = n
		}
	}
	if len(nl1.Ports) != 2 || nl1.Ports[0].Port != 443 || nl1.Ports[0].Verdict != "blocked" || nl1.Ports[1].Verdict != "open" {
		t.Fatalf("nl1 ports = %v", nl1.Ports)
	}
	rows := e.rows()
	if rows["nl1"].Verdict != "partial" || rows["fi1"].Verdict != "unknown" || rows["fi1"].DetailJSON != `[{"port":443,"verdict":"timeout"}]` {
		t.Fatalf("rows = %+v", rows)
	}
}

// Each word the agent can answer with: ok stores rows; control_failed and no_dpi_hop store only the run status; busy is asked
// again soon; a transport error skips the round and is written down.
func TestEachAnswerCode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		answer   func(c call) (fleet.CountryProbeResult, error)
		status   string
		detail   string
		rows     bool
		retryIn  time.Duration // when the next run is due after this one
		dpiHop   int
		callsMin int
	}{
		{"ok", func(c call) (fleet.CountryProbeResult, error) { return openAll(c), nil }, "ok", "", true, 120 * time.Minute, 4, 1},
		{"control_failed", func(call) (fleet.CountryProbeResult, error) {
			return fleet.CountryProbeResult{Code: "control_failed", DPIHop: 3}, nil
		}, "control_failed", "", false, 120 * time.Minute, 3, 1},
		{"no_dpi_hop", func(call) (fleet.CountryProbeResult, error) { return fleet.CountryProbeResult{Code: "no_dpi_hop"}, nil }, "no_dpi_hop", "", false, 120 * time.Minute, 0, 2},
		{"busy", func(call) (fleet.CountryProbeResult, error) { return fleet.CountryProbeResult{Code: "busy"}, nil }, "busy", "", false, busyRetry, 0, 1},
		{"agent too old", func(call) (fleet.CountryProbeResult, error) {
			return fleet.CountryProbeResult{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("agent too old"))
		}, "failed", "agent_too_old", false, 120 * time.Minute, 0, 1},
		{"link lost", func(call) (fleet.CountryProbeResult, error) {
			return fleet.CountryProbeResult{}, errors.New("link lost")
		}, "failed", "unreachable", false, 120 * time.Minute, 0, 1},
		{"timeout", func(call) (fleet.CountryProbeResult, error) {
			return fleet.CountryProbeResult{}, context.DeadlineExceeded
		}, "failed", "timeout", false, 120 * time.Minute, 0, 1},
		{"agent refuses the run", func(call) (fleet.CountryProbeResult, error) {
			return fleet.CountryProbeResult{Code: "unsupported_host"}, nil
		}, "failed", "agent_error", false, 120 * time.Minute, 0, 1},
		{"agent cannot do it", func(call) (fleet.CountryProbeResult, error) {
			return fleet.CountryProbeResult{Code: "unsupported"}, nil
		}, "failed", "agent_unsupported", false, 120 * time.Minute, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.ff.script = func(_ int, c call) (fleet.CountryProbeResult, error) { return tc.answer(c) }
			e.enable()
			if !e.tick() {
				t.Fatal("no run")
			}
			if n := len(e.ff.sent()); n != tc.callsMin {
				t.Errorf("%d commands, want %d", n, tc.callsMin)
			}
			r := e.lastRun()
			if r.Status != tc.status || r.Detail != tc.detail || r.Node != "probe1" || r.DPIHop != tc.dpiHop || r.At != e.now.Unix() {
				t.Errorf("last run = %+v", r)
			}
			if got := len(e.rows()) > 0; got != tc.rows {
				t.Errorf("rows stored = %v, want %v", got, tc.rows)
			}
			// the next run is due after retryIn, not before
			sent := len(e.ff.sent())
			e.advance(tc.retryIn - time.Second)
			if e.tick() || len(e.ff.sent()) != sent {
				t.Errorf("asked again before %v", tc.retryIn)
			}
			e.advance(2 * time.Second)
			if !e.tick() {
				t.Errorf("not asked again after %v", tc.retryIn)
			}
			// the status reaches the screens: Get and the health module's state
			if g := e.get(); g.LastRun == nil || g.LastRun.Status != tc.status {
				t.Errorf("Get last_run = %v", g.LastRun)
			}
			if st := e.svc.State(context.Background()); !st.On || st.Vantage != "probe1" || st.MaxAge != 360*time.Minute || st.Run == nil {
				t.Errorf("State = %+v", st)
			}
		})
	}
}

// A run that fails in the middle keeps what the earlier batches stored and writes the failure down.
func TestAFailureInTheMiddleKeepsTheEarlierBatches(t *testing.T) {
	e := newEnv(t)
	e.setNodes(func(ns []Node) []Node {
		for i := range 5 {
			id := "x" + strconv.Itoa(i)
			ns = append(ns, Node{ID: id, Name: id, Address: "192.0.2." + strconv.Itoa(10+i), Online: true, Ports: []uint16{443, 8443}})
		}
		return ns
	})
	e.ff.script = func(n int, c call) (fleet.CountryProbeResult, error) {
		if n == 1 {
			return fleet.CountryProbeResult{Code: "control_failed", DPIHop: 4}, nil
		}
		return openAll(c), nil
	}
	e.enable()
	if !e.tick() {
		t.Fatal("no run")
	}
	rows := e.rows()
	if len(rows) == 0 || len(rows) >= 7 {
		t.Fatalf("%d rows, want only the first batch", len(rows))
	}
	if r := e.lastRun(); r.Status != "control_failed" || r.Checked != len(rows) {
		t.Fatalf("last run = %+v", r)
	}
}

// A probe node whose TSPU cannot be placed with the first reference is asked once more with the next; the one that worked is
// recorded and is where the next run starts.
func TestSecondReferenceOnNoDPIHop(t *testing.T) {
	e := newEnv(t)
	refs, _ := ReferencesFor("RU")
	if len(refs.Blocked) < 2 {
		t.Fatal("the candidate list must hold at least two references")
	}
	e.ff.script = func(_ int, c call) (fleet.CountryProbeResult, error) {
		if c.req.BlockedRef == refs.Blocked[0] {
			return fleet.CountryProbeResult{Code: "no_dpi_hop"}, nil
		}
		return openAll(c), nil
	}
	e.enable()
	if !e.tick() {
		t.Fatal("no run")
	}
	calls := e.ff.sent()
	if len(calls) != 2 || calls[0].req.BlockedRef != refs.Blocked[0] || calls[1].req.BlockedRef != refs.Blocked[1] {
		t.Fatalf("references tried: %+v", calls)
	}
	if r := e.lastRun(); r.Status != "ok" || r.Ref != refs.Blocked[1] {
		t.Fatalf("last run = %+v, want ok through %s", r, refs.Blocked[1])
	}
	if g := e.get(); g.LastRun.BlockedRef != refs.Blocked[1] || g.LastRun.Status != "ok" {
		t.Fatalf("Get last_run = %v", g.LastRun)
	}
	// the next run starts with the one that worked
	e.advance(121 * time.Minute)
	if !e.tick() {
		t.Fatal("no second run")
	}
	if calls = e.ff.sent(); len(calls) != 3 || calls[2].req.BlockedRef != refs.Blocked[1] {
		t.Fatalf("the second run started with %+v", calls[2:])
	}
	// if the second fails too, the run says so, after two tries only
	e.ff.script = func(int, call) (fleet.CountryProbeResult, error) {
		return fleet.CountryProbeResult{Code: "no_dpi_hop"}, nil
	}
	e.advance(121 * time.Minute)
	e.tick()
	if calls = e.ff.sent(); len(calls) != 5 {
		t.Fatalf("%d commands for a run that never placed the TSPU, want 2", len(calls)-3)
	}
	if r := e.lastRun(); r.Status != "no_dpi_hop" {
		t.Fatalf("last run = %+v", r)
	}
}

func TestDisconnectDeletesTheRowsAndTheSettings(t *testing.T) {
	e := newEnv(t)
	e.enable()
	if _, err := e.update(&adminv1.UpdateTspuProbeRequest{IntervalMin: ptr(uint32(45))}); err != nil {
		t.Fatal(err)
	}
	if !e.tick() || len(e.rows()) == 0 {
		t.Fatal("no run, or no rows")
	}
	disconnect := func() *adminv1.TspuProbeSettings {
		r, err := (rpc{e.svc}).DisconnectTspuProbe(ownerCtx(), connect.NewRequest(&adminv1.DisconnectTspuProbeRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg.Settings
	}
	s := disconnect()
	if s.Enabled || s.ProbeNodeId != "" || s.IntervalMin != 120 || s.LastRun != nil {
		t.Fatalf("after Disconnect = %+v", s)
	}
	if len(e.rows()) != 0 {
		t.Fatalf("rows left: %v", e.rows())
	}
	for _, k := range []string{settingEnabled, settingNode, settingInterval, settingLastRun} {
		if v, err := e.st.Setting(context.Background(), k); err == nil && v != "" {
			t.Errorf("setting %s = %q left", k, v)
		}
	}
	// idempotent, and nothing runs afterwards
	disconnect()
	e.advance(5 * time.Hour)
	if e.tick() {
		t.Fatal("a run after Disconnect")
	}
	if st := e.svc.State(context.Background()); st.On {
		t.Fatalf("State after Disconnect = %+v", st)
	}
}

// Off keeps the rows (they are hidden, like the other sources) and stops the schedule; switching on again picks it up.
func TestSwitchingOffStopsTheScheduleAndHidesTheResults(t *testing.T) {
	e := newEnv(t)
	e.enable()
	if !e.tick() {
		t.Fatal("no run")
	}
	if _, err := e.update(&adminv1.UpdateTspuProbeRequest{Enabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if st := e.svc.State(context.Background()); st.On || st.Run != nil {
		t.Fatalf("State while off = %+v", st)
	}
	if g := e.get(); g.Enabled || g.LastRun != nil || g.ProbeNodeId != "probe1" {
		t.Fatalf("Get while off = %+v", g)
	}
	sent := len(e.ff.sent())
	e.advance(5 * time.Hour)
	if e.tick() || len(e.ff.sent()) != sent {
		t.Fatal("a run while off")
	}
	if len(e.rows()) == 0 {
		t.Fatal("switching off must keep the rows until the retention")
	}
	if _, err := e.update(&adminv1.UpdateTspuProbeRequest{Enabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if !e.tick() {
		t.Fatal("no run after switching on")
	}
}

// A run in flight when the source is disconnected stores nothing and writes no status.
func TestDisconnectInTheMiddleOfARunStoresNothing(t *testing.T) {
	e := newEnv(t)
	e.enable()
	inFlight, release := make(chan struct{}), make(chan struct{})
	e.ff.script = func(_ int, c call) (fleet.CountryProbeResult, error) {
		close(inFlight)
		<-release
		return openAll(c), nil // a late answer that must be dropped
	}
	done := make(chan bool)
	go func() { done <- e.tick() }()
	<-inFlight
	if _, err := (rpc{e.svc}).DisconnectTspuProbe(ownerCtx(), connect.NewRequest(&adminv1.DisconnectTspuProbeRequest{})); err != nil {
		t.Fatal(err)
	}
	close(release)
	if <-done {
		t.Fatal("the cancelled run reported success")
	}
	if len(e.rows()) != 0 || e.lastRun().At != 0 {
		t.Fatalf("a cancelled run stored rows %v / status %+v", e.rows(), e.lastRun())
	}
}

func TestAnotherCountryIsNotAvailable(t *testing.T) {
	e := newEnv(t)
	e.enable()
	e.mu.Lock()
	e.country = "BY"
	e.mu.Unlock()
	s := e.get()
	if s.Available || s.UnavailableReason != "country" || !s.Enabled {
		t.Fatalf("Get for another country = %+v", s)
	}
	if e.tick() || len(e.ff.sent()) != 0 {
		t.Fatal("a run for another country")
	}
	if _, err := e.test(); connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "tspu_unavailable_for_country") {
		t.Fatalf("Test for another country = %v", err)
	}
	if st := e.svc.State(context.Background()); st.On {
		t.Fatalf("State for another country = %+v", st)
	}
	e.mu.Lock()
	e.country = "ru"
	e.mu.Unlock()
	if !e.get().Available || !e.tick() {
		t.Fatal("back to Russia the source did not work (the country is case-insensitive)")
	}
}

func TestTestIsRateLimited(t *testing.T) {
	e := newEnv(t)
	e.enable()
	// a refusal before the run does not use up the minute
	e.setNodes(func(ns []Node) []Node { ns[0].Online = false; return ns })
	if _, err := e.test(); connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "tspu_probe_node_unfit") {
		t.Fatalf("Test with an offline probe node = %v", err)
	}
	e.setNodes(func(ns []Node) []Node { ns[0].Online = true; return ns })
	if r, err := e.test(); err != nil || r.Run.Status != "ok" || len(r.Nodes) != 2 {
		t.Fatalf("first Test = %v, %v", r, err)
	}
	if _, err := e.test(); connectCode(err) != connect.CodeResourceExhausted {
		t.Fatalf("second Test within the minute = %v", err)
	}
	if n := len(e.ff.sent()); n != 1 {
		t.Fatalf("%d commands, want 1", n)
	}
	e.advance(59 * time.Second)
	if _, err := e.test(); connectCode(err) != connect.CodeResourceExhausted {
		t.Fatalf("Test after 59 s = %v", err)
	}
	e.advance(2 * time.Second)
	if _, err := e.test(); err != nil {
		t.Fatalf("Test after 61 s = %v", err)
	}
	// a Test counts as the run of the schedule: the next scheduled one waits for the interval
	if e.tick() {
		t.Fatal("a scheduled run right after a Test")
	}
}

// A Test while a scheduled run is in progress says so and sends nothing.
func TestTestDuringARun(t *testing.T) {
	e := newEnv(t)
	e.enable()
	inFlight, release := make(chan struct{}), make(chan struct{})
	e.ff.script = func(_ int, c call) (fleet.CountryProbeResult, error) {
		close(inFlight)
		<-release
		return openAll(c), nil
	}
	done := make(chan bool)
	go func() { done <- e.tick() }()
	<-inFlight
	if _, err := e.test(); connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "tspu_run_in_progress") {
		t.Fatalf("Test during a run = %v", err)
	}
	close(release)
	if !<-done {
		t.Fatal("the scheduled run did not finish")
	}
	if n := len(e.ff.sent()); n != 1 {
		t.Fatalf("%d commands, want 1", n)
	}
}

// The Cloudflare edition: Get says the source is not available, Update and Test are refused, nothing runs, and Disconnect still
// cleans up.
func TestEdgeEditionRunsNothing(t *testing.T) {
	e := newEnv(t, func(e *env) { e.edge = true })
	s := e.get()
	if s.Available || s.UnavailableReason != "edition" || s.Enabled {
		t.Fatalf("Get on the edge = %+v", s)
	}
	for name, err := range map[string]error{
		"Update": func() error { _, err := e.update(&adminv1.UpdateTspuProbeRequest{Enabled: ptr(true)}); return err }(),
		"Test":   func() error { _, err := e.test(); return err }(),
	} {
		if connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "unavailable_in_edition: feature=tspu_probe") {
			t.Errorf("%s on the edge = %v", name, err)
		}
	}
	// settings that arrived some other way (a database from a VPS panel) still run nothing
	if err := e.st.SetSettings(context.Background(), map[string]string{settingEnabled: "1", settingNode: "probe1"}); err != nil {
		t.Fatal(err)
	}
	if e.tick() || len(e.ff.sent()) != 0 {
		t.Fatal("a run on the edge")
	}
	if st := e.svc.State(context.Background()); st.On {
		t.Fatalf("State on the edge = %+v", st)
	}
	if e.steps != 0 {
		t.Errorf("a refused Update asked for the step-up %d times", e.steps)
	}
	if err := e.st.InsertTSPUChecks(context.Background(), []store.TSPUCheck{{NodeID: "de1", Vantage: "probe1", At: e.now, Verdict: "open"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := (rpc{e.svc}).DisconnectTspuProbe(ownerCtx(), connect.NewRequest(&adminv1.DisconnectTspuProbeRequest{})); err != nil {
		t.Fatal(err)
	}
	if len(e.rows()) != 0 {
		t.Fatal("Disconnect on the edge left rows")
	}
}

// Nothing is ever sent to a real address: the references and the targets of these tests are documentation addresses or the
// built-in references, and the fake fleet opens no socket. This keeps the list of built-in references honest.
func TestReferencesAreWellFormed(t *testing.T) {
	refs, ok := ReferencesFor("ru")
	if !ok || refs.Control != "77.88.55.88" || refs.ControlSNI != "www.yandex.ru" {
		t.Fatalf("RU references = %+v, %v", refs, ok)
	}
	if len(refs.Blocked) < 2 || len(refs.Blocked) > 3 {
		t.Fatalf("%d blocked references, want 2 or 3", len(refs.Blocked))
	}
	seen := map[string]bool{}
	for _, r := range refs.Blocked {
		if seen[r] || r == refs.Control || strings.Count(r, ".") != 3 {
			t.Errorf("reference %q is a duplicate, the control, or not an IPv4 address", r)
		}
		seen[r] = true
	}
	if _, ok := ReferencesFor("BY"); ok {
		t.Error("a country without references must say so")
	}
}

func TestOrderPortsAndBatches(t *testing.T) {
	if got := orderPorts([]uint16{8443, 443, 2053, 443, 9000, 1}); !slices.Equal(got, []uint16{443, 1, 2053, 8443}) {
		t.Errorf("orderPorts = %v", got)
	}
	if got := orderPorts(nil); len(got) != 0 {
		t.Errorf("orderPorts(nil) = %v", got)
	}
	node := func(n int) Node { return Node{ID: "n", Ports: make([]uint16, n)} }
	got := batches([]Node{node(4), node(4), node(1), node(4), node(4), node(4)})
	var sizes []int
	for _, b := range got {
		pairs := 0
		for _, n := range b {
			pairs += len(n.Ports)
		}
		sizes = append(sizes, pairs)
	}
	if !slices.Equal(sizes, []int{8, 5, 8}) {
		t.Errorf("batch sizes = %v, want [8 5 8]", sizes)
	}
	if len(batches(nil)) != 0 {
		t.Error("batches of nothing")
	}
}

// What the screens read is the same as what the combine reads: the stored detail parses back to the ports.
func TestStoredDetailMatchesTheCombine(t *testing.T) {
	e := newEnv(t)
	e.ff.script = func(_ int, c call) (fleet.CountryProbeResult, error) {
		res := openAll(c)
		res.Verdicts["203.0.113.10:443"] = "blocked"
		return res, nil
	}
	e.enable()
	e.tick()
	r := e.rows()["de1"]
	ports := countryreach.ParseTSPUDetail(r.DetailJSON)
	if r.Verdict != "partial" || len(ports) != 2 || countryreach.TSPUVerdict(r.Verdict, ports) != countryreach.VerdictOK {
		t.Fatalf("de1 = %+v / %v: a node with an open port is reachable", r, ports)
	}
}
