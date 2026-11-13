package agent

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/awg"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/osupdate"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The agent side of "UpdateOs" (agent.proto "OS UPDATES"): the capability, what is refused, the one-job rule, the events of a
// job and of its end, the job in the snapshot, and the return after a reboot. The machine and the unit are fakes: nothing is
// installed, no systemd-run is started.

type osNode struct {
	mu       sync.Mutex
	files    map[string]string
	launches atomic.Int32
	specs    []osupdate.Spec
	launchEr error
	path     string
	excluded atomic.Value // string
}

func newOsNode() *osNode {
	return &osNode{files: map[string]string{
		"/etc/os-release":                 "ID=ubuntu\nVERSION_ID=\"24.04\"\n",
		"/proc/1/comm":                    "systemd\n",
		"/proc/sys/kernel/random/boot_id": "boot-one\n",
		"/proc/sys/kernel/osrelease":      "6.8.0-100-generic\n",
	}}
}

func (n *osNode) host() osupdate.Host {
	return osupdate.Host{ReadFile: func(p string) ([]byte, error) {
		n.mu.Lock()
		defer n.mu.Unlock()
		if s, ok := n.files[p]; ok {
			return []byte(s), nil
		}
		return nil, errors.New("not found")
	}}
}

func (n *osNode) set(path, body string) { n.mu.Lock(); n.files[path] = body; n.mu.Unlock() }

func (n *osNode) controller(stateDir string) *osupdate.Controller {
	n.path = filepath.Join(stateDir, osupdate.StatusFileName)
	return osupdate.New(osupdate.Config{
		StatusPath: n.path, Host: n.host(),
		Launch: func(_ context.Context, s osupdate.Spec) error {
			n.launches.Add(1)
			n.mu.Lock()
			n.specs = append(n.specs, s)
			n.mu.Unlock()
			return n.launchEr
		},
		Excluded: func(context.Context) string {
			if v, ok := n.excluded.Load().(string); ok {
				return v
			}
			return ""
		},
	})
}

// newOsAgent is an agent with the controller on the fake machine; the status file is read every 10 ms.
func newOsAgent(t *testing.T, n *osNode, before func(path string)) *harness {
	t.Helper()
	return newHarness(t, harnessOpts{
		cfg: func(c *Config) {
			c.OSUpdateCapability = func() bool { return true }
			c.OSUpdate = n.controller(c.StateDir)
			if before != nil {
				before(n.path)
			}
		},
		tune: func(a *Agent) { a.osJobWatch = 10 * time.Millisecond; a.osStatusDelay = 0 },
	})
}

func askOs(h *harness, r *pb.UpdateOs) *pb.CommandResult {
	h.t.Helper()
	r.RequestId = "req_os"
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_UpdateOs{UpdateOs: r}})
	res := h.panel.nextCmd()
	if res.RequestId != "req_os" {
		h.t.Fatalf("answer to %q", res.RequestId)
	}
	return res
}

func writeOsStatus(t *testing.T, path string, s osupdate.Status) {
	t.Helper()
	var err error
	for range 200 {
		if err = osupdate.WriteStatus(path, s); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(err)
}

func TestOsUpdateCapabilityNeedsTheControllerAndTheHost(t *testing.T) {
	n := newOsNode()
	h := newOsAgent(t, n, nil)
	h.waitConnected()
	hello := h.panel.current().hello
	if !hasCapability(hello, osupdate.Capability) || !hasCapability(hello, osupdate.CapabilityUpdate) {
		t.Fatalf("capabilities = %v, want os-status/1 and os-update/1", hello.GetCapabilities())
	}
	// A host that cannot run it (not root, no systemd) lists neither.
	h2 := newHarness(t, harnessOpts{cfg: func(c *Config) {
		c.OSUpdateCapability = func() bool { return false }
		c.OSUpdate = newOsNode().controller(c.StateDir)
	}})
	h2.waitConnected()
	if hello := h2.panel.current().hello; hasCapability(hello, osupdate.CapabilityUpdate) || hasCapability(hello, osupdate.Capability) {
		t.Fatalf("capabilities = %v on a host that cannot run OS jobs", hello.GetCapabilities())
	}
}

// An agent build without the controller answers an UpdateOs it should never get with an error and runs nothing.
func TestOsUpdateWithoutAControllerIsRefused(t *testing.T) {
	h := newHarness(t, harnessOpts{cfg: func(c *Config) { c.OSUpdateCapability = func() bool { return true } }})
	h.waitConnected()
	if hello := h.panel.current().hello; hasCapability(hello, osupdate.CapabilityUpdate) {
		t.Fatalf("os-update/1 listed without a controller: %v", hello.GetCapabilities())
	}
	if r := askOs(h, &pb.UpdateOs{Action: "refresh", Reboot: "never"}); r.Ok || r.Error != "unsupported_host" {
		t.Fatalf("answer = %+v", r)
	}
}

func TestOsUpdateRefusesEveryValueOutsideTheVocabulary(t *testing.T) {
	n := newOsNode()
	h := newOsAgent(t, n, nil)
	h.waitConnected()
	for name, r := range map[string]*pb.UpdateOs{
		"empty":               {},
		"unknown action":      {Action: "dist-upgrade", Reboot: "never"},
		"shell in action":     {Action: "refresh; reboot", Reboot: "never"},
		"unknown mode":        {Action: "upgrade", Mode: "dist-upgrade", Reboot: "never"},
		"package as mode":     {Action: "upgrade", Mode: "openssh-server", Reboot: "never"},
		"path as mode":        {Action: "upgrade", Mode: "/bin/sh", Reboot: "never"},
		"no mode":             {Action: "upgrade", Reboot: "never"},
		"mode on refresh":     {Action: "refresh", Mode: "all", Reboot: "never"},
		"mode on reboot":      {Action: "reboot", Mode: "all", Reboot: "always"},
		"reboot value":        {Action: "upgrade", Mode: "all", Reboot: "now"},
		"hyphen spelling":     {Action: "upgrade", Mode: "all", Reboot: "if-required"},
		"always with upgrade": {Action: "upgrade", Mode: "all", Reboot: "always"},
		"refresh with reboot": {Action: "refresh", Reboot: "if_required"},
		"reboot never":        {Action: "reboot", Reboot: "never"},
		"hand-made job id":    {Action: "refresh", Reboot: "never", JobId: "manual-1"},
		"hostile job id":      {Action: "refresh", Reboot: "never", JobId: "osj_a;reboot"},
		"job id with space":   {Action: "refresh", Reboot: "never", JobId: "osj_a b"},
		"job id too long":     {Action: "refresh", Reboot: "never", JobId: "osj_" + string(make([]byte, 60))},
		"job id upper case":   {Action: "refresh", Reboot: "never", JobId: "osj_ABC"},
	} {
		res := askOs(h, r)
		if !res.Ok || res.Params["state"] != "unsupported" || res.Params["code"] != "bad_request" || res.Params["reason"] == "" {
			t.Errorf("%s: %+v", name, res)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if n.launches.Load() != 0 || h.panel.hasEvent("os_update_started") {
		t.Fatalf("a refused request started a unit (launches %d)", n.launches.Load())
	}
}

func TestOsUpdateStartsOneJobAndTheRepeatIsANoOp(t *testing.T) {
	n := newOsNode()
	h := newOsAgent(t, n, nil)
	h.waitConnected()
	r := askOs(h, &pb.UpdateOs{Action: "upgrade", Mode: "security", Reboot: "if_required", JobId: "osj_job1"})
	if !r.Ok || r.Params["state"] != "started" || r.Params["job_id"] != "osj_job1" || r.Params["since_unix"] == "" {
		t.Fatalf("start: %+v", r)
	}
	eventually(t, func() bool { return h.panel.hasEvent("os_update_started") }, "the started event")
	if e := h.panel.event("os_update_started"); e.Severity != pb.Severity_SEVERITY_INFO || e.Params["job_id"] != "osj_job1" || e.Params["action"] != "upgrade" || e.Params["mode"] != "security" {
		t.Errorf("started event = %+v", e)
	}
	again := askOs(h, &pb.UpdateOs{Action: "reboot", Reboot: "always", JobId: "osj_job2"})
	if !again.Ok || again.Params["state"] != "running" || again.Params["job_id"] != "osj_job1" {
		t.Fatalf("a second job while one runs: %+v", again)
	}
	time.Sleep(100 * time.Millisecond)
	if n.launches.Load() != 1 || h.panel.countEvents("os_update_started") != 1 {
		t.Errorf("launches = %d, started events = %d", n.launches.Load(), h.panel.countEvents("os_update_started"))
	}
	n.mu.Lock()
	spec := n.specs[0]
	n.mu.Unlock()
	if spec.Action != "upgrade" || spec.Mode != "security" || spec.Reboot != "if_required" || spec.JobID != "osj_job1" {
		t.Errorf("launched spec = %+v", spec)
	}
}

func TestOsUpdateRefusesWhileOtherHostWorkRuns(t *testing.T) {
	n := newOsNode()
	n.excluded.Store("an agent update or a unit refresh is going")
	h := newOsAgent(t, n, nil)
	h.waitConnected()
	r := askOs(h, &pb.UpdateOs{Action: "refresh", Reboot: "never"})
	if !r.Ok || r.Params["state"] != "unsupported" || r.Params["code"] != "busy" || n.launches.Load() != 0 {
		t.Fatalf("answer = %+v launches = %d", r, n.launches.Load())
	}
	n.excluded.Store("")
	if r := askOs(h, &pb.UpdateOs{Action: "refresh", Reboot: "never"}); r.Params["state"] != "started" {
		t.Fatalf("after the other work ended: %+v", r)
	}
}

func TestOsUpdateRefusesAnUnsupportedSystemAndALaunchFailure(t *testing.T) {
	n := newOsNode()
	n.set("/etc/os-release", "ID=ubuntu\nVERSION_ID=\"20.04\"\n")
	h := newOsAgent(t, n, nil)
	h.waitConnected()
	if r := askOs(h, &pb.UpdateOs{Action: "refresh", Reboot: "never"}); r.Params["state"] != "unsupported" || r.Params["code"] != "unsupported_os" {
		t.Fatalf("answer = %+v", r)
	}
	n2 := newOsNode()
	n2.launchEr = errors.New("systemd-run: boom")
	h2 := newOsAgent(t, n2, nil)
	h2.waitConnected()
	r := askOs(h2, &pb.UpdateOs{Action: "refresh", Reboot: "never"})
	if r.Params["state"] != "unsupported" || r.Params["code"] != "launch_failed" || h2.panel.hasEvent("os_update_started") {
		t.Fatalf("answer = %+v", r)
	}
}

// The end of a job is one event, with the doctor looking again; a second end is a second event, not a repeat of the first.
func TestOsUpdateEndsAreEventsReportedOnce(t *testing.T) {
	n := newOsNode()
	h := newOsAgent(t, n, nil)
	h.waitConnected()
	askOs(h, &pb.UpdateOs{Action: "upgrade", Mode: "security", Reboot: "if_required", JobId: "osj_job1"})
	now := time.Now().Unix()
	writeOsStatus(t, n.path, osupdate.Status{JobID: "osj_job1", Action: "upgrade", Mode: "security", RebootPolicy: "if_required", State: osupdate.StateFailed,
		Started: now - 240, Updated: now, Finished: now, Code: osupdate.CodeAwgModuleMissing, Reason: "reboot refused: the AmneziaWG module is not built",
		Reasons: []string{"dpkg_interrupted", "awg_module_missing"}, Package: "linux-generic", Reboot: osupdate.RebootHeld, HeldReason: osupdate.CodeAwgModuleMissing,
		KernelTo: "6.8.0-110-generic"})
	eventually(t, func() bool { return h.panel.hasEvent("os_update_failed") }, "the failed event")
	e := h.panel.event("os_update_failed")
	if e.Severity != pb.Severity_SEVERITY_ERROR || e.Params["job_id"] != "osj_job1" || e.Params["code"] != "awg_module_missing" || e.Params["package"] != "linux-generic" ||
		e.Params["reasons"] != "dpkg_interrupted,awg_module_missing" || e.Params["reboot"] != "held" || e.Params["reason"] == "" {
		t.Errorf("failed event = %+v", e)
	}
	rep := nextDoctor(t, h)
	if rep.RequestId != "" || !rep.Partial || resultIDs(rep) != "security_updates,kernel_headers,awg_backend,resolver" {
		t.Errorf("the re-check after the end = %+v", rep)
	}
	time.Sleep(200 * time.Millisecond)
	if h.panel.countEvents("os_update_failed") != 1 || h.panel.hasEvent("os_update_done") {
		t.Errorf("each end must be reported once: %v", h.panel.eventCodes())
	}

	askOs(h, &pb.UpdateOs{Action: "refresh", Reboot: "never", JobId: "osj_job2"})
	now = time.Now().Unix()
	writeOsStatus(t, n.path, osupdate.Status{JobID: "osj_job2", Action: "refresh", RebootPolicy: "never", State: osupdate.StateDone, Started: now - 30, Updated: now,
		Finished: now, Reboot: osupdate.RebootNotAsked, Upgraded: 3})
	eventually(t, func() bool { return h.panel.hasEvent("os_update_done") }, "the done event")
	if d := h.panel.event("os_update_done"); d.Severity != pb.Severity_SEVERITY_INFO || d.Params["job_id"] != "osj_job2" || d.Params["upgraded"] != "3" || d.Params["reboot"] != "not_asked" {
		t.Errorf("done event = %+v", d)
	}
}

// A job that ended while the agent was down (or an agent restarted mid-job) is reported when the agent is back; a job that
// rebooted the host is reported on the kernel the host came back with.
func TestOsUpdateReportsAnEndThatHappenedWhileTheAgentWasDown(t *testing.T) {
	now := time.Now().Unix()
	n := newOsNode()
	h := newOsAgent(t, n, func(path string) {
		_ = osupdate.WriteStatus(path, osupdate.Status{JobID: "osj_old", Action: "refresh", RebootPolicy: "never", State: osupdate.StateDone, Started: now - 100, Updated: now - 50, Finished: now - 50})
	})
	h.waitConnected()
	eventually(t, func() bool { return h.panel.hasEvent("os_update_done") }, "the done event of the job that ended meanwhile")
}

func TestOsUpdateReportsTheReturnAfterTheReboot(t *testing.T) {
	now := time.Now().Unix()
	n := newOsNode() // the host is on boot-two now, on the kernel the job installed
	n.set("/proc/sys/kernel/random/boot_id", "boot-two\n")
	n.set("/proc/sys/kernel/osrelease", "6.8.0-110-generic\n")
	h := newOsAgent(t, n, func(path string) {
		_ = osupdate.WriteStatus(path, osupdate.Status{JobID: "osj_rb", Action: "upgrade", Mode: "security", RebootPolicy: "if_required", State: osupdate.StateRunning,
			Phase: osupdate.PhaseRebooting, Started: now - 900, Updated: now - 5, KernelFrom: "6.8.0-100-generic", KernelTo: "6.8.0-110-generic", BootID: "boot-one"})
	})
	h.waitConnected()
	eventually(t, func() bool { return h.panel.hasEvent("os_update_done") }, "the done event after the reboot")
	d := h.panel.event("os_update_done")
	if d.Params["kernel"] != "6.8.0-110-generic" || d.Params["reboot"] != "done" || d.Params["job_id"] != "osj_rb" {
		t.Errorf("done event = %+v", d)
	}

	// The other way: the host came back on the old kernel.
	n2 := newOsNode()
	n2.set("/proc/sys/kernel/random/boot_id", "boot-two\n")
	h2 := newOsAgent(t, n2, func(path string) {
		_ = osupdate.WriteStatus(path, osupdate.Status{JobID: "osj_rb", Action: "reboot", RebootPolicy: "if_required", State: osupdate.StateRunning,
			Phase: osupdate.PhaseRebooting, Started: now - 900, Updated: now - 5, KernelFrom: "6.8.0-100-generic", KernelTo: "6.8.0-110-generic", BootID: "boot-one"})
	})
	h2.waitConnected()
	eventually(t, func() bool { return h2.panel.hasEvent("os_update_failed") }, "the failed event")
	if e := h2.panel.event("os_update_failed"); e.Params["code"] != "kernel_not_switched" || e.Params["reboot"] != "done" {
		t.Errorf("failed event = %+v", e)
	}
}

func TestOsRebootStartedIsAnEventOnce(t *testing.T) {
	n := newOsNode()
	h := newOsAgent(t, n, nil)
	h.waitConnected()
	askOs(h, &pb.UpdateOs{Action: "reboot", Reboot: "if_required", JobId: "osj_r1"})
	now := time.Now().Unix()
	st := osupdate.Status{JobID: "osj_r1", Action: "reboot", RebootPolicy: "if_required", State: osupdate.StateRunning, Phase: osupdate.PhaseRebooting,
		Started: now, Updated: now, KernelFrom: "6.8.0-100-generic", KernelTo: "6.8.0-110-generic", BootID: "boot-one"}
	writeOsStatus(t, n.path, st)
	eventually(t, func() bool { return h.panel.hasEvent("os_reboot_started") }, "the reboot event")
	st.Updated++
	writeOsStatus(t, n.path, st)
	time.Sleep(200 * time.Millisecond)
	if e := h.panel.event("os_reboot_started"); h.panel.countEvents("os_reboot_started") != 1 || e.Params["kernel_to"] != "6.8.0-110-generic" || e.Params["kernel_from"] != "6.8.0-100-generic" {
		t.Errorf("reboot events = %v (%+v)", h.panel.eventCodes(), e)
	}
}

// The job rides in the snapshot only while the panel asked for snapshots; an old panel (os_status false) hears nothing.
func TestOsUpdateJobRidesInTheSnapshotOnlyWhenAsked(t *testing.T) {
	n := newOsNode()
	var simulations atomic.Int32
	h := newHarness(t, harnessOpts{
		cfg: func(c *Config) {
			c.OSUpdateReader = osStatusFixtureReader(t, &simulations)
			c.OSUpdateCapability = func() bool { return true }
			c.OSUpdate = n.controller(c.StateDir)
		},
		tune: func(a *Agent) { a.osJobWatch = 10 * time.Millisecond; a.osStatusDelay = 0 },
	})
	h.waitConnected()
	askOs(h, &pb.UpdateOs{Action: "upgrade", Mode: "security", Reboot: "never", JobId: "osj_s1"})
	if s := nextOsStatus(h, 400*time.Millisecond); s != nil {
		t.Fatalf("a snapshot was sent to a panel that did not ask: %+v", s)
	}

	asked := &pb.NodeSettings{StatsIntervalS: 10, DnsResolvers: []string{"192.0.2.53"}, OsStatus: true}
	h.panel.settings.Store(asked)
	h.panel.push(&pb.DesiredState{Revision: 1, Settings: asked})
	var withJob *pb.OsStatus
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
		if s := nextOsStatus(h, 500*time.Millisecond); s != nil && s.GetJob() != nil {
			withJob = s
			break
		}
	}
	if withJob == nil || withJob.Job.JobId != "osj_s1" || withJob.Job.State != "running" || withJob.Job.Action != "upgrade" || withJob.Job.Mode != "security" {
		t.Fatalf("snapshot with the running job = %+v", withJob)
	}

	now := time.Now().Unix()
	writeOsStatus(t, n.path, osupdate.Status{JobID: "osj_s1", Action: "upgrade", Mode: "security", RebootPolicy: "never", State: osupdate.StateDone, Started: now - 60,
		Updated: now, Finished: now, Reboot: osupdate.RebootNotAsked, Upgraded: 2})
	var final *pb.OsStatus
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
		if s := nextOsStatus(h, 500*time.Millisecond); s != nil && s.GetJob().GetState() == "done" {
			final = s
			break
		}
	}
	if final == nil || final.Job.Upgraded != 2 || final.Job.Reboot != "not_asked" {
		t.Fatalf("snapshot with the finished job = %+v", final)
	}
}

// While an OS job runs, the agent does not replace its own binary (the job may be in the middle of dpkg or about to reboot).
func TestOsUpdateBlocksAnAgentUpdateWhileItRuns(t *testing.T) {
	n := newOsNode()
	h := newOsAgent(t, n, nil)
	h.waitConnected()
	askOs(h, &pb.UpdateOs{Action: "refresh", Reboot: "never", JobId: "osj_u1"})
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_UpdateAgent{UpdateAgent: &pb.UpdateAgent{RequestId: "req_up"}}})
	r := h.panel.nextCmd()
	if r.RequestId != "req_up" || r.Ok || r.Error != "busy" {
		t.Fatalf("UpdateAgent during an OS job = %+v", r)
	}
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_RollbackAgent{RollbackAgent: &pb.RollbackAgent{RequestId: "req_rb"}}})
	if r := h.panel.nextCmd(); r.RequestId != "req_rb" || r.Error != "busy" {
		t.Fatalf("RollbackAgent during an OS job = %+v", r)
	}
}

// AwgRequired, the main protection of a command that comes from the panel, is taken from the backend the AmneziaWG engine really
// runs on: the kernel module with AmneziaWG inbounds on this node means a reboot must not leave the module behind. The panel
// does not send it and cannot switch it off.
func TestOsUpdateAwgRequiredComesFromTheRealBackend(t *testing.T) {
	cases := map[string]struct {
		backend string // the engine's backend: kernel | userspace
		inbound bool
		want    bool
	}{
		"kernel module with inbounds":    {"kernel", true, true},
		"userspace with inbounds":        {"userspace", true, false},
		"kernel module without inbounds": {"kernel", false, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			n := newOsNode()
			engineAwg := newFakeAwg(&order{})
			engineAwg.status = awg.BackendStatus{Mode: "auto", Name: c.backend, Available: true}
			h := newHarness(t, harnessOpts{
				extra: map[string]engine.Factory{awg.Protocol: engineAwg.factory()},
				cfg: func(c *Config) {
					c.OSUpdateCapability = func() bool { return true }
					c.OSUpdate = n.controller(c.StateDir)
				},
				tune: func(a *Agent) { a.osJobWatch = 10 * time.Millisecond },
			})
			h.waitConnected()
			if c.inbound {
				m := newModel()
				m.inbounds["inb_awg"] = &inbound{spec: plugin.InboundSpec{ID: "inb_awg", Protocol: awg.Protocol, Enabled: true}}
				h.a.setModel(m)
			}
			r := askOs(h, &pb.UpdateOs{Action: "upgrade", Mode: "security", Reboot: "if_required", JobId: "osj_awg1"})
			if !r.Ok || r.Params["state"] != "started" {
				t.Fatalf("answer = %+v", r)
			}
			n.mu.Lock()
			spec := n.specs[0]
			n.mu.Unlock()
			if spec.AwgRequired != c.want {
				t.Fatalf("AwgRequired = %v, want %v", spec.AwgRequired, c.want)
			}
			st, ok, _ := osupdate.ReadStatus(n.path)
			if !ok || st.AwgRequired != c.want {
				t.Fatalf("status file AwgRequired = %v (ok %v), want %v", st.AwgRequired, ok, c.want)
			}
		})
	}
}

// A panel that asks twice with the same job id (after a reconnect) gets the job's result and starts nothing: a "reboot always"
// is not a second reboot.
func TestOsUpdateRepeatedJobIdIsAnsweredWithTheResult(t *testing.T) {
	n := newOsNode()
	h := newOsAgent(t, n, nil)
	h.waitConnected()
	req := func() *pb.UpdateOs { return &pb.UpdateOs{Action: "reboot", Reboot: "always", JobId: "osj_rb1"} }
	if r := askOs(h, req()); r.Params["state"] != "started" {
		t.Fatalf("first: %+v", r)
	}
	now := time.Now().Unix()
	writeOsStatus(t, n.path, osupdate.Status{JobID: "osj_rb1", Action: "reboot", RebootPolicy: "always", State: osupdate.StateFailed, Started: now - 30, Updated: now,
		Finished: now, Code: osupdate.CodeTooSoon, Reason: "the host booted 3 minutes ago", Reboot: osupdate.RebootHeld, HeldReason: osupdate.CodeTooSoon})
	eventually(t, func() bool { return h.panel.hasEvent("os_update_failed") }, "the failed event")
	r := askOs(h, req())
	if !r.Ok || r.Params["state"] != "finished" || r.Params["result"] != "failed" || r.Params["code"] != "too_soon" || r.Params["job_id"] != "osj_rb1" {
		t.Fatalf("repeated: %+v", r)
	}
	time.Sleep(100 * time.Millisecond)
	if n.launches.Load() != 1 || h.panel.countEvents("os_update_started") != 1 {
		t.Fatalf("launches = %d, started events = %d", n.launches.Load(), h.panel.countEvents("os_update_started"))
	}
}

// When the host came back and the engines are still settling, the reboot is over: os_reboot_started is not raised again.
func TestOsRebootStartedIsNotRepeatedAfterTheReturn(t *testing.T) {
	now := time.Now().Unix()
	n := newOsNode()
	n.set("/proc/sys/kernel/random/boot_id", "boot-two\n")
	n.set("/proc/sys/kernel/osrelease", "6.8.0-110-generic\n")
	h := newOsAgent(t, n, func(path string) {
		_ = osupdate.WriteStatus(path, osupdate.Status{JobID: "osj_rb", Action: "upgrade", Mode: "security", RebootPolicy: "if_required", State: osupdate.StateRunning,
			Phase: osupdate.PhaseRebooting, Started: now - 900, Updated: now - 5, KernelFrom: "6.8.0-100-generic", KernelTo: "6.8.0-110-generic", BootID: "boot-one"})
	})
	h.waitConnected()
	eventually(t, func() bool { return h.panel.hasEvent("os_update_done") }, "done")
	time.Sleep(200 * time.Millisecond)
	if h.panel.countEvents("os_reboot_started") > 0 {
		t.Fatalf("os_reboot_started raised again after the host came back: %v", h.panel.eventCodes())
	}

	// Still settling (Back set, not finished): the same.
	n2 := newOsNode()
	n2.set("/proc/sys/kernel/random/boot_id", "boot-two\n")
	n2.set("/proc/sys/kernel/osrelease", "6.8.0-110-generic\n")
	h2 := newOsAgent(t, n2, func(path string) {
		_ = osupdate.WriteStatus(path, osupdate.Status{JobID: "osj_rb", Action: "reboot", RebootPolicy: "if_required", State: osupdate.StateRunning, Phase: osupdate.PhaseRebooting,
			Started: now - 900, Updated: now - 5, Back: now - 20, KernelFrom: "6.8.0-100-generic", KernelTo: "6.8.0-110-generic", BootID: "boot-one"})
	})
	h2.waitConnected()
	time.Sleep(300 * time.Millisecond)
	if h2.panel.countEvents("os_reboot_started") > 0 {
		t.Fatalf("os_reboot_started raised for a host that is already back: %v", h2.panel.eventCodes())
	}
}

// A failing build of the AmneziaWG module reaches the panel as a code, the package and the kernel in the sentence.
func TestOsUpdateFailedEventCarriesTheModuleBuildFailure(t *testing.T) {
	n := newOsNode()
	h := newOsAgent(t, n, nil)
	h.waitConnected()
	askOs(h, &pb.UpdateOs{Action: "upgrade", Mode: "security", Reboot: "if_required", JobId: "osj_bf"})
	now := time.Now().Unix()
	writeOsStatus(t, n.path, osupdate.Status{JobID: "osj_bf", Action: "upgrade", Mode: "security", RebootPolicy: "if_required", State: osupdate.StateFailed, Started: now - 90,
		Updated: now, Finished: now, Code: osupdate.CodeAwgBuildFailed, Package: "linux-headers-7.0.0-38-generic", Reboot: osupdate.RebootHeld, HeldReason: osupdate.CodeAwgBuildFailed,
		Reason: "the AmneziaWG module does not build for kernel 7.0.0-38-generic; nothing was removed and nothing more was installed"})
	eventually(t, func() bool { return h.panel.hasEvent("os_update_failed") }, "the failed event")
	e := h.panel.event("os_update_failed")
	if e.Params["code"] != "awg_module_build_failed" || e.Params["package"] != "linux-headers-7.0.0-38-generic" || e.Params["reboot"] != "held" {
		t.Fatalf("event = %+v", e)
	}
}
