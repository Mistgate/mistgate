package fleet

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
)

// fakeUpdates is the updates module as the stream sees it.
type fakeUpdates struct {
	mu       sync.Mutex
	updating map[string]bool
	served   []string // "node/name@offset"
	data     []byte
	err      error
	binary   map[string]string // "linux/amd64" -> path of the trusted bundle's file
}

func (u *fakeUpdates) NodeBinary(goos, goarch string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.binary[goos+"/"+goarch]
}

func (u *fakeUpdates) Updating(id string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.updating[id]
}

func (u *fakeUpdates) set(id string, v bool) {
	u.mu.Lock()
	if u.updating == nil {
		u.updating = map[string]bool{}
	}
	u.updating[id] = v
	u.mu.Unlock()
}

func (u *fakeUpdates) Serve(_ context.Context, nodeID, name string, offset uint64, send func([]byte, uint64) error) error {
	u.mu.Lock()
	u.served = append(u.served, nodeID+"/"+name)
	data, err := u.data, u.err
	u.mu.Unlock()
	if err != nil {
		return err
	}
	if offset > uint64(len(data)) {
		return connect.NewError(connect.CodeNotFound, errors.New("offset beyond the end of the file"))
	}
	for rest := data[offset:]; len(rest) > 0; {
		n := min(len(rest), 4)
		if err := send(rest[:n], uint64(len(data))); err != nil {
			return err
		}
		rest = rest[n:]
	}
	return nil
}

func helloWithUpdate(instance string, built int64, caps ...string) *agentv1.ConnectRequest {
	m := hello(instance, 0, "")
	m.GetHello().Built, m.GetHello().Capabilities = built, caps
	return m
}

// connectUpdater connects an agent that lists the update capability and confirms the full state.
func connectUpdater(a *agent, instance string, built int64, caps ...string) *conn {
	a.e.t.Helper()
	c := a.open()
	c.send(0, helloWithUpdate(instance, built, caps...))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	ds := c.desired()
	m := newModel()
	m.apply(ds)
	c.send(0, applied(ds, m.hash()))
	return c
}

// Hello.built, the capabilities and Hello.last_update end up on the node row (what an offline node keeps showing).
func TestHelloCarriesTheBuildAndTheLastUpdate(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	h := helloWithUpdate("inst1", 1_790_000_000, "doctor/1", "update/1", "update-guard/1")
	h.GetHello().LastUpdate = &agentv1.LastUpdate{Outcome: agentv1.UpdateOutcome_UPDATE_OUTCOME_ROLLED_BACK, FromVersion: "0.1.0-a", FromBuilt: 1_789_000_000,
		ToVersion: "0.2.0-b", ToBuilt: 1_790_000_000, Reason: "crash_loop", AtUnix: 1_790_000_100}
	c := a.open()
	c.send(0, h)
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	n := mustNode(e, a.nodeID)
	if n.AgentBuilt != 1_790_000_000 || strings.Join(n.AgentCaps, " ") != "doctor/1 update/1 update-guard/1" {
		t.Fatalf("node row: built %d caps %v", n.AgentBuilt, n.AgentCaps)
	}
	lu, ok := n.LastUpdate()
	if !ok || lu.Outcome != "rolled_back" || lu.Reason != "crash_loop" || lu.FromBuilt != 1_789_000_000 || lu.ToVersion != "0.2.0-b" || lu.AtUnix != 1_790_000_100 {
		t.Fatalf("last update: %+v %v", lu, ok)
	}

	// an agent that predates the fields: nothing new is stored, what was stored stays
	c.st.CloseRequest()
	c.ended()
	c2 := a.open()
	c2.send(0, hello("inst2", 0, ""))
	c2.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	n = mustNode(e, a.nodeID)
	if n.AgentBuilt != 0 || len(n.AgentCaps) != 0 {
		t.Fatalf("an old agent: built %d caps %v", n.AgentBuilt, n.AgentCaps)
	}
	if _, ok := n.LastUpdate(); !ok {
		t.Fatal("the stored outcome was lost")
	}
}

func TestCommandsAreOfflineAfterPanelRestartWithFreshLiveRow(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	c := connectUpdater(a, "inst-restarted", 1_790_000_000, "update/1", "doctor/1", "bandwidth/1")
	live, err := e.f.NodeLive(e.ctx, a.nodeID)
	if err != nil || !live.Exists || !live.Connected {
		t.Fatalf("fresh live row = %+v, %v", live, err)
	}
	e.f.mu.Lock()
	delete(e.f.sessions, a.nodeID)
	e.f.mu.Unlock()

	checkOffline := func(name string, err error) {
		t.Helper()
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(strings.ToLower(err.Error()), "offline") {
			t.Errorf("%s after restart = %v, want an offline precondition", name, err)
		}
	}
	_, err = e.f.UpdateAgent(e.ctx, a.nodeID, nil, nil, time.Millisecond)
	checkOffline("UpdateAgent", err)
	_, err = e.f.RunDoctor(e.ctx, a.nodeID, nil, time.Millisecond)
	checkOffline("RunDoctor", err)
	_, err = e.f.measureBandwidth(e.ctx, mustNode(e, a.nodeID))
	checkOffline("MeasureBandwidth", err)

	c.st.CloseRequest()
	c.ended()
}

func TestCommandSessionLiveReadErrorsAreInternal(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Fleet, context.Context, string) error
	}{
		{name: "update", call: func(f *Fleet, ctx context.Context, id string) error {
			_, err := f.command(ctx, id, time.Millisecond, func(reqID string) *agentv1.ConnectResponse {
				return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UpdateAgent{UpdateAgent: &agentv1.UpdateAgent{RequestId: reqID}}}
			})
			return err
		}},
		{name: "doctor", call: func(f *Fleet, ctx context.Context, id string) error {
			_, err := f.ask(ctx, id, time.Millisecond, func(reqID string) *agentv1.ConnectResponse {
				return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RunDoctor{RunDoctor: &agentv1.RunDoctor{RequestId: reqID}}}
			})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			a := e.enroll("nodea")
			e.fixture(a.nodeID)
			e.exec(`DROP TABLE node_live`)
			err := tc.call(e.f, e.ctx, a.nodeID)
			if code(err) != connect.CodeInternal || err.Error() != "internal: internal error" {
				t.Fatalf("live read error reached the command API: %v", err)
			}
		})
	}
}

// UpdateAgent reaches an agent with the capability, and its answer arrives even when the stream ends right after it
// (the agent re-executes).
func TestUpdateAgentAnswerSurvivesTheStreamEnding(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	c := connectUpdater(a, "inst1", 100, "update/1")

	type out struct {
		res *agentv1.CommandResult
		err error
	}
	done := make(chan out, 1)
	go func() {
		res, err := e.f.UpdateAgent(e.ctx, a.nodeID, []byte("manifest-bytes"), []byte("signature-bytes"), 10*time.Second)
		done <- out{res, err}
	}()
	msg := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetUpdateAgent() != nil }).GetUpdateAgent()
	if msg.RequestId == "" || !bytes.Equal(msg.Manifest, []byte("manifest-bytes")) || !bytes.Equal(msg.Signature, []byte("signature-bytes")) {
		t.Fatalf("UpdateAgent: %+v", msg)
	}
	c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: &agentv1.CommandResult{
		RequestId: msg.RequestId, Ok: true, Affected: 1, Params: map[string]string{"to_version": "0.2.0"}}}})
	c.st.CloseRequest() // the agent re-executes: the stream is gone at once
	got := <-done
	if got.err != nil || !got.res.Ok || got.res.Params["to_version"] != "0.2.0" {
		t.Fatalf("answer: %+v %v", got.res, got.err)
	}
}

func TestRollbackAgentRoundTrip(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	c := connectUpdater(a, "inst1", 100, "update/1")
	done := make(chan *agentv1.CommandResult, 1)
	go func() {
		res, err := e.f.RollbackAgent(e.ctx, a.nodeID, 10*time.Second)
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	msg := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetRollbackAgent() != nil }).GetRollbackAgent()
	c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: &agentv1.CommandResult{
		RequestId: msg.RequestId, Ok: false, Error: "no_previous"}}})
	if res := <-done; res == nil || res.Ok || res.Error != "no_previous" {
		t.Fatalf("answer %+v", res)
	}
}

// Nothing is ever sent to a node that did not list update/1, or is not connected.
func TestUpdateCommandsNeedTheCapability(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	_, err := e.f.UpdateAgent(e.ctx, a.nodeID, []byte("m"), []byte("s"), time.Second)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("offline: %v", err)
	}
	c := connectUpdater(a, "inst1", 100, "doctor/1") // no update/1
	for name, call := range map[string]func() error{
		"UpdateAgent": func() error {
			_, err := e.f.UpdateAgent(e.ctx, a.nodeID, []byte("m"), []byte("s"), time.Second)
			return err
		},
		"RollbackAgent": func() error { _, err := e.f.RollbackAgent(e.ctx, a.nodeID, time.Second); return err },
	} {
		err := call()
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "cannot update") {
			t.Errorf("%s: %v", name, err)
		}
	}
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case m := <-c.in:
			if m.GetUpdateAgent() != nil || m.GetRollbackAgent() != nil {
				t.Fatalf("an update command reached an agent without the capability: %v", m)
			}
		case <-deadline:
			return
		}
	}
}

// FetchUpdate: only a node with a valid certificate gets bytes, and the module sees which node asked.
func TestFetchUpdateNeedsAnAgentCertificate(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	fetch := func(cli agentv1connect.AgentServiceClient, name string, offset uint64) ([]byte, uint64, error) {
		st, err := cli.FetchUpdate(e.ctx, connect.NewRequest(&agentv1.FetchUpdateRequest{Name: name, Offset: offset}))
		if err != nil {
			return nil, 0, err
		}
		var got []byte
		var total uint64
		for st.Receive() {
			got = append(got, st.Msg().Data...)
			total = st.Msg().TotalSize
		}
		return got, total, st.Err()
	}
	withCert := agentv1connect.NewAgentServiceClient(e.httpClient(&a.cert, testSNI), e.srv.URL)
	noCert := agentv1connect.NewAgentServiceClient(e.httpClient(nil, testSNI), e.srv.URL)

	// the module is not connected yet
	if _, _, err := fetch(withCert, "mistgate-node-linux-amd64", 0); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("without the module: %v", err)
	}
	u := &fakeUpdates{data: []byte("0123456789")}
	e.f.SetUpdates(u)
	got, total, err := fetch(withCert, "mistgate-node-linux-amd64", 0)
	if err != nil || string(got) != "0123456789" || total != 10 {
		t.Fatalf("download: %q %d %v", got, total, err)
	}
	got, total, err = fetch(withCert, "mistgate-node-linux-amd64", 6) // resume
	if err != nil || string(got) != "6789" || total != 10 {
		t.Fatalf("resume: %q %d %v", got, total, err)
	}
	if _, _, err := fetch(withCert, "x", 99); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("offset beyond the end: %v", err)
	}
	u.mu.Lock()
	served := append([]string(nil), u.served...)
	u.mu.Unlock()
	if len(served) != 3 || served[0] != a.nodeID+"/mistgate-node-linux-amd64" {
		t.Fatalf("the module was asked %v", served)
	}
	before := len(served)
	if _, _, err := fetch(noCert, "mistgate-node-linux-amd64", 0); err == nil {
		t.Fatal("a client without a certificate got bytes")
	}
	u.mu.Lock()
	n := len(u.served)
	u.mu.Unlock()
	if n != before {
		t.Fatal("the module was reached without a certificate")
	}
	// the module's own errors reach the agent as they are
	u.mu.Lock()
	u.err = connect.NewError(connect.CodeResourceExhausted, errors.New("too many downloads"))
	u.mu.Unlock()
	if _, _, err := fetch(withCert, "mistgate-node-linux-amd64", 0); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("busy: %v", err)
	}
}

// While a rollout step of the node is in flight its status is UPDATING and the re-exec reconnect is not a blip.
func TestUpdatingNodeIsNotABlip(t *testing.T) {
	e := newEnv(t)
	u := &fakeUpdates{}
	e.f.SetUpdates(u)
	h := &fakeHealth{}
	e.f.SetHealth(h)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	c, _, _ := connectFull(a, "inst1")

	statusOf := func() adminv1.NodeStatus { return mustStatus(t, e, a.nodeID) }
	if statusOf() != adminv1.NodeStatus_NODE_STATUS_ONLINE {
		t.Fatalf("status %v", statusOf())
	}
	u.set(a.nodeID, true)
	if statusOf() != adminv1.NodeStatus_NODE_STATUS_UPDATING {
		t.Fatalf("status while updating: %v", statusOf())
	}
	c.st.CloseRequest()
	c.ended()
	time.Sleep(100 * time.Millisecond)
	if statusOf() != adminv1.NodeStatus_NODE_STATUS_UPDATING { // not BLIP while it re-executes
		t.Fatalf("status between the streams: %v", statusOf())
	}
	e.exec(`UPDATE node SET last_seen_at = last_seen_at - 180, last_disconnected_at = last_disconnected_at - 180, last_connected_at = last_connected_at - 180 WHERE id = ?`, a.nodeID)
	c2 := a.open()
	c2.send(0, hello("inst1", 0, ""))
	c2.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	if e.count(`SELECT count(*) FROM event WHERE code IN ('node_blip', 'node_recovered')`) != 0 || len(h.returned) != 0 {
		t.Fatal("the reconnect of an updating node was recorded as a blip")
	}

	// the same gap after the update ended is a blip again
	u.set(a.nodeID, false)
	c2.st.CloseRequest()
	c2.ended()
	time.Sleep(100 * time.Millisecond)
	e.exec(`UPDATE node SET last_seen_at = last_seen_at - 180, last_disconnected_at = last_disconnected_at - 180, last_connected_at = last_connected_at - 180 WHERE id = ?`, a.nodeID)
	c3 := a.open()
	c3.send(0, hello("inst1", 0, ""))
	c3.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	if e.count(`SELECT count(*) FROM event WHERE code = 'node_blip'`) != 1 || len(h.returned) != 1 {
		t.Fatalf("blip events %d, records %d", e.count(`SELECT count(*) FROM event WHERE code = 'node_blip'`), len(h.returned))
	}
}
