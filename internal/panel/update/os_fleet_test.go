package update

import (
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/osupdate"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func addOsFleetNode(e *env, name string, online, inbounds int) string {
	e.t.Helper()
	id := e.addNode(name, nodeOpts{caps: osCaps, online: online, inbounds: inbounds})
	e.osBoot(id, e.clk.Now().Add(-48*time.Hour), kernelOld)
	e.osStatus(id, nil)
	if inbounds > 0 {
		e.hl.set(id, inbounds, inbounds, 0)
	}
	return id
}

func completeOsStep(e *env, id string) {
	e.t.Helper()
	x := e.osStep(id)
	e.osJob(id, &agentv1.OsJob{JobId: x.JobID, Action: "upgrade", Mode: osupdate.ModeSecurity,
		State: osupdate.StateDone, Upgraded: 1, Reboot: osupdate.RebootNotAsked})
	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "1", "reboot": "not_asked"})
	e.osTick()
	for range 3 {
		if e.osStep(id).State != store.OsStepGating {
			return
		}
		e.osTick()
	}
	if got := e.osStep(id); got.State != store.OsStepPassed {
		e.t.Fatalf("completed OS job did not pass its gate: %+v", got)
	}
}

func TestOsFleetCanaryOrderAndDefaultSingleNodeBatches(t *testing.T) {
	e := newEnv(t)
	de := addOsFleetNode(e, "de1", 1, 2)
	nl := addOsFleetNode(e, "nl1", 0, 3)
	fi := addOsFleetNode(e, "fi1", 0, 1)
	msk := addOsFleetNode(e, "msk1", 0, 1)
	ro, err := e.s.startOsWithBatch(e.ctx, []string{de, nl, fi, msk}, store.OsModeSecurity, store.OsRebootNever, 0)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := e.st.OsRolloutSteps(e.ctx, ro.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{fi, msk, nl, de}
	for i, step := range steps {
		if step.NodeID != want[i] || step.Stage != i {
			t.Fatalf("step %d = node %s stage %d, want %s stage %d", i, step.NodeID, step.Stage, want[i], i)
		}
	}
	if ro.BatchSize != 1 {
		t.Fatalf("default batch size %d, want 1", ro.BatchSize)
	}
	e.osTick()
	if jobs := e.fl.osJobs(); len(jobs) != 1 || jobs[0].nodeID != fi {
		t.Fatalf("first batch = %v, want only canary %s", jobs, fi)
	}
	completeOsStep(e, fi)
	if jobs := e.fl.osJobs(); len(jobs) != 2 || jobs[1].nodeID != msk {
		t.Fatalf("second batch = %v, want %s after canary gate", jobs, msk)
	}
}

func TestOsFleetBatchOfTwoWaitsForTheWholePreviousBatch(t *testing.T) {
	e := newEnv(t)
	ids := []string{addOsFleetNode(e, "de1", 0, 0), addOsFleetNode(e, "nl1", 0, 0),
		addOsFleetNode(e, "fi1", 0, 0), addOsFleetNode(e, "msk1", 0, 0)}
	ro, err := e.s.startOsWithBatch(e.ctx, ids, store.OsModeSecurity, store.OsRebootNever, 2)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := e.st.OsRolloutSteps(e.ctx, ro.ID)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[int]int{}
	for _, step := range steps {
		counts[step.Stage]++
	}
	if counts[0] != 1 || counts[1] != 2 || counts[2] != 1 {
		t.Fatalf("stage sizes %v, want canary then batches of two", counts)
	}
	e.osTick()
	if jobs := e.fl.osJobs(); len(jobs) != 1 {
		t.Fatalf("initial jobs = %v, want only canary", jobs)
	}
	canary := e.fl.osJobs()[0].nodeID
	completeOsStep(e, canary)
	if jobs := e.fl.osJobs(); len(jobs) != 3 {
		t.Fatalf("jobs after canary gate = %v, want canary plus two", jobs)
	}
	var stageTwo []string
	for _, id := range ids {
		if id != canary && e.osStep(id).Stage == 1 {
			stageTwo = append(stageTwo, id)
		}
	}
	completeOsStep(e, stageTwo[0])
	if jobs := e.fl.osJobs(); len(jobs) != 3 {
		t.Fatalf("later stage started before both batch steps passed: %v", jobs)
	}
	completeOsStep(e, stageTwo[1])
	if jobs := e.fl.osJobs(); len(jobs) != 4 {
		t.Fatalf("last stage did not start after batch gate: %v", jobs)
	}
}

func TestOsFleetTooSoonWaitsUntilRebootPossibleThenRetries(t *testing.T) {
	e := newEnv(t)
	id := addOsFleetNode(e, "de1", 0, 0)
	calls := 0
	e.fl.onOs = func(_ string, req OsJobRequest) (*agentv1.CommandResult, error) {
		calls++
		if calls == 1 {
			boot := e.clk.Now().Add(-3 * time.Minute)
			e.osBoot(id, boot, kernelOld)
			return &agentv1.CommandResult{Ok: true, Params: map[string]string{"state": osupdate.AnswerUnsupported, "code": osupdate.CodeTooSoon}}, nil
		}
		return &agentv1.CommandResult{Ok: true, Params: map[string]string{"state": osupdate.AnswerStarted, "job_id": req.JobID}}, nil
	}
	e.startOs(id, store.OsModeRebootOnly, store.OsRebootAlways)
	e.osTick()
	waiting := e.wantOsStep(id, store.OsStepPending, "")
	wantAt := e.clk.Now().Add(7 * time.Minute).Unix()
	if waiting.Phase != osWaitPhase || waiting.Params[osRetryAfter] != strconv.FormatInt(wantAt, 10) {
		t.Fatalf("retry wait = phase %q after %q, want %q at %d", waiting.Phase, waiting.Params[osRetryAfter], osWaitPhase, wantAt)
	}
	e.osTick()
	if len(e.fl.osJobs()) != 1 {
		t.Fatal("the reboot retry ran before the reported possible time")
	}
	e.clk.Advance(7 * time.Minute)
	e.osTick()
	if jobs := e.fl.osJobs(); len(jobs) != 2 || jobs[1].nodeID != id {
		t.Fatalf("retry jobs = %v, want a second job for %s", jobs, id)
	}
	e.wantOsStep(id, store.OsStepSent, "")
}

func TestOsFleetRetryCeilingSkipsAndContinues(t *testing.T) {
	e := newEnv(t)
	first := addOsFleetNode(e, "de1", 0, 0)
	second := addOsFleetNode(e, "nl1", 0, 0)
	e.osJob(first, running("manual-1700000000", "installing"))
	e.startOsWithBatchOrFail([]string{first, second}, 1)
	e.osTick()
	waiting := e.wantOsStep(first, store.OsStepPending, "")
	if waiting.Phase != osWaitPhase {
		t.Fatalf("busy step phase %q, want %q", waiting.Phase, osWaitPhase)
	}
	e.clk.Advance(osRetryLimit)
	e.osTick()
	e.wantOsStep(first, store.OsStepSkipped, osErrTryLater)
	e.osTick()
	e.wantOsStep(second, store.OsStepSent, "")
}

func TestOsFleetHardRebootBlockerSkipsAndContinues(t *testing.T) {
	e := newEnv(t)
	first := addOsFleetNode(e, "de1", 0, 0)
	second := addOsFleetNode(e, "nl1", 0, 0)
	e.osStatus(first, func(s *agentv1.OsStatus) { s.RebootBlockers = []string{osupdate.CodeKernelOlder} })
	e.startOsWithBatchOrFail([]string{first, second}, 1)
	e.osTick()
	e.wantOsStep(first, store.OsStepSkipped, osupdate.CodeKernelOlder)
	e.wantOsStep(second, store.OsStepSent, "")
}

func (e *env) startOsWithBatchOrFail(ids []string, batch int) store.OsRolloutRow {
	e.t.Helper()
	ro, err := e.s.startOsWithBatch(e.ctx, ids, store.OsModeSecurity, store.OsRebootIfRequired, batch)
	if err != nil {
		e.t.Fatalf("start OS rollout: %v", err)
	}
	return ro
}

func TestOsFleetFailurePausesResumeRetriesOnceAndSkipFailedContinues(t *testing.T) {
	e := newEnv(t)
	first := addOsFleetNode(e, "de1", 0, 0)
	second := addOsFleetNode(e, "nl1", 0, 0)
	calls := 0
	e.fl.onOs = func(_ string, req OsJobRequest) (*agentv1.CommandResult, error) {
		calls++
		if calls == 1 {
			return &agentv1.CommandResult{Ok: true, Params: map[string]string{"state": osupdate.AnswerUnsupported, "code": osupdate.CodeDiskFull}}, nil
		}
		return &agentv1.CommandResult{Ok: true, Params: map[string]string{"state": osupdate.AnswerStarted, "job_id": req.JobID}}, nil
	}
	ro := e.startOsWithBatchOrFail([]string{first, second}, 1)
	e.osTick()
	failed := e.wantOsStep(first, store.OsStepFailed, osErrAgentRefused)
	e.wantOsRollout(store.RolloutPaused, pauseStepFailed)
	if _, err := e.s.resumeOs(e.ctx, ro.ID, false); err != nil {
		t.Fatalf("resume retry: %v", err)
	}
	resp, err := (rpc{s: e.s}).GetOsUpdates(e.ctx, connect.NewRequest(&adminv1.GetOsUpdatesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Msg.GetRollout().GetSteps()[0]; got.GetRetryCount() != 1 || got.GetParams()[osRetryCount] != "1" {
		t.Fatalf("typed retry count = %d, params = %v; want 1 in both", got.GetRetryCount(), got.GetParams())
	}
	e.osTick()
	retried := e.wantOsStep(first, store.OsStepSent, "")
	if retried.Params[osRetryCount] != "1" || retried.JobID == failed.JobID {
		t.Fatalf("retried job = %+v, want a new id with retry marker 1", retried)
	}
	e.osJob(first, &agentv1.OsJob{JobId: retried.JobID, Action: "upgrade", Mode: osupdate.ModeSecurity,
		State: osupdate.StateFailed, Code: osupdate.CodeInstallFailed, Upgraded: 1})
	e.osEvent(first, "os_update_failed", map[string]string{"job_id": retried.JobID, "code": osupdate.CodeInstallFailed, "upgraded": "1"})
	e.osTick()
	e.wantOsStep(first, store.OsStepFailed, osErrJobFailed)
	e.wantOsRollout(store.RolloutPaused, pauseStepFailed)
	if _, err := e.s.resumeOs(e.ctx, ro.ID, true); err != nil {
		t.Fatalf("resume skipping failed step: %v", err)
	}
	e.osTick()
	e.wantOsStep(first, store.OsStepSkipped, osErrSkippedAfterFailure)
	e.wantOsStep(second, store.OsStepSent, "")
}

func TestOsFleetCancelPausedRunClosesQueue(t *testing.T) {
	e := newEnv(t)
	first := addOsFleetNode(e, "de1", 0, 0)
	second := addOsFleetNode(e, "nl1", 0, 0)
	e.fl.onOs = func(string, OsJobRequest) (*agentv1.CommandResult, error) {
		return &agentv1.CommandResult{Ok: true, Params: map[string]string{"state": osupdate.AnswerUnsupported, "code": osupdate.CodeDiskFull}}, nil
	}
	ro := e.startOsWithBatchOrFail([]string{first, second}, 1)
	e.osTick()
	e.wantOsRollout(store.RolloutPaused, pauseStepFailed)
	if _, err := e.s.cancelOs(e.ctx, ro.ID); err != nil {
		t.Fatalf("cancel paused rollout: %v", err)
	}
	e.wantOsRollout(store.RolloutCancelled, "")
	e.wantOsStep(second, store.OsStepSkipped, errCancelled)
	if got := e.s.Conditions(e.ctx); len(got) != 0 {
		t.Fatalf("cancelled rollout left an alert: %+v", got)
	}
}

func TestOsFleetContinuesAfterPanelRestart(t *testing.T) {
	e := newEnv(t)
	first := addOsFleetNode(e, "de1", 0, 0)
	second := addOsFleetNode(e, "nl1", 0, 0)
	ro := e.startOsWithBatchOrFail([]string{first, second}, 1)
	e.osTick()
	if len(e.fl.osJobs()) != 1 {
		t.Fatal("canary was not sent before restart")
	}
	e.restart()
	e.osTick()
	if len(e.fl.osJobs()) != 1 {
		t.Fatalf("restart duplicated the in-flight job: %v", e.fl.osJobs())
	}
	x := e.osStep(first)
	e.osJob(first, &agentv1.OsJob{JobId: x.JobID, Action: "upgrade", Mode: osupdate.ModeSecurity,
		State: osupdate.StateDone, Upgraded: 1, Reboot: osupdate.RebootNotAsked})
	e.osEvent(first, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "1", "reboot": "not_asked"})
	e.osTick()
	e.osTick()
	e.wantOsStep(first, store.OsStepPassed, "")
	e.wantOsStep(second, store.OsStepSent, "")
	if got, err := e.st.OsRollout(e.ctx, ro.ID); err != nil || got.Status != store.RolloutRunning {
		t.Fatalf("stored rollout after restart = %+v, %v", got, err)
	}
}

func TestOsFleetModeAllUsesOrdinaryPackageMode(t *testing.T) {
	e := newEnv(t)
	id := addOsFleetNode(e, "de1", 0, 0)
	if _, err := e.s.startOsWithBatch(e.ctx, []string{id}, store.OsModeAll, store.OsRebootNever, 1); err != nil {
		t.Fatal(err)
	}
	e.osTick()
	jobs := e.fl.osJobs()
	if len(jobs) != 1 || jobs[0].req.Mode != osupdate.ModeAll || jobs[0].req.Reboot != store.OsRebootNever {
		t.Fatalf("mode all request = %+v", jobs)
	}
}

func TestOsFleetAPIReportsBatchStageAndRetryTime(t *testing.T) {
	e := newEnv(t)
	first := addOsFleetNode(e, "de1", 0, 0)
	second := addOsFleetNode(e, "nl1", 0, 0)
	e.osStatus(first, func(s *agentv1.OsStatus) { s.Apt = "locked" })
	ro := e.startOsWithBatchOrFail([]string{first, second}, 2)
	e.osTick()
	resp, err := (rpc{e.s}).GetOsUpdates(e.ctx, connect.NewRequest(&adminv1.GetOsUpdatesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	got := resp.Msg.GetRollout()
	if got == nil || got.GetBatchSize() != 2 || len(got.GetSteps()) != 2 {
		t.Fatalf("API rollout = %+v", got)
	}
	byID := map[string]*adminv1.OsStep{}
	for _, step := range got.GetSteps() {
		byID[step.GetNodeId()] = step
	}
	if byID[first].GetStage() != 0 || byID[second].GetStage() != 1 || byID[first].GetPhase() != osWaitPhase || byID[first].GetRetryAfterUnix() <= e.clk.Now().Unix() {
		t.Fatalf("API steps = %+v", got.GetSteps())
	}
	if got.GetId() != ro.ID {
		t.Fatalf("API rollout id %q, want %q", got.GetId(), ro.ID)
	}
}

// a node that answers "busy" to every send must not restart the two-hour wait on each resend
func TestOsFleetRepeatedBusyAnswersDoNotExtendTheRetryCeiling(t *testing.T) {
	e := newEnv(t)
	id := addOsFleetNode(e, "de1", 0, 0)
	e.fl.onOs = func(_ string, req OsJobRequest) (*agentv1.CommandResult, error) {
		return &agentv1.CommandResult{Ok: true, Params: map[string]string{"state": osupdate.AnswerRunning, "job_id": "manual-1700000000"}}, nil
	}
	e.startOsWithBatchOrFail([]string{id}, 1)
	for waited := time.Duration(0); waited <= osRetryLimit+time.Minute; waited += osRetryInterval {
		e.osTick()
		e.clk.Advance(osRetryInterval)
	}
	e.osTick()
	e.wantOsStep(id, store.OsStepSkipped, osErrTryLater)
	e.wantOsRollout(store.RolloutDone, "")
}

// the node's "busy" answer arriving after the owner's cancel must not put the step back in the queue
func TestOsFleetBusyAnswerAfterCancelDoesNotRequeue(t *testing.T) {
	e := newEnv(t)
	first := addOsFleetNode(e, "de1", 0, 0)
	second := addOsFleetNode(e, "nl1", 0, 0)
	e.startOsWithBatchOrFail([]string{first, second}, 1)
	e.osTick()
	e.wantOsStep(first, store.OsStepSent, "")
	if _, err := e.s.cancelOs(e.ctx, ""); err != nil {
		t.Fatal(err)
	}
	e.wantOsStep(second, store.OsStepSkipped, errCancelled)
	e.s.mu.Lock()
	p, x := e.s.stepPassOs(e.ctx, e.osStep(first).RolloutID, first)
	p.waitTryLater(x, osupdate.CodeBusy, e.clk.Now())
	e.s.mu.Unlock()
	e.wantOsStep(first, store.OsStepSkipped, errCancelled)
	e.osTick()
	e.wantOsRollout(store.RolloutCancelled, "")
}
