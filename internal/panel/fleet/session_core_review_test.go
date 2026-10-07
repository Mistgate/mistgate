package fleet

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

func reviewSession(t *testing.T, name string) (*env, *session, *agentv1.ConnectRequest, time.Time) {
	t.Helper()
	e, core, ctx, state, sidecar, now := coreFixture(t, name)
	frame := hello("instance-1", 0, "")
	if _, err := core.Step(ctx, &state, &sidecar, SessionEvent{Kind: EventOpen, At: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Step(ctx, &state, &sidecar, SessionEvent{Kind: EventHello, At: now, Frame: frame}); err != nil {
		t.Fatal(err)
	}
	s := &session{f: e.f, nodeID: state.NodeID, owner: state.OwnerGeneration, caps: state.Capabilities,
		ctx: ctx, done: make(chan struct{}), out: make(chan *agentv1.ConnectResponse, outQueue),
		core: core, coreState: state, coreSidecar: sidecar}
	return e, s, frame, now
}

func drainDesiredFrames(s *session) []*agentv1.DesiredState {
	var out []*agentv1.DesiredState
	for {
		select {
		case frame := <-s.out:
			if desired := frame.GetDesiredState(); desired != nil {
				out = append(out, desired)
			}
		default:
			return out
		}
	}
}

func TestInitialReconcileSurvivesConcurrentRecompute(t *testing.T) {
	e, s, frame, now := reviewSession(t, "review-initial-reconcile")
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
		_, err := s.stepDesired(s.ctx, SessionEvent{Kind: EventDesiredChanged, At: now, Frame: frame})
		initial <- err
	}()
	<-started
	if _, err := s.stepDesired(s.ctx, SessionEvent{Kind: EventDesiredChanged, At: now.Add(time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-initial; err != nil {
		t.Fatal(err)
	}
	if got := drainDesiredFrames(s); len(got) == 0 {
		t.Fatal("initial desired state was lost when recompute finished first")
	}
}

func TestDesiredPreparationSerializesReadsAndSendsLatestState(t *testing.T) {
	e, s, frame, now := reviewSession(t, "review-prepare-order")
	if _, err := s.stepDesired(s.ctx, SessionEvent{Kind: EventDesiredChanged, At: now, Frame: frame}); err != nil {
		t.Fatal(err)
	}
	drainDesiredFrames(s)

	var version atomic.Int32
	version.Store(1)
	var reads, active, maxActive atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	e.f.cfg.Desired = func(context.Context, string) ([]statehash.Inbound, error) {
		v := version.Load()
		call := reads.Add(1)
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
		return []statehash.Inbound{{Spec: plugin.InboundSpec{ID: "inb_review", Protocol: "fakehy", ProfileID: "prf_review", Version: uint64(v), Enabled: true}}}, nil
	}
	first := make(chan error, 1)
	go func() {
		_, err := s.stepDesired(s.ctx, SessionEvent{Kind: EventDesiredChanged, At: now.Add(time.Second)})
		first <- err
	}()
	<-started
	version.Store(2)
	if _, err := s.stepDesired(s.ctx, SessionEvent{Kind: EventDesiredChanged, At: now.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	readsBeforeRelease := reads.Load()
	close(release)
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
	if s.coreSidecar.SentDesired == nil || s.coreSidecar.SentDesired.hash != latest.desired.hash {
		t.Fatalf("last sent hash = %v, latest database hash = %v", s.coreSidecar.SentDesired, latest.desired.hash)
	}
	frames := drainDesiredFrames(s)
	if len(frames) == 0 || frames[len(frames)-1].StateHash != latest.desired.hash {
		t.Fatalf("last DesiredState = %v, latest database hash = %v", frames, latest.desired.hash)
	}
}

func TestStepAfterEndDoesNotRearmOrRewritePoison(t *testing.T) {
	e, s, _, now := reviewSession(t, "review-step-after-end")
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
	e, core, ctx, state, sidecar, now := coreFixture(t, "review-prepare-retry")
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
	if failed.State.Preparing || failed.State.PrepareRetryAtUnixNano != now.Add(2*time.Second+prepareRetryDelay).UnixNano() || !failed.State.FullResendPending {
		t.Fatalf("failed preparation state = %+v", failed.State)
	}
	retryAt := time.Unix(0, failed.State.PrepareRetryAtUnixNano)
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
