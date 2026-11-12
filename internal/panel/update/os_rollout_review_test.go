package update

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/osupdate"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Cases found by a review of the OS rollout: each pins one way it could reboot the wrong thing, report the wrong thing or hold up
// the wrong thing.

// osDown plays a node that dropped off: its stream is gone and the row says when.
func (e *env) osDown(id string) {
	e.t.Helper()
	e.drop(id)
	e.exec(`UPDATE node SET last_disconnected_at = ? WHERE id = ?`, e.clk.Now().Unix(), id)
}

func (e *env) lostAnswer() func(string, OsJobRequest) (*agentv1.CommandResult, error) {
	return func(string, OsJobRequest) (*agentv1.CommandResult, error) {
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("no answer"))
	}
}

// a resend that finds the node offline says nothing about whether the first send started the job
func TestOsResendThatFindsTheNodeOfflineKeepsTheStep(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	calls := 0
	e.fl.onOs = func(_ string, req OsJobRequest) (*agentv1.CommandResult, error) {
		calls++
		if calls == 1 {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("the node link dropped"))
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node is offline"))
	}
	e.startOs(id, store.OsModeRebootOnly, store.OsRebootAlways)
	e.osTick()
	e.clk.Advance(91 * time.Second)
	e.osTick()
	x := e.wantOsStep(id, store.OsStepSent, "")
	if x.Sends != 2 {
		t.Fatalf("sends = %d, want 2", x.Sends)
	}
	e.wantOsRollout(store.RolloutRunning, "")
	if !e.s.Updating(id) {
		t.Fatal("the node is not UPDATING although a job may be running on it")
	}
	// the job did start: its events lead the step to the end
	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "reboot": "not_asked"})
	e.osTick()
	e.wantOsStep(id, store.OsStepGating, "")
}

// a refusal after a resend is not conclusive either: the first send may have started the job
func TestOsRefusalAfterAResendDoesNotEndTheStep(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	calls := 0
	e.fl.onOs = func(_ string, req OsJobRequest) (*agentv1.CommandResult, error) {
		calls++
		if calls == 1 {
			return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("no answer"))
		}
		return &agentv1.CommandResult{Ok: true, Params: map[string]string{"state": osupdate.AnswerRunning, "job_id": "manual-1700000000"}}, nil
	}
	e.startOs(id, store.OsModeSecurity, store.OsRebootNever)
	e.osTick()
	e.clk.Advance(91 * time.Second)
	e.osTick()
	e.wantOsStep(id, store.OsStepSent, "")
	e.wantOsRollout(store.RolloutRunning, "")
}

func TestOsResendsAtMostThreeTimes(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.fl.onOs = e.lostAnswer()
	e.startOs(id, store.OsModeSecurity, store.OsRebootNever)
	e.osTick()
	for range 12 {
		e.clk.Advance(30 * time.Second)
		e.osTick()
	}
	if jobs := e.fl.osJobs(); len(jobs) != maxOsResends {
		t.Fatalf("sent %d times, want %d: the first and %d resends", len(jobs), maxOsResends, maxOsResends-1)
	}
	if x := e.osStep(id); x.Sends != maxOsResends {
		t.Fatalf("sends = %d", x.Sends)
	}
}

// an answer for the job of an earlier attempt does not touch the step of the new one
func TestOsLateAnswerOfAnotherJobIsIgnored(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	ro := e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.osTick()
	x := e.osStep(id)
	e.s.mu.Lock()
	e.s.onOsAnswer(ro.ID, id, "osj_other", &agentv1.CommandResult{Ok: true, Params: map[string]string{"state": osupdate.AnswerUnsupported, "code": osupdate.CodeBusy}}, nil)
	e.s.mu.Unlock()
	if got := e.osStep(id); got.State != store.OsStepSent || got.JobID != x.JobID {
		t.Fatalf("a late answer of another job id moved the step: %+v", got)
	}
}

// "finished" only says the job is over: how it ended comes from the node's events, with what it decided about the reboot
func TestOsFinishedAnswerOnlyAcknowledges(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	calls := 0
	e.fl.onOs = func(_ string, req OsJobRequest) (*agentv1.CommandResult, error) {
		calls++
		if calls == 1 {
			return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("no answer"))
		}
		return &agentv1.CommandResult{Ok: true, Params: map[string]string{"state": osupdate.AnswerFinished, "job_id": req.JobID, "result": "failed", "code": osupdate.CodeRebootLimit}}, nil
	}
	e.startOs(id, store.OsModeRebootOnly, store.OsRebootAlways)
	e.osTick()
	e.clk.Advance(91 * time.Second)
	e.osTick()
	x := e.wantOsStep(id, store.OsStepSent, "")
	if x.AckedAt.IsZero() {
		t.Fatal("the answer was not taken as an acknowledgement")
	}
	e.wantOsRollout(store.RolloutRunning, "")
	// the end the node reported by event: a reboot it held
	e.osEvent(id, "os_update_failed", map[string]string{"job_id": x.JobID, "code": osupdate.CodeRebootLimit, "reboot": "held"})
	e.osTick()
	got := e.wantOsStep(id, store.OsStepFailed, osErrRebootHeld)
	if got.Params["code"] != osupdate.CodeRebootLimit || got.Params["reboot"] != "held" {
		t.Fatalf("params %+v", got.Params)
	}
	e.wantOsRollout(store.RolloutPaused, pauseOsRebootHeld)
}

// when the node did reboot, the kernel is checked even though the panel never saw the reboot begin
func TestOsRebootOnlyDoneByEventStillChecksTheKernel(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeRebootOnly, store.OsRebootAlways)
	e.osTick()
	x := e.osStep(id)
	e.osEvent(id, "os_reboot_started", map[string]string{"job_id": x.JobID, "kernel_from": kernelOld, "kernel_to": kernelNew})
	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "reboot": "done", "kernel": kernelOld})
	e.osTick()
	got := e.osStep(id)
	if got.State != store.OsStepGating || got.RebootAt.IsZero() {
		t.Fatalf("step %+v: a done reboot has a reboot time and is checked", got)
	}
	e.osTick()
	e.wantOsStep(id, store.OsStepFailed, osErrKernel) // the node is still on the old kernel
}

// the gate does not pass a node whose job end is not known: a late failure must not follow a "Done"
func TestOsGatePassesOnlyWithTheJobsEnd(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeRebootOnly, store.OsRebootAlways)
	e.osTick()
	x := e.wantOsStep(id, store.OsStepSent, "")
	e.osDown(id)
	e.clk.Advance(3 * time.Minute)
	e.osBoot(id, e.clk.Now().Add(-time.Minute), kernelOld)
	e.osTick()
	e.wantOsStep(id, store.OsStepGating, "")
	e.osTick()
	e.osTick()
	e.wantOsStep(id, store.OsStepGating, "") // nothing is known about how the job ended
	e.osEvent(id, "os_update_failed", map[string]string{"job_id": x.JobID, "code": osupdate.CodeAwgNotLoaded, "reboot": "done"})
	e.osTick()
	got := e.wantOsStep(id, store.OsStepFailed, osErrJobFailed)
	if got.Params["code"] != osupdate.CodeAwgNotLoaded {
		t.Fatalf("params %+v", got.Params)
	}
	e.wantOsRollout(store.RolloutPaused, pauseGateFailed)
}

func TestOsGateWithoutTheJobsEndFailsAtItsDeadline(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeRebootOnly, store.OsRebootAlways)
	e.osTick()
	e.osDown(id)
	e.clk.Advance(3 * time.Minute)
	e.osBoot(id, e.clk.Now().Add(-time.Minute), kernelOld)
	e.osTick()
	e.clk.Advance(9 * time.Minute)
	e.osTick()
	e.wantOsStep(id, store.OsStepFailed, osErrJobNotSettled)
}

// the first end counts: a later, different end of the same job does not turn a pass into a failure
func TestOsFirstEndOfAJobCounts(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeSecurity, store.OsRebootNever)
	e.osTick()
	x := e.osStep(id)
	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "2", "reboot": "not_asked"})
	e.osEvent(id, "os_update_failed", map[string]string{"job_id": x.JobID, "code": osupdate.CodeInstallFailed})
	e.osTick()
	e.osTick()
	e.wantOsStep(id, store.OsStepPassed, "")
}

// a refresh asked a moment ago is a job on the node: an install is not started into "busy"
func TestOsStartRightAfterARefreshIsBlockedAsBusy(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.osStatus(id, func(s *agentv1.OsStatus) { s.ListsUnix = e.clk.Now().Unix() - 3*3600 })
	var refreshJob string
	e.fl.onOs = func(_ string, req OsJobRequest) (*agentv1.CommandResult, error) {
		refreshJob = req.JobID
		return &agentv1.CommandResult{Ok: true, Params: map[string]string{"state": osupdate.AnswerStarted, "job_id": req.JobID}}, nil
	}
	if res, err := e.s.refreshOs(e.ctx, []string{id}); err != nil || res[0].Outcome != "started" {
		t.Fatalf("refresh = %v, %v", res, err)
	}
	e.wantBlocked(id, store.OsModeSecurity, store.OsRebootIfRequired, blockNodeBusy)
	// the node shows the job running: still busy; shows it done: free
	e.clk.Advance(3 * time.Minute)
	e.osJob(id, &agentv1.OsJob{JobId: refreshJob, Action: "refresh", State: "running", Phase: "refreshing"})
	e.wantBlocked(id, store.OsModeSecurity, store.OsRebootIfRequired, blockNodeBusy)
	e.osJob(id, &agentv1.OsJob{JobId: refreshJob, Action: "refresh", State: "done"})
	e.osStatus(id, func(s *agentv1.OsStatus) { s.Job = &agentv1.OsJob{JobId: refreshJob, Action: "refresh", State: "done"} })
	if _, err := e.s.startOs(e.ctx, []string{id}, store.OsModeSecurity, store.OsRebootIfRequired); err != nil {
		t.Fatalf("start after the refresh ended: %v", err)
	}
}

func TestOsLaunchSkipsANodeThatWasAskedToRefreshMeanwhile(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.s.osMu.Lock()
	e.s.osRefreshes[id] = osRefresh{at: e.clk.Now(), jobID: "osj_refreshing"}
	e.s.osMu.Unlock()
	e.osTick()
	e.wantOsStep(id, store.OsStepSkipped, osErrBusy)
	if len(e.fl.osJobs()) != 0 {
		t.Fatal("a job was sent into a node that was refreshing")
	}
}

func TestOsLaunchSkipsTheNodeOfThePanelAndAnUnneededReboot(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.s.bootHash = "0123456789abcdef" // the panel turned out to run on this server's boot
	e.osTick()
	e.wantOsStep(id, store.OsStepSkipped, osErrPanelHost)
	if len(e.fl.osJobs()) != 0 {
		t.Fatal("a job was sent to the panel's own server")
	}

	e.s.bootHash = ""
	e.startOs(id, store.OsModeRebootOnly, store.OsRebootIfRequired)
	e.osStatus(id, func(s *agentv1.OsStatus) { s.RebootRequired, s.RebootReasons = false, nil }) // the reboot was done meanwhile
	e.osTick()
	e.wantOsStep(id, store.OsStepSkipped, osErrNotRequired)
	if len(e.fl.osJobs()) != 0 {
		t.Fatal("a reboot that was no longer needed was sent")
	}
}

func TestOsStartSeesARollbackOnTheNode(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.s.mu.Lock()
	e.s.inflight[id] = true // a RollbackAgent is on its way
	e.s.mu.Unlock()
	e.wantBlocked(id, store.OsModeSecurity, store.OsRebootIfRequired, blockNodeBusy)
}

// a job on the node and an agent rollback exclude each other through separate marks
func TestOsCommandInFlightBlocksARollback(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.s.mu.Lock()
	e.s.osInflight[id] = true
	e.s.mu.Unlock()
	if _, err := e.s.rollbackNode(e.ctx, id); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "node_updating") {
		t.Fatalf("rollback while an OS command waits for its answer = %v", err)
	}
	e.s.mu.Lock()
	if e.s.inflight[id] {
		t.Fatal("the OS command shares the agent command's mark")
	}
	e.s.mu.Unlock()
}

// the node went away after the job was sent: that is an alert before the 90 minutes are out, and the owner can close it
func TestOsSentNodeThatDoesNotComeBack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		acked   bool
		wantKey string
		pause   string
		sev     int
	}{
		{"acknowledged", true, osErrNotBack, pauseOsNotBack, 3},
		{"never answered", false, errNoAnswer, pauseStepFailed, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			id := e.osNode("de1")
			if !tc.acked {
				e.fl.onOs = e.lostAnswer()
			}
			e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
			e.osTick()
			e.osDown(id)
			e.clk.Advance(10 * time.Minute)
			e.osTick()
			e.wantOsStep(id, store.OsStepSent, "")
			e.clk.Advance(6 * time.Minute)
			e.osTick()
			e.wantOsStep(id, store.OsStepFailed, tc.wantKey)
			e.wantOsRollout(store.RolloutPaused, tc.pause)
			if c := e.s.Conditions(e.ctx); len(c) != 1 || c[0].Severity != tc.sev {
				t.Fatalf("conditions %+v", c)
			}
			if _, err := e.s.cancelOs(e.ctx, ""); err != nil {
				t.Fatal(err)
			}
			e.wantOsRollout(store.RolloutFailed, "")
		})
	}
}

func TestOsGateWithoutARebootSaysTheNodeWasLost(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeSecurity, store.OsRebootNever)
	e.osTick()
	x := e.osStep(id)
	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "2", "reboot": "not_asked"})
	e.osTick()
	e.wantOsStep(id, store.OsStepGating, "")
	e.osDown(id)
	e.clk.Advance(9 * time.Minute)
	e.osTick()
	e.wantOsStep(id, store.OsStepFailed, osErrNodeLost)
	ro := e.wantOsRollout(store.RolloutPaused, pauseGateFailed)
	if ro.PauseParams["reason"] != osErrNodeLost || ro.PauseParams["mode"] != store.OsModeSecurity {
		t.Fatalf("pause params %+v", ro.PauseParams)
	}
}

// the boot id of the snapshot tells a reboot, with the boot time of the Hello as the second word, and a jitter is not a boot
func TestOsReturnIsSeenByTheBootIDAndNotByAJitter(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.reachReboot(id)
	e.osBoot(id, e.clk.Now().Add(-48*time.Hour+30*time.Second), kernelNew) // the same boot, the time moved by half a minute
	e.osTick()
	e.wantOsStep(id, store.OsStepRebooting, "")
	e.osStatus(id, func(s *agentv1.OsStatus) {
		s.BootIdHash = "fedcba9876543210"
		s.Job = running(e.osStep(id).JobID, "rebooting")
	})
	e.osTick()
	e.wantOsStep(id, store.OsStepGating, "")
}

func TestOsGateIgnoresProfilesThatWereFailedBeforeTheJob(t *testing.T) {
	e := newEnv(t)
	id := e.addNode("de1", nodeOpts{caps: osCaps, inbounds: 1})
	e.osBoot(id, e.clk.Now().Add(-48*time.Hour), kernelOld)
	e.osStatus(id, nil)
	e.exec(`UPDATE inbound SET state = 'failed' WHERE node_id = ?`, id) // broken before anybody touched the node
	e.startOs(id, store.OsModeSecurity, store.OsRebootNever)
	e.osTick()
	x := e.osStep(id)
	if len(x.PreFailed) != 1 {
		t.Fatalf("pre-failed %v", x.PreFailed)
	}
	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "2", "reboot": "not_asked"})
	e.osTick()
	e.osTick()
	e.clk.Advance(time.Minute)
	e.osTick()
	e.wantOsStep(id, store.OsStepPassed, "")
}

func TestOsRefreshSkipsANodeWithAStepInFlight(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.osStatus(id, func(s *agentv1.OsStatus) { s.ListsUnix = e.clk.Now().Unix() - 3*3600 })
	e.startOs(id, store.OsModeSecurity, store.OsRebootNever)
	e.osTick()
	before := len(e.fl.osJobs())
	res, err := e.s.refreshOs(e.ctx, []string{id})
	if err != nil || res[0].Outcome != "running" || len(e.fl.osJobs()) != before {
		t.Fatalf("refresh = %v, %v; jobs %d -> %d", res, err, before, len(e.fl.osJobs()))
	}
}

// every change of the OS layer asks for the owner's fresh confirmation first
func TestOsRolloutRPCsNeedTheStepUp(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.stepUp = func(context.Context) error {
		return connect.NewError(connect.CodePermissionDenied, errors.New("step_up_required"))
	}
	if _, err := (rpc{e.s}).StartOsRollout(e.ctx, connect.NewRequest(&adminv1.StartOsRolloutRequest{NodeIds: []string{id},
		Mode: adminv1.OsRolloutMode_OS_ROLLOUT_MODE_SECURITY, Reboot: adminv1.OsRebootPolicy_OS_REBOOT_POLICY_IF_REQUIRED})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("StartOsRollout without the step-up = %v", err)
	}
	if _, err := e.st.LatestOsRollout(e.ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a rollout was created without the step-up: %v", err)
	}

	e.stepUp = func(context.Context) error { return nil }
	ro := e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.stepUp = func(context.Context) error {
		return connect.NewError(connect.CodePermissionDenied, errors.New("step_up_required"))
	}
	if _, err := (rpc{e.s}).CancelOsRollout(e.ctx, connect.NewRequest(&adminv1.CancelOsRolloutRequest{RolloutId: ro.ID})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("CancelOsRollout without the step-up = %v", err)
	}
	if got := e.osRollout(); got.Status != store.RolloutRunning {
		t.Fatalf("the rollout was cancelled without the step-up: %+v", got)
	}
}

// The other kinds of work wait for an OS rollout that is running and not for one that is only stopped.
type countingBundleSource struct{ syncs int }

func (c *countingBundleSource) Sync(context.Context, int64) (bool, error) {
	c.syncs++
	return false, nil
}
func (c *countingBundleSource) BundleStatus() (bool, string) { return false, "" }

func TestOnlyARunningOsRolloutHoldsUpTheBundleSyncAndTheSchedule(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	src := &countingBundleSource{}
	e.s.cfg.NodeBundleSource = src
	osn := e.osNode("de1")
	sel := e.addNode("selected", nodeOpts{})
	b := e.s.current()
	local := scheduleLocalAfter(e, 2*time.Minute, defaultScheduleTimezoneOffsetMin)
	if _, err := e.s.scheduleNodeUpdate(e.ctx, sel, local, defaultScheduleTimezoneOffsetMin, b.manifest.Version, b.manifest.Built); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(3 * time.Minute)

	e.startOs(osn, store.OsModeSecurity, store.OsRebootIfRequired)
	e.osTick() // sent: running
	e.s.syncNodeBundle(e.ctx)
	e.s.processScheduledNodeUpdates(e.ctx)
	if src.syncs != 0 {
		t.Fatalf("the bundle was synced %d times during an OS update", src.syncs)
	}
	if _, err := e.st.ActiveRollout(e.ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a scheduled update started during an OS update: %v", err)
	}
	if left, _ := e.st.NodeUpdateSchedules(e.ctx); len(left) != 1 {
		t.Fatalf("the schedule did not wait: %+v", left)
	}

	// the OS update stops (paused): nothing is on any node, the other work goes on
	x := e.osStep(osn)
	e.osEvent(osn, "os_update_failed", map[string]string{"job_id": x.JobID, "code": osupdate.CodeInstallFailed})
	e.osTick()
	e.wantOsRollout(store.RolloutPaused, pauseStepFailed)
	e.s.syncNodeBundle(e.ctx)
	if src.syncs != 1 {
		t.Fatalf("the bundle sync was held up by a stopped OS update (%d syncs)", src.syncs)
	}
	e.s.processScheduledNodeUpdates(e.ctx)
	if _, err := e.st.ActiveRollout(e.ctx); err != nil {
		t.Fatalf("the scheduled update did not start beside a stopped OS update: %v", err)
	}
}

func TestOsRolloutRunningHoldsUpTheAgentRolloutAndThePanelUpdate(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	osn := e.osNode("de1")
	other := e.addNode("nl1", nodeOpts{})
	e.startOs(osn, store.OsModeSecurity, store.OsRebootIfRequired)
	if _, err := e.s.start(e.ctx, []string{other}, 0); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("an agent rollout beside a running OS update = %v", err)
	}
	if err := e.s.beginPanelUpdate(e.ctx); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a panel update beside a running OS update = %v", err)
	}
}

// each node code the agent can send has its own entry in the vocabulary the panel keeps
func TestOsCodesOfTheNodeAreKnown(t *testing.T) {
	for _, c := range osupdate.Codes {
		if osCode(c) != c {
			t.Errorf("%s is not kept as it came", c)
		}
	}
	if osCode("rm -rf /") != "unknown" {
		t.Error("a word of the node's own was kept")
	}
}
