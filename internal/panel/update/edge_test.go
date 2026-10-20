package update

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/release"
)

const edgeReleaseTag = "v1.2.3"

func newEdgeFixture(t *testing.T, panelVersion string, opts fakeReleaseOptions) (*env, *fakeRelease) {
	t.Helper()
	e := newEnv(t)
	if opts.tag == "" {
		opts.tag = edgeReleaseTag
	}
	if opts.built == 0 {
		opts.built = newBuilt
	}
	if opts.expires == 0 {
		opts.expires = e.clk.Now().Add(30 * 24 * time.Hour).Unix()
	}
	f := newFakeRelease(t, []byte("signed node binary"), opts)
	oldBase := releaseDownloadBase
	releaseDownloadBase = f.server.URL + "/"
	t.Cleanup(func() { releaseDownloadBase = oldBase })
	if panelVersion == "" {
		panelVersion = opts.tag
	}
	e.pub = f.key
	e.s = e.newEdgeService(f.key, panelVersion)
	return e, f
}

func edgeTick(e *env) {
	e.t.Helper()
	e.s.EdgeTick(e.ctx, e.clk.Now())
}

func edgeNode(e *env, name string, inbounds int) string {
	e.t.Helper()
	return e.addNode(name, nodeOpts{caps: []string{"doctor/1", capLinkUpdate, capGuard}, built: oldBuilt, inbounds: inbounds})
}

func startEdgeRollout(e *env, batch int, nodeIDs ...string) store.RolloutRow {
	e.t.Helper()
	ro, err := e.s.start(e.ctx, nodeIDs, batch)
	if err != nil {
		e.t.Fatalf("start edge rollout: %v", err)
	}
	return ro
}

func updateFrameFor(t *testing.T, f *fakeFleet, nodeID string) sentUpdateFrame {
	t.Helper()
	for _, frame := range f.updateFrames() {
		if frame.nodeID == nodeID {
			return frame
		}
	}
	t.Fatalf("no UpdateAgent frame for %s", nodeID)
	return sentUpdateFrame{}
}

func recordEdgeAnswer(t *testing.T, e *env, nodeID, requestID string, result *agentv1.CommandResult) {
	t.Helper()
	if err := e.s.RecordAnswer(e.ctx, nodeID, requestID, result); err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}
}

func edgeUpgrade(e *env, nodeID string) {
	e.t.Helper()
	e.hello(nodeID, "v1.2.3-node", newBuilt, []string{"doctor/1", capLinkUpdate, capGuard}, "")
}

func TestEdgeTickCanaryThenBatchOfTwo(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	ids := []string{edgeNode(e, "a", 0), edgeNode(e, "b", 0), edgeNode(e, "c", 0), edgeNode(e, "d", 0), edgeNode(e, "e", 0)}
	startEdgeRollout(e, 2, ids...)
	if e.step(ids[0]).Stage != 0 || e.step(ids[1]).Stage != 1 || e.step(ids[2]).Stage != 1 || e.step(ids[3]).Stage != 2 || e.step(ids[4]).Stage != 2 {
		t.Fatal("rollout stages do not have a canary followed by batches of two")
	}

	edgeTick(e)
	frames := e.fl.updateFrames()
	if len(frames) != 1 || frames[0].nodeID != ids[0] {
		t.Fatalf("initial frames = %+v, want only the canary", frames)
	}
	frame := frames[0]
	if frame.token == "" || len(e.step(ids[0]).DownloadSHA) != 64 || frame.deadline != e.clk.Now().Add(e.s.cfg.AnswerWait+e.s.cfg.ReconnectWait) {
		t.Fatalf("sent frame/step missing token metadata or deadline: frame=%+v step=%+v", frame, e.step(ids[0]))
	}
	recordEdgeAnswer(t, e, ids[0], frame.requestID, okResult())
	edgeTick(e)
	edgeUpgrade(e, ids[0])
	e.commit(ids[0])
	edgeTick(e) // reconnect moves the canary into GATING
	edgeTick(e) // the gate passes, then the next batch is sent

	frames = e.fl.updateFrames()
	if len(frames) != 3 || frames[1].nodeID != ids[1] || frames[2].nodeID != ids[2] {
		t.Fatalf("frames after canary passed = %+v, want the next batch of two", frames)
	}
}

func TestEdgeTickRecordedAnswerOutcomesAndDeadline(t *testing.T) {
	for _, tc := range []struct {
		name        string
		result      *agentv1.CommandResult
		advance     time.Duration
		wantState   string
		wantError   string
		wantPause   string
		wantRollout string
	}{
		{name: "download failed", result: &agentv1.CommandResult{Error: "download_failed: upstream"}, wantState: store.StepFailed,
			wantError: "download_failed", wantPause: pauseStepFailed, wantRollout: store.RolloutPaused},
		{name: "noop", result: &agentv1.CommandResult{Ok: true, Params: map[string]string{"noop": "1"}}, wantState: store.StepSkipped,
			wantError: errUpToDate, wantRollout: store.RolloutDone},
		{name: "no answer at thirteen minutes", advance: 13*time.Minute + time.Second, wantState: store.StepFailed,
			wantError: errNoAnswer, wantPause: pauseGateFailed, wantRollout: store.RolloutPaused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
			id := edgeNode(e, "a", 0)
			startEdgeRollout(e, 0, id)
			edgeTick(e)
			frame := updateFrameFor(t, e.fl, id)
			if tc.result != nil {
				recordEdgeAnswer(t, e, id, frame.requestID, tc.result)
			}
			e.clk.Advance(tc.advance)
			edgeTick(e)
			step := e.step(id)
			if step.State != tc.wantState || step.ErrorKey != tc.wantError {
				t.Fatalf("step = %+v, want state=%s error=%s", step, tc.wantState, tc.wantError)
			}
			rollout := e.rollout()
			if rollout.Status != tc.wantRollout || rollout.PauseKey != tc.wantPause {
				t.Fatalf("rollout = %+v, want status=%s pause=%s", rollout, tc.wantRollout, tc.wantPause)
			}
		})
	}
}

func TestEdgeTickAppliesRecordedAnswerAndPassesGateWithoutHealth(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "a", 0)
	startEdgeRollout(e, 0, id)
	edgeTick(e)
	frame := updateFrameFor(t, e.fl, id)
	recordEdgeAnswer(t, e, id, frame.requestID, okResult())
	edgeTick(e) // record AckedAt
	edgeUpgrade(e, id)
	e.commit(id)
	edgeTick(e) // reconnect
	if step := e.step(id); step.State != store.StepGating {
		t.Fatalf("after reconnect step = %+v, want GATING", step)
	}
	edgeTick(e) // nil Health means there is no client-eye probe gate
	if step := e.step(id); step.State != store.StepPassed {
		t.Fatalf("after gate step = %+v, want PASSED", step)
	}
	if rollout := e.rollout(); rollout.Status != store.RolloutDone {
		t.Fatalf("rollout = %+v, want DONE", rollout)
	}
}

func TestEdgeTickRollbackUsesRecordedAnswerOrOldBuild(t *testing.T) {
	for _, tc := range []struct {
		name      string
		answer    bool
		wantState string
		wantError string
	}{
		{name: "recorded ok", answer: true, wantState: store.StepRolledBack, wantError: errInboundFailed},
		{name: "old build after timeout", wantState: store.StepRolledBack, wantError: errInboundFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
			id := edgeNode(e, "a", 1)
			startEdgeRollout(e, 0, id)
			edgeTick(e)
			update := updateFrameFor(t, e.fl, id)
			recordEdgeAnswer(t, e, id, update.requestID, okResult())
			edgeTick(e)
			edgeUpgrade(e, id)
			e.commit(id)
			edgeTick(e)
			e.exec(`UPDATE inbound SET state = 'failed' WHERE node_id = ?`, id)
			edgeTick(e) // start the inbound grace window
			e.clk.Advance(e.s.cfg.InboundGrace)
			edgeTick(e) // send rollback
			step := e.step(id)
			if step.State != store.StepGating || step.RequestID == "" || step.Params["gate"] != errInboundFailed {
				t.Fatalf("rollback request step = %+v", step)
			}
			frames := e.fl.rollbackRequestFrames()
			if len(frames) != 1 || frames[0].requestID != step.RequestID {
				t.Fatalf("rollback frames = %+v, step request = %q", frames, step.RequestID)
			}
			if tc.answer {
				recordEdgeAnswer(t, e, id, step.RequestID, okResult())
			} else {
				e.hello(id, "0.1.0-old", oldBuilt, []string{"doctor/1", capLinkUpdate, capGuard}, "")
				e.clk.Advance(edgeRollbackTimeout)
			}
			edgeTick(e)
			step = e.step(id)
			if step.State != tc.wantState || step.ErrorKey != tc.wantError {
				t.Fatalf("rollback outcome = %+v, want %s/%s", step, tc.wantState, tc.wantError)
			}
			if rollout := e.rollout(); rollout.Status != store.RolloutPaused || rollout.PauseKey != pauseGateFailed {
				t.Fatalf("rollout = %+v, want paused for gate failure", rollout)
			}
		})
	}
}

func TestEdgeCancelBetweenLoadAndSendDoesNotSend(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "a", 0)
	ro := startEdgeRollout(e, 0, id)
	e.fl.mu.Lock()
	e.fl.afterLive = func(*fakeFleet) {
		if _, err := e.st.SetRolloutStatus(e.ctx, ro.ID, []string{store.RolloutRunning}, store.RolloutPaused, pauseOwner, nil, time.Time{}); err != nil {
			e.t.Errorf("pause rollout between load and send: %v", err)
		}
	}
	e.fl.mu.Unlock()
	edgeTick(e)
	e.fl.mu.Lock()
	e.fl.afterLive = nil
	e.fl.mu.Unlock()
	if frames := e.fl.updateFrames(); len(frames) != 0 {
		t.Fatalf("frames after the rollout lost its send CAS = %+v", frames)
	}
	if step := e.step(id); step.State != store.StepPending {
		t.Fatalf("step after losing MarkStepSent CAS = %+v, want PENDING", step)
	}
}

func TestEdgeOverlappingPassesEmitOneOutcomeEvent(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "a", 0)
	startEdgeRollout(e, 0, id)
	edgeTick(e)
	frame := updateFrameFor(t, e.fl, id)
	recordEdgeAnswer(t, e, id, frame.requestID, &agentv1.CommandResult{Error: "download_failed"})
	ro := e.rollout()
	step := e.step(id) // both passes retain the same SENT + answer snapshot
	liveRows, err := e.fl.Live(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	live := map[string]store.NodeLiveRow{}
	for _, row := range liveRows {
		live[row.NodeID] = row
	}
	passFor := func() *pass {
		copyStep := step
		return &pass{s: e.s, ctx: e.ctx, ro: ro, steps: []store.StepRow{copyStep}, live: live, now: e.clk.Now()}
	}
	p1, p2 := passFor(), passFor()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); p1.advanceSent(0) }()
	go func() { defer wg.Done(); p2.advanceSent(0) }()
	wg.Wait()
	var events int
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT count(*) FROM event WHERE node_id = ? AND code = 'update_step_failed'`, id).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("failure events = %d, want one", events)
	}
	if rollout := e.rollout(); rollout.Status != store.RolloutPaused || rollout.PauseKey != pauseStepFailed {
		t.Fatalf("rollout = %+v, want one step_failed pause", rollout)
	}
}

func TestEdgeBundleChangedPausesPendingSteps(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "a", 0)
	startEdgeRollout(e, 0, id)
	e.s.cfg.PanelVersion = "v1.2.4"
	edgeTick(e)
	rollout := e.rollout()
	if rollout.Status != store.RolloutPaused || rollout.PauseKey != pauseBundleChanged {
		t.Fatalf("rollout = %+v, want bundle_changed", rollout)
	}
	if step := e.step(id); step.State != store.StepPending {
		t.Fatalf("pending step changed: %+v", step)
	}
	if frames := e.fl.updateFrames(); len(frames) != 0 {
		t.Fatalf("bundle-changed rollout sent frames: %+v", frames)
	}
}

func TestEdgeScheduledUpdateDoesNotRequireLocalSession(t *testing.T) {
	e, f := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "scheduled", 0)
	e.fl.mu.Lock()
	e.fl.noSession[id] = true
	e.fl.mu.Unlock()
	row := store.NodeUpdateScheduleRow{NodeID: id, ToVersion: edgeReleaseTag, ToBuilt: newBuilt,
		ScheduledAt: e.clk.Now().Unix() - 1, CreatedBy: "adm_test", CreatedAt: e.clk.Now()}
	if err := e.st.SetNodeUpdateSchedule(e.ctx, row); err != nil {
		t.Fatal(err)
	}
	edgeTick(e) // schedules are created in the admin isolate; this jobs isolate starts with a cold release cache
	if _, err := e.st.ActiveRollout(e.ctx); err != nil {
		t.Fatalf("scheduled rollout did not start with HasSession=false: %v", err)
	}
	edgeTick(e)
	frame := updateFrameFor(t, e.fl, id)
	if frame.nodeID != id || !bytes.Contains(frame.manifest, []byte(f.tag)) {
		t.Fatalf("scheduled frame = %+v", frame)
	}
}

func TestEdgeUpdatingRefreshesAfterTenSeconds(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "a", 0)
	startEdgeRollout(e, 0, id)
	edgeTick(e)
	if !e.s.Updating(id) {
		t.Fatal("sent step is not reported as updating")
	}
	e.exec(`UPDATE update_step SET state = 'passed' WHERE node_id = ?`, id)
	e.exec(`UPDATE update_rollout SET status = 'done'`)
	if !e.s.Updating(id) {
		t.Fatal("edge updating cache refreshed before its ten-second TTL")
	}
	e.clk.Advance(updatingTTL + time.Second)
	if e.s.Updating(id) {
		t.Fatal("edge updating cache did not refresh after its TTL")
	}
}

func TestEdgeLinkDownloadAuthorizesOnlyCurrentSentStep(t *testing.T) {
	t.Run("empty token is rejected before the store read", func(t *testing.T) {
		e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
		ctx, cancel := context.WithCancel(e.ctx)
		cancel()
		assertLinkNotFound(t, e.s, ctx, "nod_missing", "mistgate-node-linux-amd64", "")
	})
	t.Run("valid token and listed name", func(t *testing.T) {
		e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
		id := edgeNode(e, "a", 0)
		startEdgeRollout(e, 0, id)
		edgeTick(e)
		frame := updateFrameFor(t, e.fl, id)
		want := releaseFileURL(releaseDownloadBase, edgeReleaseTag, "mistgate-node-linux-amd64")
		got, err := e.s.LinkDownload(e.ctx, id, "mistgate-node-linux-amd64", frame.token)
		if err != nil || got != want {
			t.Fatalf("LinkDownload = %q, %v; want %q", got, err, want)
		}
		if frame.token == e.step(id).DownloadSHA {
			t.Fatal("raw token was stored instead of its digest")
		}
	})
	t.Run("gating step", func(t *testing.T) {
		e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
		id := edgeNode(e, "a", 0)
		startEdgeRollout(e, 0, id)
		edgeTick(e)
		frame := updateFrameFor(t, e.fl, id)
		recordEdgeAnswer(t, e, id, frame.requestID, okResult())
		edgeTick(e)
		edgeUpgrade(e, id)
		edgeTick(e)
		e.wantStep(id, store.StepGating, "")
		assertLinkNotFound(t, e.s, e.ctx, id, "mistgate-node-linux-amd64", frame.token)
	})

	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *env, string, sentUpdateFrame)
	}{
		{name: "wrong token", mutate: func(t *testing.T, e *env, id string, _ sentUpdateFrame) {
			assertLinkNotFound(t, e.s, e.ctx, id, "mistgate-node-linux-amd64", "wrong-token")
		}},
		{name: "another node", mutate: func(t *testing.T, e *env, _ string, frame sentUpdateFrame) {
			other := edgeNode(e, "other", 0)
			assertLinkNotFound(t, e.s, e.ctx, other, "mistgate-node-linux-amd64", frame.token)
		}},
		{name: "name outside manifest", mutate: func(t *testing.T, e *env, id string, frame sentUpdateFrame) {
			assertLinkNotFound(t, e.s, e.ctx, id, "other-binary", frame.token)
		}},
		{name: "empty digest from a VPS-era SENT row", mutate: func(t *testing.T, e *env, id string, frame sentUpdateFrame) {
			e.exec(`UPDATE update_step SET download_sha = '' WHERE node_id = ?`, id)
			assertLinkNotFound(t, e.s, e.ctx, id, "mistgate-node-linux-amd64", frame.token)
		}},
		{name: "non-manifest pinned data", mutate: func(t *testing.T, e *env, id string, frame sentUpdateFrame) {
			ro := e.rollout()
			e.exec(`UPDATE update_rollout SET manifest = ? WHERE id = ?`, []byte("not a manifest"), ro.ID)
			assertLinkNotFound(t, e.s, e.ctx, id, "mistgate-node-linux-amd64", frame.token)
		}},
		{name: "cancelled rollout", mutate: func(t *testing.T, e *env, id string, frame sentUpdateFrame) {
			ro := e.rollout()
			if _, err := e.st.SetRolloutStatus(e.ctx, ro.ID, []string{store.RolloutRunning}, store.RolloutCancelled, "", nil, e.clk.Now()); err != nil {
				t.Fatal(err)
			}
			assertLinkNotFound(t, e.s, e.ctx, id, "mistgate-node-linux-amd64", frame.token)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
			id := edgeNode(e, "a", 0)
			startEdgeRollout(e, 0, id)
			edgeTick(e)
			frame := updateFrameFor(t, e.fl, id)
			tc.mutate(t, e, id, frame)
		})
	}
	t.Run("cancelled context", func(t *testing.T) {
		e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
		id := edgeNode(e, "a", 0)
		startEdgeRollout(e, 0, id)
		edgeTick(e)
		frame := updateFrameFor(t, e.fl, id)
		ctx, cancel := context.WithCancel(e.ctx)
		cancel()
		if got, err := e.s.LinkDownload(ctx, id, "mistgate-node-linux-amd64", frame.token); err == nil || got != "" {
			t.Fatalf("LinkDownload with cancelled context = %q, %v; want no URL and an error", got, err)
		}
	})

	t.Run("pending step", func(t *testing.T) {
		e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
		id := edgeNode(e, "a", 0)
		startEdgeRollout(e, 0, id)
		assertLinkNotFound(t, e.s, e.ctx, id, "mistgate-node-linux-amd64", "any-token")
	})
}

func assertLinkNotFound(t *testing.T, s *Service, ctx context.Context, nodeID, name, token string) {
	t.Helper()
	if got, err := s.LinkDownload(ctx, nodeID, name, token); err == nil || connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("LinkDownload = %q, %v; want NotFound", got, err)
	}
}

func TestEdgeReleaseBundleValidationAndCache(t *testing.T) {
	_, otherPriv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		panel       string
		opts        fakeReleaseOptions
		unreachable bool
		wantStatus  adminv1.BundleStatus
		wantError   string
	}{
		{name: "bad signature", panel: edgeReleaseTag, opts: fakeReleaseOptions{signWith: otherPriv}, wantStatus: adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, wantError: "updates.bundle.err.bad_signature"},
		{name: "wrong version", panel: edgeReleaseTag, opts: fakeReleaseOptions{version: "v1.2.4"}, wantStatus: adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, wantError: "updates.bundle.err.bad_manifest"},
		{name: "expired", panel: edgeReleaseTag, wantStatus: adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, wantError: "updates.bundle.err.expired"},
		{name: "development build", panel: "0.2.0-dev", wantStatus: adminv1.BundleStatus_BUNDLE_STATUS_MISSING, wantError: "updates.bundle.err.no_release"},
		{name: "unreachable", panel: edgeReleaseTag, unreachable: true, wantStatus: adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, wantError: "updates.bundle.err.release_unreachable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			if tc.name == "expired" {
				tc.opts.expires = e.clk.Now().Add(-time.Second).Unix()
			}
			tag := tc.opts.tag
			if tag == "" {
				tag = edgeReleaseTag
			}
			tc.opts.tag = tag
			if tc.opts.built == 0 {
				tc.opts.built = newBuilt
			}
			if tc.opts.expires == 0 {
				tc.opts.expires = e.clk.Now().Add(30 * 24 * time.Hour).Unix()
			}
			f := newFakeRelease(t, []byte("signed node binary"), tc.opts)
			oldBase := releaseDownloadBase
			releaseDownloadBase = f.server.URL + "/"
			t.Cleanup(func() { releaseDownloadBase = oldBase })
			if tc.unreachable {
				f.server.Close()
			}
			e.s = e.newEdgeService(f.key, tc.panel)
			b := e.s.releaseBundle(e.ctx)
			if b.view.Status != tc.wantStatus || b.view.ErrorKey != tc.wantError {
				t.Fatalf("release bundle = %+v, want status=%s error=%s", b.view, tc.wantStatus, tc.wantError)
			}
			if tc.name == "development build" && f.requests.Load() != 0 {
				t.Fatalf("development build fetched release metadata %d times", f.requests.Load())
			}
		})
	}
	t.Run("one minute cache after a failed fetch", func(t *testing.T) {
		e, f := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
		f.server.Close()
		first := e.s.releaseBundle(e.ctx)
		if first.view.ErrorKey != "updates.bundle.err.release_unreachable" {
			t.Fatalf("failed fetch bundle = %+v", first.view)
		}
		if second := e.s.releaseBundle(e.ctx); second != first {
			t.Fatal("failed release fetch was not cached")
		}
		e.clk.Advance(releaseBundleErrorTTL)
		if third := e.s.releaseBundle(e.ctx); third == first {
			t.Fatal("failed release fetch remained cached past one minute")
		}
	})

	t.Run("ten minute cache and download base", func(t *testing.T) {
		e, f := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
		first := e.s.releaseBundle(e.ctx)
		if first.view.Status != adminv1.BundleStatus_BUNDLE_STATUS_TRUSTED || first.view.DownloadBase != releaseVersionBase(edgeReleaseTag) {
			t.Fatalf("edge bundle = %+v", first.view)
		}
		if second := e.s.releaseBundle(e.ctx); second != first || f.requests.Load() != 2 {
			t.Fatalf("bundle cache missed: same=%v metadata requests=%d", second == first, f.requests.Load())
		}
		e.clk.Advance(10 * time.Minute)
		if third := e.s.releaseBundle(e.ctx); third == first || f.requests.Load() != 4 {
			t.Fatalf("bundle cache did not refresh at ten minutes: same=%v metadata requests=%d", third == first, f.requests.Load())
		}
	})
}

func TestEdgeGetUpdatesIncludesDownloadBase(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	resp, err := (rpc{s: e.s}).GetUpdates(e.ctx, connect.NewRequest(&adminv1.GetUpdatesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resp.Msg.Bundle.GetDownloadBase(), releaseVersionBase(edgeReleaseTag); got != want {
		t.Fatalf("download_base = %q, want %q", got, want)
	}
}

func TestEdgeReleaseBundleExpiryIsRecheckedOnCacheRead(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	b := e.s.releaseBundle(e.ctx)
	if !b.trusted {
		t.Fatalf("initial edge bundle is not trusted: %+v", b.view)
	}
	e.clk.Advance(31 * 24 * time.Hour)
	b = e.s.releaseBundle(e.ctx)
	if b.trusted || b.view.ErrorKey != "updates.bundle.err.expired" {
		t.Fatalf("expired cached bundle = %+v, want expired/untrusted", b.view)
	}
}

func edgeLiveByID(t *testing.T, e *env) map[string]store.NodeLiveRow {
	t.Helper()
	rows, err := e.fl.Live(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	live := make(map[string]store.NodeLiveRow, len(rows))
	for _, row := range rows {
		live[row.NodeID] = row
	}
	return live
}

// P1: a stale pass that loses the step CAS must not re-pause a rollout the owner resumed meanwhile.
func TestEdgeStalePassDoesNotRepauseAfterResume(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "a", 0)
	ro := startEdgeRollout(e, 0, id)
	edgeTick(e)
	frame := updateFrameFor(t, e.fl, id)
	recordEdgeAnswer(t, e, id, frame.requestID, &agentv1.CommandResult{Error: "download_failed"})
	step, live := e.step(id), edgeLiveByID(t, e)
	p1 := &pass{s: e.s, ctx: e.ctx, ro: ro, steps: []store.StepRow{step}, live: live, now: e.clk.Now()}
	p1.advanceSent(0)
	if r := e.rollout(); r.Status != store.RolloutPaused {
		t.Fatalf("first pass did not pause: %+v", r)
	}
	if _, err := e.st.SetRolloutStatus(e.ctx, ro.ID, []string{store.RolloutPaused}, store.RolloutRunning, "", nil, time.Time{}); err != nil {
		t.Fatal(err)
	}
	p2 := &pass{s: e.s, ctx: e.ctx, ro: ro, steps: []store.StepRow{step}, live: live, now: e.clk.Now()}
	p2.advanceSent(0)
	if r := e.rollout(); r.Status != store.RolloutRunning {
		t.Fatalf("stale pass re-paused a resumed rollout: %+v", r)
	}
}

// P2: a failed link send is SKIPPED as offline and leaves no request metadata behind.
func TestEdgeSendUpdateErrorSkipsOffline(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "a", 0)
	startEdgeRollout(e, 0, id)
	e.fl.mu.Lock()
	e.fl.sendUpdateErr = errors.New("not sent")
	e.fl.mu.Unlock()
	edgeTick(e)
	x := e.wantStep(id, store.StepSkipped, errOffline)
	if x.DownloadSHA != "" || x.RequestID != "" {
		t.Fatalf("skipped step keeps request metadata: %+v", x)
	}
	e.wantRollout(store.RolloutDone, "")
}

func edgeAdvanceToGating(t *testing.T, e *env, id string) {
	t.Helper()
	edgeTick(e)
	frame := updateFrameFor(t, e.fl, id)
	recordEdgeAnswer(t, e, id, frame.requestID, okResult())
	edgeTick(e)
	edgeUpgrade(e, id)
	edgeTick(e)
	e.wantStep(id, store.StepGating, "")
}

// P3: a failed rollback send ends as rollback_failed with the gate recorded and the rollout paused.
func TestEdgeSendRollbackErrorFails(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "a", 0)
	startEdgeRollout(e, 0, id)
	edgeAdvanceToGating(t, e, id)
	e.fl.mu.Lock()
	e.fl.sendRollbackErr = errors.New("not sent")
	e.fl.mu.Unlock()
	e.clk.Advance(e.s.cfg.GateWait)
	edgeTick(e)
	x := e.wantStep(id, store.StepFailed, errRollbackFailed)
	if x.Params["gate"] != errNotCommitted || x.RequestID != "" {
		t.Fatalf("step params or request metadata = %+v", x)
	}
	e.wantRollout(store.RolloutPaused, pauseGateFailed)
}

// P4: an acknowledged update gets the full reconnect interval from the recorded acknowledgement.
func TestEdgeAckedNoReconnectWaitsThreeMinutes(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "a", 0)
	startEdgeRollout(e, 0, id)
	edgeTick(e)
	frame := updateFrameFor(t, e.fl, id)
	recordEdgeAnswer(t, e, id, frame.requestID, okResult())
	e.drop(id)
	edgeTick(e)
	e.clk.Advance(e.s.cfg.ReconnectWait)
	edgeTick(e)
	e.wantStep(id, store.StepSent, "")
	e.clk.Advance(time.Second)
	edgeTick(e)
	e.wantStep(id, store.StepFailed, errNotReconnected)
	e.wantRollout(store.RolloutPaused, pauseGateFailed)
}

// P5: an unanswered rollback with the new build still live fails once, at its deadline.
func TestEdgeRollbackTimeoutStillNew(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "a", 0)
	startEdgeRollout(e, 0, id)
	edgeAdvanceToGating(t, e, id)
	e.clk.Advance(e.s.cfg.GateWait)
	edgeTick(e)
	if len(e.fl.rollbackRequestFrames()) != 1 {
		t.Fatal("no rollback frame at the gate deadline")
	}
	e.clk.Advance(edgeRollbackTimeout - time.Second)
	edgeTick(e)
	e.wantStep(id, store.StepGating, "")
	e.clk.Advance(time.Second)
	edgeTick(e)
	x := e.wantStep(id, store.StepFailed, errRollbackFailed)
	if x.Params["reason"] != errNoAnswer || x.Params["gate"] != errNotCommitted {
		t.Fatalf("rollback params = %+v", x.Params)
	}
	if n := len(e.fl.rollbackRequestFrames()); n != 1 {
		t.Fatalf("rollback frames = %d, want one", n)
	}
}

// P6: an answer recorded after a pass reads SENT defeats its stale no_answer write.
func TestEdgeAnswerRacesDeadline(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "a", 0)
	startEdgeRollout(e, 0, id)
	edgeTick(e)
	frame := updateFrameFor(t, e.fl, id)
	e.clk.Advance(e.s.cfg.AnswerWait + e.s.cfg.ReconnectWait + time.Second)
	e.fl.mu.Lock()
	e.fl.afterLive = func(*fakeFleet) {
		if _, err := e.st.SetStepAnswer(e.ctx, id, frame.requestID, `{"ok":true}`); err != nil {
			t.Error(err)
		}
	}
	e.fl.mu.Unlock()
	edgeTick(e)
	e.fl.mu.Lock()
	e.fl.afterLive = nil
	e.fl.mu.Unlock()
	e.wantStep(id, store.StepSent, "") // the stale no_answer lost its CAS
	if r := e.rollout(); r.Status != store.RolloutRunning {
		t.Fatalf("lost CAS still paused the rollout: %+v", r)
	}
	edgeTick(e)
	x := e.wantStep(id, store.StepSent, "")
	if x.AckedAt.IsZero() {
		t.Fatalf("answer not applied: %+v", x)
	}
}

// P7: rollback deadlines use the wall clock even when this cron invocation is late.
func TestEdgeRollbackDeadlineUsesWallClockAfterCronLag(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "a", 0)
	startEdgeRollout(e, 0, id)
	edgeAdvanceToGating(t, e, id)
	e.clk.Advance(e.s.cfg.GateWait + 70*time.Second)
	// The updates job runs 70 s after scheduledTime because earlier cron jobs took that long.
	e.s.EdgeTick(e.ctx, e.clk.Now().Add(-70*time.Second))
	frames := e.fl.rollbackRequestFrames()
	if len(frames) != 1 {
		t.Fatalf("rollback frames = %+v", frames)
	}
	if !frames[0].deadline.After(e.clk.Now()) {
		t.Fatalf("rollback deadline %v is already past at wall clock %v", frames[0].deadline, e.clk.Now())
	}
}

// P8: a due schedule can populate the release cache in a cold jobs isolate.
func TestEdgeScheduledUpdateStartsWithColdReleaseCache(t *testing.T) {
	e, _ := newEdgeFixture(t, edgeReleaseTag, fakeReleaseOptions{})
	id := edgeNode(e, "scheduled", 0)
	row := store.NodeUpdateScheduleRow{NodeID: id, ToVersion: edgeReleaseTag, ToBuilt: newBuilt,
		ScheduledAt: e.clk.Now().Unix() - 1, CreatedBy: "adm_test", CreatedAt: e.clk.Now()}
	if err := e.st.SetNodeUpdateSchedule(e.ctx, row); err != nil {
		t.Fatal(err)
	}
	edgeTick(e)
	if _, err := e.st.ActiveRollout(e.ctx); err != nil {
		t.Fatalf("cold jobs isolate did not start scheduled rollout: %v", err)
	}
}
