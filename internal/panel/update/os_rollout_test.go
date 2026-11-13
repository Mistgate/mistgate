package update

import (
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/osupdate"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The OS rollout: the node's side is played by the test (snapshots, events, boots), the panel's by the Service on a real store.

var osCaps = []string{"doctor/1", osupdate.Capability, osupdate.CapabilityUpdate}

const (
	kernelOld = "6.8.0-147-generic"
	kernelNew = "6.8.0-150-generic"
)

func (e *env) osStatus(id string, change func(*agentv1.OsStatus)) {
	e.t.Helper()
	s := &agentv1.OsStatus{
		MeasuredUnix: e.clk.Now().Unix(), DistroId: "ubuntu", DistroVersion: "24.04", Pending: 6, Security: 4, KernelRunning: kernelOld, KernelNewest: kernelNew,
		RebootRequired: true, RebootReasons: []string{"kernel"}, Apt: "ok", AutoUpdates: "on", ListsUnix: e.clk.Now().Unix() - 3600, BootIdHash: "0123456789abcdef",
	}
	if change != nil {
		change(s)
	}
	body, err := protojson.Marshal(s)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.PutOsStatus(e.ctx, id, string(body), e.clk.Now(), e.clk.Now()); err != nil {
		e.t.Fatal(err)
	}
}

// osNode adds an online node with the OS capabilities, a stored snapshot (security updates and a kernel waiting) and a boot time.
func (e *env) osNode(name string) string {
	e.t.Helper()
	id := e.addNode(name, nodeOpts{caps: osCaps})
	e.osBoot(id, e.clk.Now().Add(-48*time.Hour), kernelOld)
	e.osStatus(id, nil)
	return id
}

// osBoot plays a Hello of a host that booted at boot and runs kernel.
func (e *env) osBoot(id string, boot time.Time, kernel string) {
	e.t.Helper()
	if _, _, err := e.st.NodeHello(e.ctx, id, 1, store.HelloInfo{AgentVersion: "0.1.0-old", Instance: "i-" + id, Built: oldBuilt, Caps: osCaps, BootAt: boot,
		Facts: store.NodeFactsRow{Hostname: "host-" + id, Kernel: kernel}}, e.clk.Now()); err != nil {
		e.t.Fatal(err)
	}
	e.fl.mu.Lock()
	e.fl.live[id] = true
	e.fl.mu.Unlock()
}

// osJob plays a snapshot whose job is the given one.
func (e *env) osJob(id string, job *agentv1.OsJob) {
	e.t.Helper()
	e.osStatus(id, func(s *agentv1.OsStatus) { s.Job = job })
}

// osEvent plays an event of the agent about a job.
func (e *env) osEvent(id, code string, params map[string]string) {
	e.t.Helper()
	if params == nil {
		params = map[string]string{}
	}
	body, err := jsonOf(params)
	if err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO event (ts, severity, code, source, node_id, params_json) VALUES (?, 1, ?, 'agent', ?, ?)`, e.clk.Now().Unix(), code, id, body)
}

func jsonOf(m map[string]string) (string, error) { return jsonString(m), nil }

func (e *env) osTick() {
	e.t.Helper()
	e.s.tickOs(e.ctx)
	e.s.wg.Wait()
}

func (e *env) startOs(id, mode, reboot string) store.OsRolloutRow {
	e.t.Helper()
	ro, err := e.s.startOs(e.ctx, []string{id}, mode, reboot)
	if err != nil {
		e.t.Fatalf("start OS rollout: %v", err)
	}
	return ro
}

func (e *env) osRollout() store.OsRolloutRow {
	e.t.Helper()
	ro, err := e.st.LatestOsRollout(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return ro
}

func (e *env) osStep(id string) store.OsStepRow {
	e.t.Helper()
	steps, err := e.st.OsRolloutSteps(e.ctx, e.osRollout().ID)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, x := range steps {
		if x.NodeID == id {
			return x
		}
	}
	e.t.Fatalf("no OS step for %s", id)
	return store.OsStepRow{}
}

func (e *env) wantOsStep(id, state, errKey string) store.OsStepRow {
	e.t.Helper()
	x := e.osStep(id)
	if x.State != state || x.ErrorKey != errKey {
		e.t.Fatalf("OS step of %s: state %s error %q, want %s %q (%+v)", id, x.State, x.ErrorKey, state, errKey, x)
	}
	return x
}

func (e *env) wantOsRollout(status, pauseKey string) store.OsRolloutRow {
	e.t.Helper()
	ro := e.osRollout()
	if ro.Status != status || ro.PauseKey != pauseKey {
		e.t.Fatalf("OS rollout: status %s pause %q, want %s %q", ro.Status, ro.PauseKey, status, pauseKey)
	}
	return ro
}

func (e *env) wantBlocked(id, mode, reboot, reason string) {
	e.t.Helper()
	_, err := e.s.startOs(e.ctx, []string{id}, mode, reboot)
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeFailedPrecondition || ce.Message() != "os_blocked: reason="+reason {
		e.t.Fatalf("start OS rollout = %v, want os_blocked: reason=%s", err, reason)
	}
}

func running(jobID, phase string) *agentv1.OsJob {
	return &agentv1.OsJob{JobId: jobID, Action: "upgrade", Mode: "security", State: "running", Phase: phase}
}

// reachReboot plays a security job with a reboot up to the moment the host is down.
func (e *env) reachReboot(id string) store.OsStepRow {
	e.t.Helper()
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.osTick()
	x := e.wantOsStep(id, store.OsStepSent, "")
	e.osJob(id, running(x.JobID, "installing"))
	e.osTick()
	job := running(x.JobID, "rebooting")
	job.KernelTo = kernelNew
	e.osJob(id, job)
	e.osEvent(id, "os_reboot_started", map[string]string{"job_id": x.JobID, "kernel_from": kernelOld, "kernel_to": kernelNew})
	e.osTick()
	e.wantOsStep(id, store.OsStepRebooting, "")
	e.drop(id)
	return e.osStep(id)
}

func TestOsRolloutSecurityWithRebootPasses(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	if e.s.Updating(id) {
		t.Fatal("a pending step already makes the node UPDATING")
	}
	e.osTick()
	x := e.wantOsStep(id, store.OsStepSent, "")
	jobs := e.fl.osJobs()
	if len(jobs) != 1 || jobs[0].nodeID != id || jobs[0].req.JobID != x.JobID || !strings.HasPrefix(x.JobID, "osj_") || !osupdate.ValidJobID(x.JobID) {
		t.Fatalf("sent %+v for step %+v", jobs, x)
	}
	if want := (OsJobRequest{Action: "upgrade", Mode: "security", Reboot: "if_required", JobID: x.JobID}); jobs[0].req != want {
		t.Fatalf("request %+v, want %+v", jobs[0].req, want)
	}
	if x.AckedAt.IsZero() || x.Sends != 1 || x.KernelFrom != kernelOld || x.Params["security"] != "4" || x.Params["pending"] != "6" {
		t.Fatalf("step after the send: %+v", x)
	}
	if !e.s.Updating(id) {
		t.Fatal("a sent step does not make the node UPDATING: its reboot would look like an outage")
	}

	e.osJob(id, running(x.JobID, "installing"))
	e.osTick()
	if got := e.osStep(id); got.Phase != "installing" || got.State != store.OsStepSent {
		t.Fatalf("phase after a snapshot: %+v", got)
	}

	job := running(x.JobID, "rebooting")
	job.KernelTo = kernelNew
	e.osJob(id, job)
	e.osTick()
	got := e.wantOsStep(id, store.OsStepRebooting, "")
	if got.KernelTo != kernelNew || got.RebootAt.IsZero() {
		t.Fatalf("rebooting step: %+v", got)
	}

	e.drop(id)
	e.clk.Advance(2 * time.Minute)
	e.osTick()
	e.wantOsStep(id, store.OsStepRebooting, "")
	if !e.s.Updating(id) {
		t.Fatal("a node that is rebooting is not UPDATING")
	}

	e.clk.Advance(3 * time.Minute)
	e.osBoot(id, e.clk.Now().Add(-time.Minute), kernelNew)
	e.osTick()
	e.wantOsStep(id, store.OsStepGating, "")

	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "6", "reboot": "done", "kernel": kernelNew})
	e.osTick()
	done := e.wantOsStep(id, store.OsStepPassed, "")
	if done.Params["upgraded"] != "6" || done.Params["reboot"] != "done" || done.Params["kernel"] != kernelNew {
		t.Fatalf("what the step keeps of the job: %+v", done.Params)
	}
	e.wantOsRollout(store.RolloutDone, "")
	if e.s.Updating(id) {
		t.Fatal("a finished step still makes the node UPDATING")
	}
	if ev := e.wantEvent(id, "os_step_passed"); ev.Params["job_id"] != x.JobID {
		t.Fatalf("event %+v", ev)
	}
	if len(e.fl.osJobs()) != 1 {
		t.Fatalf("the job was sent %d times", len(e.fl.osJobs()))
	}
}

func TestOsRolloutWithoutRebootGoesThroughTheGate(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("nl1")
	e.hl.set(id, 1, 0, 0) // a profile the probes have not seen since the job began
	e.startOs(id, store.OsModeSecurity, store.OsRebootNever)
	e.osTick()
	x := e.osStep(id)
	e.osJob(id, &agentv1.OsJob{JobId: x.JobID, Action: "upgrade", Mode: "security", State: "done", Upgraded: 3, Reboot: "not_asked"})
	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "3", "reboot": "not_asked", "kernel": kernelOld})
	e.osTick()
	e.wantOsStep(id, store.OsStepGating, "")
	e.osTick()
	e.wantOsStep(id, store.OsStepGating, "") // the probes have not run since the job: not yet
	if got := e.hl.since[id]; !got.Equal(x.SentAt) {
		t.Fatalf("probes looked since %v, want the time the job was sent %v (there was no reboot to be back from)", got, x.SentAt)
	}
	e.hl.set(id, 1, 1, 0)
	e.osTick()
	e.wantOsStep(id, store.OsStepPassed, "")
	e.wantOsRollout(store.RolloutDone, "")
}

func TestOsRolloutStepKeepsNodeJobDetailsAfterStatusDisappears(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.hl.set(id, 1, 0, 0)
	e.startOs(id, store.OsModeSecurity, store.OsRebootNever)
	e.osTick()
	x := e.osStep(id)
	e.osJob(id, &agentv1.OsJob{JobId: x.JobID, Action: "upgrade", Mode: "security", State: "done", Upgraded: 2, Reboot: "not_asked",
		AptLockHolder: "apt-get", AptLockMinutes: 17, NoteCode: osupdate.NoteSecurityLeftoverFailed,
		Note: "Some security updates remain pending; check the node and retry."})
	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "2", "reboot": "not_asked"})
	e.osTick()
	e.wantOsStep(id, store.OsStepGating, "")

	// The job has disappeared from the node's live snapshot before the gate closes.
	e.osStatus(id, nil)
	e.hl.set(id, 1, 1, 0)
	e.osTick()
	got := e.wantOsStep(id, store.OsStepPassed, "")
	if got.Params["apt_lock_holder"] != "apt-get" || got.Params["apt_lock_minutes"] != "17" ||
		got.Params["note_code"] != osupdate.NoteSecurityLeftoverFailed || got.Params["note"] != "Some security updates remain pending; check the node and retry." {
		t.Fatalf("finished step lost node job details: %+v", got.Params)
	}
}

func TestOsRolloutRetryableRefusalKeepsNodeDetailsWhenItExpires(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("nl1")
	e.startOs(id, store.OsModeSecurity, store.OsRebootNever)
	e.osTick()
	x := e.osStep(id)
	e.osJob(id, &agentv1.OsJob{JobId: x.JobID, Action: "upgrade", Mode: "security", State: "failed", Code: osupdate.CodeAptLock,
		AptLockHolder: "apt-get", AptLockMinutes: 12, NoteCode: osupdate.NoteSecurityLeftoverFailed,
		Note: "Some security updates remain pending; check the node and retry."})
	e.osEvent(id, "os_update_failed", map[string]string{"job_id": x.JobID, "code": osupdate.CodeAptLock, "holder": "apt-get", "minutes": "12"})
	e.osTick()
	waiting := e.wantOsStep(id, store.OsStepPending, "")
	if waiting.Params["apt_lock_holder"] != "apt-get" || waiting.Params["apt_lock_minutes"] != "12" ||
		waiting.Params["note_code"] != osupdate.NoteSecurityLeftoverFailed || waiting.Params["note"] == "" {
		t.Fatalf("waiting step lost node refusal details: %+v", waiting.Params)
	}

	e.osStatus(id, nil)
	e.clk.Advance(osRetryLimit)
	e.osTick()
	finished := e.wantOsStep(id, store.OsStepSkipped, osErrTryLater)
	if finished.Params["apt_lock_holder"] != "apt-get" || finished.Params["apt_lock_minutes"] != "12" ||
		finished.Params["note_code"] != osupdate.NoteSecurityLeftoverFailed || finished.Params["note"] == "" {
		t.Fatalf("expired refusal step lost node details: %+v", finished.Params)
	}
}

func TestOsRolloutRebootOnly(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeRebootOnly, store.OsRebootAlways)
	e.osTick()
	if jobs := e.fl.osJobs(); len(jobs) != 1 || jobs[0].req.Action != "reboot" || jobs[0].req.Mode != "" || jobs[0].req.Reboot != "always" {
		t.Fatalf("sent %+v", jobs)
	}
	if _, err := e.s.startOs(e.ctx, []string{id}, store.OsModeRebootOnly, store.OsRebootNever); err == nil {
		t.Fatal("a reboot-only rollout that never reboots was accepted")
	}
}

func TestOsStartRefusals(t *testing.T) {
	e := newEnv(t)
	sec, ifReq := store.OsModeSecurity, store.OsRebootIfRequired
	id := e.osNode("de1")

	for _, c := range []struct{ mode, reboot string }{{sec, store.OsRebootAlways}, {store.OsModeRebootOnly, store.OsRebootNever},
		{store.OsModeAll, store.OsRebootAlways}, {"", ifReq}, {sec, ""}} {
		if _, err := e.s.startOs(e.ctx, []string{id}, c.mode, c.reboot); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("%q/%q = %v, want INVALID_ARGUMENT", c.mode, c.reboot, err)
		}
	}
	for _, ids := range [][]string{nil, {""}, {id, id}} {
		if _, err := e.s.startOs(e.ctx, ids, sec, ifReq); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("nodes %v = %v, want INVALID_ARGUMENT", ids, err)
		}
	}
	if _, err := e.s.startOs(e.ctx, []string{"nod_nobody"}, sec, ifReq); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("an unknown node = %v", err)
	}

	old := e.addNode("old1", nodeOpts{caps: []string{"doctor/1", osupdate.Capability}})
	e.osStatus(old, nil)
	e.wantBlocked(old, sec, ifReq, blockAgentOld)

	off := e.osNode("off1")
	e.drop(off)
	e.wantBlocked(off, sec, ifReq, blockOffline)

	rpm := e.osNode("rpm1")
	e.osStatus(rpm, func(s *agentv1.OsStatus) { s.DistroId = "fedora" })
	e.wantBlocked(rpm, sec, ifReq, blockUnsupported)

	none := e.osNode("fi1")
	e.osStatus(none, func(s *agentv1.OsStatus) { s.Pending, s.Security, s.RebootRequired = 0, 0, false })
	e.wantBlocked(none, sec, ifReq, blockNothingToDo)
	e.wantBlocked(none, store.OsModeRebootOnly, ifReq, blockNothingToDo) // "if required" and none is
	if _, err := e.s.startOs(e.ctx, []string{none}, store.OsModeRebootOnly, store.OsRebootAlways); err != nil {
		t.Fatalf("an owner's reboot of a node that does not ask for one: %v", err)
	}
	if _, err := e.s.cancelOs(e.ctx, ""); err != nil {
		t.Fatal(err)
	}

	blank := e.addNode("blank1", nodeOpts{caps: osCaps})
	e.wantBlocked(blank, sec, ifReq, blockNoStatus)

	busy := e.osNode("se1")
	e.osJob(busy, running("manual-1700000000", "installing"))
	e.wantBlocked(busy, sec, ifReq, blockNodeBusy)

	e.exec(`UPDATE node SET awg_prepare_json = ? WHERE id = ?`, `{"state":"running","since":1}`, e.osNode("awg1"))
	e.wantBlocked("nod_awg1", sec, ifReq, blockAwgBuild)
}

func TestOsStartRefusedByOtherWork(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	os1 := e.osNode("de1")
	other := e.addNode("nl1", nodeOpts{caps: []string{"doctor/1", capUpdate, capGuard}})

	e.startRollout(other)
	e.wantBlocked(os1, store.OsModeSecurity, store.OsRebootIfRequired, blockAgentRollout)
	if _, err := e.s.cancel(e.ctx, ""); err != nil {
		t.Fatal(err)
	}
	e.tick()

	e.startOs(os1, store.OsModeSecurity, store.OsRebootIfRequired)
	// while it is active nothing else starts: not a second one, not an agent rollout, not a panel update, not a bundle sync
	e.wantBlocked(os1, store.OsModeSecurity, store.OsRebootIfRequired, blockRolloutOn)
	if _, err := e.s.start(e.ctx, []string{other}, 0); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("an agent rollout next to an OS rollout = %v", err)
	}
	if err := e.s.beginPanelUpdate(e.ctx); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a panel update next to an OS rollout = %v", err)
	}
	e.s.cfg.NodeBundleSource = nil
	if active, err := e.s.osRolloutRunning(e.ctx); err != nil || !active {
		t.Fatalf("osRolloutActive = %v, %v", active, err)
	}
}

func TestRollbackNodeIsRefusedWhileAnOsJobIsOnTheNode(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.osTick()
	if _, err := e.s.rollbackNode(e.ctx, id); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "node_updating") {
		t.Fatalf("rollback during an OS job = %v", err)
	}
	if got := e.fl.rolledBack(); len(got) != 0 {
		t.Fatalf("the agent was told to roll back: %v", got)
	}
}

func TestOsRolloutNodeGoneOfflineBeforeLaunchIsSkipped(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.drop(id)
	e.osTick()
	e.wantOsStep(id, store.OsStepSkipped, errOffline)
	e.wantOsRollout(store.RolloutDone, "")
	if len(e.fl.osJobs()) != 0 {
		t.Fatal("a job was sent to an offline node")
	}
}

func TestOsRolloutHardRefusalPausesAndRetryUsesANewJobID(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	other := e.osNode("nl1")
	e.fl.onOs = func(_ string, req OsJobRequest) (*agentv1.CommandResult, error) {
		return &agentv1.CommandResult{Ok: true, Params: map[string]string{"state": osupdate.AnswerUnsupported, "code": osupdate.CodeDiskFull, "reason": "the disk is full"}}, nil
	}
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.osTick()
	x := e.wantOsStep(id, store.OsStepFailed, osErrAgentRefused)
	if x.Params["code"] != osupdate.CodeDiskFull {
		t.Fatalf("params %+v", x.Params)
	}
	// a real refusal pauses the run and keeps later nodes queued until the owner decides.
	ro := e.wantOsRollout(store.RolloutPaused, pauseStepFailed)
	if got := e.s.Conditions(e.ctx); len(got) != 1 {
		t.Fatalf("a real refusal must raise a health alert: %+v", got)
	}
	e.wantEvent(id, "os_step_failed")
	if _, err := e.s.startOs(e.ctx, []string{other}, store.OsModeSecurity, store.OsRebootIfRequired); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a second OS rollout beside the paused one = %v", err)
	}
	if _, err := e.s.cancelOs(e.ctx, ro.ID); err != nil {
		t.Fatal(err)
	}
	e.wantOsRollout(store.RolloutCancelled, "")

	e.fl.onOs = nil
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.osTick()
	if y := e.wantOsStep(id, store.OsStepSent, ""); y.JobID == x.JobID {
		t.Fatalf("the next attempt reused the job id %s", y.JobID)
	}
}

func TestOsRolloutThatHappenedOnTheNodePausesAndBlocksOnlyTheNextOsUpdate(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	id := e.osNode("de1")
	other := e.addNode("nl1", nodeOpts{caps: []string{"doctor/1", capUpdate, capGuard}})
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.osTick()
	x := e.osStep(id)
	e.osEvent(id, "os_update_failed", map[string]string{"job_id": x.JobID, "code": osupdate.CodeInstallFailed, "package": "libssl3"})
	e.osTick()
	e.wantOsRollout(store.RolloutPaused, pauseStepFailed)
	c := e.s.Conditions(e.ctx)
	if len(c) != 1 || c[0].Kind != "os_update_failed" || c[0].Params["mode"] != "security" || c[0].Params["code"] != osupdate.CodeInstallFailed {
		t.Fatalf("conditions %+v", c)
	}
	e.wantBlocked(id, store.OsModeSecurity, store.OsRebootIfRequired, blockRolloutOn)
	// a paused OS update holds no node: the agent rollout and the panel update go on
	if _, err := e.s.start(e.ctx, []string{other}, 0); err != nil {
		t.Fatalf("an agent rollout beside a paused OS update: %v", err)
	}
	if _, err := e.s.cancel(e.ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.s.beginPanelUpdate(e.ctx); err != nil {
		t.Fatalf("a panel update beside a paused OS update: %v", err)
	}
	e.s.finishPanelUpdate()
	if _, err := e.s.cancelOs(e.ctx, ""); err != nil {
		t.Fatal(err)
	}
}

func TestOsRolloutAnswerRunningOtherJobFailsTheStep(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.fl.onOs = func(_ string, req OsJobRequest) (*agentv1.CommandResult, error) {
		return &agentv1.CommandResult{Ok: true, Params: map[string]string{"state": osupdate.AnswerRunning, "job_id": "manual-1700000000"}}, nil
	}
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.osTick()
	// another job on the node: nothing changed and nothing is wrong, so the step waits to retry.
	if x := e.wantOsStep(id, store.OsStepPending, ""); x.Phase != osWaitPhase || x.Params[osRetryCode] != osupdate.CodeBusy {
		t.Fatalf("params %+v", x.Params)
	}
	e.wantOsRollout(store.RolloutRunning, "")
}

func TestOsRolloutNodeCodesAreOnlyTheKnownOnes(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.fl.onOs = func(_ string, req OsJobRequest) (*agentv1.CommandResult, error) {
		return &agentv1.CommandResult{Ok: true, Params: map[string]string{"state": osupdate.AnswerUnsupported, "code": "rm -rf /"}}, nil
	}
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.osTick()
	if x := e.wantOsStep(id, store.OsStepFailed, osErrAgentRefused); x.Params["code"] != "unknown" {
		t.Fatalf("a code the panel does not know was kept: %+v", x.Params)
	}
	e.wantOsRollout(store.RolloutPaused, pauseStepFailed)
}

func TestOsRolloutRebootHeldPausesWithTheNodesCode(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.osTick()
	x := e.osStep(id)
	e.osJob(id, &agentv1.OsJob{JobId: x.JobID, Action: "upgrade", Mode: "security", State: "failed", Code: osupdate.CodeBootDefaultCustom,
		Reason: "GRUB does not start its first entry", Reboot: "held", HeldReason: osupdate.CodeBootDefaultCustom, Upgraded: 6})
	e.osEvent(id, "os_update_failed", map[string]string{"job_id": x.JobID, "code": osupdate.CodeBootDefaultCustom, "reboot": "held"})
	e.osTick()
	got := e.wantOsStep(id, store.OsStepFailed, osErrRebootHeld)
	if got.Params["code"] != osupdate.CodeBootDefaultCustom || got.Params["reboot"] != "held" || got.Params["held_reason"] != osupdate.CodeBootDefaultCustom || got.Params["upgraded"] != "6" {
		t.Fatalf("params %+v", got.Params)
	}
	e.wantOsRollout(store.RolloutPaused, pauseOsRebootHeld)
	if c := e.s.Conditions(e.ctx); len(c) != 1 || c[0].Why != "health.alert.os_update_failed.why.reboot_held" || c[0].Severity != 2 {
		t.Fatalf("conditions %+v", c)
	}
}

func TestOsRolloutJobFailureWithAPackage(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.osTick()
	x := e.osStep(id)
	e.osEvent(id, "os_update_failed", map[string]string{"job_id": x.JobID, "code": osupdate.CodeInstallFailed, "package": "libssl3", "reason": "installing the updates failed"})
	e.osTick()
	got := e.wantOsStep(id, store.OsStepFailed, osErrJobFailed)
	if got.Params["code"] != osupdate.CodeInstallFailed || got.Params["package"] != "libssl3" {
		t.Fatalf("params %+v", got.Params)
	}
	e.wantOsRollout(store.RolloutPaused, pauseStepFailed)
}

func TestOsRolloutNodeNotBackIsCritical(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.reachReboot(id)
	e.clk.Advance(14 * time.Minute)
	e.osTick()
	e.wantOsStep(id, store.OsStepRebooting, "")
	e.clk.Advance(2 * time.Minute)
	e.osTick()
	e.wantOsStep(id, store.OsStepFailed, osErrNotBack)
	e.wantOsRollout(store.RolloutPaused, pauseOsNotBack)
	c := e.s.Conditions(e.ctx)
	if len(c) != 1 || c[0].Severity != 3 || c[0].Why != "health.alert.os_update_failed.why.not_back" {
		t.Fatalf("conditions %+v", c)
	}
}

func TestOsRolloutKernelNotSwitched(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.reachReboot(id)
	e.clk.Advance(3 * time.Minute)
	e.osBoot(id, e.clk.Now().Add(-time.Minute), kernelOld) // back on the old kernel
	e.osTick()
	e.wantOsStep(id, store.OsStepGating, "")
	e.osTick()
	if x := e.wantOsStep(id, store.OsStepFailed, osErrKernel); x.Params["kernel"] != kernelOld {
		t.Fatalf("params %+v", x.Params)
	}
	e.wantOsRollout(store.RolloutPaused, pauseGateFailed)
}

func TestOsRolloutJobFailsAfterTheReboot(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	x := e.reachReboot(id)
	e.clk.Advance(3 * time.Minute)
	e.osBoot(id, e.clk.Now().Add(-time.Minute), kernelNew)
	e.osTick()
	e.wantOsStep(id, store.OsStepGating, "")
	e.osEvent(id, "os_update_failed", map[string]string{"job_id": x.JobID, "code": osupdate.CodeInboundFailed, "reboot": "done"})
	e.osTick()
	if got := e.wantOsStep(id, store.OsStepFailed, osErrJobFailed); got.Params["code"] != osupdate.CodeInboundFailed || got.Params["reboot"] != "done" {
		t.Fatalf("params %+v", got.Params)
	}
	e.wantOsRollout(store.RolloutPaused, pauseGateFailed)
}

func TestOsRolloutGateWaitsForTheNodeToSettleThenFails(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	x := e.reachReboot(id)
	e.clk.Advance(3 * time.Minute)
	e.osBoot(id, e.clk.Now().Add(-time.Minute), kernelNew)
	e.osTick()
	// the node is back but still looking at itself: its job is not over
	e.osJob(id, running(x.JobID, "rebooting"))
	e.osTick()
	e.wantOsStep(id, store.OsStepGating, "")
	e.clk.Advance(9 * time.Minute)
	e.osTick()
	e.wantOsStep(id, store.OsStepFailed, osErrJobNotSettled)
	e.wantOsRollout(store.RolloutPaused, pauseGateFailed)
}

func TestOsRolloutGateFailsOnABrokenProfile(t *testing.T) {
	e := newEnv(t)
	id := e.addNode("de1", nodeOpts{caps: osCaps, inbounds: 1})
	e.osBoot(id, e.clk.Now().Add(-48*time.Hour), kernelOld)
	e.osStatus(id, nil)
	x := func() store.OsStepRow {
		e.startOs(id, store.OsModeSecurity, store.OsRebootNever)
		e.osTick()
		return e.osStep(id)
	}()
	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "2", "reboot": "not_asked"})
	e.osTick()
	e.wantOsStep(id, store.OsStepGating, "")
	e.exec(`UPDATE inbound SET state = 'failed' WHERE node_id = ?`, id)
	e.osTick() // the first sight of a failed profile starts its 30 seconds
	e.wantOsStep(id, store.OsStepGating, "")
	e.clk.Advance(31 * time.Second)
	e.osTick()
	e.wantOsStep(id, store.OsStepFailed, errInboundFailed)
	e.wantOsRollout(store.RolloutPaused, pauseGateFailed)
}

func TestOsRolloutEventsOfAnotherJobOrRepeatedAreHarmless(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeSecurity, store.OsRebootNever)
	e.osTick()
	x := e.osStep(id)
	e.osEvent(id, "os_update_failed", map[string]string{"job_id": "osj_someoneelse", "code": osupdate.CodeInstallFailed})
	e.osEvent(id, "os_update_done", map[string]string{"job_id": "manual-1700000000", "reboot": "not_asked"})
	e.osTick()
	e.wantOsStep(id, store.OsStepSent, "")
	for range 3 { // the agent restarted and raised the end again
		e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "2", "reboot": "not_asked"})
	}
	e.osTick()
	e.wantOsStep(id, store.OsStepGating, "")
	e.osTick()
	e.wantOsStep(id, store.OsStepPassed, "")
	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "2", "reboot": "not_asked"}) // late repeat
	e.osTick()
	e.wantOsRollout(store.RolloutDone, "")
	var n int
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT count(*) FROM event WHERE code = 'os_step_passed'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("os_step_passed events = %d, %v", n, err)
	}
}

func TestOsRolloutLostAnswerIsSentAgainUnderTheSameJobID(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	calls := 0
	e.fl.onOs = func(_ string, req OsJobRequest) (*agentv1.CommandResult, error) {
		calls++
		if calls == 1 {
			return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("no answer"))
		}
		return &agentv1.CommandResult{Ok: true, Params: map[string]string{"state": osupdate.AnswerStarted, "job_id": req.JobID}}, nil
	}
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.osTick()
	x := e.wantOsStep(id, store.OsStepSent, "")
	if !x.AckedAt.IsZero() {
		t.Fatal("a lost answer was taken for an acknowledgement")
	}
	e.clk.Advance(30 * time.Second)
	e.osTick()
	if len(e.fl.osJobs()) != 1 {
		t.Fatal("sent again too soon")
	}
	e.clk.Advance(time.Minute)
	e.osTick()
	jobs := e.fl.osJobs()
	if len(jobs) != 2 || jobs[1].req.JobID != x.JobID {
		t.Fatalf("sent %+v, want the same job id again", jobs)
	}
	if y := e.osStep(id); y.AckedAt.IsZero() || y.Sends != 2 {
		t.Fatalf("step %+v", y)
	}
	e.clk.Advance(10 * time.Minute)
	e.osTick()
	if len(e.fl.osJobs()) != 2 {
		t.Fatal("sent again after it was acknowledged")
	}
}

func TestOsRolloutDoesNotResendLate(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.fl.onOs = func(string, OsJobRequest) (*agentv1.CommandResult, error) {
		return nil, connect.NewError(connect.CodeDeadlineExceeded, errors.New("no answer"))
	}
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.osTick()
	e.clk.Advance(6 * time.Minute) // past the window in which the node can only have our job
	e.osTick()
	e.osTick()
	if jobs := e.fl.osJobs(); len(jobs) != 1 {
		t.Fatalf("sent %d times, want 1", len(jobs))
	}
	e.clk.Advance(86 * time.Minute)
	e.osTick()
	e.wantOsStep(id, store.OsStepFailed, errNoAnswer)
	e.wantOsRollout(store.RolloutPaused, pauseStepFailed)
}

func TestOsRolloutSurvivesAPanelRestart(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	x := e.reachReboot(id)
	e.restart()
	e.s.recoverOs(e.ctx)
	if !e.s.Updating(id) {
		t.Fatal("after a restart the rebooting node is not UPDATING")
	}
	e.clk.Advance(3 * time.Minute)
	e.osBoot(id, e.clk.Now().Add(-time.Minute), kernelNew)
	e.osTick()
	e.wantOsStep(id, store.OsStepGating, "")
	e.clk.Advance(4 * time.Minute)
	e.restart()
	e.s.recoverOs(e.ctx)
	if got := e.osStep(id); !got.BackAt.Equal(e.clk.Now()) {
		t.Fatalf("the gate window was not given again: back %v now %v", got.BackAt, e.clk.Now())
	}
	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "6", "reboot": "done", "kernel": kernelNew})
	e.osTick()
	e.wantOsStep(id, store.OsStepPassed, "")
}

func TestOsRolloutHostRebootedUnseen(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.osTick()
	x := e.osStep(id)
	// the panel was down while the job rebooted the host: it finds the node on a new boot and the job's end
	e.clk.Advance(10 * time.Minute)
	e.osBoot(id, e.clk.Now().Add(-5*time.Minute), kernelNew)
	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "6", "reboot": "done", "kernel": kernelNew})
	e.osTick()
	got := e.wantOsStep(id, store.OsStepGating, "")
	if got.RebootAt.IsZero() {
		t.Fatalf("a reboot nobody saw begin is not recorded: %+v", got)
	}
	e.osTick()
	e.wantOsStep(id, store.OsStepPassed, "")
}

func TestOsCancel(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	if _, err := e.s.cancelOs(e.ctx, ""); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("cancel with nothing active = %v", err)
	}
	ro := e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	got, err := e.s.cancelOs(e.ctx, ro.ID) // before the first pass: the step has not been sent
	if err != nil || got.Status != store.RolloutCancelled {
		t.Fatalf("cancel a pending step = %+v, %v", got, err)
	}
	e.wantOsStep(id, store.OsStepSkipped, errCancelled)
	e.osTick()
	if len(e.fl.osJobs()) != 0 {
		t.Fatal("a cancelled rollout sent a job")
	}

	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.osTick()
	if _, err := e.s.cancelOs(e.ctx, ""); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "os_cancel_inflight") {
		t.Fatalf("cancel while the job runs on the node = %v", err)
	}
	e.wantOsRollout(store.RolloutRunning, "")
}

func TestOsRolloutPrune(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	if _, err := e.s.cancelOs(e.ctx, ""); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(100 * 24 * time.Hour)
	if n, err := e.st.PruneOsRollouts(e.ctx, 0, e.clk.Now().Add(-rolloutAge)); err != nil || n != 1 {
		t.Fatalf("prune = %d, %v", n, err)
	}
}

func TestOnPanelServerNeedsMoreThanTheBootID(t *testing.T) {
	s := &Service{hostname: "panel1"}
	host := &agentv1.OsStatus{BootIdHash: "0123456789abcdef"}
	for _, c := range []struct {
		name     string
		node     *agentv1.OsStatus
		nodeHost string
		want     bool
	}{
		{"same boot, a machine of its own", &agentv1.OsStatus{BootIdHash: "0123456789abcdef"}, "", true},
		{"another boot", &agentv1.OsStatus{BootIdHash: "fedcba9876543210"}, "", false},
		{"no boot id read", &agentv1.OsStatus{}, "", false},
		{"a container on the panel's kernel", &agentv1.OsStatus{BootIdHash: "0123456789abcdef", Container: "lxc"}, "box2", false},
		{"the panel's own container", &agentv1.OsStatus{BootIdHash: "0123456789abcdef", Container: "lxc"}, "panel1", true},
		{"no status", nil, "panel1", false},
	} {
		if got := s.onPanelServer(c.node, host, c.nodeHost); got != c.want {
			t.Errorf("%s: onPanelServer = %v, want %v", c.name, got, c.want)
		}
	}
	if s.onPanelServer(&agentv1.OsStatus{BootIdHash: "0123456789abcdef"}, nil, "") {
		t.Error("a node is the panel's server although the panel knows no boot id of its own")
	}
	// the panel without a snapshot of its own (not root, or before its first reading) still knows its boot
	s.bootHash = "0123456789abcdef"
	if !s.onPanelServer(&agentv1.OsStatus{BootIdHash: "0123456789abcdef"}, nil, "") {
		t.Error("without a snapshot of its own the panel lets a node on its own server be rebooted")
	}
	if s.onPanelServer(&agentv1.OsStatus{BootIdHash: "fedcba9876543210"}, nil, "") {
		t.Error("another server counted as the panel's")
	}
}

func TestOsBlockOrder(t *testing.T) {
	ok := osNode{connected: true, updateCap: true, supported: true, status: &agentv1.OsStatus{Security: 1}}
	if got := osBlock(osGlobal{}, ok, false); got != "" {
		t.Fatalf("a good node is blocked: %q", got)
	}
	g := osGlobal{rollout: true, agentRollout: true, panelBusy: true}
	if got := osBlock(g, ok, false); got != blockRolloutOn {
		t.Fatalf("got %q", got)
	}
	if got := osBlock(osGlobal{agentRollout: true, panelBusy: true}, ok, false); got != blockAgentRollout {
		t.Fatalf("got %q", got)
	}
	off := ok
	off.connected = false
	if got := osBlock(g, off, false); got != blockOffline { // the node's own state is told first
		t.Fatalf("got %q", got)
	}
}

func TestGetOsUpdatesCarriesBlocksAndTheRollout(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	old := e.addNode("old1", nodeOpts{caps: []string{"doctor/1", osupdate.Capability}})
	e.osStatus(old, nil)
	get := func() *adminv1.GetOsUpdatesResponse {
		t.Helper()
		resp, err := rpc{e.s}.GetOsUpdates(e.ctx, connect.NewRequest(&adminv1.GetOsUpdatesRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg
	}
	find := func(r *adminv1.GetOsUpdatesResponse, nodeID string) *adminv1.NodeOs {
		for _, n := range r.Nodes {
			if n.NodeId == nodeID {
				return n
			}
		}
		t.Fatalf("node %s not listed", nodeID)
		return nil
	}
	r := get()
	if r.Rollout != nil {
		t.Fatalf("a rollout before any: %+v", r.Rollout)
	}
	if n := find(r, id); !n.UpdateCapable || n.UpdateBlock != "" || n.RebootBlock != "" {
		t.Fatalf("a node that can: %+v", n)
	}
	if n := find(r, old); n.UpdateCapable || n.UpdateBlock != blockAgentOld || n.RebootBlock != blockAgentOld {
		t.Fatalf("an agent that only reads: %+v", n)
	}
	e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
	e.osTick()
	r = get()
	n := find(r, id)
	if n.UpdateBlock != blockRolloutOn || r.Rollout == nil || r.Rollout.Status != adminv1.OsRolloutStatus_OS_ROLLOUT_STATUS_RUNNING ||
		r.Rollout.Mode != adminv1.OsRolloutMode_OS_ROLLOUT_MODE_SECURITY || r.Rollout.Reboot != adminv1.OsRebootPolicy_OS_REBOOT_POLICY_IF_REQUIRED ||
		len(r.Rollout.Steps) != 1 || r.Rollout.Steps[0].State != adminv1.OsStepState_OS_STEP_STATE_SENT || r.Rollout.Steps[0].JobId == "" || r.Rollout.Steps[0].NodeName != "de1" {
		t.Fatalf("while it runs: node %+v rollout %+v", n, r.Rollout)
	}
}

func TestRefreshOsUpdatesIsRateLimited(t *testing.T) {
	e := newEnv(t)
	var ids []string
	for _, name := range []string{"de1", "nl1", "fi1", "se1"} {
		id := e.osNode(name)
		e.osStatus(id, func(s *agentv1.OsStatus) { s.ListsUnix = e.clk.Now().Unix() - 3*3600 })
		ids = append(ids, id)
	}
	off := e.osNode("off1")
	e.osStatus(off, func(s *agentv1.OsStatus) { s.ListsUnix = e.clk.Now().Unix() - 3*3600 })
	e.drop(off)
	old := e.addNode("old1", nodeOpts{caps: []string{"doctor/1", osupdate.Capability}})
	got := func(res []*adminv1.OsRefreshResult) map[string]string {
		m := map[string]string{}
		for _, r := range res {
			m[r.NodeId] = r.Outcome
		}
		return m
	}
	res, err := e.s.refreshOs(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	outcomes := got(res)
	started := 0
	for _, id := range ids {
		switch outcomes[id] {
		case "started":
			started++
		case "limit":
		default:
			t.Fatalf("%s: %q", id, outcomes[id])
		}
	}
	if started != 3 || outcomes[off] != "offline" || outcomes[old] != "unsupported" {
		t.Fatalf("outcomes %v", outcomes)
	}
	for _, j := range e.fl.osJobs() {
		if j.req.Action != "refresh" || j.req.Mode != "" || j.req.Reboot != "never" || !osupdate.ValidJobID(j.req.JobID) {
			t.Fatalf("a refresh job is %+v", j)
		}
	}
	// the same click again: the three were asked a moment ago
	res, _ = e.s.refreshOs(e.ctx, nil)
	for _, r := range res {
		if r.Outcome == "started" {
			t.Fatalf("%s was asked twice in 30 minutes", r.NodeId)
		}
	}
	// after the gap the fourth gets its turn
	e.clk.Advance(31 * time.Minute)
	res, _ = e.s.refreshOs(e.ctx, nil)
	if n := 0; true {
		for _, r := range res {
			if r.Outcome == "started" {
				n++
			}
		}
		if n != 3 {
			t.Fatalf("after the gap %d started, want 3 of the 4", n)
		}
	}
	if _, err := e.s.refreshOs(e.ctx, []string{"nod_nobody"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("an unknown node = %v", err)
	}
}

func TestRefreshOsUpdatesSkipsANodeThatRefreshedItself(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.osStatus(id, func(s *agentv1.OsStatus) { s.ListsUnix = e.clk.Now().Unix() - 60 })
	res, err := e.s.refreshOs(e.ctx, []string{id})
	if err != nil || res[0].Outcome != "recent" || len(e.fl.osJobs()) != 0 {
		t.Fatalf("%v %v", res, err)
	}
}
