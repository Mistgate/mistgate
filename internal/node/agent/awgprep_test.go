package agent

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/awg"
	"github.com/mistgate/mistgate/internal/node/awgprep"
	"github.com/mistgate/mistgate/internal/node/engine"
)

// The agent side of "PrepareAwgKernel" (agent.proto "AWG AND WARP"): the capability, the answers, the events of a run and of
// its end, the doctor re-check, and that the agent itself never touches its AmneziaWG backend. The machine and the job are
// fakes: nothing is installed, no systemd-run is started.

type prepNode struct {
	env      awgprep.Env
	ready    atomic.Bool
	launches atomic.Int32
	path     string
}

func okPrepEnv() awgprep.Env {
	return awgprep.Env{OSRelease: map[string]string{"ID": "ubuntu", "ID_LIKE": "debian", "PRETTY_NAME": "Ubuntu 24.04"}, Kernel: "6.8.0-142-generic", CPUs: 2, Systemd: true}
}

func (p *prepNode) controller(stateDir string) *awgprep.Controller {
	p.path = filepath.Join(stateDir, "awg-prepare.json")
	return awgprep.New(awgprep.Config{
		StatusPath: p.path,
		Env:        func(context.Context) awgprep.Env { return p.env },
		Ready:      p.ready.Load,
		Launch:     func(context.Context) error { p.launches.Add(1); return nil },
	})
}

// newPrep is an agent with the awg engine and a controller on the fake machine; the status file is read every 10 ms.
func newPrep(t *testing.T, env awgprep.Env, before func(statePath string)) (*harness, *prepNode) {
	t.Helper()
	p := &prepNode{env: env}
	h := newHarness(t, harnessOpts{
		extra: map[string]engine.Factory{awg.Protocol: newFakeAwg(&order{}).factory()},
		cfg: func(c *Config) {
			c.AwgPrepare = p.controller(c.StateDir)
			if before != nil {
				before(p.path)
			}
		},
		tune: func(a *Agent) { a.prepWatch = 10 * time.Millisecond },
	})
	return h, p
}

// writeStatus plays the job. On Windows a rename onto a file the watcher has open fails for a moment; Linux, where the
// agent runs, replaces it atomically, so a retry is all the test needs.
func writeStatus(t *testing.T, path string, s awgprep.Status) {
	t.Helper()
	var err error
	for range 200 {
		if err = awgprep.WriteStatus(path, s); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(err)
}

func askPrepare(h *harness, dryRun bool) *pb.CommandResult {
	h.t.Helper()
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_PrepareAwgKernel{PrepareAwgKernel: &pb.PrepareAwgKernel{RequestId: "req_p", DryRun: dryRun}}})
	r := h.panel.nextCmd()
	if r.RequestId != "req_p" {
		h.t.Fatalf("answer to %q", r.RequestId)
	}
	return r
}

func (p *fakePanel) event(code string) *pb.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.events {
		if e.Code == code {
			return e
		}
	}
	return nil
}

func (p *fakePanel) countEvents(code string) int {
	n := 0
	for _, c := range p.eventCodes() {
		if c == code {
			n++
		}
	}
	return n
}

func TestAwgPrepareCapabilityNeedsTheControllerAndTheEngine(t *testing.T) {
	has := func(h *harness) bool {
		select {
		case hello := <-h.panel.hellos:
			for _, c := range hello.Capabilities {
				if c == "awg-prepare/1" {
					return true
				}
			}
			return false
		case <-time.After(8 * time.Second):
			t.Fatal("no Hello")
			return false
		}
	}
	h, _ := newPrep(t, okPrepEnv(), nil)
	if !has(h) {
		t.Error("an agent with the controller and the awg engine does not list awg-prepare/1")
	}
	if has(newHarness(t, harnessOpts{extra: map[string]engine.Factory{awg.Protocol: newFakeAwg(&order{}).factory()}})) {
		t.Error("an agent without a controller lists awg-prepare/1")
	}
	if has(newHarness(t, harnessOpts{cfg: func(c *Config) { c.AwgPrepare = (&prepNode{env: okPrepEnv()}).controller(c.StateDir) }})) {
		t.Error("an agent without the awg engine lists awg-prepare/1")
	}
}

func TestAwgPrepareDryRunAnswersAndChangesNothing(t *testing.T) {
	h, p := newPrep(t, okPrepEnv(), nil)
	h.waitConnected()
	r := askPrepare(h, true)
	if !r.Ok || r.Params["state"] != "needs_prepare" || r.Params["kernel"] != "6.8.0-142-generic" {
		t.Fatalf("a dry run on a plain node: %+v", r)
	}
	p.ready.Store(true)
	if r := askPrepare(h, true); !r.Ok || r.Params["state"] != "ready" {
		t.Fatalf("module loaded: %+v", r)
	}
	if p.launches.Load() != 0 || h.panel.hasEvent("awg_kernel_prepare_started") {
		t.Error("a dry run started something")
	}
}

func TestAwgPrepareStartsOneRunAndAnswersARepeatWithoutStartingAnother(t *testing.T) {
	h, p := newPrep(t, okPrepEnv(), nil)
	h.waitConnected()
	r := askPrepare(h, false)
	if !r.Ok || r.Params["state"] != "started" || r.Params["kernel"] != "6.8.0-142-generic" || r.Params["since_unix"] == "" {
		t.Fatalf("start: %+v", r)
	}
	eventually(t, func() bool { return h.panel.hasEvent("awg_kernel_prepare_started") }, "the started event")
	if e := h.panel.event("awg_kernel_prepare_started"); e.Severity != pb.Severity_SEVERITY_INFO || e.Params["kernel"] != "6.8.0-142-generic" {
		t.Errorf("started event = %+v", e)
	}
	again := askPrepare(h, false)
	if !again.Ok || again.Params["state"] != "running" {
		t.Fatalf("a repeated request while it runs: %+v", again)
	}
	time.Sleep(100 * time.Millisecond)
	if p.launches.Load() != 1 || h.panel.countEvents("awg_kernel_prepare_started") != 1 {
		t.Errorf("launches = %d, started events = %d: a repeat must be a no-op", p.launches.Load(), h.panel.countEvents("awg_kernel_prepare_started"))
	}
}

func TestAwgPrepareRefusesWhatTheHostCannotDo(t *testing.T) {
	for name, c := range map[string]struct {
		mut  func(*awgprep.Env)
		code string
	}{
		"container":      {func(e *awgprep.Env) { e.Container = "lxc" }, "container"},
		"not debian":     {func(e *awgprep.Env) { e.OSRelease = map[string]string{"ID": "alpine"} }, "distro"},
		"secure boot on": {func(e *awgprep.Env) { e.SecureBoot = true }, "secure_boot"},
	} {
		env := okPrepEnv()
		c.mut(&env)
		h, p := newPrep(t, env, nil)
		h.waitConnected()
		for _, dry := range []bool{true, false} {
			r := askPrepare(h, dry)
			if !r.Ok || r.Params["state"] != "unsupported" || r.Params["code"] != c.code || r.Params["reason"] == "" {
				t.Errorf("%s (dry=%v): %+v", name, dry, r)
			}
		}
		if p.launches.Load() != 0 || h.panel.hasEvent("awg_kernel_prepare_started") {
			t.Errorf("%s: something was started", name)
		}
	}
}

// The end of a run becomes one event, with the doctor looking again at the module and the backend; the agent never changes
// its own backend setting (switching is the panel's job, after "done").
func TestAwgPrepareEndsAreEventsAndTheBackendIsLeftAlone(t *testing.T) {
	h, p := newPrep(t, okPrepEnv(), nil)
	h.waitConnected()
	askPrepare(h, false)
	eventually(t, func() bool { return h.panel.hasEvent("awg_kernel_prepare_started") }, "started")

	// the job fails (it writes the file; the agent only reads it)
	writeStatus(t, p.path, awgprep.Status{State: awgprep.StateFailed, Kernel: "6.8.0-142-generic", Started: time.Now().Add(-4 * time.Minute).Unix(),
		Updated: time.Now().Unix(), Finished: time.Now().Unix(), Code: awgprep.CodeStepFailed, Reason: "step 2 of 4 failed: install the PPA tooling"})
	eventually(t, func() bool { return h.panel.hasEvent("awg_kernel_prepare_failed") }, "the failed event")
	e := h.panel.event("awg_kernel_prepare_failed")
	if e.Severity != pb.Severity_SEVERITY_ERROR || e.Params["code"] != "step_failed" || e.Params["reason"] == "" || e.Params["minutes"] != "4" || e.Params["kernel"] != "6.8.0-142-generic" {
		t.Errorf("failed event = %+v", e)
	}
	rep := nextDoctor(t, h)
	if rep.RequestId != "" || !rep.Partial || resultIDs(rep) != "kernel_headers,awg_backend" {
		t.Errorf("the re-check after the end = %+v", rep)
	}
	if h.panel.countEvents("awg_kernel_prepare_done") != 0 {
		t.Error("a done event for a failed run")
	}

	// a retry that succeeds
	askPrepare(h, false)
	writeStatus(t, p.path, awgprep.Status{State: awgprep.StateDone, Kernel: "6.8.0-142-generic", Started: time.Now().Add(-90 * time.Second).Unix(),
		Updated: time.Now().Unix(), Finished: time.Now().Unix()})
	eventually(t, func() bool { return h.panel.hasEvent("awg_kernel_prepare_done") }, "the done event")
	if d := h.panel.event("awg_kernel_prepare_done"); d.Severity != pb.Severity_SEVERITY_INFO || d.Params["minutes"] != "2" {
		t.Errorf("done event = %+v", d)
	}
	time.Sleep(200 * time.Millisecond)
	if h.panel.countEvents("awg_kernel_prepare_failed") != 1 || h.panel.countEvents("awg_kernel_prepare_done") != 1 {
		t.Errorf("each end must be reported once: %v", h.panel.eventCodes())
	}
	if b := h.a.AwgBackend(); b != "" {
		t.Errorf("the agent changed its own AmneziaWG backend setting to %q", b)
	}
}

// A run that ended while the agent was down (or was restarted mid-run) is reported when the agent is back.
func TestAwgPrepareReportsAnEndThatHappenedWhileTheAgentWasDown(t *testing.T) {
	h, _ := newPrep(t, okPrepEnv(), func(path string) {
		_ = awgprep.WriteStatus(path, awgprep.Status{State: awgprep.StateDone, Kernel: "6.8.0-142-generic", Started: 100, Updated: 400, Finished: 400})
	})
	h.waitConnected()
	eventually(t, func() bool { return h.panel.hasEvent("awg_kernel_prepare_done") }, "the done event of the run that ended meanwhile")
}
