package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

func coreFixture(t *testing.T, name string) (*env, *SessionCore, context.Context, SessionState, SessionSidecar, time.Time) {
	t.Helper()
	e := newCoreEnv(t)
	nodeID, _, _ := e.createEnrollment(name, name+".example.com")
	_, ownerCtx := e.f.claimOwner(nodeID, e.ctx)
	e.t.Cleanup(func() {
		e.f.mu.Lock()
		owner := e.f.owners[nodeID]
		e.f.mu.Unlock()
		if owner.cancel != nil {
			owner.cancel(nil)
		}
	})
	core := NewSessionCore(e.f)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	state := SessionState{Version: sessionStateVersion, NodeID: nodeID, OwnerGeneration: 1,
		HelloDeadline: now.Add(helloTimeout)}
	return e, core, ownerCtx, state, SessionSidecar{}, now
}

// newTestSession builds an adapter session the way runSession does: its own cancelable context under parent, done, the
// out queue, the alarm timer and a core (a fresh one when core is nil), with state and sidecar as the starting point.
func newTestSession(e *env, parent context.Context, core *SessionCore, state SessionState, sidecar SessionSidecar) *session {
	e.t.Helper()
	ctx, cancel := context.WithCancelCause(parent)
	timer := time.NewTimer(time.Hour)
	e.t.Cleanup(func() {
		cancel(nil)
		timer.Stop()
	})
	if core == nil {
		core = NewSessionCore(e.f)
	}
	return &session{f: e.f, nodeID: state.NodeID, owner: state.OwnerGeneration, caps: state.Capabilities,
		ctx: ctx, cancel: cancel, done: make(chan struct{}), out: make(chan *agentv1.ConnectResponse, outQueue),
		cmds: map[string]chan *agentv1.CommandResult{}, docs: map[string]chan *agentv1.DoctorReport{}, logs: map[string]*logSub{},
		core: core, coreState: state, coreSidecar: sidecar, alarmTimer: timer}
}

type testTransition struct {
	Transition
	State   SessionState
	Sidecar SessionSidecar
}

func coreStep(ctx context.Context, core *SessionCore, state SessionState, sidecar SessionSidecar, event SessionEvent) (testTransition, error) {
	tr, err := core.Step(ctx, &state, &sidecar, event)
	return testTransition{Transition: tr, State: state, Sidecar: sidecar}, err
}

func fireAlarmsThrough(t *testing.T, ctx context.Context, core *SessionCore, tr testTransition, target time.Time) testTransition {
	t.Helper()
	for i := 0; i < 10_000 && tr.Close == nil && tr.NextAlarm != nil && !tr.NextAlarm.After(target); i++ {
		at := *tr.NextAlarm
		var err error
		tr, err = coreStep(ctx, core, tr.State, tr.Sidecar, SessionEvent{Kind: EventAlarm, At: at})
		if err != nil {
			t.Fatal(err)
		}
	}
	return tr
}

func stepHello(t *testing.T, core *SessionCore, ctx context.Context, state SessionState, sidecar SessionSidecar, now time.Time, h *agentv1.Hello) testTransition {
	t.Helper()
	frame := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Hello{Hello: h}}
	started, err := coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventHello, At: now, Frame: frame})
	if err != nil {
		t.Fatal(err)
	}
	if started.Close != nil {
		t.Fatalf("hello closed: %+v", started.Close)
	}
	requested, err := coreStep(ctx, core, started.State, started.Sidecar, SessionEvent{Kind: EventDesiredChanged, At: now})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := core.f.prepareDesiredState(ctx, requested.State.NodeID, requested.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := coreStep(ctx, core, requested.State, requested.Sidecar, SessionEvent{Kind: EventDesiredPrepared, At: now, Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Close != nil {
		t.Fatalf("initial reconcile closed: %+v", tr.Close)
	}
	tr.Frames = append(started.Frames, tr.Frames...)
	tr.Effects = append(started.Effects, tr.Effects...)
	return tr
}

func reconcilePrepared(t *testing.T, e *env, core *SessionCore, ctx context.Context, tr testTransition, at time.Time) testTransition {
	t.Helper()
	requested := tr
	if !requested.State.Preparing {
		var err error
		requested, err = coreStep(ctx, core, requested.State, requested.Sidecar, SessionEvent{Kind: EventDesiredChanged, At: at})
		if err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := e.f.prepareDesiredState(ctx, requested.State.NodeID, requested.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := coreStep(ctx, core, requested.State, requested.Sidecar, SessionEvent{Kind: EventDesiredPrepared, At: at, Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	for _, effect := range tr.Effects {
		if effect.Kind != EffectPrepareDesired {
			updated.Effects = append([]SessionEffect{effect}, updated.Effects...)
		}
	}
	return updated
}

func TestSessionCoreHelloAndInitialState(t *testing.T) {
	t.Run("sends desired state when applied hash differs", func(t *testing.T) {
		_, core, ctx, state, sidecar, now := coreFixture(t, "core-hello-send")
		tr := stepHello(t, core, ctx, state, sidecar, now, hello("instance-1", 0, "").GetHello())
		if len(tr.Frames) != 2 || tr.Frames[0].GetHelloAck() == nil || tr.Frames[1].GetDesiredState() == nil {
			t.Fatalf("hello frames = %#v", tr.Frames)
		}
		if tr.State.SentRevision == 0 || tr.State.SentStateHash == "" {
			t.Fatalf("initial desired state not recorded: state=%+v", tr.State)
		}
		if tr.NextAlarm == nil || !tr.NextAlarm.Equal(tr.State.NextAckTick) {
			t.Fatalf("next alarm = %v, ack deadline = %v", tr.NextAlarm, tr.State.NextAckTick)
		}
	})

	t.Run("skips desired state when applied hash matches", func(t *testing.T) {
		e, core, ctx, state, sidecar, now := coreFixture(t, "core-hello-skip")
		node, err := e.st.Node(ctx, state.NodeID)
		if err != nil {
			t.Fatal(err)
		}
		want, err := e.f.buildState(ctx, node, nil)
		if err != nil {
			t.Fatal(err)
		}
		tr := stepHello(t, core, ctx, state, sidecar, now, hello("instance-2", 7, want.hash).GetHello())
		if len(tr.Frames) != 1 || tr.Frames[0].GetHelloAck() == nil {
			t.Fatalf("matching applied state sent frames = %#v", tr.Frames)
		}
		if tr.State.SentRevision != 7 || tr.State.SentStateHash != want.hash {
			t.Fatalf("matching baseline not recorded: state=%+v", tr.State)
		}
	})
}

func TestSessionCoreReconnectUsesStoredSentDigest(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-reconnect-digest")
	e.fixture(state.NodeID)
	first := stepHello(t, core, ctx, state, sidecar, now, hello("instance-before-reconnect", 0, "").GetHello())

	newOwner, newCtx := e.f.claimOwner(state.NodeID, e.ctx)
	newCore := NewSessionCore(e.f)
	newState := SessionState{Version: sessionStateVersion, NodeID: state.NodeID, OwnerGeneration: newOwner,
		HelloDeadline: now.Add(2 * time.Second).Add(helloTimeout)}
	reconnected, err := coreStep(newCtx, newCore, newState, SessionSidecar{}, SessionEvent{Kind: EventHello,
		At: now.Add(2 * time.Second), Frame: hello("instance-after-reconnect", first.State.SentRevision, first.State.SentStateHash)})
	if err != nil || reconnected.Close != nil {
		t.Fatalf("reconnect Hello = close %v, err %v", reconnected.Close, err)
	}
	requested, err := coreStep(newCtx, newCore, reconnected.State, reconnected.Sidecar,
		SessionEvent{Kind: EventDesiredChanged, At: now.Add(3 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE user SET status = 'disabled' WHERE id = 'usr_alice'`)
	prepared, err := e.f.prepareDesiredState(newCtx, state.NodeID, requested.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := coreStep(newCtx, newCore, requested.State, requested.Sidecar, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(3 * time.Second), Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Frames) != 1 || updated.Frames[0].GetDesiredState() == nil {
		t.Fatalf("reconnect desired frames = %#v", updated.Frames)
	}
	delta := updated.Frames[0].GetDesiredState()
	if delta.BaseRevision != first.State.SentRevision || len(delta.Inbounds) == 0 {
		t.Fatalf("reconnect change = %+v, want delta based on revision %d", delta, first.State.SentRevision)
	}
}

func TestSessionCoreReconnectHelloMismatchSendsFullState(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-reconnect-mismatch")
	e.fixture(state.NodeID)
	first := stepHello(t, core, ctx, state, sidecar, now, hello("instance-before-mismatch", 0, "").GetHello())
	newOwner, newCtx := e.f.claimOwner(state.NodeID, e.ctx)
	newCore := NewSessionCore(e.f)
	newState := SessionState{Version: sessionStateVersion, NodeID: state.NodeID, OwnerGeneration: newOwner,
		HelloDeadline: now.Add(2 * time.Second).Add(helloTimeout)}
	reconnected, err := coreStep(newCtx, newCore, newState, SessionSidecar{}, SessionEvent{Kind: EventHello,
		At: now.Add(2 * time.Second), Frame: hello("instance-mismatch", first.State.SentRevision, "different-hash")})
	if err != nil || reconnected.Close != nil {
		t.Fatalf("mismatched Hello = close %v, err %v", reconnected.Close, err)
	}
	requested, err := coreStep(newCtx, newCore, reconnected.State, reconnected.Sidecar,
		SessionEvent{Kind: EventDesiredChanged, At: now.Add(3 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e.f.prepareDesiredState(newCtx, state.NodeID, requested.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := coreStep(newCtx, newCore, requested.State, requested.Sidecar, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(3 * time.Second), Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Frames) != 1 || updated.Frames[0].GetDesiredState() == nil || updated.Frames[0].GetDesiredState().BaseRevision != 0 {
		t.Fatalf("mismatched Hello state = %#v, want full resend after revision %d", updated.Frames, first.State.SentRevision)
	}
}

func TestSessionCoreOlderSentDigestSendsFullState(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-stale-digest")
	e.fixture(state.NodeID)
	connected := stepHello(t, core, ctx, state, sidecar, now, hello("instance-stale-digest", 0, "").GetHello())
	_, rawDigest, err := e.st.NodeWithSentDigest(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	var digest sentDigest
	if err := json.Unmarshal(rawDigest, &digest); err != nil {
		t.Fatal(err)
	}
	digest.Revision = connected.State.SentRevision - 1
	rawDigest, err = json.Marshal(digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.NodeDesired(ctx, state.NodeID, digest.Revision, digest.Hash, rawDigest); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE user SET status = 'disabled' WHERE id = 'usr_alice'`)
	requested, err := coreStep(ctx, core, connected.State, connected.Sidecar,
		SessionEvent{Kind: EventDesiredChanged, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e.f.prepareDesiredState(ctx, state.NodeID, requested.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := coreStep(ctx, core, requested.State, requested.Sidecar, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(time.Second), Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Frames) != 1 || updated.Frames[0].GetDesiredState() == nil || updated.Frames[0].GetDesiredState().BaseRevision != 0 {
		t.Fatalf("older digest state = %#v, want full resend after revision %d", updated.Frames, connected.State.SentRevision)
	}
}

func TestSessionCoreRetiredNodeDoesNotSendDesiredState(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-retired-desired")
	e.fixture(state.NodeID)
	connected := stepHello(t, core, ctx, state, sidecar, now, hello("instance-retired-desired", 0, "").GetHello())
	if err := e.st.RetireNode(ctx, state.NodeID, now); err != nil {
		t.Fatal(err)
	}
	requested, err := coreStep(ctx, core, connected.State, connected.Sidecar,
		SessionEvent{Kind: EventDesiredChanged, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e.f.prepareDesiredState(ctx, state.NodeID, requested.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := coreStep(ctx, core, requested.State, requested.Sidecar, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(time.Second), Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range updated.Frames {
		if frame.GetDesiredState() != nil {
			t.Fatalf("retired node received a desired state: %+v", frame.GetDesiredState())
		}
	}
	if updated.State.SentRevision != connected.State.SentRevision {
		t.Fatalf("retired node sent revision %d, want %d", updated.State.SentRevision, connected.State.SentRevision)
	}
}

func TestSessionCoreKeepsMonotonicLivenessDeadline(t *testing.T) {
	_, core, ctx, state, sidecar, _ := coreFixture(t, "core-monotonic-live")
	state.HelloDeadline = time.Time{}
	now := time.Now()
	if _, err := core.Step(ctx, &state, &sidecar, SessionEvent{Kind: EventOpen, At: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Step(ctx, &state, &sidecar, SessionEvent{Kind: EventHello, At: now, Frame: hello("instance-monotonic", 0, "")}); err != nil {
		t.Fatal(err)
	}
	if state.LivenessDeadline.IsZero() || state.LivenessDeadline == state.LivenessDeadline.Round(0) {
		t.Fatalf("liveness deadline lost its monotonic reading: %v", state.LivenessDeadline)
	}
	tr := nextSessionAlarm(state, sidecar, now)
	if tr == nil || *tr == tr.Round(0) {
		t.Fatalf("next alarm lost its monotonic reading: %v", tr)
	}
	tick, err := core.Step(ctx, &state, &sidecar, SessionEvent{Kind: EventAlarm, At: *tr})
	if err != nil || tick.Close != nil || deadlineDue(state.LivenessDeadline, *tr) {
		t.Fatalf("regular alarm closed the live session: close=%+v err=%v", tick.Close, err)
	}
}

func TestSessionCoreHelloDeadlineIsDecidedByAlarm(t *testing.T) {
	_, core, ctx, state, sidecar, now := coreFixture(t, "core-hello-deadline")
	opened, err := coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventOpen, At: now})
	if err != nil {
		t.Fatal(err)
	}
	if opened.NextAlarm == nil || !opened.NextAlarm.Equal(opened.State.HelloDeadline) {
		t.Fatalf("open next alarm = %v, hello deadline = %v", opened.NextAlarm, opened.State.HelloDeadline)
	}
	tr, err := coreStep(ctx, core, opened.State, opened.Sidecar, SessionEvent{Kind: EventAlarm, At: *opened.NextAlarm})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Close == nil || tr.Close.Class != CloseDeadline {
		t.Fatalf("hello alarm = %+v, want a deadline close", tr.Close)
	}
}

func TestSessionCoreAutoBandwidthAlarm(t *testing.T) {
	_, core, ctx, state, sidecar, now := coreFixture(t, "core-auto-bandwidth")
	h := hello("instance-auto-bandwidth", 0, "")
	h.GetHello().Capabilities = []string{capBandwidth}
	tr := stepHello(t, core, ctx, state, sidecar, now, h.GetHello())
	want := now.Add(core.f.measureDelay)
	if !tr.State.AutoBandwidthDeadline.Equal(want) {
		t.Fatalf("auto-bandwidth deadline = %v, want %v", tr.State.AutoBandwidthDeadline, want)
	}
	deadline := want
	var due testTransition
	var fired bool
	for i := 0; i < 10_000 && tr.NextAlarm != nil && !tr.NextAlarm.After(deadline); i++ {
		at := *tr.NextAlarm
		next, stepErr := coreStep(ctx, core, tr.State, tr.Sidecar, SessionEvent{Kind: EventAlarm, At: at})
		if stepErr != nil {
			t.Fatal(stepErr)
		}
		tr = next
		if hasEffect(next, EffectAutoBandwidth) {
			due = next
			fired = true
			break
		}
	}
	if !fired || !due.State.AutoBandwidthDeadline.IsZero() || due.State.AutoBandwidthPending {
		t.Fatalf("due auto-bandwidth alarm = %+v state=%+v", due.Effects, due.State)
	}
	again, err := coreStep(ctx, core, due.State, due.Sidecar, SessionEvent{Kind: EventAlarm, At: deadline.Add(ackEvery)})
	if err != nil || hasEffect(again, EffectAutoBandwidth) {
		t.Fatalf("auto-bandwidth repeated after its deadline: effects=%+v err=%v", again.Effects, err)
	}
}

func TestAutoBandwidthDeadlineStartsInHello(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-bw-start")
	h := hello("instance-auto-bandwidth-start", 0, "")
	h.GetHello().Capabilities = []string{capBandwidth}
	helloTransition, err := coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventHello, At: now, Frame: h})
	if err != nil {
		t.Fatal(err)
	}
	want := now.Add(e.f.measureDelay)
	if !helloTransition.State.AutoBandwidthDeadline.Equal(want) {
		t.Fatalf("auto-bandwidth deadline = %v, want %v", helloTransition.State.AutoBandwidthDeadline, want)
	}
}

func TestSessionStepErrorCancelsSession(t *testing.T) {
	e, core, ctx, state, sidecar, _ := coreFixture(t, "core-step-error")
	s := newTestSession(e, ctx, core, state, sidecar)
	tr, err := s.stepCore(s.ctx, SessionEvent{Kind: EventKind(255), At: time.Now()})
	if err == nil || tr.NextAlarm != nil {
		t.Fatalf("failed Step transition = %+v, %v", tr, err)
	}
	if cause := context.Cause(s.ctx); cause == nil || code(cause) != connect.CodeInternal || errOrCause(s.ctx) != cause {
		t.Fatalf("Step error cause = %v, want an internal connect error", cause)
	}
}

// A full out queue ends the session with ResourceExhausted on every branch of the loop, not only the frame branch.
func TestRunSessionEndsWithResourceExhaustedWhenQueueIsFull(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("queue-full-core")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner, ownerCtx := e.f.claimOwner(a.nodeID, ctx)
	stream := &refillingAgentStream{manualAgentSessionStream: manualAgentSessionStream{ctx: ownerCtx,
		in: make(chan *agentv1.ConnectRequest, 2), out: make(chan *agentv1.ConnectResponse, 16)}}
	done := make(chan error, 1)
	go func() {
		done <- (agentService{e.f}).runSession(ownerCtx, a.nodeID, peerCert{serial: a.leaf.SerialNumber.Text(16), notAfter: a.leaf.NotAfter}, owner, stream)
	}()
	stream.in <- hello("instance-queue-full", 0, "")
	waitSessionAck(t, stream.out, 0)
	s := e.f.session(a.nodeID)
	if s == nil {
		t.Fatal("session was not registered after HelloAck")
	}
	stream.full.Store(s)
	for s.enqueue(&agentv1.ConnectResponse{}) {
	}
	stream.in <- &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{Code: "core_test"}}}
	select {
	case err := <-done:
		if code(err) != connect.CodeResourceExhausted {
			t.Fatalf("session ended with %v, want ResourceExhausted", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end on a full queue")
	}
}

func TestSlowDesiredPreparationDoesNotBlockStatsOrLiveness(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-slow-desired")
	helloTransition := stepHello(t, core, ctx, state, sidecar, now, hello("instance-slow-desired", 0, "").GetHello())
	s := newTestSession(e, ctx, core, helloTransition.State, helloTransition.Sidecar)

	originalDesired := e.f.cfg.Desired
	entered, release := make(chan struct{}), make(chan struct{})
	reconcileDone := make(chan struct{})
	type stepResult struct {
		transition Transition
		err        error
	}
	stepDone := make(chan stepResult, 1)
	stepStarted, stepFinished := false, false
	released := false
	reconcileFinished := false
	defer func() {
		if !reconcileFinished {
			if !released {
				close(release)
			}
			select {
			case <-reconcileDone:
			case <-time.After(5 * time.Second):
				t.Error("desired-state preparation did not stop")
			}
		}
		if stepStarted && !stepFinished {
			select {
			case <-stepDone:
			case <-time.After(5 * time.Second):
				t.Error("stats frame step did not stop")
			}
		}
	}()
	e.f.cfg.Desired = func(ctx context.Context, nodeID string) ([]statehash.Inbound, error) {
		close(entered)
		<-release
		return originalDesired(ctx, nodeID)
	}
	go func() {
		e.f.reconcile(e.ctx, s)
		close(reconcileDone)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("desired-state builder did not block")
	}
	oldDeadline := s.coreState.LivenessDeadline
	frameAt := now.Add(time.Duration(s.coreState.LivenessNanos) - time.Second)
	stats := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: frameAt.Add(-10 * time.Second).Unix(), IntervalEndUnix: frameAt.Unix(),
	}}}
	stepStarted = true
	go func() {
		transition, err := s.stepCore(s.ctx, SessionEvent{Kind: EventAgentFrame, At: frameAt, Frame: stats})
		stepDone <- stepResult{transition: transition, err: err}
	}()
	var transition Transition
	var err error
	select {
	case result := <-stepDone:
		stepFinished = true
		transition, err = result.transition, result.err
	case <-time.After(5 * time.Second):
		t.Fatal("stats frame did not remain responsive while desired-state preparation was blocked")
	}
	if err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-s.out:
		if response.GetAck().GetUpToSeq() != 1 {
			t.Fatalf("stats ack = %+v", response.GetAck())
		}
	default:
		t.Fatal("stats frame was not acked while desired-state preparation was blocked")
	}
	if transition.Close != nil || !s.coreState.LivenessDeadline.After(oldDeadline) {
		t.Fatalf("stats did not reset liveness: close=%+v state=%+v", transition.Close, s.coreState)
	}
	liveness, err := s.stepCore(s.ctx, SessionEvent{Kind: EventAlarm, At: oldDeadline})
	if err != nil || liveness.Close != nil {
		t.Fatalf("old liveness deadline closed the session: close=%+v err=%v", liveness.Close, err)
	}

	close(release)
	released = true
	select {
	case <-reconcileDone:
		reconcileFinished = true
	case <-time.After(5 * time.Second):
		t.Fatal("desired-state preparation did not finish")
	}
	if s.ctx.Err() != nil || s.coreState.Preparing {
		t.Fatalf("reconcile left the session ended or preparing: cause=%v state=%+v", context.Cause(s.ctx), s.coreState)
	}
}

func TestSessionCoreLivenessTimeoutChangeTakesEffectOnNextFrame(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-liveness-change")
	connected := stepHello(t, core, ctx, state, sidecar, now, hello("instance-liveness-change", 0, "").GetHello())
	oldDeadline := connected.State.LivenessDeadline
	e.exec(`UPDATE node SET liveness_timeout_s = 15 WHERE id = ?`, state.NodeID)
	updated := reconcilePrepared(t, e, core, ctx, connected, now.Add(5*time.Second))
	if updated.State.LivenessNanos != int64(15*time.Second) || !updated.State.LivenessDeadline.Equal(oldDeadline) {
		t.Fatalf("timeout change moved the current deadline: timeout=%v deadline=%v, want 15s and %v",
			time.Duration(updated.State.LivenessNanos), updated.State.LivenessDeadline, oldDeadline)
	}
	beforeNextFrame := fireAlarmsThrough(t, ctx, core, updated, now.Add(16*time.Second))
	if beforeNextFrame.Close != nil {
		t.Fatalf("new timeout closed the session before another frame: %+v", beforeNextFrame.Close)
	}
	frameAt := now.Add(20 * time.Second)
	frame, err := coreStep(ctx, core, beforeNextFrame.State, beforeNextFrame.Sidecar, SessionEvent{Kind: EventAgentFrame, At: frameAt,
		Frame: &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Pong{Pong: &agentv1.Pong{Nonce: 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	wantDeadline := frameAt.Add(15 * time.Second)
	if !frame.State.LivenessDeadline.Equal(wantDeadline) {
		t.Fatalf("next frame deadline = %v, want %v", frame.State.LivenessDeadline, wantDeadline)
	}
	expired := fireAlarmsThrough(t, ctx, core, frame, wantDeadline)
	if expired.Close == nil || expired.Close.Class != CloseDeadline {
		t.Fatalf("new liveness deadline alarm = %+v, want deadline close", expired.Close)
	}
}

func TestSessionCoreAckCoalescingAndImmediateFlush(t *testing.T) {
	_, core, ctx, state, sidecar, now := coreFixture(t, "core-ack")
	tr := stepHello(t, core, ctx, state, sidecar, now, hello("instance-ack", 0, "").GetHello())
	state, sidecar = tr.State, tr.Sidecar
	state.LastAck = now

	batch := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix(),
	}}}
	tr, err := coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventAgentFrame, At: now, Frame: batch})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Frames) != 0 || tr.State.AckPending != 1 || tr.State.AckSent != 0 {
		t.Fatalf("first ack should wait for ticker: state=%+v frames=%#v", tr.State, tr.Frames)
	}
	if tr.NextAlarm == nil || !tr.NextAlarm.Equal(now.Add(ackEvery)) {
		t.Fatalf("next alarm = %v, want ack deadline %v", tr.NextAlarm, now.Add(ackEvery))
	}
	state, sidecar = tr.State, tr.Sidecar
	tr, err = coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventAlarm, At: *tr.NextAlarm})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Frames) != 1 || tr.Frames[0].GetAck().GetUpToSeq() != 1 {
		t.Fatalf("ticker ack = %#v", tr.Frames)
	}
	state, sidecar = tr.State, tr.Sidecar
	batch.Seq = 2
	tr, err = coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventAgentFrame, At: now.Add(2*ackEvery + 100*time.Millisecond), Frame: batch})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Frames) != 1 || tr.Frames[0].GetAck().GetUpToSeq() != 2 {
		t.Fatalf("immediate ack = %#v", tr.Frames)
	}
}

func TestSessionCoreDuplicateStatsAndEvents(t *testing.T) {
	_, core, ctx, state, sidecar, now := coreFixture(t, "core-dedupe")
	tr := stepHello(t, core, ctx, state, sidecar, now, hello("instance-dedupe", 0, "").GetHello())
	state, sidecar = tr.State, tr.Sidecar
	stats := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix(),
		Host: &agentv1.HostMetrics{CpuPct: 12.5},
	}}}
	first, err := coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventAgentFrame, At: now, Frame: stats})
	if err != nil {
		t.Fatal(err)
	}
	second, err := coreStep(ctx, core, first.State, first.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now.Add(time.Second), Frame: stats})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sidecar.Live.Metrics == nil || second.Sidecar.Live.Metrics == nil {
		t.Fatalf("stats did not update the core live snapshot: first=%+v second=%+v", first.Sidecar.Live, second.Sidecar.Live)
	}
	if len(first.Frames) != 1 || len(second.Frames) != 0 {
		t.Fatalf("stats acknowledgements: first=%#v second=%#v", first.Frames, second.Frames)
	}

	ev := &agentv1.ConnectRequest{Seq: 2, Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{Code: "core_test"}}}
	first, err = coreStep(ctx, core, second.State, second.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now.Add(2 * time.Second), Frame: ev})
	if err != nil {
		t.Fatal(err)
	}
	second, err = coreStep(ctx, core, first.State, first.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now.Add(3 * time.Second), Frame: ev})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Frames) != 1 || first.Frames[0].GetAck().GetUpToSeq() != 2 || len(second.Frames) != 0 {
		t.Fatalf("event acknowledgements: first=%#v second=%#v", first.Frames, second.Frames)
	}
}

func TestSessionCorePoisonBatchDroppedAfterReconnect(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-poison")
	ids := e.fixture(state.NodeID)
	tr := stepHello(t, core, ctx, state, sidecar, now, hello("instance-poison", 0, "").GetHello())
	e.exec(`CREATE TRIGGER reject_core_stats BEFORE INSERT ON traffic_bucket WHEN NEW.user_id = 'usr_alice' BEGIN SELECT RAISE(ABORT, 'test refusal'); END`)
	batch := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix(),
		Traffic: []*agentv1.TrafficDelta{{CredId: "crd_alice_hy", InboundId: ids.i1, BytesUp: 5}},
	}}}
	first, err := coreStep(ctx, core, tr.State, tr.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now, Frame: batch})
	if err != nil {
		t.Fatal(err)
	}
	if first.Close == nil || first.Close.Class != CloseInternal || len(first.Frames) != 0 {
		t.Fatalf("first refusal = close %v, frames %#v", first.Close, first.Frames)
	}
	oldAdapter := newTestSession(e, ctx, nil, state, SessionSidecar{})
	oldAdapter.persistPoison(first.State.Poison)
	disconnected, err := coreStep(ctx, core, first.State, first.Sidecar, SessionEvent{Kind: EventDisconnected, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	oldAdapter.persistPoison(disconnected.State.Poison)

	newOwner, newCtx := e.f.claimOwner(state.NodeID, e.ctx)
	newCore := NewSessionCore(e.f)
	newState := SessionState{Version: sessionStateVersion, NodeID: state.NodeID, OwnerGeneration: newOwner,
		HelloDeadline: now.Add(2 * time.Second).Add(helloTimeout), Poison: e.f.poisonFor(state.NodeID)}
	newSidecar := SessionSidecar{}
	newHello, err := coreStep(newCtx, newCore, newState, newSidecar, SessionEvent{Kind: EventHello, At: now.Add(2 * time.Second), Frame: hello("instance-poison", 0, "")})
	if err != nil || newHello.Close != nil {
		t.Fatalf("reconnect Hello = close %v, err %v", newHello.Close, err)
	}
	second, err := coreStep(newCtx, newCore, newHello.State, newHello.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now.Add(3 * time.Second), Frame: batch})
	if err != nil {
		t.Fatal(err)
	}
	if second.Close != nil || len(second.Frames) != 1 || second.Frames[0].GetAck().GetUpToSeq() != 1 {
		t.Fatalf("second refusal = close %v, frames %#v", second.Close, second.Frames)
	}
	newAdapter := newTestSession(e, newCtx, nil, newState, SessionSidecar{})
	newAdapter.persistPoison(second.State.Poison)
	if e.f.poisonFor(state.NodeID) != nil {
		t.Fatal("the successful drop left poison state behind")
	}
	if n := e.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'stats_dropped'`, state.NodeID); n != 1 {
		t.Fatalf("stats_dropped events = %d, want 1", n)
	}
}

func TestSessionCoreRetainsPoisonWhenDropCannotAdvanceSequence(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-poison-skip-error")
	ids := e.fixture(state.NodeID)
	connected := stepHello(t, core, ctx, state, sidecar, now, hello("instance-poison-skip-error", 0, "").GetHello())
	e.exec(`CREATE TRIGGER reject_core_stats BEFORE INSERT ON traffic_bucket WHEN NEW.user_id = 'usr_alice' BEGIN SELECT RAISE(ABORT, 'test refusal'); END`)
	batch := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix(),
		Traffic: []*agentv1.TrafficDelta{{CredId: "crd_alice_hy", InboundId: ids.i1, BytesUp: 5}},
	}}}
	first, err := coreStep(ctx, core, connected.State, connected.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now, Frame: batch})
	if err != nil || first.Close == nil || first.State.Poison == nil {
		t.Fatalf("first refusal = transition %+v, err %v", first, err)
	}
	adapter := newTestSession(e, ctx, nil, state, SessionSidecar{})
	adapter.persistPoison(first.State.Poison)
	disconnected, err := coreStep(ctx, core, first.State, first.Sidecar, SessionEvent{Kind: EventDisconnected, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	adapter.persistPoison(disconnected.State.Poison)

	newOwner, newCtx := e.f.claimOwner(state.NodeID, e.ctx)
	newCore := NewSessionCore(e.f)
	newState := SessionState{Version: sessionStateVersion, NodeID: state.NodeID, OwnerGeneration: newOwner,
		HelloDeadline: now.Add(2 * time.Second).Add(helloTimeout), Poison: e.f.poisonFor(state.NodeID)}
	newHello, err := coreStep(newCtx, newCore, newState, SessionSidecar{}, SessionEvent{Kind: EventHello,
		At: now.Add(2 * time.Second), Frame: hello("instance-poison-skip-error", 0, "")})
	if err != nil || newHello.Close != nil {
		t.Fatalf("reconnect Hello = close %v, err %v", newHello.Close, err)
	}
	e.exec(`CREATE TRIGGER reject_core_skip_seq BEFORE UPDATE OF last_seq ON node WHEN NEW.last_seq > OLD.last_seq BEGIN SELECT RAISE(ABORT, 'test skip refusal'); END`)
	second, err := coreStep(newCtx, newCore, newHello.State, newHello.Sidecar, SessionEvent{Kind: EventAgentFrame,
		At: now.Add(3 * time.Second), Frame: batch})
	if err != nil || second.Close == nil || len(second.Frames) != 0 {
		t.Fatalf("failed sequence skip = close %v, frames %#v, err %v", second.Close, second.Frames, err)
	}
	if second.State.Poison == nil || second.State.Poison.Instance != "instance-poison-skip-error" || second.State.Poison.Seq != 1 {
		t.Fatalf("failed sequence skip cleared poison state: %+v", second.State.Poison)
	}
	adapter = newTestSession(e, newCtx, nil, newState, SessionSidecar{})
	adapter.persistPoison(second.State.Poison)
	if poison := e.f.poisonFor(state.NodeID); poison == nil || *poison != *second.State.Poison {
		t.Fatalf("adapter did not retain poison state after failed skip: %+v", poison)
	}
	if count := e.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'stats_dropped'`, state.NodeID); count != 0 {
		t.Fatalf("failed sequence skip recorded %d stats_dropped events", count)
	}
}

func TestSessionCoreApplyResultsAndAlarms(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-apply")
	tr := stepHello(t, core, ctx, state, sidecar, now, hello("instance-apply", 0, "").GetHello())
	state, sidecar = tr.State, tr.Sidecar
	revision := state.SentRevision
	base := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: revision, Status: agentv1.ApplyStatus_APPLY_STATUS_BASE_MISMATCH,
	}}}
	tr, err := coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventAgentFrame, At: now, Frame: base})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEffect(tr, EffectPrepareDesired) {
		t.Fatalf("base mismatch effects = %#v", tr.Effects)
	}
	tr = reconcilePrepared(t, e, core, ctx, tr, now)
	if len(tr.Frames) != 1 || tr.Frames[0].GetDesiredState() == nil || tr.Frames[0].GetDesiredState().BaseRevision != 0 {
		t.Fatalf("base mismatch resend = %#v", tr.Frames)
	}

	state, sidecar = tr.State, tr.Sidecar
	stale := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: state.SentRevision - 1, Status: agentv1.ApplyStatus_APPLY_STATUS_APPLIED, StateHash: "stale",
	}}}
	tr, err = coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventAgentFrame, At: now, Frame: stale})
	if err != nil {
		t.Fatal(err)
	}
	if tr.State.DriftResent || tr.State.Drift || len(tr.Frames) != 0 {
		t.Fatalf("stale result affected drift: state=%+v frames=%#v", tr.State, tr.Frames)
	}

	bad := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: state.SentRevision, Status: agentv1.ApplyStatus_APPLY_STATUS_APPLIED, StateHash: "wrong",
	}}}
	first, err := coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventAgentFrame, At: now, Frame: bad})
	if err != nil {
		t.Fatal(err)
	}
	if !first.State.DriftResent || !hasEffect(first, EffectPrepareDesired) {
		t.Fatalf("first drift = state %+v effects %#v", first.State, first.Effects)
	}
	first = reconcilePrepared(t, e, core, ctx, first, now)
	if len(first.Frames) != 1 || first.Frames[0].GetDesiredState().BaseRevision != 0 {
		t.Fatalf("first drift resend = %#v", first.Frames)
	}
	bad.GetApplyResult().Revision = first.State.SentRevision
	second, err := coreStep(ctx, core, first.State, first.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now.Add(time.Second), Frame: bad})
	if err != nil {
		t.Fatal(err)
	}
	if !second.State.Drift || !second.State.DriftResent || len(second.Frames) != 0 {
		t.Fatalf("persistent drift = state %+v frames %#v", second.State, second.Frames)
	}

	liveAt := second.State.LivenessDeadline
	expired := fireAlarmsThrough(t, ctx, core, second, liveAt)
	if expired.Close == nil || expired.Close.Class != CloseDeadline {
		t.Fatalf("liveness alarm = %+v", expired.Close)
	}

	certAt := second.State.NextCertCheck
	cert := fireAlarmsThrough(t, ctx, core, second, certAt)
	if cert.Close != nil {
		t.Fatalf("uncertified test session unexpectedly closed on certificate check: %+v", cert.Close)
	}
}

func TestSessionCoreCommandLogsAndSupersede(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-requests")
	tr := stepHello(t, core, ctx, state, sidecar, now, hello("instance-requests", 0, "").GetHello())
	state, sidecar = tr.State, tr.Sidecar
	command := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UpdateAgent{UpdateAgent: &agentv1.UpdateAgent{RequestId: "req-command"}}}
	tr, err := coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventAdminCommand, At: now, Request: &AdminRequest{
		RequestID: "req-command", Deadline: now.Add(time.Minute), Frame: command,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Frames) != 1 || len(tr.Sidecar.Pending) != 1 {
		t.Fatalf("command transition = state %+v frames %#v", tr.State, tr.Frames)
	}
	result := &agentv1.CommandResult{RequestId: "req-command", Ok: true}
	beforeClose, err := coreStep(ctx, core, tr.State, tr.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now.Add(time.Second),
		Frame: &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: result}}})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEffect(beforeClose, EffectCommandResult) || beforeClose.Effects[0].RequestID != "req-command" {
		t.Fatalf("command result effects = %+v", beforeClose.Effects)
	}
	if len(beforeClose.Sidecar.Pending) != 0 {
		t.Fatalf("command result left a pending request: %+v", beforeClose.Sidecar.Pending)
	}
	disconnected, err := coreStep(ctx, core, beforeClose.State, beforeClose.Sidecar, SessionEvent{Kind: EventDisconnected, At: now.Add(2 * time.Second)})
	if err != nil || len(disconnected.Sidecar.Pending) != 0 {
		t.Fatalf("disconnect after command result left pending requests: %+v, err %v", disconnected.Sidecar.Pending, err)
	}
	duplicate, err := coreStep(ctx, core, disconnected.State, disconnected.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now.Add(3 * time.Second),
		Frame: &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: result}}})
	if err != nil || hasEffect(duplicate, EffectCommandResult) {
		t.Fatalf("disconnected session emitted a second command result: %+v, err %v", duplicate.Effects, err)
	}
	logFrame := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_LogRequest{LogRequest: &agentv1.LogRequest{RequestId: "req-log"}}}
	started, err := coreStep(ctx, core, beforeClose.State, beforeClose.Sidecar, SessionEvent{Kind: EventLogStart, At: now, Request: &AdminRequest{
		RequestID: "req-log", Deadline: now.Add(time.Minute), Frame: logFrame,
	}})
	if err != nil {
		t.Fatal(err)
	}
	chunk := &agentv1.LogChunk{RequestId: "req-log", Lines: []*agentv1.LogLine{{Message: "line"}}}
	routed, err := coreStep(ctx, core, started.State, started.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now,
		Frame: &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_LogChunk{LogChunk: chunk}}})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEffect(routed, EffectLogChunk) || routed.Effects[0].RequestID != "req-log" {
		t.Fatalf("log routing effects = %+v", routed.Effects)
	}

	adapter := newTestSession(e, ctx, nil, state, SessionSidecar{})
	adapter.logs["req-log"] = &logSub{ch: make(chan *agentv1.LogChunk, 1)}
	adapter.deliverLog(chunk)
	adapter.deliverLog(&agentv1.LogChunk{RequestId: "req-log", Lines: []*agentv1.LogLine{{Message: "overflow"}}})
	if got := adapter.logs["req-log"].dropped.Load(); got != 1 {
		t.Fatalf("log overflow dropped lines = %d, want 1", got)
	}

	superseded, err := coreStep(ctx, core, routed.State, routed.Sidecar, SessionEvent{Kind: EventOwnerSuperseded, At: now})
	if err != nil || superseded.Close == nil || superseded.Close.Class != CloseConflict {
		t.Fatalf("supersede = %+v, %v", superseded.Close, err)
	}
}

func TestSessionCorePendingRequestExpiresAtNextAlarm(t *testing.T) {
	_, core, ctx, state, sidecar, now := coreFixture(t, "core-request-expiry")
	tr := stepHello(t, core, ctx, state, sidecar, now, hello("instance-request-expiry", 0, "").GetHello())
	deadline := now.Add(500 * time.Millisecond)
	request := &AdminRequest{RequestID: "req-expiry", Deadline: deadline, Frame: &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UpdateAgent{
		UpdateAgent: &agentv1.UpdateAgent{RequestId: "req-expiry"},
	}}}
	tr, err := coreStep(ctx, core, tr.State, tr.Sidecar, SessionEvent{Kind: EventAdminCommand, At: now, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	if tr.NextAlarm == nil || !tr.NextAlarm.Equal(deadline) {
		t.Fatalf("next alarm = %v, want pending request expiry %v", tr.NextAlarm, deadline)
	}
	expired, err := coreStep(ctx, core, tr.State, tr.Sidecar, SessionEvent{Kind: EventAlarm, At: *tr.NextAlarm})
	if err != nil {
		t.Fatal(err)
	}
	if len(expired.Sidecar.Pending) != 0 {
		t.Fatalf("expired request is still pending: %+v", expired.Sidecar.Pending)
	}
}

func TestSessionCoreCertificateRevocationClosesOnAlarm(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("node-cert-alarm")
	owner, ctx := e.f.claimOwner(a.nodeID, e.ctx)
	t.Cleanup(func() {
		e.f.mu.Lock()
		current := e.f.owners[a.nodeID]
		e.f.mu.Unlock()
		if current.cancel != nil {
			current.cancel(nil)
		}
	})
	core := NewSessionCore(e.f)
	now := e.f.now().UTC()
	serial := a.leaf.SerialNumber.Text(16)
	state := SessionState{Version: sessionStateVersion, NodeID: a.nodeID, OwnerGeneration: owner,
		PeerCertSerial: serial, PeerCertNotAfter: a.leaf.NotAfter,
		HelloDeadline: now.Add(helloTimeout)}
	var sidecar SessionSidecar
	helloTr, err := coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventHello, At: now, Frame: hello("instance-cert-alarm", 0, "")})
	if err != nil || helloTr.Close != nil {
		t.Fatalf("hello transition = close %v, err %v", helloTr.Close, err)
	}
	e.exec(`UPDATE node_cert SET revoked_at = ?, revoke_reason = 'test' WHERE node_id = ?`, now.Add(-time.Second).Unix(), a.nodeID)
	certDeadline := helloTr.State.NextCertCheck
	closed := fireAlarmsThrough(t, ctx, core, helloTr, certDeadline)
	if closed.Close == nil || closed.Close.Class != CloseUnauthenticated {
		t.Fatalf("revoked certificate alarm = %+v, want unauthenticated close", closed.Close)
	}
}

func TestSessionCoreCoalescesDesiredPreparation(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-stale-prepare")
	e.fixture(state.NodeID)
	current := stepHello(t, core, ctx, state, sidecar, now, hello("instance-stale-prepare", 0, "").GetHello())
	requested, err := coreStep(ctx, core, current.State, current.Sidecar, SessionEvent{Kind: EventDesiredChanged, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if !requested.State.Preparing || !hasEffect(requested, EffectPrepareDesired) {
		t.Fatalf("desired change did not start preparation: %+v", requested.State)
	}
	older, err := e.f.prepareDesiredState(ctx, state.NodeID, current.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE user SET status = 'disabled' WHERE id = 'usr_erin'`)
	dirty, err := coreStep(ctx, core, requested.State, requested.Sidecar, SessionEvent{Kind: EventDesiredChanged, At: now.Add(2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if !dirty.State.Preparing || !dirty.State.PrepareDirty || hasEffect(dirty, EffectPrepareDesired) {
		t.Fatalf("change during preparation was not coalesced: state=%+v effects=%v", dirty.State, effectKinds(dirty))
	}
	first, err := coreStep(ctx, core, dirty.State, dirty.Sidecar, SessionEvent{Kind: EventDesiredPrepared, At: now.Add(2 * time.Second), Prepared: older})
	if err != nil {
		t.Fatal(err)
	}
	if !first.State.Preparing || first.State.PrepareDirty || !hasEffect(first, EffectPrepareDesired) {
		t.Fatalf("dirty preparation did not start once after apply: state=%+v effects=%v", first.State, effectKinds(first))
	}
	fresh, err := e.f.prepareDesiredState(ctx, state.NodeID, current.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := coreStep(ctx, core, first.State, first.Sidecar, SessionEvent{Kind: EventDesiredPrepared, At: now.Add(3 * time.Second), Prepared: fresh})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Frames) != 1 || updated.Frames[0].GetDesiredState() == nil {
		t.Fatalf("coalesced prepared state frames = %#v", updated.Frames)
	}
	newRevision := updated.State.SentRevision
	node, err := e.st.Node(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node.DesiredRevision != newRevision || node.DesiredHash != updated.State.SentStateHash {
		t.Fatalf("stored desired state = %d/%s, newest prepared state = %d/%s", node.DesiredRevision, node.DesiredHash, newRevision, updated.State.SentStateHash)
	}
}

func TestDesiredPreparationFromEndedOwnerDoesNotReachNewSession(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-ended-prepare")
	e.fixture(state.NodeID)
	connected := stepHello(t, core, ctx, state, sidecar, now, hello("instance-ended-prepare", 0, "").GetHello())
	old := newTestSession(e, ctx, core, connected.State, connected.Sidecar)

	originalDesired := e.f.cfg.Desired
	entered, release := make(chan struct{}), make(chan struct{})
	var blockNext atomic.Bool
	blockNext.Store(true)
	e.f.cfg.Desired = func(ctx context.Context, nodeID string) ([]statehash.Inbound, error) {
		if blockNext.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return originalDesired(ctx, nodeID)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		e.f.cfg.Desired = originalDesired
	})
	oldDone := make(chan error, 1)
	go func() {
		_, err := old.stepDesired(context.Background())
		oldDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("old session preparation did not reach the desired-state read")
	}

	newOwner, newCtx := e.f.claimOwner(state.NodeID, e.ctx)
	old.cancel(nil)
	close(old.done)
	newCore := NewSessionCore(e.f)
	newState := SessionState{Version: sessionStateVersion, NodeID: state.NodeID, OwnerGeneration: newOwner}
	newHelloFrame := hello("instance-ended-prepare-new", 0, "")
	newHello, err := coreStep(newCtx, newCore, newState, sidecar, SessionEvent{Kind: EventHello, At: now.Add(2 * time.Second), Frame: newHelloFrame})
	if err != nil || newHello.Close != nil {
		t.Fatalf("new session Hello = close %v, err %v", newHello.Close, err)
	}
	newSession := newTestSession(e, newCtx, newCore, newHello.State, newHello.Sidecar)
	if _, err := newSession.stepDesired(newCtx); err != nil {
		t.Fatal(err)
	}
	newRevision := newSession.coreState.SentRevision
	close(release)
	select {
	case err := <-oldDone:
		if err != nil {
			t.Fatalf("ended session preparation returned an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ended session preparation did not finish")
	}
	node, err := e.st.Node(e.ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node.DesiredRevision != newRevision || node.DesiredHash != newSession.coreState.SentStateHash || len(old.out) != 0 {
		t.Fatalf("old preparation reached the new session: stored=%d/%s new=%d/%s old frames=%d",
			node.DesiredRevision, node.DesiredHash, newRevision, newSession.coreState.SentStateHash, len(old.out))
	}
}

func TestSessionCoreStepMutatesCallerState(t *testing.T) {
	_, core, ctx, state, sidecar, now := coreFixture(t, "core-pointer-step")
	tr, err := core.Step(ctx, &state, &sidecar, SessionEvent{Kind: EventOpen, At: now})
	if err != nil {
		t.Fatal(err)
	}
	if state.HelloDeadline.IsZero() || tr.NextAlarm == nil || !tr.NextAlarm.Equal(state.HelloDeadline) {
		t.Fatalf("Step did not update caller state: state=%+v transition=%+v", state, tr)
	}
	if sidecar.Pending == nil {
		t.Fatalf("Step did not initialize caller sidecar: %+v", sidecar)
	}
}

func TestSessionViewIsAtomicAcrossCoreStepsAndAdminReads(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-view-atomic")
	e.fixture(state.NodeID)
	initial := stepHello(t, core, ctx, state, sidecar, now, hello("instance-view-atomic", 0, "").GetHello())
	s := newTestSession(e, ctx, core, initial.State, initial.Sidecar)
	s.publishView()
	if !e.f.register(s) {
		t.Fatal("could not register read-snapshot session")
	}
	defer e.f.unregister(s)
	stats := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix(), Host: &agentv1.HostMetrics{CpuPct: 30},
	}}}
	if _, err := s.stepCore(ctx, SessionEvent{Kind: EventAgentFrame, At: now, Frame: stats}); err != nil {
		t.Fatal(err)
	}
	wrongApply := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: initial.State.SentRevision, Status: agentv1.ApplyStatus_APPLY_STATUS_APPLIED, StateHash: "wrong",
	}}}
	for i := 0; i < 2; i++ {
		s.coreMu.Lock()
		tr, err := s.core.Step(ctx, &s.coreState, &s.coreSidecar, SessionEvent{Kind: EventAgentFrame,
			At: now.Add(time.Duration(i+1) * time.Second), Frame: wrongApply})
		s.publishView()
		s.coreMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && !hasEffect(tr, EffectPrepareDesired) {
			t.Fatalf("first drift transition = %+v, want a desired-state preparation effect", tr.Effects)
		}
	}
	if view := s.view.Load(); view == nil || !view.State.Drift || !view.State.DriftResent {
		t.Fatalf("initial read snapshot does not contain persistent drift: %+v", view)
	}
	var readers sync.WaitGroup
	start, stop := make(chan struct{}), make(chan struct{})
	failures := make(chan string, 8)
	for i := 0; i < 4; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			<-start
			for {
				select {
				case <-stop:
					return
				default:
				}
				view := s.view.Load()
				if view == nil {
					failures <- "missing session view"
					return
				}
				if view.State.SentRevision != 0 && view.State.SentStateHash == "" {
					failures <- "sent revision snapshot lost its state hash"
					return
				}
				if view.State.Drift && !view.State.DriftResent {
					failures <- "drift snapshot lost its resend state"
					return
				}
				e.f.Live(state.NodeID)
				e.f.Online()
				e.f.NetworkUsage(state.NodeID)
				e.f.CPUUsage(state.NodeID)
			}
		}()
	}
	close(start)
	for i := 0; i < 20; i++ {
		status := "disabled"
		if i%2 == 0 {
			status = "active"
		}
		e.exec(`UPDATE user SET status = ? WHERE id = 'usr_erin'`, status)
		if _, err := s.stepDesired(ctx); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	readers.Wait()
	select {
	case failure := <-failures:
		t.Fatal(failure)
	default:
	}
}

type manualAgentSessionStream struct {
	ctx context.Context
	in  chan *agentv1.ConnectRequest
	out chan *agentv1.ConnectResponse
}

func (stream *manualAgentSessionStream) Context() context.Context { return stream.ctx }
func (stream *manualAgentSessionStream) Receive() (*agentv1.ConnectRequest, error) {
	select {
	case request, ok := <-stream.in:
		if !ok {
			return nil, io.EOF
		}
		return request, nil
	case <-stream.ctx.Done():
		return nil, io.EOF
	}
}
func (stream *manualAgentSessionStream) Send(response *agentv1.ConnectResponse) error {
	select {
	case stream.out <- response:
		return nil
	case <-stream.ctx.Done():
		return stream.ctx.Err()
	}
}

// refillingAgentStream drops what the loop sends and, once full is set, tops the session's out queue up again on every
// Send, so the next step that queues a frame finds it full.
type refillingAgentStream struct {
	manualAgentSessionStream
	full atomic.Pointer[session]
}

func (stream *refillingAgentStream) Send(response *agentv1.ConnectResponse) error {
	s := stream.full.Load()
	if s == nil {
		return stream.manualAgentSessionStream.Send(response)
	}
	for s.enqueue(&agentv1.ConnectResponse{}) {
	}
	return nil
}

func TestRunSessionStepsDisconnectBeforeClosingDone(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("disconnect-core")
	e.f.mu.Lock()
	e.f.stuck[a.nodeID] = stuckSeq{instance: "instance-disconnect", seq: 7}
	e.f.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner, ownerCtx := e.f.claimOwner(a.nodeID, ctx)
	stream := &manualAgentSessionStream{ctx: ownerCtx, in: make(chan *agentv1.ConnectRequest, 2), out: make(chan *agentv1.ConnectResponse, 16)}
	done := make(chan error, 1)
	go func() {
		done <- (agentService{e.f}).runSession(ownerCtx, a.nodeID, peerCert{serial: a.leaf.SerialNumber.Text(16), notAfter: a.leaf.NotAfter}, owner, stream)
	}()
	stream.in <- hello("instance-disconnect", 0, "")
	waitSessionAck(t, stream.out, 0)
	s := e.f.session(a.nodeID)
	if s == nil {
		t.Fatal("session was not registered after HelloAck")
	}
	close(stream.in)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("session close returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session did not stop after cancellation")
	}
	if !s.coreState.Disconnected || nextSessionAlarm(s.coreState, s.coreSidecar, e.f.now()) != nil {
		t.Fatalf("runSession ended without clearing core alarms: state=%+v", s.coreState)
	}
	e.f.mu.Lock()
	poison := e.f.stuck[a.nodeID]
	e.f.mu.Unlock()
	if poison != (stuckSeq{instance: "instance-disconnect", seq: 7}) {
		t.Fatalf("disconnect rewrote poison state: %+v", poison)
	}
}

func waitSessionAck(t *testing.T, responses <-chan *agentv1.ConnectResponse, upTo uint64) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case response := <-responses:
			if ack := response.GetAck(); ack != nil && ack.UpToSeq >= upTo && upTo != 0 {
				return
			}
			if upTo == 0 && response.GetHelloAck() != nil {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for ack up to %d", upTo)
		}
	}
}

func TestSessionStateSerializationSizeBudget(t *testing.T) {
	_, core, ctx, state, sidecar, now := coreFixture(t, "core-size")
	tr := stepHello(t, core, ctx, state, sidecar, now, hello("instance-large-node", 0, "").GetHello())
	state = tr.State
	state.SentWithheld = []string{"inb_awg", "inb_tunnel"}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SessionState
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, decoded) {
		t.Fatalf("session state round trip differs: before=%+v after=%+v", state, decoded)
	}
	if len(b) >= 4<<10 {
		t.Fatalf("session state serialized to %d bytes, want under 4096", len(b))
	}
}

func effectsOf(tr any) []SessionEffect {
	switch value := tr.(type) {
	case Transition:
		return value.Effects
	case testTransition:
		return value.Effects
	default:
		return nil
	}
}

func hasEffect(tr any, kind EffectKind) bool {
	for _, effect := range effectsOf(tr) {
		if effect.Kind == kind {
			return true
		}
	}
	return false
}

func effectKinds(tr any) []EffectKind {
	effects := effectsOf(tr)
	out := make([]EffectKind, len(effects))
	for i, effect := range effects {
		out[i] = effect.Kind
	}
	return out
}

// helloSession is a session whose core has taken Open and Hello; it is not registered with the fleet.
func helloSession(t *testing.T, name string) (*env, *session) {
	t.Helper()
	e, core, ctx, state, sidecar, now := coreFixture(t, name)
	if _, err := core.Step(ctx, &state, &sidecar, SessionEvent{Kind: EventOpen, At: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Step(ctx, &state, &sidecar, SessionEvent{Kind: EventHello, At: now, Frame: hello("instance-1", 0, "")}); err != nil {
		t.Fatal(err)
	}
	return e, newTestSession(e, ctx, core, state, sidecar)
}

// registeredSession is a registered session that has sent its first desired state; end unregisters it.
func registeredSession(t *testing.T, e *env, name string) (*session, func()) {
	t.Helper()
	nodeID, _, _ := e.createEnrollment(name, name+".example.com")
	owner, ctx := e.f.claimOwner(nodeID, e.ctx)
	core := NewSessionCore(e.f)
	state := SessionState{Version: sessionStateVersion, NodeID: nodeID, OwnerGeneration: owner}
	var sidecar SessionSidecar
	now := e.f.now()
	if _, err := core.Step(ctx, &state, &sidecar, SessionEvent{Kind: EventOpen, At: now}); err != nil {
		t.Fatal(err)
	}
	helloTr, err := core.Step(ctx, &state, &sidecar, SessionEvent{Kind: EventHello, At: now, Frame: hello("instance-"+name, 0, "")})
	if err != nil || helloTr.Close != nil {
		t.Fatalf("session Hello = close %v, err %v", helloTr.Close, err)
	}
	s := newTestSession(e, ctx, core, state, sidecar)
	if !e.f.register(s) {
		t.Fatal("session owner was not registered")
	}
	if _, err := s.stepDesired(s.ctx); err != nil {
		t.Fatal(err)
	}
	receiveDesired(t, s, 0)

	var endOnce sync.Once
	end := func() {
		endOnce.Do(func() {
			s.cancel(nil)
			close(s.done)
			e.f.unregister(s)
		})
	}
	t.Cleanup(end)
	return s, end
}

// receiveDesired returns the DesiredState frames queued for the agent: it waits for at least atLeast of them, then takes
// whatever else is already queued.
func receiveDesired(t *testing.T, s *session, atLeast int) []*agentv1.DesiredState {
	t.Helper()
	timeout := time.After(5 * time.Second)
	var frames []*agentv1.DesiredState
	for {
		var frame *agentv1.ConnectResponse
		if len(frames) < atLeast {
			select {
			case frame = <-s.out:
			case <-timeout:
				t.Fatalf("received %d DesiredState frames, want %d", len(frames), atLeast)
			}
		} else {
			select {
			case frame = <-s.out:
			default:
				return frames
			}
		}
		if desired := frame.GetDesiredState(); desired != nil {
			frames = append(frames, desired)
		}
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestInitialReconcileSurvivesConcurrentRecompute(t *testing.T) {
	e, s := helloSession(t, "initial-reconcile")
	started, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	e.f.cfg.Desired = func(context.Context, string) ([]statehash.Inbound, error) {
		if reads.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil, nil
	}
	initial := make(chan error, 1)
	go func() {
		_, err := s.stepDesired(s.ctx)
		initial <- err
	}()
	<-started
	if _, err := s.stepDesired(s.ctx); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-initial; err != nil {
		t.Fatal(err)
	}
	if got := receiveDesired(t, s, 0); len(got) == 0 {
		t.Fatal("initial desired state was lost when recompute finished first")
	}
}

func TestDesiredPreparationSerializesReadsAndSendsLatestState(t *testing.T) {
	e, s := helloSession(t, "prepare-order")
	if _, err := s.stepDesired(s.ctx); err != nil {
		t.Fatal(err)
	}
	receiveDesired(t, s, 0)

	var version atomic.Int32
	version.Store(1)
	var reads, active, maxActive atomic.Int32
	started, followupStarted, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	e.f.cfg.Desired = func(context.Context, string) ([]statehash.Inbound, error) {
		v := version.Load()
		call := reads.Add(1)
		if call == 2 {
			close(followupStarted)
		}
		inFlight := active.Add(1)
		defer active.Add(-1)
		for old := maxActive.Load(); inFlight > old; old = maxActive.Load() {
			if maxActive.CompareAndSwap(old, inFlight) {
				break
			}
		}
		if call == 1 {
			close(started)
			<-release
		}
		return []statehash.Inbound{{Spec: plugin.InboundSpec{ID: "inb_prepare", Protocol: "fakehy", ProfileID: "prf_prepare", Version: uint64(v), Enabled: true}}}, nil
	}
	first := make(chan error, 1)
	go func() {
		_, err := s.stepDesired(s.ctx)
		first <- err
	}()
	<-started
	version.Store(2)
	if _, err := s.stepDesired(s.ctx); err != nil {
		t.Fatal(err)
	}
	readsBeforeRelease := reads.Load()
	close(release)
	waitSignal(t, followupStarted, "the dirty preparation hand-off")
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if maxActive.Load() != 1 || readsBeforeRelease != 1 {
		t.Fatalf("preparations overlapped: max active=%d reads before release=%d", maxActive.Load(), readsBeforeRelease)
	}
	latest, err := e.f.prepareDesiredState(s.ctx, s.nodeID, s.caps)
	if err != nil {
		t.Fatal(err)
	}
	frames := receiveDesired(t, s, 2)
	view := s.view.Load()
	if view == nil || view.State.SentStateHash != latest.desired.hash {
		var sent string
		if view != nil {
			sent = view.State.SentStateHash
		}
		t.Fatalf("last sent hash = %q, latest database hash = %v", sent, latest.desired.hash)
	}
	if frames[len(frames)-1].StateHash != latest.desired.hash {
		t.Fatalf("last DesiredState = %v, latest database hash = %v", frames, latest.desired.hash)
	}
}

func TestStepAfterEndDoesNotRearmOrRewritePoison(t *testing.T) {
	e, s := helloSession(t, "step-after-end")
	now := e.f.now()
	poison := &PoisonBatch{Instance: "instance-1", Seq: 7}
	s.coreState.Poison = poison
	e.f.mu.Lock()
	e.f.stuck[s.nodeID] = stuckSeq{instance: poison.Instance, seq: poison.Seq}
	e.f.mu.Unlock()
	ended, err := s.stepCore(s.ctx, SessionEvent{Kind: EventDisconnected, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if ended.NextAlarm != nil {
		t.Fatalf("disconnected session has next alarm %v", ended.NextAlarm)
	}
	again, err := s.stepCore(s.ctx, SessionEvent{Kind: EventAlarm, At: now.Add(2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if again.NextAlarm != nil || !s.coreState.Disconnected || nextSessionAlarm(s.coreState, s.coreSidecar, now.Add(2*time.Second)) != nil {
		t.Fatalf("event after end changed session scheduling: disconnected=%t next=%v", s.coreState.Disconnected, again.NextAlarm)
	}
	e.f.mu.Lock()
	got := e.f.stuck[s.nodeID]
	e.f.mu.Unlock()
	if got != (stuckSeq{instance: poison.Instance, seq: poison.Seq}) {
		t.Fatalf("poison entry changed after end: %+v", got)
	}
}

func TestFailedPreparationRetriesOnAlarmAndKeepsFullResend(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "prepare-retry")
	connected := stepHello(t, core, ctx, state, sidecar, now, hello("instance-retry", 0, "").GetHello())
	mismatchFrame := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: connected.State.SentRevision, Status: agentv1.ApplyStatus_APPLY_STATUS_BASE_MISMATCH,
	}}}
	mismatch, err := coreStep(ctx, core, connected.State, connected.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now.Add(time.Second), Frame: mismatchFrame})
	if err != nil || !hasEffect(mismatch, EffectPrepareDesired) || !mismatch.State.FullResendPending {
		t.Fatalf("base mismatch transition = %+v, err %v", mismatch, err)
	}
	failed, err := coreStep(ctx, core, mismatch.State, mismatch.Sidecar, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(2 * time.Second), Err: errors.New("desired source unavailable")})
	if err != nil {
		t.Fatal(err)
	}
	if failed.State.Preparing || !failed.State.PrepareRetryAt.Equal(now.Add(2*time.Second+prepareRetryDelay)) || !failed.State.FullResendPending {
		t.Fatalf("failed preparation state = %+v", failed.State)
	}
	retryAt := failed.State.PrepareRetryAt
	retry, err := coreStep(ctx, core, failed.State, failed.Sidecar, SessionEvent{Kind: EventAlarm, At: retryAt})
	if err != nil || !retry.State.Preparing || !hasEffect(retry, EffectPrepareDesired) {
		t.Fatalf("retry alarm transition = %+v, err %v", retry, err)
	}
	prepared, err := e.f.prepareDesiredState(ctx, state.NodeID, retry.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	resent, err := coreStep(ctx, core, retry.State, retry.Sidecar, SessionEvent{Kind: EventDesiredPrepared, At: retryAt, Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if len(resent.Frames) != 1 || resent.Frames[0].GetDesiredState() == nil || resent.Frames[0].GetDesiredState().BaseRevision != 0 || resent.State.FullResendPending {
		t.Fatalf("successful retry did not send the pending full state: state=%+v frames=%#v", resent.State, resent.Frames)
	}
}

func TestRepeatedBaseMismatchCoalescesDesiredPreparation(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "rev-base-flight")
	connected := stepHello(t, core, ctx, state, sidecar, now, hello("instance-base-mismatch", 0, "").GetHello())
	frame := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: connected.State.SentRevision, Status: agentv1.ApplyStatus_APPLY_STATUS_BASE_MISMATCH,
	}}}
	inFlight, err := coreStep(ctx, core, connected.State, connected.Sidecar, SessionEvent{Kind: EventAgentFrame,
		At: now.Add(time.Second), Frame: frame})
	if err != nil || !hasEffect(inFlight, EffectPrepareDesired) || !inFlight.State.Preparing {
		t.Fatalf("initial base mismatch did not start preparation: state=%+v effects=%v err=%v", inFlight.State, effectKinds(inFlight), err)
	}
	for i := 2; i < 10; i++ {
		again, stepErr := coreStep(ctx, core, inFlight.State, inFlight.Sidecar, SessionEvent{Kind: EventAgentFrame,
			At: now.Add(time.Duration(i) * time.Second), Frame: frame})
		if stepErr != nil {
			t.Fatal(stepErr)
		}
		if hasEffect(again, EffectPrepareDesired) || !again.State.PrepareDirty || !again.State.Preparing {
			t.Fatalf("base mismatch %d started extra work: state=%+v effects=%v", i, again.State, effectKinds(again))
		}
		inFlight = again
	}
	prepared, err := e.f.prepareDesiredState(ctx, state.NodeID, inFlight.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	first, err := coreStep(ctx, core, inFlight.State, inFlight.Sidecar, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(10 * time.Second), Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if !first.State.Preparing || first.State.PrepareDirty || !hasEffect(first, EffectPrepareDesired) ||
		len(first.Frames) != 1 || first.Frames[0].GetDesiredState().BaseRevision != 0 {
		t.Fatalf("first completion did not send full state and request one more prepare: state=%+v effects=%v frames=%v",
			first.State, effectKinds(first), first.Frames)
	}
	secondPrepared, err := e.f.prepareDesiredState(ctx, state.NodeID, first.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	second, err := coreStep(ctx, core, first.State, first.Sidecar, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(11 * time.Second), Prepared: secondPrepared})
	if err != nil {
		t.Fatal(err)
	}
	if second.State.Preparing || hasEffect(second, EffectPrepareDesired) {
		t.Fatalf("coalesced second preparation left extra work: state=%+v effects=%v", second.State, effectKinds(second))
	}
}

func TestRecomputeWaitsForOnePreparationRoundPerNode(t *testing.T) {
	e := newCoreEnv(t)
	slow, endSlow := registeredSession(t, e, "de")
	fast, endFast := registeredSession(t, e, "node1")
	originalDesired := e.f.cfg.Desired
	firstRead, secondRead, fastRead := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseFirst, releaseSecond := make(chan struct{}), make(chan struct{})
	recomputeDone := make(chan struct{})
	var closeFirst, closeSecond sync.Once
	var reads atomic.Int32
	e.f.cfg.Desired = func(ctx context.Context, nodeID string) ([]statehash.Inbound, error) {
		if nodeID == slow.nodeID {
			switch reads.Add(1) {
			case 1:
				close(firstRead)
				<-releaseFirst
				if err := answerBaseMismatch(slow); err != nil {
					return nil, err
				}
			case 2:
				close(secondRead)
				if err := answerBaseMismatch(slow); err != nil {
					return nil, err
				}
				<-releaseSecond
			}
		} else if nodeID == fast.nodeID {
			close(fastRead)
		}
		return originalDesired(ctx, nodeID)
	}
	t.Cleanup(func() {
		closeFirst.Do(func() { close(releaseFirst) })
		closeSecond.Do(func() { close(releaseSecond) })
		endSlow()
		endFast()
		select {
		case <-recomputeDone:
		case <-time.After(5 * time.Second):
			t.Error("recompute did not stop after test sessions ended")
		}
	})

	go func() {
		e.f.recomputeAll(e.ctx)
		close(recomputeDone)
	}()
	waitSignal(t, firstRead, "the slow node's inline read")
	waitSignal(t, fastRead, "the other node's read")
	closeFirst.Do(func() { close(releaseFirst) })
	waitSignal(t, secondRead, "the slow node's background follow-up")
	select {
	case <-recomputeDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("recompute waited for the slow node's background follow-up; reads=%d", reads.Load())
	}
}

func answerBaseMismatch(s *session) error {
	view := s.view.Load()
	if view == nil || view.State.SentRevision == 0 {
		return nil
	}
	frame := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: view.State.SentRevision, Status: agentv1.ApplyStatus_APPLY_STATUS_BASE_MISMATCH,
	}}}
	_, err := s.stepCore(s.ctx, SessionEvent{Kind: EventAgentFrame, At: s.f.now(), Frame: frame})
	return err
}

func TestHelloShortcutSendsChangedSettingsDelta(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "hello-settings-shortcut")
	node, err := e.st.Node(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	preparedBefore, err := e.f.prepareDesiredState(ctx, state.NodeID, nil)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventHello, At: now,
		Frame: hello("instance-settings", 7, preparedBefore.desired.hash)})
	if err != nil || opened.Close != nil {
		t.Fatalf("Hello = close %v, err %v", opened.Close, err)
	}
	country := "DE"
	updatedNode, err := e.st.UpdateNode(ctx, node.ID, store.NodePatch{CountryCode: &country})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := coreStep(ctx, core, opened.State, opened.Sidecar, SessionEvent{Kind: EventDesiredChanged, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e.f.prepareDesiredState(ctx, state.NodeID, requested.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := coreStep(ctx, core, requested.State, requested.Sidecar, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(time.Second), Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if len(sent.Frames) != 1 || sent.Frames[0].GetDesiredState() == nil {
		t.Fatalf("changed settings did not produce DesiredState: %+v", sent.Frames)
	}
	delta := sent.Frames[0].GetDesiredState()
	if delta.BaseRevision != 7 || delta.Settings == nil || delta.Settings.CountryCode != updatedNode.CountryCode {
		t.Fatalf("settings delta = %+v, want base revision 7 and country %q", delta, updatedNode.CountryCode)
	}
}

// The node row may already hold a higher desired revision than the one the agent applied (sent, then changed back):
// the shortcut keeps that revision and still records the state the agent holds as the desired one.
func TestHelloShortcutRecordsDesiredHashBehindAHigherRevision(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "hello-higher-rev")
	if err := e.st.NodeDesired(ctx, state.NodeID, 9, "sent-then-changed-back", []byte(`{"r":9,"h":"sent-then-changed-back"}`)); err != nil {
		t.Fatal(err)
	}
	want, err := e.f.prepareDesiredState(ctx, state.NodeID, nil)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventHello, At: now, Frame: hello("instance-higher", 7, want.desired.hash)})
	if err != nil || opened.Close != nil {
		t.Fatalf("Hello = close %v, err %v", opened.Close, err)
	}
	requested, err := coreStep(ctx, core, opened.State, opened.Sidecar, SessionEvent{Kind: EventDesiredChanged, At: now})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e.f.prepareDesiredState(ctx, state.NodeID, requested.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	done, err := coreStep(ctx, core, requested.State, requested.Sidecar, SessionEvent{Kind: EventDesiredPrepared, At: now, Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if len(done.Frames) != 0 || done.State.SentRevision != 7 {
		t.Fatalf("shortcut sent %d frames, sent revision %d; want none and 7", len(done.Frames), done.State.SentRevision)
	}
	node, raw, err := e.st.NodeWithSentDigest(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	var digest sentDigest
	if err := json.Unmarshal(raw, &digest); err != nil {
		t.Fatal(err)
	}
	if node.DesiredRevision != 9 || node.DesiredHash != want.desired.hash || digest.Revision != 7 {
		t.Fatalf("node desired %d/%q, digest revision %d; want 9/%q and 7", node.DesiredRevision, node.DesiredHash, digest.Revision, want.desired.hash)
	}
}

func TestHelloShortcutResendsFullStateAfterSidecarLoss(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "hello-sidecar-loss")
	state.SentRevision = 9
	preparedBefore, err := e.f.prepareDesiredState(ctx, state.NodeID, nil)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventHello, At: now,
		Frame: hello("instance-sidecar-loss", 7, preparedBefore.desired.hash)})
	if err != nil || opened.Close != nil {
		t.Fatalf("Hello = close %v, err %v", opened.Close, err)
	}
	requested, err := coreStep(ctx, core, opened.State, SessionSidecar{}, SessionEvent{Kind: EventDesiredChanged, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e.f.prepareDesiredState(ctx, state.NodeID, requested.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := coreStep(ctx, core, requested.State, SessionSidecar{}, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(time.Second), Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if len(sent.Frames) != 1 || sent.Frames[0].GetDesiredState() == nil {
		t.Fatalf("sidecar loss skipped DesiredState: %+v", sent.Frames)
	}
	full := sent.Frames[0].GetDesiredState()
	if full.BaseRevision != 0 || full.Revision <= state.SentRevision || full.Settings == nil {
		t.Fatalf("sidecar loss state = %+v, want a full state after revision %d", full, state.SentRevision)
	}
}

func TestSessionStepCloseCancelsWithReceiveCause(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "session-close-cause")
	s := newTestSession(e, ctx, core, state, sidecar)
	tr, err := s.stepCore(s.ctx, SessionEvent{Kind: EventOwnerSuperseded, At: now})
	if err != nil || tr.Close == nil {
		t.Fatalf("session close transition = %+v, err %v", tr.Close, err)
	}
	cause := context.Cause(s.ctx)
	closeErr := sessionCloseError(tr.Close)
	if cause == nil || code(cause) != code(closeErr) || cause.Error() != closeErr.Error() {
		t.Fatalf("session cause = %v, want %v", cause, closeErr)
	}
	if received := errOrCause(s.ctx); received == nil || code(received) != code(cause) || received.Error() != cause.Error() {
		t.Fatalf("receive loop error = %v, want close cause %v", received, cause)
	}
}
