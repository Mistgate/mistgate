package update

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/osupdate"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// "Not now" of an OS job: when a reboot is possible again, the refusals that close themselves, what the check waits for.

func TestOsRebootWait(t *testing.T) {
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	m, h := time.Minute, time.Hour
	for _, tc := range []struct {
		name    string
		boot    time.Time
		reboots []time.Time
		wantIn  time.Duration // 0: possible now
		code    string
	}{
		{"booted long ago, never rebooted by the panel", ago(48 * h), nil, 0, ""},
		{"no boot time known", time.Time{}, nil, 0, ""},
		{"booted 3 minutes ago: the node wants 10 minutes of uptime", ago(3 * m), nil, 7 * m, osupdate.CodeTooSoon},
		{"booted exactly 10 minutes ago: allowed (the node refuses only less)", ago(10 * m), nil, 0, ""},
		{"booted 10 minutes less a second ago", ago(10*m - time.Second), nil, time.Second, osupdate.CodeTooSoon},
		{"a boot from the future is a skewed clock, capped at the limit itself", now.Add(2 * h), nil, 10 * m, osupdate.CodeTooSoon},
		{"rebooted 20 minutes ago: once an hour", ago(20 * m), []time.Time{ago(20 * m)}, 40 * m, osupdate.CodeRebootLimit},
		{"rebooted exactly an hour ago: allowed", ago(h), []time.Time{ago(h)}, 0, ""},
		{"rebooted an hour less a second ago", ago(h), []time.Time{ago(h - time.Second)}, time.Second, osupdate.CodeRebootLimit},
		{"the hourly limit outlasts the uptime one", ago(2 * m), []time.Time{ago(2 * m)}, 58 * m, osupdate.CodeRebootLimit},
		{"the uptime limit outlasts the hourly one (rebooted by hand 3 minutes ago)", ago(3 * m), []time.Time{ago(59 * m)}, 7 * m, osupdate.CodeTooSoon},
		{"three in 24 hours: free when the oldest turns 24 hours", ago(30 * h), []time.Time{ago(23 * h), ago(12 * h), ago(90 * m)}, h, osupdate.CodeRebootLimit},
		{"three in 24 hours, the newest 30 minutes ago: the daily limit is the later one", ago(30 * m), []time.Time{ago(23 * h), ago(12 * h), ago(30 * m)}, h, osupdate.CodeRebootLimit},
		{"three, the oldest exactly 24 hours ago, no longer counts", ago(30 * h), []time.Time{ago(24 * h), ago(12 * h), ago(90 * m)}, 0, ""},
		{"four in 24 hours: the third newest decides", ago(30 * h), []time.Time{ago(23*h + 30*m), ago(23 * h), ago(2 * h), ago(90 * m)}, h, osupdate.CodeRebootLimit},
		{"two in 24 hours are fine", ago(30 * h), []time.Time{ago(23 * h), ago(2 * h)}, 0, ""},
		{"reboots older than a day are forgotten", ago(30 * h), []time.Time{ago(25 * h), ago(26 * h), ago(27 * h)}, 0, ""},
		{"the order of the list does not matter", ago(30 * h), []time.Time{ago(90 * m), ago(23 * h), ago(12 * h)}, h, osupdate.CodeRebootLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at, code := osRebootWait(tc.boot, tc.reboots, now)
			if tc.wantIn == 0 {
				if !at.IsZero() || code != "" {
					t.Fatalf("osRebootWait = %v %q, want possible now", at, code)
				}
				return
			}
			if !at.Equal(now.Add(tc.wantIn)) || code != tc.code {
				t.Fatalf("osRebootWait = in %v %q, want in %v %q", at.Sub(now), code, tc.wantIn, tc.code)
			}
		})
	}
}

func (e *env) osNodeViews() map[string]*adminv1.NodeOs {
	e.t.Helper()
	resp, err := rpc{e.s}.GetOsUpdates(e.ctx, connect.NewRequest(&adminv1.GetOsUpdatesRequest{}))
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]*adminv1.NodeOs{}
	for _, n := range resp.Msg.Nodes {
		out[n.NodeId] = n
	}
	return out
}

// the panel says when the Reboot action opens, refuses a reboot before that, and keeps the install open
func TestGetOsUpdatesTellsWhenARebootBecomesPossible(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.osBoot(id, e.clk.Now().Add(-3*time.Minute), kernelOld)
	n := e.osNodeViews()[id]
	if want := e.clk.Now().Add(7 * time.Minute).Unix(); n.RebootPossibleUnix != want || n.RebootBlock != blockRebootWait || n.UpdateBlock != "" {
		t.Fatalf("a node that booted 3 minutes ago: possible %d (want %d), reboot block %q, update block %q", n.RebootPossibleUnix, want, n.RebootBlock, n.UpdateBlock)
	}
	e.wantBlocked(id, store.OsModeRebootOnly, store.OsRebootAlways, blockRebootWait)
	e.clk.Advance(7 * time.Minute)
	if n := e.osNodeViews()[id]; n.RebootPossibleUnix != 0 || n.RebootBlock != "" {
		t.Fatalf("at the limit the reboot is possible: %+v", n)
	}
	e.startOs(id, store.OsModeRebootOnly, store.OsRebootAlways)
}

// the panel counts its own reboots: after one the hour limit holds the Reboot action, however long ago the node booted
func TestGetOsUpdatesCountsTheRebootsOfThePanel(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	x := e.reachReboot(id)
	e.clk.Advance(3 * time.Minute)
	e.osBoot(id, e.clk.Now().Add(-time.Minute), kernelNew)
	e.osTick()
	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "6", "reboot": "done", "kernel": kernelNew})
	e.osTick()
	done := e.wantOsStep(id, store.OsStepPassed, "")
	e.osStatus(id, func(s *agentv1.OsStatus) { s.RebootRequired, s.KernelRunning = false, kernelNew }) // the node's next snapshot: no job any more
	n := e.osNodeViews()[id]
	if want := done.RebootAt.Add(time.Hour).Unix(); n.RebootPossibleUnix != want || n.RebootBlock != blockRebootWait {
		t.Fatalf("after the panel's reboot: possible %d (want %d), block %q", n.RebootPossibleUnix, want, n.RebootBlock)
	}
	e.wantBlocked(id, store.OsModeRebootOnly, store.OsRebootAlways, blockRebootWait)
	e.clk.Advance(time.Hour)
	if n := e.osNodeViews()[id]; n.RebootPossibleUnix != 0 || n.RebootBlock != "" {
		t.Fatalf("an hour later: %+v", n)
	}
}

// the node still refuses (a race with the panel's count): the step waits for the limit and retries without an alert
func TestOsRebootRefusedByTheNodeWaitsAndRetries(t *testing.T) {
	for _, code := range []string{osupdate.CodeTooSoon, osupdate.CodeRebootLimit} {
		t.Run(code, func(t *testing.T) {
			e := newEnv(t)
			id := e.osNode("de1")
			e.startOs(id, store.OsModeRebootOnly, store.OsRebootAlways)
			e.osTick()
			x := e.osStep(id)
			e.osJob(id, &agentv1.OsJob{JobId: x.JobID, Action: "reboot", State: "failed", Code: code, Reboot: "held", HeldReason: code})
			e.osEvent(id, "os_update_failed", map[string]string{"job_id": x.JobID, "code": code, "reboot": "held", "reasons": code})
			e.osTick()
			got := e.wantOsStep(id, store.OsStepPending, "")
			if got.Phase != osWaitPhase || got.Params[osRetryCode] != code || got.Params[osRetryAfter] == "" {
				t.Fatalf("retry params %+v", got.Params)
			}
			e.wantOsRollout(store.RolloutRunning, "")
			if c := e.s.Conditions(e.ctx); len(c) != 0 {
				t.Fatalf("an alert for a refusal that changed nothing: %+v", c)
			}
			e.clk.Advance(osRetryInterval)
			e.osTick()
			if jobs := e.fl.osJobs(); len(jobs) != 2 || jobs[1].nodeID != id || jobs[1].req.JobID == x.JobID {
				t.Fatalf("refused job was not retried with a new id: %v", jobs)
			}
			e.wantOsStep(id, store.OsStepSent, "")
		})
	}
}

// a refusal that needs a look at the node keeps its stop, even when a "not now" reason came with it
func TestOsRebootRefusedForARealReasonStillStops(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeRebootOnly, store.OsRebootAlways)
	e.osTick()
	x := e.osStep(id)
	e.osJob(id, &agentv1.OsJob{JobId: x.JobID, Action: "reboot", State: "failed", Code: osupdate.CodeTooSoon, Reboot: "held", HeldReason: osupdate.CodeTooSoon})
	e.osEvent(id, "os_update_failed", map[string]string{"job_id": x.JobID, "code": osupdate.CodeTooSoon, "reboot": "held", "reasons": "too_soon,boot_entry_missing"})
	e.osTick()
	e.wantOsStep(id, store.OsStepFailed, osErrRebootHeld)
	e.wantOsRollout(store.RolloutPaused, pauseOsRebootHeld)
	e.wantBlocked(id, store.OsModeSecurity, store.OsRebootIfRequired, blockRolloutOn)
}

// an install: a lock timeout before anything was installed waits; a reboot held after the install, or packages already
// installed, still stop
func TestOsInstallThatChangedNothingClosesItself(t *testing.T) {
	run := func(job *agentv1.OsJob, event map[string]string) (*env, string) {
		e := newEnv(t)
		id := e.osNode("de1")
		e.startOs(id, store.OsModeSecurity, store.OsRebootIfRequired)
		e.osTick()
		x := e.osStep(id)
		job.JobId, job.Action, job.Mode, job.State = x.JobID, "upgrade", "security", "failed"
		event["job_id"] = x.JobID
		e.osJob(id, job)
		e.osEvent(id, "os_update_failed", event)
		e.osTick()
		return e, id
	}
	e, id := run(&agentv1.OsJob{Code: osupdate.CodeAptLock}, map[string]string{"code": osupdate.CodeAptLock})
	if x := e.wantOsStep(id, store.OsStepPending, ""); x.Phase != osWaitPhase || x.Params[osRetryCode] != osupdate.CodeAptLock {
		t.Fatalf("apt lock retry wait %+v", x)
	}
	e.wantOsRollout(store.RolloutRunning, "")

	e, id = run(&agentv1.OsJob{Code: osupdate.CodeAptLock, Upgraded: 3}, map[string]string{"code": osupdate.CodeAptLock})
	e.wantOsStep(id, store.OsStepFailed, osErrJobFailed)
	e.wantOsRollout(store.RolloutPaused, pauseStepFailed)

	e, id = run(&agentv1.OsJob{Code: osupdate.CodeTooSoon, Reboot: "held", HeldReason: osupdate.CodeTooSoon, Upgraded: 6}, map[string]string{"code": osupdate.CodeTooSoon, "reboot": "held"})
	e.wantOsStep(id, store.OsStepFailed, osErrRebootHeld)
	e.wantOsRollout(store.RolloutPaused, pauseOsRebootHeld)
}

// a reboot that is not possible yet when its turn comes (the host rebooted by itself meanwhile) waits for the reported time
func TestOsLaunchWaitsUntilARebootIsPossible(t *testing.T) {
	e := newEnv(t)
	id := e.osNode("de1")
	e.startOs(id, store.OsModeRebootOnly, store.OsRebootAlways)
	e.osBoot(id, e.clk.Now().Add(-time.Minute), kernelOld)
	e.osTick()
	got := e.wantOsStep(id, store.OsStepPending, "")
	wantAt := e.clk.Now().Add(9 * time.Minute).Unix()
	if got.Phase != osWaitPhase || got.Params[osRetryCode] != osupdate.CodeTooSoon || got.Params[osRetryAfter] != strconv.FormatInt(wantAt, 10) {
		t.Fatalf("retry params %+v, want time %d", got.Params, wantAt)
	}
	e.wantOsRollout(store.RolloutRunning, "")
	if len(e.fl.osJobs()) != 0 {
		t.Fatal("a reboot was sent to a node that would refuse it")
	}
	e.clk.Advance(9 * time.Minute)
	e.osTick()
	e.wantOsStep(id, store.OsStepSent, "")
}

func gateNamesOf(t *testing.T, x store.OsStepRow) []string {
	t.Helper()
	var names []string
	if err := json.Unmarshal([]byte(x.Params[gateNames]), &names); err != nil {
		t.Fatalf("gate_names %q: %v", x.Params[gateNames], err)
	}
	return names
}

// while the check waits, the step says what for: the profiles that have not passed, and the window
func TestOsGateSaysWhatItWaitsFor(t *testing.T) {
	e := newEnv(t)
	id := e.addNode("de1", nodeOpts{caps: osCaps, inbounds: 2})
	e.osBoot(id, e.clk.Now().Add(-48*time.Hour), kernelOld)
	e.osStatus(id, nil)
	e.exec(`UPDATE profile SET name = 'Berlin main' WHERE id = 'prf_de1_0'`)
	e.exec(`UPDATE profile SET name = 'Berlin backup' WHERE id = 'prf_de1_1'`)
	e.startOs(id, store.OsModeSecurity, store.OsRebootNever)
	e.osTick()
	x := e.osStep(id)
	e.hl.set(id, 2, 0, 0)
	e.hl.setPending(id, "inb_de1_1", "inb_de1_0")
	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "2", "reboot": "not_asked"})
	e.osTick()
	e.osTick()
	got := e.wantOsStep(id, store.OsStepGating, "")
	if got.Params[gateFor] != "profiles" || got.Params[gateMaxS] != "480" {
		t.Fatalf("params %+v", got.Params)
	}
	if names := gateNamesOf(t, got); len(names) != 2 || names[0] != "Berlin backup" || names[1] != "Berlin main" {
		t.Fatalf("names %v", names)
	}
	// one of them passed: the list shrinks
	e.hl.set(id, 2, 1, 0)
	e.hl.setPending(id, "inb_de1_0")
	e.osTick()
	if names := gateNamesOf(t, e.wantOsStep(id, store.OsStepGating, "")); len(names) != 1 || names[0] != "Berlin main" {
		t.Fatalf("names after one passed: %v", names)
	}
	// everything passed: the step is done and keeps nothing of the wait
	e.hl.set(id, 2, 2, 0)
	e.hl.setPending(id)
	e.osTick()
	done := e.wantOsStep(id, store.OsStepPassed, "")
	for _, k := range gateKeys {
		if _, ok := done.Params[k]; ok {
			t.Fatalf("a finished step keeps %s: %+v", k, done.Params)
		}
	}
}

// a check that waits for something that is not a profile says what kind of thing; more than five profiles are counted
func TestOsGateWaitParams(t *testing.T) {
	e := newEnv(t)
	names := []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7"}
	got := e.s.gateWaitParams(gateResult{soft: errProbeFailed, waiting: names})
	if got[gateFor] != "profiles" || got[gateMore] != "2" || got[gateMaxS] != "480" {
		t.Fatalf("params %+v", got)
	}
	long := e.s.gateWaitParams(gateResult{waiting: []string{"a very long name of a profile that goes on and on and on"}})
	var shown []string
	if err := json.Unmarshal([]byte(long[gateNames]), &shown); err != nil || len([]rune(shown[0])) != gateNameLn {
		t.Fatalf("a long name is cut to %d characters: %q (%v)", gateNameLn, shown, err)
	}
	for soft, want := range map[string]string{errWarpDown: "warp", osErrJobNotSettled: "job", errStateNotApplied: "settings", errInboundsUnread: "settings"} {
		if p := e.s.gateWaitParams(gateResult{soft: soft}); p[gateFor] != want || p[gateNames] != "" {
			t.Fatalf("%s: %+v", soft, p)
		}
	}
}

// the wait holds a reboot only: an install is not stopped by it, and the node's own state is told first
func TestOsBlockRebootWait(t *testing.T) {
	n := osNode{connected: true, updateCap: true, supported: true, status: &agentv1.OsStatus{Security: 1}, rebootAt: time.Now().Add(time.Minute), rebootCode: osupdate.CodeTooSoon}
	if got := osBlock(osGlobal{}, n, true); got != blockRebootWait {
		t.Fatalf("a reboot before its time: %q", got)
	}
	if got := osBlock(osGlobal{}, n, false); got != "" {
		t.Fatalf("an install is held by the reboot wait: %q", got)
	}
	n.busy = true
	if got := osBlock(osGlobal{}, n, true); got != blockNodeBusy {
		t.Fatalf("busy is told before the wait: %q", got)
	}
}
