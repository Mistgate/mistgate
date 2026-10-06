package fleet

import (
	"context"
	"encoding/json"
	"reflect"
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

func stepHello(t *testing.T, core *SessionCore, ctx context.Context, state SessionState, sidecar SessionSidecar, now time.Time, h *agentv1.Hello) Transition {
	t.Helper()
	frame := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Hello{Hello: h}}
	started, err := core.Step(ctx, state, sidecar, SessionEvent{Kind: EventHello, At: now, Frame: frame})
	if err != nil {
		t.Fatal(err)
	}
	if started.Close != nil {
		t.Fatalf("hello closed: %+v", started.Close)
	}
	prepared, err := core.f.prepareDesiredState(ctx, started.State.NodeID, started.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	var autoBandwidthAt time.Time
	for _, effect := range started.Effects {
		if effect.Kind == EffectSessionStarted && effect.Started != nil && effect.Started.AutoMeasure {
			autoBandwidthAt = now
		}
	}
	tr, err := core.Step(ctx, started.State, started.Sidecar, SessionEvent{Kind: EventInitialReconcile, At: now, Frame: frame,
		Prepared: prepared, AutoBandwidthAt: autoBandwidthAt})
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
	prepared, err := e.f.prepareDesiredState(ctx, tr.State.NodeID, tr.State.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := core.Step(ctx, tr.State, tr.Sidecar, SessionEvent{Kind: EventDesiredChanged, At: at, Mode: mode, Prepared: prepared})
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
	_, core, ctx, state, sidecar, _ := coreFixture(t, "core-hello-deadline")
	deadline := time.Unix(0, state.HelloDeadlineUnixNano).UTC()
	tr, err := core.Step(ctx, state, sidecar, SessionEvent{Kind: EventHello, At: deadline, Frame: hello("instance-deadline", 0, "")})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Close != nil || tr.State.InstanceID == "" {
		t.Fatalf("Hello at the deadline was rejected without an alarm: close=%+v state=%+v", tr.Close, tr.State)
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
	before, err := core.Step(ctx, tr.State, tr.Sidecar, SessionEvent{Kind: EventAlarm, Alarm: AlarmAutoBandwidth, At: time.Unix(0, want-1).UTC()})
	if err != nil || hasEffect(before, EffectAutoBandwidth) {
		t.Fatalf("early auto-bandwidth alarm = %+v, %v", before.Effects, err)
	}
	due, err := core.Step(ctx, tr.State, tr.Sidecar, SessionEvent{Kind: EventAlarm, Alarm: AlarmAutoBandwidth, At: time.Unix(0, want).UTC()})
	if err != nil || !hasEffect(due, EffectAutoBandwidth) || due.State.AutoBandwidthUnixNano != 0 {
		t.Fatalf("due auto-bandwidth alarm = %+v state=%+v, %v", due.Effects, due.State, err)
	}
}

func TestSessionAutoBandwidthDeadlineStartsAfterConnectEvents(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-bw-start")
	h := hello("instance-auto-bandwidth-start", 0, "")
	h.GetHello().Capabilities = []string{capBandwidth}
	helloTransition, err := core.Step(ctx, state, sidecar, SessionEvent{Kind: EventHello, At: now, Frame: h})
	if err != nil {
		t.Fatal(err)
	}
	var started *SessionStarted
	for _, effect := range helloTransition.Effects {
		if effect.Kind == EffectSessionStarted {
			started = effect.Started
		}
	}
	if started == nil || !started.AutoMeasure {
		t.Fatalf("session start effect = %+v", started)
	}

	sctx, cancel := context.WithCancelCause(ctx)
	s := &session{f: e.f, nodeID: state.NodeID, owner: state.OwnerGeneration, caps: helloTransition.State.Capabilities,
		ctx: sctx, cancel: cancel, done: make(chan struct{}), out: make(chan *agentv1.ConnectResponse, outQueue),
		core: core, coreState: helloTransition.State, coreSidecar: helloTransition.Sidecar}
	defer func() {
		cancel(nil)
		close(s.done)
	}()
	startAt := now.Add(5 * time.Second)
	e.f.now = func() time.Time { return startAt }
	s.coreMu.Lock()
	err = s.dispatchCoreEffect(sctx, &helloTransition, SessionEffect{Kind: EffectSessionStarted, Started: started}, nil)
	deadline := s.coreState.AutoBandwidthUnixNano
	s.coreMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	want := startAt.Add(e.f.measureDelay).UnixNano()
	if deadline != want {
		t.Fatalf("auto-bandwidth deadline = %d, want %d", deadline, want)
	}
}

func TestSessionStepErrorCancelsSession(t *testing.T) {
	e, core, ctx, state, _, _ := coreFixture(t, "core-step-error")
	sctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	s := &session{f: e.f, nodeID: state.NodeID, ctx: sctx, cancel: cancel, core: core}
	tr, err := s.stepCore(sctx, SessionEvent{Kind: EventKind(255), At: time.Now()}, nil)
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
		transition, err := s.stepCore(sctx, SessionEvent{Kind: EventAgentFrame, At: frameAt, Frame: stats}, nil)
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
	liveness, err := s.stepCore(sctx, SessionEvent{Kind: EventAlarm, Alarm: AlarmLiveness, At: time.Unix(0, oldDeadline)}, nil)
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

func TestSessionCoreAckCoalescingAndImmediateFlush(t *testing.T) {
	_, core, ctx, state, sidecar, now := coreFixture(t, "core-ack")
	tr := stepHello(t, core, ctx, state, sidecar, now, hello("instance-ack", 0, "").GetHello())
	state, sidecar = tr.State, tr.Sidecar
	state.LastAckUnixNano = now.UnixNano()

	batch := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix(),
	}}}
	tr, err := core.Step(ctx, state, sidecar, SessionEvent{Kind: EventAgentFrame, At: now, Frame: batch})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Frames) != 0 || tr.State.AckPending != 1 || tr.State.AckSent != 0 {
		t.Fatalf("first ack should wait for ticker: state=%+v frames=%#v", tr.State, tr.Frames)
	}
	state, sidecar = tr.State, tr.Sidecar
	tr, err = core.Step(ctx, state, sidecar, SessionEvent{Kind: EventAlarm, Alarm: AlarmAck, At: now.Add(ackEvery)})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Frames) != 1 || tr.Frames[0].GetAck().GetUpToSeq() != 1 {
		t.Fatalf("ticker ack = %#v", tr.Frames)
	}
	state, sidecar = tr.State, tr.Sidecar
	batch.Seq = 2
	tr, err = core.Step(ctx, state, sidecar, SessionEvent{Kind: EventAgentFrame, At: now.Add(2*ackEvery + 100*time.Millisecond), Frame: batch})
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
	}}}
	first, err := core.Step(ctx, state, sidecar, SessionEvent{Kind: EventAgentFrame, At: now, Frame: stats})
	if err != nil {
		t.Fatal(err)
	}
	second, err := core.Step(ctx, first.State, first.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now.Add(time.Second), Frame: stats})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEffect(first, EffectLiveUpdate) || hasEffect(second, EffectLiveUpdate) {
		t.Fatalf("duplicate stats live effects: first=%v second=%v", effectKinds(first), effectKinds(second))
	}
	if len(first.Frames) != 1 || len(second.Frames) != 0 {
		t.Fatalf("stats acknowledgements: first=%#v second=%#v", first.Frames, second.Frames)
	}

	ev := &agentv1.ConnectRequest{Seq: 2, Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{Code: "core_test"}}}
	first, err = core.Step(ctx, second.State, second.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now.Add(2 * time.Second), Frame: ev})
	if err != nil {
		t.Fatal(err)
	}
	second, err = core.Step(ctx, first.State, first.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now.Add(3 * time.Second), Frame: ev})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Frames) != 1 || first.Frames[0].GetAck().GetUpToSeq() != 2 || len(second.Frames) != 0 {
		t.Fatalf("event acknowledgements: first=%#v second=%#v", first.Frames, second.Frames)
	}
}

func TestSessionCorePoisonBatch(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "core-poison")
	ids := e.fixture(state.NodeID)
	tr := stepHello(t, core, ctx, state, sidecar, now, hello("instance-poison", 0, "").GetHello())
	state, sidecar = tr.State, tr.Sidecar
	e.exec(`CREATE TRIGGER reject_core_stats BEFORE INSERT ON traffic_bucket WHEN NEW.user_id = 'usr_alice' BEGIN SELECT RAISE(ABORT, 'test refusal'); END`)
	batch := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix(),
		Traffic: []*agentv1.TrafficDelta{{CredId: "crd_alice_hy", InboundId: ids.i1, BytesUp: 5}},
	}}}
	first, err := core.Step(ctx, state, sidecar, SessionEvent{Kind: EventAgentFrame, At: now, Frame: batch})
	if err != nil {
		t.Fatal(err)
	}
	if first.Close == nil || first.Close.Class != CloseInternal || len(first.Frames) != 0 {
		t.Fatalf("first refusal = close %v, frames %#v", first.Close, first.Frames)
	}
	second, err := core.Step(ctx, first.State, first.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now.Add(time.Second), Frame: batch})
	if err != nil {
		t.Fatal(err)
	}
	if second.Close != nil || len(second.Frames) != 1 || second.Frames[0].GetAck().GetUpToSeq() != 1 {
		t.Fatalf("second refusal = close %v, frames %#v", second.Close, second.Frames)
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
	tr, err := core.Step(ctx, state, sidecar, SessionEvent{Kind: EventAgentFrame, At: now, Frame: base})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEffect(tr, EffectDesiredReconcile) {
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
	tr, err = core.Step(ctx, state, sidecar, SessionEvent{Kind: EventAgentFrame, At: now, Frame: stale})
	if err != nil {
		t.Fatal(err)
	}
	if tr.State.DriftResent || tr.State.Drift || len(tr.Frames) != 0 {
		t.Fatalf("stale result affected drift: state=%+v frames=%#v", tr.State, tr.Frames)
	}

	bad := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: state.SentRevision, Status: agentv1.ApplyStatus_APPLY_STATUS_APPLIED, StateHash: "wrong",
	}}}
	first, err := core.Step(ctx, state, sidecar, SessionEvent{Kind: EventAgentFrame, At: now, Frame: bad})
	if err != nil {
		t.Fatal(err)
	}
	if !first.State.DriftResent || !hasEffect(first, EffectDesiredReconcile) {
		t.Fatalf("first drift = state %+v effects %#v", first.State, first.Effects)
	}
	first = reconcilePrepared(t, e, core, ctx, first, reconcileFull, now)
	if len(first.Frames) != 1 || first.Frames[0].GetDesiredState().BaseRevision != 0 {
		t.Fatalf("first drift resend = %#v", first.Frames)
	}
	bad.GetApplyResult().Revision = first.State.SentRevision
	second, err := core.Step(ctx, first.State, first.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now.Add(time.Second), Frame: bad})
	if err != nil {
		t.Fatal(err)
	}
	if !second.State.Drift || !second.State.DriftResent || len(second.Frames) != 0 {
		t.Fatalf("persistent drift = state %+v frames %#v", second.State, second.Frames)
	}

	liveAt := time.Unix(0, second.State.LivenessDeadlineUnixNano)
	expired, err := core.Step(ctx, second.State, second.Sidecar, SessionEvent{Kind: EventAlarm, Alarm: AlarmLiveness, At: liveAt})
	if err != nil {
		t.Fatal(err)
	}
	if expired.Close == nil || expired.Close.Class != CloseDeadline {
		t.Fatalf("liveness alarm = %+v", expired.Close)
	}

	certAt := time.Unix(0, second.State.NextCertCheckUnixNano)
	cert, err := core.Step(ctx, second.State, second.Sidecar, SessionEvent{Kind: EventAlarm, Alarm: AlarmCertificate, At: certAt})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEffect(cert, EffectCheckCertificate) {
		t.Fatalf("certificate alarm effects = %v", effectKinds(cert))
	}
}

func TestSessionCoreCommandLogsAndSupersede(t *testing.T) {
	_, core, ctx, state, sidecar, now := coreFixture(t, "core-requests")
	tr := stepHello(t, core, ctx, state, sidecar, now, hello("instance-requests", 0, "").GetHello())
	state, sidecar = tr.State, tr.Sidecar
	command := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UpdateAgent{UpdateAgent: &agentv1.UpdateAgent{RequestId: "req-command"}}}
	tr, err := core.Step(ctx, state, sidecar, SessionEvent{Kind: EventAdminCommand, At: now, Request: &AdminRequest{
		RequestID: "req-command", Deadline: now.Add(time.Minute), Frame: command,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Frames) != 1 || len(tr.Sidecar.Pending) != 1 {
		t.Fatalf("command transition = state %+v frames %#v", tr.State, tr.Frames)
	}
	result := &agentv1.CommandResult{RequestId: "req-command", Ok: true}
	beforeClose, err := core.Step(ctx, tr.State, tr.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now.Add(time.Second),
		Frame: &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: result}}})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEffect(beforeClose, EffectCommandResult) || beforeClose.Effects[0].RequestID != "req-command" {
		t.Fatalf("command result effects = %+v", beforeClose.Effects)
	}
	logFrame := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_LogRequest{LogRequest: &agentv1.LogRequest{RequestId: "req-log"}}}
	started, err := core.Step(ctx, beforeClose.State, beforeClose.Sidecar, SessionEvent{Kind: EventLogStart, At: now, Request: &AdminRequest{
		RequestID: "req-log", Deadline: now.Add(time.Minute), Frame: logFrame,
	}})
	if err != nil {
		t.Fatal(err)
	}
	chunk := &agentv1.LogChunk{RequestId: "req-log", Lines: []*agentv1.LogLine{{Message: "line"}}}
	routed, err := core.Step(ctx, started.State, started.Sidecar, SessionEvent{Kind: EventAgentFrame, At: now,
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

	superseded, err := core.Step(ctx, routed.State, routed.Sidecar, SessionEvent{Kind: EventOwnerSuperseded, At: now})
	if err != nil || superseded.Close == nil || superseded.Close.Class != CloseConflict {
		t.Fatalf("supersede = %+v, %v", superseded.Close, err)
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
