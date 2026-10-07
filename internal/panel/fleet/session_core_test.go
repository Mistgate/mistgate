package fleet

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
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
		HelloDeadlineUnixNano: now.Add(helloTimeout).UnixNano()}
	return e, core, ownerCtx, state, SessionSidecar{}, now
}

func coreStep(ctx context.Context, core *SessionCore, state SessionState, sidecar SessionSidecar, event SessionEvent) (Transition, error) {
	return core.Step(ctx, &state, &sidecar, event)
}

func fireAlarmsThrough(t *testing.T, ctx context.Context, core *SessionCore, tr Transition, target time.Time) Transition {
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

func stepHello(t *testing.T, core *SessionCore, ctx context.Context, state SessionState, sidecar SessionSidecar, now time.Time, h *agentv1.Hello) Transition {
	t.Helper()
	frame := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Hello{Hello: h}}
	started, err := coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventHello, At: now, Frame: frame})
	if err != nil {
		t.Fatal(err)
	}
	if started.Close != nil {
		t.Fatalf("hello closed: %+v", started.Close)
	}
	ticketStep, err := coreStep(ctx, core, started.State, started.Sidecar, SessionEvent{Kind: EventDesiredPrepareStarted, At: now})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := core.f.prepareDesiredState(ctx, ticketStep.State.NodeID, ticketStep.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	prepared.ticket = ticketStep.State.PrepareTicket
	prepared.ownerGeneration = ticketStep.State.OwnerGeneration
	tr, err := coreStep(ctx, core, ticketStep.State, ticketStep.Sidecar, SessionEvent{Kind: EventInitialReconcile, At: now, Frame: frame,
		Prepared: prepared})
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

func reconcilePrepared(t *testing.T, e *env, core *SessionCore, ctx context.Context, tr Transition, mode reconcileMode, at time.Time) Transition {
	t.Helper()
	ticketStep, err := coreStep(ctx, core, tr.State, tr.Sidecar, SessionEvent{Kind: EventDesiredPrepareStarted, At: at})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e.f.prepareDesiredState(ctx, ticketStep.State.NodeID, ticketStep.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	prepared.ticket = ticketStep.State.PrepareTicket
	prepared.ownerGeneration = ticketStep.State.OwnerGeneration
	updated, err := coreStep(ctx, core, ticketStep.State, ticketStep.Sidecar, SessionEvent{Kind: EventDesiredChanged, At: at, Mode: mode, Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	updated.Effects = append(tr.Effects, updated.Effects...)
	return updated
}

func TestSessionCoreHelloAndInitialState(t *testing.T) {
	t.Run("sends desired state when applied hash differs", func(t *testing.T) {
		_, core, ctx, state, sidecar, now := coreFixture(t, "core-hello-send")
		tr := stepHello(t, core, ctx, state, sidecar, now, hello("instance-1", 0, "").GetHello())
		if len(tr.Frames) != 2 || tr.Frames[0].GetHelloAck() == nil || tr.Frames[1].GetDesiredState() == nil {
			t.Fatalf("hello frames = %#v", tr.Frames)
		}
		if tr.State.SentRevision == 0 || tr.Sidecar.SentDesired == nil {
			t.Fatalf("initial desired state not recorded: state=%+v sidecar=%+v", tr.State, tr.Sidecar)
		}
		if tr.NextAlarm == nil || !tr.NextAlarm.Equal(time.Unix(0, tr.State.NextAckTickUnixNano)) {
			t.Fatalf("next alarm = %v, ack deadline = %v", tr.NextAlarm, tr.State.NextAckTickUnixNano)
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
		if tr.State.SentRevision != 7 || tr.Sidecar.SentDesired == nil {
			t.Fatalf("matching baseline not recorded: state=%+v sidecar=%+v", tr.State, tr.Sidecar)
		}
	})
}

func TestSessionCoreHelloDeadlineIsDecidedByAlarm(t *testing.T) {
	_, core, ctx, state, sidecar, now := coreFixture(t, "core-hello-deadline")
	opened, err := coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventOpen, At: now})
	if err != nil {
		t.Fatal(err)
	}
	if opened.NextAlarm == nil || !opened.NextAlarm.Equal(time.Unix(0, opened.State.HelloDeadlineUnixNano)) {
		t.Fatalf("open next alarm = %v, hello deadline = %d", opened.NextAlarm, opened.State.HelloDeadlineUnixNano)
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
	want := now.Add(core.f.measureDelay).UnixNano()
	if tr.State.AutoBandwidthUnixNano != want {
		t.Fatalf("auto-bandwidth deadline = %d, want %d", tr.State.AutoBandwidthUnixNano, want)
	}
	deadline := time.Unix(0, want).UTC()
	var due Transition
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
	if !fired || due.State.AutoBandwidthUnixNano != 0 || due.State.AutoBandwidthPending {
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
	want := now.Add(e.f.measureDelay).UnixNano()
	if helloTransition.State.AutoBandwidthUnixNano != want {
		t.Fatalf("auto-bandwidth deadline = %d, want %d", helloTransition.State.AutoBandwidthUnixNano, want)
	}
}

func TestSessionStepErrorCancelsSession(t *testing.T) {
	e, core, ctx, state, _, _ := coreFixture(t, "core-step-error")
	sctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	s := &session{f: e.f, nodeID: state.NodeID, ctx: sctx, cancel: cancel, core: core}
	tr, err := s.stepCore(sctx, SessionEvent{Kind: EventKind(255), At: time.Now()})
	if err == nil || tr.State.Version != 0 {
		t.Fatalf("failed Step transition = %+v, %v", tr, err)
	}
	if context.Cause(sctx) == nil {
		t.Fatal("Step error did not cancel the session")
	}
}

func TestSlowDesiredPreparationDoesNotBlockStatsOrLiveness(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-slow-desired")
	helloTransition := stepHello(t, core, ctx, state, sidecar, now, hello("instance-slow-desired", 0, "").GetHello())
	sctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	s := &session{f: e.f, nodeID: state.NodeID, owner: state.OwnerGeneration, caps: helloTransition.State.Capabilities,
		ctx: sctx, cancel: cancel, done: make(chan struct{}), out: make(chan *agentv1.ConnectResponse, outQueue),
		core: core, coreState: helloTransition.State, coreSidecar: helloTransition.Sidecar}

	originalDesired := e.f.cfg.Desired
	entered, release := make(chan struct{}), make(chan struct{})
	reconcileDone := make(chan error, 1)
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
	go func() { reconcileDone <- e.f.reconcile(e.ctx, s, reconcileChange) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("desired-state builder did not block")
	}
	oldDeadline := s.coreState.LivenessDeadlineUnixNano
	frameAt := now.Add(time.Duration(s.coreState.LivenessNanos) - time.Second)
	stats := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: frameAt.Add(-10 * time.Second).Unix(), IntervalEndUnix: frameAt.Unix(),
	}}}
	stepStarted = true
	go func() {
		transition, err := s.stepCore(sctx, SessionEvent{Kind: EventAgentFrame, At: frameAt, Frame: stats})
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
	if transition.Close != nil || transition.State.LivenessDeadlineUnixNano <= oldDeadline {
		t.Fatalf("stats did not reset liveness: close=%+v state=%+v", transition.Close, transition.State)
	}
	liveness, err := s.stepCore(sctx, SessionEvent{Kind: EventAlarm, At: time.Unix(0, oldDeadline)})
	if err != nil || liveness.Close != nil {
		t.Fatalf("old liveness deadline closed the session: close=%+v err=%v", liveness.Close, err)
	}

	close(release)
	released = true
	select {
	case err := <-reconcileDone:
		reconcileFinished = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("desired-state preparation did not finish")
	}
}

func TestSessionCoreLivenessTimeoutChangeTakesEffectOnNextFrame(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-liveness-change")
	connected := stepHello(t, core, ctx, state, sidecar, now, hello("instance-liveness-change", 0, "").GetHello())
	oldDeadline := connected.State.LivenessDeadlineUnixNano
	e.exec(`UPDATE node SET liveness_timeout_s = 15 WHERE id = ?`, state.NodeID)
	updated := reconcilePrepared(t, e, core, ctx, connected, reconcileChange, now.Add(5*time.Second))
	if updated.State.LivenessNanos != int64(15*time.Second) || updated.State.LivenessDeadlineUnixNano != oldDeadline {
		t.Fatalf("timeout change moved the current deadline: timeout=%v deadline=%v, want 15s and %v",
			time.Duration(updated.State.LivenessNanos), time.Unix(0, updated.State.LivenessDeadlineUnixNano), time.Unix(0, oldDeadline))
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
	wantDeadline := frameAt.Add(15 * time.Second).UnixNano()
	if frame.State.LivenessDeadlineUnixNano != wantDeadline {
		t.Fatalf("next frame deadline = %v, want %v", time.Unix(0, frame.State.LivenessDeadlineUnixNano), time.Unix(0, wantDeadline))
	}
	expired := fireAlarmsThrough(t, ctx, core, frame, time.Unix(0, wantDeadline))
	if expired.Close == nil || expired.Close.Class != CloseDeadline {
		t.Fatalf("new liveness deadline alarm = %+v, want deadline close", expired.Close)
	}
}

func TestSessionCoreAckCoalescingAndImmediateFlush(t *testing.T) {
	_, core, ctx, state, sidecar, now := coreFixture(t, "core-ack")
	tr := stepHello(t, core, ctx, state, sidecar, now, hello("instance-ack", 0, "").GetHello())
	state, sidecar = tr.State, tr.Sidecar
	state.LastAckUnixNano = now.UnixNano()

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
	oldAdapter := &session{f: e.f, nodeID: state.NodeID, owner: state.OwnerGeneration}
	oldAdapter.persistPoison(first.State.Poison)
	disconnected, err := coreStep(ctx, core, first.State, first.Sidecar, SessionEvent{Kind: EventDisconnected, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	oldAdapter.persistPoison(disconnected.State.Poison)

	newOwner, newCtx := e.f.claimOwner(state.NodeID, e.ctx)
	newCore := NewSessionCore(e.f)
	newState := SessionState{Version: sessionStateVersion, NodeID: state.NodeID, OwnerGeneration: newOwner,
		HelloDeadlineUnixNano: now.Add(2 * time.Second).Add(helloTimeout).UnixNano(), Poison: e.f.poisonFor(state.NodeID)}
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
	newAdapter := &session{f: e.f, nodeID: state.NodeID, owner: newOwner}
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
	adapter := &session{f: e.f, nodeID: state.NodeID, owner: state.OwnerGeneration}
	adapter.persistPoison(first.State.Poison)
	disconnected, err := coreStep(ctx, core, first.State, first.Sidecar, SessionEvent{Kind: EventDisconnected, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	adapter.persistPoison(disconnected.State.Poison)

	newOwner, newCtx := e.f.claimOwner(state.NodeID, e.ctx)
	newCore := NewSessionCore(e.f)
	newState := SessionState{Version: sessionStateVersion, NodeID: state.NodeID, OwnerGeneration: newOwner,
		HelloDeadlineUnixNano: now.Add(2 * time.Second).Add(helloTimeout).UnixNano(), Poison: e.f.poisonFor(state.NodeID)}
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
	adapter = &session{f: e.f, nodeID: state.NodeID, owner: newOwner}
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
	tr = reconcilePrepared(t, e, core, ctx, tr, reconcileFull, now)
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
	first = reconcilePrepared(t, e, core, ctx, first, reconcileFull, now)
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

	liveAt := time.Unix(0, second.State.LivenessDeadlineUnixNano)
	expired := fireAlarmsThrough(t, ctx, core, second, liveAt)
	if expired.Close == nil || expired.Close.Class != CloseDeadline {
		t.Fatalf("liveness alarm = %+v", expired.Close)
	}

	certAt := time.Unix(0, second.State.NextCertCheckUnixNano)
	cert := fireAlarmsThrough(t, ctx, core, second, certAt)
	if cert.Close != nil {
		t.Fatalf("uncertified test session unexpectedly closed on certificate check: %+v", cert.Close)
	}
}

func TestSessionCoreCommandLogsAndSupersede(t *testing.T) {
	_, core, ctx, state, sidecar, now := coreFixture(t, "core-requests")
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

	adapter := &session{logs: map[string]*logSub{"req-log": {ch: make(chan *agentv1.LogChunk, 1)}}}
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
		PeerCertSerial: serial, PeerCertNotAfterUnixNano: a.leaf.NotAfter.UnixNano(),
		HelloDeadlineUnixNano: now.Add(helloTimeout).UnixNano()}
	var sidecar SessionSidecar
	helloTr, err := coreStep(ctx, core, state, sidecar, SessionEvent{Kind: EventHello, At: now, Frame: hello("instance-cert-alarm", 0, "")})
	if err != nil || helloTr.Close != nil {
		t.Fatalf("hello transition = close %v, err %v", helloTr.Close, err)
	}
	e.exec(`UPDATE node_cert SET revoked_at = ?, revoke_reason = 'test' WHERE node_id = ?`, now.Add(-time.Second).Unix(), a.nodeID)
	certDeadline := time.Unix(0, helloTr.State.NextCertCheckUnixNano).UTC()
	closed := fireAlarmsThrough(t, ctx, core, helloTr, certDeadline)
	if closed.Close == nil || closed.Close.Class != CloseUnauthenticated {
		t.Fatalf("revoked certificate alarm = %+v, want unauthenticated close", closed.Close)
	}
}

func TestSessionCoreDropsStalePreparedDesiredState(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-stale-prepare")
	e.fixture(state.NodeID)
	current := stepHello(t, core, ctx, state, sidecar, now, hello("instance-stale-prepare", 0, "").GetHello())
	olderTicket, err := coreStep(ctx, core, current.State, current.Sidecar, SessionEvent{Kind: EventDesiredPrepareStarted, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	older, err := e.f.prepareDesiredState(ctx, state.NodeID, current.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	older.ticket, older.ownerGeneration = olderTicket.State.PrepareTicket, olderTicket.State.OwnerGeneration
	e.exec(`UPDATE user SET status = 'disabled' WHERE id = 'usr_erin'`)
	newerTicket, err := coreStep(ctx, core, olderTicket.State, olderTicket.Sidecar, SessionEvent{Kind: EventDesiredPrepareStarted, At: now.Add(2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	newer, err := e.f.prepareDesiredState(ctx, state.NodeID, current.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	newer.ticket, newer.ownerGeneration = newerTicket.State.PrepareTicket, newerTicket.State.OwnerGeneration
	fresh, err := coreStep(ctx, core, newerTicket.State, newerTicket.Sidecar, SessionEvent{Kind: EventDesiredChanged, At: now.Add(2 * time.Second), Prepared: newer})
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Frames) != 1 || fresh.Frames[0].GetDesiredState() == nil {
		t.Fatalf("newer prepared state frames = %#v", fresh.Frames)
	}
	newRevision := fresh.State.SentRevision
	stale, err := coreStep(ctx, core, fresh.State, fresh.Sidecar, SessionEvent{Kind: EventDesiredChanged, At: now.Add(3 * time.Second), Prepared: older})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale.Frames) != 0 || stale.State.SentRevision != newRevision || stale.Sidecar.SentDesired.hash != fresh.Sidecar.SentDesired.hash {
		t.Fatalf("stale prepared state changed the session: sent=%d frames=%#v", stale.State.SentRevision, stale.Frames)
	}
	node, err := e.st.Node(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node.DesiredRevision != newRevision || node.DesiredHash != fresh.State.SentStateHash {
		t.Fatalf("stored desired state = %d/%s, newest prepared state = %d/%s", node.DesiredRevision, node.DesiredHash, newRevision, fresh.State.SentStateHash)
	}
}

func TestDesiredPreparationFromEndedOwnerDoesNotReachNewSession(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-ended-prepare")
	e.fixture(state.NodeID)
	connected := stepHello(t, core, ctx, state, sidecar, now, hello("instance-ended-prepare", 0, "").GetHello())
	old := &session{f: e.f, nodeID: state.NodeID, owner: state.OwnerGeneration, caps: connected.State.Capabilities,
		ctx: ctx, done: make(chan struct{}), out: make(chan *agentv1.ConnectResponse, outQueue), core: core,
		coreState: connected.State, coreSidecar: connected.Sidecar}

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
		_, err := old.stepDesired(context.Background(), SessionEvent{Kind: EventDesiredChanged, At: now.Add(time.Second)})
		oldDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("old session preparation did not reach the desired-state read")
	}
	oldTicket := old.coreState.PrepareTicket

	newOwner, newCtx := e.f.claimOwner(state.NodeID, e.ctx)
	close(old.done)
	newCore := NewSessionCore(e.f)
	newState := SessionState{Version: sessionStateVersion, NodeID: state.NodeID, OwnerGeneration: newOwner}
	newHelloFrame := hello("instance-ended-prepare-new", 0, "")
	newHello, err := coreStep(newCtx, newCore, newState, sidecar, SessionEvent{Kind: EventHello, At: now.Add(2 * time.Second), Frame: newHelloFrame})
	if err != nil || newHello.Close != nil {
		t.Fatalf("new session Hello = close %v, err %v", newHello.Close, err)
	}
	ticket, err := coreStep(newCtx, newCore, newHello.State, newHello.Sidecar, SessionEvent{Kind: EventDesiredPrepareStarted, At: now.Add(2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e.f.prepareDesiredState(newCtx, state.NodeID, ticket.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	prepared.ticket, prepared.ownerGeneration = ticket.State.PrepareTicket, ticket.State.OwnerGeneration
	newReconcile, err := coreStep(newCtx, newCore, ticket.State, ticket.Sidecar, SessionEvent{Kind: EventInitialReconcile,
		At: now.Add(2 * time.Second), Frame: newHelloFrame, Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	newRevision := newReconcile.State.SentRevision
	stalePrepared, err := e.f.prepareDesiredState(newCtx, state.NodeID, newReconcile.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	stalePrepared.ticket, stalePrepared.ownerGeneration = oldTicket, old.owner
	stale, err := coreStep(newCtx, newCore, newReconcile.State, newReconcile.Sidecar, SessionEvent{Kind: EventDesiredChanged,
		At: now.Add(3 * time.Second), Prepared: stalePrepared})
	if err != nil || stale.State.SentRevision != newRevision || len(stale.Frames) != 0 {
		t.Fatalf("older owner's preparation changed the new session: transition=%+v err=%v", stale, err)
	}
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
	if node.DesiredRevision != newRevision || node.DesiredHash != newReconcile.State.SentStateHash || len(old.out) != 0 {
		t.Fatalf("old preparation reached the new session: stored=%d/%s new=%d/%s old frames=%d",
			node.DesiredRevision, node.DesiredHash, newRevision, newReconcile.State.SentStateHash, len(old.out))
	}
}

func TestSessionCoreStepMutatesCallerState(t *testing.T) {
	_, core, ctx, state, sidecar, now := coreFixture(t, "core-pointer-step")
	tr, err := core.Step(ctx, &state, &sidecar, SessionEvent{Kind: EventOpen, At: now})
	if err != nil {
		t.Fatal(err)
	}
	if state.HelloDeadlineUnixNano == 0 || tr.State.HelloDeadlineUnixNano != state.HelloDeadlineUnixNano {
		t.Fatalf("Step did not update caller state: state=%+v transition=%+v", state, tr.State)
	}
	if sidecar.Pending == nil || tr.Sidecar.Pending == nil {
		t.Fatalf("Step did not initialize caller sidecar: %+v", sidecar)
	}
}

func TestSessionViewIsAtomicAcrossCoreStepsAndAdminReads(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-view-atomic")
	e.fixture(state.NodeID)
	initial := stepHello(t, core, ctx, state, sidecar, now, hello("instance-view-atomic", 0, "").GetHello())
	sctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	s := &session{f: e.f, nodeID: state.NodeID, owner: state.OwnerGeneration, caps: initial.State.Capabilities, ctx: sctx, cancel: cancel,
		done: make(chan struct{}), out: make(chan *agentv1.ConnectResponse, outQueue), core: core, coreState: initial.State, coreSidecar: initial.Sidecar}
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
				if view.SentDesired != nil && view.State.SentStateHash != view.SentDesired.hash {
					failures <- "sent revision snapshot mixed state and desired hash"
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
		if _, err := s.stepDesired(ctx, SessionEvent{Kind: EventDesiredChanged, At: now.Add(time.Duration(i+1) * time.Second)}); err != nil {
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

type manualSessionAlarmTimer struct {
	ch          chan time.Time
	resets      chan sessionAlarmReset
	mu          sync.Mutex
	resetCount  int
	currentWait time.Duration
}

type sessionAlarmReset struct {
	count int
	wait  time.Duration
}

func newManualSessionAlarmTimer(initialWait time.Duration) *manualSessionAlarmTimer {
	return &manualSessionAlarmTimer{ch: make(chan time.Time, 1), resets: make(chan sessionAlarmReset, 64), currentWait: initialWait}
}

func (timer *manualSessionAlarmTimer) C() <-chan time.Time { return timer.ch }
func (timer *manualSessionAlarmTimer) Stop() bool          { return true }
func (timer *manualSessionAlarmTimer) Reset(wait time.Duration) bool {
	timer.mu.Lock()
	timer.resetCount++
	timer.currentWait = wait
	reset := sessionAlarmReset{count: timer.resetCount, wait: wait}
	timer.mu.Unlock()
	timer.resets <- reset
	return true
}

func (timer *manualSessionAlarmTimer) fire(at time.Time) { timer.ch <- at }

func (timer *manualSessionAlarmTimer) resetSnapshot() (int, time.Duration) {
	timer.mu.Lock()
	defer timer.mu.Unlock()
	return timer.resetCount, timer.currentWait
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

func TestVPSSessionUsesOneAlarmTimerForAckCadence(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("single-alarm-adapter")
	base := time.Now().UTC().Truncate(time.Second)
	var nowNanos atomic.Int64
	nowNanos.Store(base.UnixNano())
	e.f.now = func() time.Time { return time.Unix(0, nowNanos.Load()).UTC() }
	timers := make(chan *manualSessionAlarmTimer, 1)
	var timerCount atomic.Int64
	previousTimerFactory := e.f.newSessionAlarmTimer
	e.f.newSessionAlarmTimer = func(wait time.Duration) sessionAlarmTimer {
		timerCount.Add(1)
		timer := newManualSessionAlarmTimer(wait)
		timers <- timer
		return timer
	}
	t.Cleanup(func() { e.f.newSessionAlarmTimer = previousTimerFactory })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner, ownerCtx := e.f.claimOwner(a.nodeID, ctx)
	stream := &manualAgentSessionStream{ctx: ownerCtx, in: make(chan *agentv1.ConnectRequest, 4), out: make(chan *agentv1.ConnectResponse, 16)}
	done := make(chan error, 1)
	go func() {
		done <- (agentService{e.f}).runSession(ownerCtx, a.nodeID, peerCert{serial: a.leaf.SerialNumber.Text(16), notAfter: a.leaf.NotAfter}, owner, stream)
	}()

	var timer *manualSessionAlarmTimer
	select {
	case timer = <-timers:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not create its alarm timer")
	}
	if _, initialWait := timer.resetSnapshot(); initialWait != helloTimeout {
		t.Fatalf("new timer wait = %v, want the Hello deadline %v", initialWait, helloTimeout)
	}
	stream.in <- hello("single-alarm-adapter", 0, "")
	waitSessionAck(t, stream.out, 0)
	_, waitAfterHello := timer.resetSnapshot()
	if waitAfterHello != ackEvery {
		t.Fatalf("timer after Hello waits %v, want %v until the ack tick", waitAfterHello, ackEvery)
	}

	stream.in <- &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{Code: "timer-first", TimeUnix: base.Unix()}}}
	waitSessionAck(t, stream.out, 1)
	beforeSecond, _ := timer.resetSnapshot()
	nowNanos.Store(base.Add(100 * time.Millisecond).UnixNano())
	stream.in <- &agentv1.ConnectRequest{Seq: 2, Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{Code: "timer-second", TimeUnix: base.Unix()}}}
	var secondReset sessionAlarmReset
	timerResetDeadline := time.After(5 * time.Second)
	for secondReset.count <= beforeSecond {
		select {
		case secondReset = <-timer.resets:
		case <-timerResetDeadline:
			t.Fatal("second frame did not reset the session alarm")
		}
	}
	if secondReset.wait != ackEvery-100*time.Millisecond {
		t.Fatalf("timer after the second frame waits %v, want %v until ack is due", secondReset.wait, ackEvery-100*time.Millisecond)
	}
	for {
		select {
		case response := <-stream.out:
			if ack := response.GetAck(); ack != nil && ack.UpToSeq >= 2 {
				t.Fatalf("pending ack was sent before its alarm: %+v", ack)
			}
		default:
			goto noEarlyAck
		}
	}
noEarlyAck:
	nowNanos.Store(base.Add(ackEvery).UnixNano())
	timer.fire(base.Add(ackEvery))
	waitSessionAck(t, stream.out, 2)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not stop after cancellation")
	}
	if got := timerCount.Load(); got != 1 {
		t.Fatalf("runSession created %d alarm timers, want exactly one", got)
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

func hasEffect(tr Transition, kind EffectKind) bool {
	for _, effect := range tr.Effects {
		if effect.Kind == kind {
			return true
		}
	}
	return false
}

func effectKinds(tr Transition) []EffectKind {
	out := make([]EffectKind, len(tr.Effects))
	for i, effect := range tr.Effects {
		out[i] = effect.Kind
	}
	return out
}
