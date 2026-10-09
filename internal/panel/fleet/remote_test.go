package fleet

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

type remoteAskCall struct {
	nodeID, requestID string
	frame             *agentv1.ConnectResponse
	deadline          time.Time
}

type remoteCloseCall struct {
	nodeID, reason string
}

type remotePokeCall struct {
	nodeIDs []string
}

type testRemote struct {
	mu        sync.Mutex
	askCalls  []remoteAskCall
	closeCall []remoteCloseCall
	pokeCalls []remotePokeCall
	answer    func(context.Context, remoteAskCall) (*agentv1.ConnectRequest, error)
	onPoke    func(context.Context, []string) error
	closeErr  error
}

func (r *testRemote) Ask(ctx context.Context, nodeID, requestID string, frame *agentv1.ConnectResponse, deadline time.Time) (*agentv1.ConnectRequest, error) {
	call := remoteAskCall{nodeID: nodeID, requestID: requestID, frame: frame, deadline: deadline}
	r.mu.Lock()
	r.askCalls = append(r.askCalls, call)
	answer := r.answer
	r.mu.Unlock()
	if answer == nil {
		return nil, nil
	}
	return answer(ctx, call)
}

func (r *testRemote) Close(_ context.Context, nodeID, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeCall = append(r.closeCall, remoteCloseCall{nodeID: nodeID, reason: reason})
	return r.closeErr
}

func (r *testRemote) Poke(ctx context.Context, nodeIDs []string) error {
	ids := append([]string(nil), nodeIDs...)
	r.mu.Lock()
	r.pokeCalls = append(r.pokeCalls, remotePokeCall{nodeIDs: ids})
	onPoke := r.onPoke
	r.mu.Unlock()
	if onPoke != nil {
		return onPoke(ctx, ids)
	}
	return nil
}

func (r *testRemote) calls() ([]remoteAskCall, []remoteCloseCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]remoteAskCall(nil), r.askCalls...), append([]remoteCloseCall(nil), r.closeCall...)
}

func (r *testRemote) pokes() []remotePokeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]remotePokeCall, len(r.pokeCalls))
	for i, call := range r.pokeCalls {
		out[i].nodeIDs = append([]string(nil), call.nodeIDs...)
	}
	return out
}

func TestFleetRemoteStateChangedSchedulesConnectedPoke(t *testing.T) {
	e, connected, _ := remoteReadyNode(t, "doctor/1")
	const staleNodeID = "nod_b"
	e.exec(`INSERT INTO node_live (node_id, session) VALUES (?, ?)`, staleNodeID, 1)
	e.exec(`UPDATE node SET last_seen_at = ? WHERE id = ?`, 1, staleNodeID)
	var lastSeen int64
	var liveRows int
	if err := e.st.R.QueryRowContext(context.Background(), `
		SELECT n.last_seen_at, (SELECT COUNT(*) FROM node_live WHERE node_id = n.id)
		FROM node n WHERE n.id = ?`, staleNodeID).Scan(&lastSeen, &liveRows); err != nil {
		t.Fatal(err)
	}
	if lastSeen != 1 || liveRows != 1 {
		t.Fatalf("stale node fixture has last_seen_at=%d and %d node_live rows, want 1 and 1", lastSeen, liveRows)
	}

	remote := &testRemote{}
	e.f.cfg.Remote = remote
	var work func()
	e.f.cfg.AfterResponse = func(fn func()) { work = fn }

	returned := make(chan struct{})
	go func() {
		e.f.StateChanged()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("StateChanged blocked while scheduling the fan-out")
	}
	if work == nil {
		t.Fatal("StateChanged did not hand work to AfterResponse")
	}
	if calls := remote.pokes(); len(calls) != 0 {
		t.Fatalf("Poke ran before the after-response work: %+v", calls)
	}

	work()
	calls := remote.pokes()
	if len(calls) != 1 || len(calls[0].nodeIDs) != 1 || calls[0].nodeIDs[0] != connected.nodeID {
		t.Fatalf("Poke calls = %+v, want only connected node %q", calls, connected.nodeID)
	}
}

func TestFleetRemoteStateChangedCoalescesDuringFanout(t *testing.T) {
	e, connected, _ := remoteReadyNode(t, "doctor/1")
	remote := &testRemote{}
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	remote.onPoke = func(ctx context.Context, _ []string) error {
		deadline, ok := ctx.Deadline()
		remaining := time.Until(deadline)
		if !ok {
			t.Error("fan-out context has no timeout deadline")
		} else if remaining > 20*time.Second || remaining < 19*time.Second {
			t.Errorf("fan-out context deadline has %s remaining, want 19-20 s", remaining)
		}
		once.Do(func() {
			close(started)
			<-release
		})
		return nil
	}
	e.f.cfg.Remote = remote
	var work func()
	e.f.cfg.AfterResponse = func(fn func()) { work = fn }
	e.f.StateChanged()
	if work == nil {
		t.Fatal("StateChanged did not hand work to AfterResponse")
	}
	done := make(chan struct{})
	go func() { work(); close(done) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("the first fan-out did not start")
	}
	e.f.StateChanged()
	e.f.StateChanged()
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the coalesced fan-out did not finish")
	}

	work = nil
	e.f.StateChanged()
	if work == nil {
		t.Fatal("StateChanged did not schedule new after-response work after the prior fan-out finished")
	}

	calls := remote.pokes()
	if len(calls) != 2 {
		t.Fatalf("Poke calls = %d, want one active fan-out and exactly one coalesced repeat", len(calls))
	}
	for _, call := range calls {
		if len(call.nodeIDs) != 1 || call.nodeIDs[0] != connected.nodeID {
			t.Errorf("Poke ids = %v, want only connected node %q", call.nodeIDs, connected.nodeID)
		}
	}
}

func remoteReadyNode(t *testing.T, caps ...string) (*env, *agent, *conn) {
	t.Helper()
	e := newEnv(t)
	a := e.enroll("node-a")
	e.fixture(a.nodeID)
	c := connectUpdater(a, "instance-remote", 100, caps...)
	return e, a, c
}

func TestFleetRemoteAskPassesFrameIDAndDeadline(t *testing.T) {
	e, a, _ := remoteReadyNode(t, "doctor/1", "update/1")
	before := e.f.now() // the session runs: f.now is not swapped under it
	var reply = &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_DoctorReport{
		DoctorReport: &agentv1.DoctorReport{RequestId: "remote-report"},
	}}
	remote := &testRemote{answer: func(_ context.Context, call remoteAskCall) (*agentv1.ConnectRequest, error) {
		reply.GetDoctorReport().RequestId = call.requestID
		return reply, nil
	}}
	e.f.cfg.Remote = remote
	got, err := e.f.ask(e.ctx, a.nodeID, 25*time.Second, func(requestID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RunDoctor{RunDoctor: &agentv1.RunDoctor{RequestId: requestID}}}
	})
	if err != nil || got != reply || got.GetDoctorReport() == nil {
		t.Fatalf("remote reply = %v, %v", got, err)
	}
	tasks, _ := remote.calls()
	if len(tasks) != 1 {
		t.Fatalf("remote Ask calls = %d", len(tasks))
	}
	call := tasks[0]
	if call.nodeID != a.nodeID || call.requestID == "" || call.requestID != call.frame.GetRunDoctor().RequestId {
		t.Fatalf("remote request identity/frame = %+v", call)
	}
	if after := e.f.now(); call.deadline.Before(before.Add(25*time.Second)) || call.deadline.After(after.Add(25*time.Second)) {
		t.Fatalf("remote deadline = %s, want 25 s after the call (%s..%s)", call.deadline, before, after)
	}
}

func TestFleetRemoteMapsNilAndTimeoutToNoAnswer(t *testing.T) {
	e, a, _ := remoteReadyNode(t, "update/1")
	remote := &testRemote{}
	e.f.cfg.Remote = remote
	cases := []struct {
		name   string
		answer func(context.Context, remoteAskCall) (*agentv1.ConnectRequest, error)
		want   error
	}{
		{name: "nil reply", answer: func(context.Context, remoteAskCall) (*agentv1.ConnectRequest, error) { return nil, nil }, want: errNoAnswer},
		{name: "untyped timeout text", answer: func(context.Context, remoteAskCall) (*agentv1.ConnectRequest, error) {
			return nil, errors.New("timeout") // the adapter maps NodeLink's timeout to DeadlineExceeded; text is not matched
		}, want: errLinkLost},
		{name: "context timeout", answer: func(context.Context, remoteAskCall) (*agentv1.ConnectRequest, error) {
			return nil, context.DeadlineExceeded
		}, want: errNoAnswer},
		{name: "Connect timeout", answer: func(context.Context, remoteAskCall) (*agentv1.ConnectRequest, error) {
			return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("remote deadline"))
		}, want: errNoAnswer},
		{name: "link lost", answer: func(context.Context, remoteAskCall) (*agentv1.ConnectRequest, error) {
			return nil, errors.New("link lost")
		}, want: errLinkLost},
		{name: "other error", answer: func(context.Context, remoteAskCall) (*agentv1.ConnectRequest, error) {
			return nil, errors.New("remote failed")
		}, want: errLinkLost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remote.answer = tc.answer
			_, err := e.f.UpdateAgent(e.ctx, a.nodeID, nil, nil, time.Second)
			if err == nil || connect.CodeOf(err) != connect.CodeOf(tc.want) || err.Error() != tc.want.Error() {
				t.Fatalf("UpdateAgent error = %v, want %v", err, tc.want)
			}
		})
	}
	tasks, _ := remote.calls()
	if len(tasks) != len(cases) {
		t.Fatalf("remote Ask calls = %d, want %d", len(tasks), len(cases))
	}
}

func TestFleetRemoteRefusesOfflineAndOldAgent(t *testing.T) {
	offline := newEnv(t)
	offlineAgent := offline.enroll("node-a")
	offline.fixture(offlineAgent.nodeID)
	offlineRemote := &testRemote{}
	offline.f.cfg.Remote = offlineRemote
	if _, err := offline.f.UpdateAgent(offline.ctx, offlineAgent.nodeID, nil, nil, time.Second); code(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: node is offline" {
		t.Fatalf("offline UpdateAgent error = %v", err)
	}

	old, oldAgent, _ := remoteReadyNode(t)
	oldRemote := &testRemote{}
	old.f.cfg.Remote = oldRemote
	if _, err := old.f.RunDoctor(old.ctx, oldAgent.nodeID, nil, time.Second); code(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: agent too old" {
		t.Fatalf("old RunDoctor error = %v", err)
	}
	if _, err := old.f.UpdateAgent(old.ctx, oldAgent.nodeID, nil, nil, time.Second); code(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: node cannot update itself" {
		t.Fatalf("old UpdateAgent error = %v", err)
	}
	for name, remote := range map[string]*testRemote{"offline": offlineRemote, "old": oldRemote} {
		tasks, _ := remote.calls()
		if len(tasks) != 0 {
			t.Errorf("%s agent received %d remote requests", name, len(tasks))
		}
	}
}

func TestFleetRemoteRetireLogsAndDrop(t *testing.T) {
	e, a, _ := remoteReadyNode(t)
	remote := &testRemote{}
	e.f.cfg.Remote = remote
	response, err := (nodeService{e.f}).RetireNode(e.ctx, connect.NewRequest(&adminv1.RetireNodeRequest{NodeId: a.nodeID, ConfirmName: "node-a"}))
	if err != nil || !response.Msg.AgentNotified {
		t.Fatalf("remote RetireNode = %v, %+v", err, response)
	}
	if e.f.session(a.nodeID) == nil {
		t.Fatal("RetireNode closed the VPS session while the remote seam was active")
	}
	logsErr := (nodeService{e.f}).StreamLogs(e.ctx, connect.NewRequest(&adminv1.StreamLogsRequest{}), nil)
	if code(logsErr) != connect.CodeUnimplemented {
		t.Fatalf("remote StreamLogs error = %v, want Unimplemented", logsErr)
	}
	e.f.drop(e.ctx, a.nodeID, "node re-enrolled")
	tasks, closes := remote.calls()
	if len(tasks) != 1 || tasks[0].frame.GetRetire() == nil || tasks[0].requestID != tasks[0].frame.GetRetire().RequestId {
		t.Fatalf("remote Retire request = %+v", tasks)
	}
	if len(closes) != 1 || closes[0] != (remoteCloseCall{nodeID: a.nodeID, reason: "node re-enrolled"}) {
		t.Fatalf("remote Close calls = %+v", closes)
	}
}
