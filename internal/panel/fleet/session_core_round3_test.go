package fleet

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/statehash"
)

func round3Session(t *testing.T, e *env, name string) (*session, func()) {
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
	e.f.mu.Lock()
	cancel := e.f.owners[nodeID].cancel
	e.f.mu.Unlock()
	s := &session{f: e.f, nodeID: nodeID, owner: owner, caps: state.Capabilities, ctx: ctx, cancel: cancel,
		done: make(chan struct{}), out: make(chan *agentv1.ConnectResponse, outQueue), core: core,
		coreState: state, coreSidecar: sidecar}
	if !e.f.register(s) {
		t.Fatal("session owner was not registered")
	}
	if _, err := s.stepDesired(ctx, SessionEvent{Kind: EventDesiredChanged, At: e.f.now()}); err != nil {
		t.Fatal(err)
	}
	drainDesiredFrames(s)

	var endOnce sync.Once
	end := func() {
		endOnce.Do(func() {
			if s.cancel != nil {
				s.cancel(nil)
			}
			close(s.done)
			e.f.unregister(s)
		})
	}
	t.Cleanup(end)
	return s, end
}

func waitRound3(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func desiredRound3Frames(t *testing.T, s *session, count int) []*agentv1.DesiredState {
	t.Helper()
	timeout := time.After(5 * time.Second)
	frames := make([]*agentv1.DesiredState, 0, count)
	for len(frames) < count {
		select {
		case frame := <-s.out:
			if desired := frame.GetDesiredState(); desired != nil {
				frames = append(frames, desired)
			}
		case <-timeout:
			t.Fatalf("received %d DesiredState frames, want %d", len(frames), count)
		}
	}
	return frames
}

func TestRecomputeWaitsForOnePreparationRoundPerNode(t *testing.T) {
	e := newCoreEnv(t)
	slow, endSlow := round3Session(t, e, "de")
	fast, endFast := round3Session(t, e, "node1")
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
				if err := answerRound3BaseMismatch(slow); err != nil {
					return nil, err
				}
			case 2:
				close(secondRead)
				if err := answerRound3BaseMismatch(slow); err != nil {
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
	waitRound3(t, firstRead, "the slow node's inline read")
	waitRound3(t, fastRead, "the other node's read")
	closeFirst.Do(func() { close(releaseFirst) })
	waitRound3(t, secondRead, "the slow node's background follow-up")
	select {
	case <-recomputeDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("recompute waited for the slow node's background follow-up; reads=%d", reads.Load())
	}
}

func answerRound3BaseMismatch(s *session) error {
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
	e, core, ctx, state, _, now := coreFixture(t, "session-close-cause")
	sctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	s := &session{f: e.f, nodeID: state.NodeID, ctx: sctx, cancel: cancel, core: core}
	tr, err := s.stepCore(sctx, SessionEvent{Kind: EventOwnerSuperseded, At: now})
	if err != nil || tr.Close == nil {
		t.Fatalf("session close transition = %+v, err %v", tr.Close, err)
	}
	cause := context.Cause(sctx)
	closeErr := sessionCloseError(tr.Close)
	if cause == nil || code(cause) != code(closeErr) || cause.Error() != closeErr.Error() {
		t.Fatalf("session cause = %v, want %v", cause, closeErr)
	}
	if received := errOrCause(sctx); received == nil || code(received) != code(cause) || received.Error() != cause.Error() {
		t.Fatalf("receive loop error = %v, want close cause %v", received, cause)
	}
}
