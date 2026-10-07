package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/doctor"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// fakeEngine implements engine.Engine in memory. It records what the agent does to it and can emit a
// fixed traffic delta per Collect so tests can check exactly-once delivery.
type fakeEngine struct {
	proto string

	mu        sync.Mutex
	in        map[string]*fakeInbound
	applies   map[string]int // Apply calls per inbound
	removed   []string
	kicked    [][]string
	kickRet   int
	applyErr  map[string]error
	closed    bool
	unhealthy map[string]bool // inbounds whose Health says FAILED
	lateCert  bool            // like ACME: Apply reports no certificate, Health reports leaf once there is one
	leaf      engine.CertInfo

	emit             bool
	upEach, downEach uint64
	emittedUp        uint64 // total handed out by Collect
	emittedDown      uint64
}

type fakeInbound struct {
	spec  plugin.InboundSpec
	creds []plugin.UserCred
}

func newFakeEngine(proto string) *fakeEngine {
	return &fakeEngine{proto: proto, in: map[string]*fakeInbound{}, applies: map[string]int{}, applyErr: map[string]error{}, upEach: 10, downEach: 5}
}

func (e *fakeEngine) factory() engine.Factory {
	return func(engine.Env) (engine.Engine, error) { return e, nil }
}

func (e *fakeEngine) Protocol() string                  { return e.proto }
func (e *fakeEngine) Version() string                   { return "fake v1" }
func (e *fakeEngine) Capabilities() engine.Capabilities { return engine.Capabilities{} }

func (e *fakeEngine) Apply(_ context.Context, spec plugin.InboundSpec, creds []plugin.UserCred) (engine.ApplyReport, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.applyErr[spec.ID]; err != nil {
		return engine.ApplyReport{}, err
	}
	e.applies[spec.ID]++
	restarted := false
	if old := e.in[spec.ID]; old != nil && statehash.Spec(old.spec) != statehash.Spec(spec) {
		restarted = true
	}
	e.in[spec.ID] = &fakeInbound{spec: spec, creds: append([]plugin.UserCred(nil), creds...)}
	cert := engine.CertInfo{PinSHA256: "aa" + spec.ID, NotAfter: time.Unix(2000000000, 0)}
	if e.lateCert {
		cert = engine.CertInfo{}
	}
	return engine.ApplyReport{SpecHash: statehash.Spec(spec), CredCount: len(creds), Restarted: restarted, Cert: cert}, nil
}

func (e *fakeEngine) Remove(_ context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.in, id)
	e.removed = append(e.removed, id)
	return nil
}

func (e *fakeEngine) Collect(context.Context) (engine.Collected, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var c engine.Collected
	if e.emit {
		ids := make([]string, 0, len(e.in))
		for id := range e.in {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if len(e.in[id].creds) == 0 {
				continue
			}
			c.Traffic = append(c.Traffic, plugin.UserTraffic{CredID: e.in[id].creds[0].CredID, InboundID: id, Up: e.upEach, Down: e.downEach})
			e.emittedUp += e.upEach
			e.emittedDown += e.downEach
			c.Sessions = append(c.Sessions, plugin.Session{CredID: e.in[id].creds[0].CredID, InboundID: id,
				Since: time.Unix(1700000000, 0)})
		}
	}
	return c, nil
}

func (e *fakeEngine) Kick(_ context.Context, ids []string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.kicked = append(e.kicked, append([]string(nil), ids...))
	return e.kickRet, nil
}

func (e *fakeEngine) Observed() []statehash.Inbound {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []statehash.Inbound
	for _, in := range e.in {
		if !in.spec.Enabled {
			continue // like a real engine: a disabled inbound holds nothing
		}
		out = append(out, statehash.Inbound{Spec: in.spec, Creds: append([]plugin.UserCred(nil), in.creds...)})
	}
	return out
}

func (e *fakeEngine) Health() []plugin.EngineHealth {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []plugin.EngineHealth
	for id, in := range e.in {
		st := plugin.RunRunning
		if !in.spec.Enabled {
			st = plugin.RunStopped
		}
		if e.unhealthy[id] {
			st = plugin.RunFailed
		}
		out = append(out, plugin.EngineHealth{InboundID: id, State: st, Since: time.Unix(1700000100, 0),
			CertPinSHA256: e.leaf.PinSHA256, CertNotAfter: e.leaf.NotAfter})
	}
	return out
}

func (e *fakeEngine) Close(context.Context) error {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	return nil
}

// --- test accessors

func (e *fakeEngine) credIDs(inbound string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	in := e.in[inbound]
	if in == nil {
		return nil
	}
	var ids []string
	for _, c := range in.creds {
		ids = append(ids, c.CredID)
	}
	sort.Strings(ids)
	return ids
}

func (e *fakeEngine) applyCount(inbound string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.applies[inbound]
}

func (e *fakeEngine) has(inbound string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.in[inbound] != nil
}

func (e *fakeEngine) setEmit(on bool) {
	e.mu.Lock()
	e.emit = on
	e.mu.Unlock()
}

func (e *fakeEngine) emitted() (up, down uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.emittedUp, e.emittedDown
}

// fakeHost implements hostctl.Host without touching the machine.
type fakeHost struct {
	mu        sync.Mutex
	baselines int
	hopCalls  [][]hostctl.Hop
	hopErr    error
	udpCalls  [][]hostctl.UDPInboundPort
	udpErr    error
	ssh       []uint16
	cleaned   bool
}

func (h *fakeHost) Facts(context.Context) hostctl.Facts {
	return hostctl.Facts{Hostname: "de1", OS: "Debian GNU/Linux 12", Kernel: "6.1.0", Arch: "amd64", CPUCount: 4,
		RAMTotal: 1 << 30, DiskTotal: 20 << 30, Virt: "kvm", HasIPv6: true, Boot: time.Unix(1700000000, 0)}
}
func (h *fakeHost) Metrics() hostctl.Metrics {
	return hostctl.Metrics{CPUPct: 12.5, SoftirqPct: 3, Load1: 0.5, RAMUsed: 100, RAMTotal: 200, UptimeS: 99}
}
func (h *fakeHost) ApplyBaseline(context.Context) error {
	h.mu.Lock()
	h.baselines++
	h.mu.Unlock()
	return nil
}
func (h *fakeHost) SetPortHops(_ context.Context, hops []hostctl.Hop) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hopCalls = append(h.hopCalls, append([]hostctl.Hop(nil), hops...))
	return h.hopErr
}
func (h *fakeHost) SyncInboundUDPPorts(_ context.Context, ports []hostctl.UDPInboundPort) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.udpCalls = append(h.udpCalls, append([]hostctl.UDPInboundPort(nil), ports...))
	return h.udpErr
}
func (h *fakeHost) SSHPorts() []uint16 { return h.ssh }
func (h *fakeHost) Cleanup(context.Context) error {
	h.mu.Lock()
	h.cleaned = true
	h.mu.Unlock()
	return nil
}
func (h *fakeHost) hops() [][]hostctl.Hop {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][]hostctl.Hop(nil), h.hopCalls...)
}
func (h *fakeHost) udpPorts() [][]hostctl.UDPInboundPort {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][]hostctl.UDPInboundPort(nil), h.udpCalls...)
}

var errBoom = errors.New("bind: address already in use")

// harness is an enrolled agent running against a fakePanel.
type harness struct {
	t      *testing.T
	panel  *fakePanel
	eng    *fakeEngine
	host   *fakeHost
	a      *Agent
	dir    string
	cancel context.CancelFunc
	done   chan error

	doctorEnv   func(doctor.Env) doctor.Env
	doctorFirst time.Duration
	doctorEvery time.Duration
	cfgMut      func(*Config) // adjusts the agent Config of every (re)built agent
	extra       map[string]engine.Factory
	wrapHost    func(*fakeHost) hostctl.Host
}

type harnessOpts struct {
	certValidity time.Duration
	doctorEnv    func(doctor.Env) doctor.Env // nil = testDoctorEnv
	doctorFirst  time.Duration
	doctorEvery  time.Duration
	tune         func(*Agent) // set timing knobs before Run
	noStart      bool
	cfg          func(*Config) // see harness.cfgMut
	// extra adds engines besides "fake" (the L3 tests add "awg"); wrapHost gives the agent a host with more
	// interfaces (hostctl.TunnelHost) on top of the fake one.
	extra    map[string]engine.Factory
	wrapHost func(*fakeHost) hostctl.Host
}

func newHarness(t *testing.T, o harnessOpts) *harness {
	t.Helper()
	panel := newFakePanel(t)
	if o.certValidity != 0 {
		panel.certValidity = o.certValidity
	}
	h := &harness{t: t, panel: panel, eng: newFakeEngine("fake"), host: &fakeHost{}, dir: t.TempDir() + "/state",
		doctorEnv: o.doctorEnv, doctorFirst: o.doctorFirst, doctorEvery: o.doctorEvery, cfgMut: o.cfg,
		extra: o.extra, wrapHost: o.wrapHost}
	if h.doctorEnv == nil {
		h.doctorEnv = testDoctorEnv(t)
	}
	if _, err := Enroll(context.Background(), EnrollConfig{
		StateDir: h.dir, Panel: panel.addr(), SNI: testSNI, CASHA256: panel.fingerprint(), Token: panel.token,
	}); err != nil {
		t.Fatal(err)
	}
	h.a = h.newAgent()
	if o.tune != nil {
		o.tune(h.a)
	}
	if !o.noStart {
		h.start()
	}
	return h
}

func (h *harness) newAgent() *Agent {
	h.t.Helper()
	cfg := Config{StateDir: h.dir, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), DoctorEnv: h.doctorEnv}
	if h.cfgMut != nil {
		h.cfgMut(&cfg)
	}
	engines := map[string]engine.Factory{"fake": h.eng.factory()}
	for p, f := range h.extra {
		engines[p] = f
	}
	var host hostctl.Host = h.host
	if h.wrapHost != nil {
		host = h.wrapHost(h.host)
	}
	a, err := New(cfg, engines, host)
	if err != nil {
		h.t.Fatal(err)
	}
	a.statsEvery, a.backoffMin, a.backoffMax = 20*time.Millisecond, 20*time.Millisecond, 100*time.Millisecond
	a.sweepEvery, a.renewEvery = 30*time.Millisecond, 50*time.Millisecond
	a.doctorFirst, a.doctorEvery = h.doctorFirst, h.doctorEvery
	return a
}

func (h *harness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.done = make(chan error, 1)
	go func() { h.done <- h.a.Run(ctx) }()
	h.t.Cleanup(func() { h.stop() })
}

// stop cancels the agent and waits for Run to return (idempotent).
func (h *harness) stop() error {
	if h.cancel == nil {
		return nil
	}
	h.cancel()
	h.cancel = nil
	select {
	case err := <-h.done:
		return err
	case <-time.After(10 * time.Second):
		h.t.Fatal("Run did not return")
		return nil
	}
}

func (h *harness) waitConnected() {
	h.t.Helper()
	select {
	case <-h.panel.connects:
	case <-time.After(8 * time.Second):
		h.t.Fatal("agent never connected")
	}
}

// fullState builds a full DesiredState for the fake protocol.
func fullState(rev uint64, inbounds ...*pb.InboundState) *pb.DesiredState {
	return &pb.DesiredState{Revision: rev, Inbounds: inbounds}
}

func inb(id string, hopFrom, hopTo uint32, creds ...*pb.Credential) *pb.InboundState {
	return &pb.InboundState{InboundId: id, CredsReplace: true, Creds: creds, Spec: &pb.InboundSpec{
		InboundId: id, Protocol: "fake", ProfileId: "prf_1", SpecVersion: 1, Enabled: true,
		Listen: &pb.Listen{Network: "udp", Port: 443, HopFrom: hopFrom, HopTo: hopTo},
		Tls:    &pb.Tls{Mode: pb.TlsMode_TLS_MODE_SELF_SIGNED, ServerName: "example.com"}, Egress: "direct",
		SettingsJson: `{"obfs":"x"}`,
	}}
}

func cred(id string) *pb.Credential {
	return &pb.Credential{CredId: id, UserId: "usr_" + id, DeviceId: "dev_" + id, DataJson: `{"auth_sha256":"` + id + `"}`}
}

// expectedHash computes the state hash the way the panel would, from the wire messages applied so far.
func expectedHash(t *testing.T, states ...*pb.DesiredState) string {
	t.Helper()
	m := newModel()
	for _, ds := range states {
		var err error
		if m, err = merge(m, ds, func(string) bool { return true }); err != nil {
			t.Fatal(err)
		}
	}
	return m.hash()
}
