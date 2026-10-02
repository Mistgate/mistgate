package agent

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/awg"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/node/warp"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// The L3 tests: the agent against fake AWG engine, fake WARP manager and a fake tunnel
// host, through the real stream to the fake panel. Nothing here touches the machine.

// order records the calls of the fakes in one sequence, so a test can assert "the firewall before the interface".
type order struct {
	mu  sync.Mutex
	log []string
}

func (o *order) add(s string) {
	o.mu.Lock()
	o.log = append(o.log, s)
	o.mu.Unlock()
}

func (o *order) all() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.log...)
}

func (o *order) index(s string) int {
	for i, x := range o.all() {
		if x == s {
			return i
		}
	}
	return -1
}

// fakeAwg is an awg engine: the fake engine plus the optional interfaces the agent looks for.
type fakeAwg struct {
	*fakeEngine
	ord *order

	hmu         sync.Mutex
	health      []awg.InboundHealth
	status      awg.BackendStatus
	lastBackend string
	resets      int
}

func newFakeAwg(ord *order) *fakeAwg {
	return &fakeAwg{fakeEngine: newFakeEngine(awg.Protocol), ord: ord,
		status: awg.BackendStatus{Mode: "auto", Name: "userspace", Version: "amneziawg-go v3.1.20260828", Available: true}}
}

func (e *fakeAwg) factory() engine.Factory {
	return func(engine.Env) (engine.Engine, error) { return e, nil }
}

func (e *fakeAwg) Apply(ctx context.Context, spec plugin.InboundSpec, creds []plugin.UserCred) (engine.ApplyReport, error) {
	rep, err := e.fakeEngine.Apply(ctx, spec, creds)
	if err == nil {
		e.ord.add("awg.apply:" + spec.ID)
	}
	return rep, err
}

func (e *fakeAwg) Remove(ctx context.Context, id string) error {
	e.ord.add("awg.remove:" + id)
	return e.fakeEngine.Remove(ctx, id)
}

func (e *fakeAwg) AwgHealth() []awg.InboundHealth {
	e.hmu.Lock()
	defer e.hmu.Unlock()
	return append([]awg.InboundHealth(nil), e.health...)
}

func (e *fakeAwg) setHealth(h ...awg.InboundHealth) {
	e.hmu.Lock()
	e.health = h
	e.hmu.Unlock()
}

func (e *fakeAwg) BackendStatus() awg.BackendStatus {
	e.hmu.Lock()
	defer e.hmu.Unlock()
	return e.status
}

// NodeSettings is the SettingsAware hook: a changed awg_backend drops everything (reset), the first sight only notes it.
func (e *fakeAwg) NodeSettings(_ context.Context, st *pb.NodeSettings) bool {
	e.hmu.Lock()
	defer e.hmu.Unlock()
	prev := e.lastBackend
	e.lastBackend = st.AwgBackend
	if prev != "" && prev != st.AwgBackend {
		e.resets++
		return true
	}
	return false
}

// fakeWarp is the WARP manager.
type fakeWarp struct {
	ord *order

	mu       sync.Mutex
	spec     *plugin.WarpSpec
	applied  int
	applyErr error
	routes   []netip.Prefix
	routeErr error
	cleaned  bool
	health   *warp.Health
	findings []warp.Finding
	ran      chan struct{}
}

func newFakeWarp(ord *order) *fakeWarp { return &fakeWarp{ord: ord, ran: make(chan struct{}, 1)} }

func (w *fakeWarp) Apply(_ context.Context, spec *plugin.WarpSpec) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.applied++
	if spec == nil {
		w.spec = nil
	} else {
		cp := *spec
		w.spec = &cp
	}
	w.ord.add("warp.apply")
	return w.applyErr
}

func (w *fakeWarp) SetRoutedSubnets(_ context.Context, ps []netip.Prefix) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.routeErr != nil {
		w.ord.add("warp.routes:error")
		return w.routeErr
	}
	w.routes = append([]netip.Prefix(nil), ps...)
	var s []string
	for _, p := range ps {
		s = append(s, p.String())
	}
	w.ord.add("warp.routes:" + strings.Join(s, ","))
	return nil
}

func (w *fakeWarp) Configured() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.spec != nil
}

func (w *fakeWarp) Health() (warp.Health, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.health == nil {
		return warp.Health{}, false
	}
	return *w.health, true
}

func (w *fakeWarp) Preflight(context.Context) []warp.Finding {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.findings
}

func (w *fakeWarp) Run(ctx context.Context) {
	select {
	case w.ran <- struct{}{}:
	default:
	}
	<-ctx.Done()
}

func (w *fakeWarp) Cleanup(context.Context) error {
	w.mu.Lock()
	w.cleaned = true
	w.spec = nil
	w.mu.Unlock()
	return nil
}

func (w *fakeWarp) applyCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.applied
}

func (w *fakeWarp) lastSpec() *plugin.WarpSpec {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.spec == nil {
		return nil
	}
	cp := *w.spec
	return &cp
}

// tunHost is the fake host plus hostctl.TunnelHost.
type tunHost struct {
	*fakeHost
	ord *order

	tmu      sync.Mutex
	sets     [][]hostctl.Tunnel
	err      error
	counters map[uint16]uint64
}

func (h *tunHost) SetTunnels(_ context.Context, ts []hostctl.Tunnel) error {
	h.tmu.Lock()
	defer h.tmu.Unlock()
	if h.err != nil {
		h.ord.add("tunnels:error")
		return h.err
	}
	h.sets = append(h.sets, append([]hostctl.Tunnel(nil), ts...))
	h.ord.add("tunnels")
	return nil
}

func (h *tunHost) TunnelCounters(context.Context) (map[uint16]uint64, error) {
	h.tmu.Lock()
	defer h.tmu.Unlock()
	out := map[uint16]uint64{}
	for k, v := range h.counters {
		out[k] = v
	}
	return out, nil
}

func (h *tunHost) lastSet() []hostctl.Tunnel {
	h.tmu.Lock()
	defer h.tmu.Unlock()
	if len(h.sets) == 0 {
		return nil
	}
	return h.sets[len(h.sets)-1]
}

func (h *tunHost) setCount() int {
	h.tmu.Lock()
	defer h.tmu.Unlock()
	return len(h.sets)
}

func (h *tunHost) setErr(err error) {
	h.tmu.Lock()
	h.err = err
	h.tmu.Unlock()
}

// l3 is a harness with the awg engine, the WARP manager and the tunnel host wired in.
type l3 struct {
	*harness
	ord  *order
	awg  *fakeAwg
	warp *fakeWarp
	tun  *tunHost
}

func newL3(t *testing.T, unitGen int, withWarp bool) *l3 {
	t.Helper()
	x := &l3{ord: &order{}}
	x.awg = newFakeAwg(x.ord)
	x.warp = newFakeWarp(x.ord)
	x.harness = newHarness(t, harnessOpts{
		extra: map[string]engine.Factory{awg.Protocol: x.awg.factory()},
		wrapHost: func(f *fakeHost) hostctl.Host {
			x.tun = &tunHost{fakeHost: f, ord: x.ord, counters: map[uint16]uint64{}}
			return x.tun
		},
		cfg: func(c *Config) {
			c.UnitGen = unitGen
			if withWarp {
				c.Warp = x.warp
			}
		},
	})
	return x
}

func awgInb(id string, port uint32, subnet, egress string, creds ...*pb.Credential) *pb.InboundState {
	return &pb.InboundState{InboundId: id, CredsReplace: true, Creds: creds, Spec: &pb.InboundSpec{
		InboundId: id, Protocol: awg.Protocol, ProfileId: "prf_awg", SpecVersion: 1, Enabled: true,
		Listen: &pb.Listen{Network: "udp", Port: port}, Egress: egress,
		SettingsJson: `{"version":"3.1"}`,
		Tunnel:       &pb.Tunnel{AddrV4: subnet + ".1/22", AddrV6: "fd66:66:0:1::1/64", Mtu: 1280},
	}}
}

func awgCred(id string) *pb.Credential {
	return &pb.Credential{CredId: id, UserId: "usr_" + id, DeviceId: "dev_" + id,
		DataJson: `{"public_key":"AAAA","allowed_ips":["10.66.4.5/32"],"psk":"BBBB"}`}
}

func warpSpecPB(enabled bool) *pb.WarpSpec {
	return &pb.WarpSpec{Enabled: enabled, PrivateKey: "cHJpdmF0ZQ==", PeerPublicKey: "cGVlcg==", EndpointV4: "203.0.113.10",
		Ports: []uint32{2408, 500, 1701, 4500}, AddressV4: "172.16.0.2/32", AddressV6: "fd00:16::2/128", Mtu: 1280,
		Reserved: []byte{1, 2, 3}, Backend: "auto"}
}

func mustApply(t *testing.T, p *fakePanel, ds *pb.DesiredState) *pb.ApplyResult {
	t.Helper()
	p.push(ds)
	r := p.nextApply()
	if r.Revision != ds.Revision {
		t.Fatalf("answered revision %d, pushed %d", r.Revision, ds.Revision)
	}
	return r
}

func TestL3CapabilitiesAreListedOnlyWhenTheBuildHasThem(t *testing.T) {
	caps := func(h *harness) map[string]bool {
		select {
		case hello := <-h.panel.hellos:
			m := map[string]bool{}
			for _, c := range hello.Capabilities {
				m[c] = true
			}
			return m
		case <-time.After(8 * time.Second):
			t.Fatal("no Hello")
			return nil
		}
	}
	full := caps(newL3(t, 3, true).harness)
	for _, c := range []string{"doctor/1", "awg/1", "warp/1", "unit/3"} {
		if !full[c] {
			t.Errorf("a full build does not list %q: %v", c, full)
		}
	}
	// An engine-less, manager-less build on a generation 2 unit lists none of the new strings.
	plain := caps(newHarness(t, harnessOpts{cfg: func(c *Config) { c.UnitGen = 2 }}))
	for _, c := range []string{"awg/1", "warp/1", "unit/3"} {
		if plain[c] {
			t.Errorf("a build without the feature lists %q", c)
		}
	}
	// AWG but no WARP manager and a generation 2 unit.
	mid := caps(newL3(t, 2, false).harness)
	if !mid["awg/1"] || mid["warp/1"] || mid["unit/3"] {
		t.Errorf("awg without warp on a gen 2 unit: %v", mid)
	}
}

func TestAwgInboundFirewallThenInterfaceAndHealth(t *testing.T) {
	x := newL3(t, 3, true)
	x.waitConnected()
	ds := fullState(1, awgInb("inb_a", 51842, "10.66.4", "direct", awgCred("c1")))
	r := mustApply(t, x.panel, ds)
	if r.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || len(r.Inbounds) != 1 || r.Inbounds[0].State != pb.InboundRunState_INBOUND_RUN_STATE_RUNNING {
		t.Fatalf("apply = %v", r)
	}
	if want := expectedHash(t, ds); r.StateHash != want {
		t.Errorf("observed hash %s, the panel computes %s", r.StateHash, want)
	}
	// The host got exactly this tunnel, before the interface existed.
	got := x.tun.lastSet()
	if len(got) != 1 {
		t.Fatalf("tunnels = %+v", got)
	}
	tn := got[0]
	if tn.Iface != "mgawg51842" || tn.Subnet4 != netip.MustParsePrefix("10.66.4.0/22") || tn.Addr4 != netip.MustParseAddr("10.66.4.1") ||
		tn.Subnet6 != netip.MustParsePrefix("fd66:66:0:1::/64") || tn.Addr6 != netip.MustParseAddr("fd66:66:0:1::1") || tn.UDPPort != 51842 || tn.ViaWarp {
		t.Errorf("tunnel = %+v", tn)
	}
	if o := x.ord.all(); len(o) < 2 || x.ord.index("tunnels") > x.ord.index("awg.apply:inb_a") || x.ord.index("tunnels") < 0 {
		t.Errorf("the firewall must precede the interface: %v", o)
	}
	// Health: the awg block of the batch, the UDP counter by the inbound's port.
	x.tun.tmu.Lock()
	x.tun.counters[51842] = 77
	x.tun.tmu.Unlock()
	x.awg.setHealth(awg.InboundHealth{InboundID: "inb_a", Backend: "userspace", BackendVersion: "amneziawg-go v3.1.20260828", IfaceUp: true,
		Peers: 3, PeersHandshaken: 2, PeersOnline: 1, NewestHandshakeUnix: 1700000500, UnknownPeerEvents: 4})
	var ah *pb.AwgHealth
	eventually(t, func() bool {
		st := x.panel.statsSeen()
		if st == nil {
			return false
		}
		for _, h := range st.Health {
			if h.InboundId == "inb_a" && h.Awg != nil && h.Awg.UdpRxPackets == 77 {
				ah = h.Awg
				return true
			}
		}
		return false
	}, "a stats batch with the awg health")
	if ah.Backend != "userspace" || !ah.IfaceUp || ah.Peers != 3 || ah.PeersHandshaken != 2 || ah.PeersOnline != 1 || ah.UnknownPeerEvents != 4 ||
		ah.NewestHandshakeUnix < 1700000500-2 || ah.NewestHandshakeUnix > 1700000500+2 || !strings.HasPrefix(ah.BackendVersion, "amneziawg-go") {
		t.Errorf("awg health = %v", ah)
	}
	// A hysteria2-style inbound next to it carries no awg block.
	for _, h := range x.panel.statsSeen().Health {
		if h.InboundId != "inb_a" && h.Awg != nil {
			t.Errorf("awg block on %s", h.InboundId)
		}
	}
}

func TestStateHashCoversTunnelAndWarp(t *testing.T) {
	x := newL3(t, 3, true)
	x.waitConnected()
	ds := fullState(1, awgInb("inb_a", 51842, "10.66.4", "direct", awgCred("c1")))
	ds.Warp = warpSpecPB(true)
	r := mustApply(t, x.panel, ds)
	if want := expectedHash(t, ds); r.StateHash != want {
		t.Fatalf("observed %s, panel %s", r.StateHash, want)
	}
	// The hash of the same inbound without WARP differs: the WARP line is part of the state.
	noWarp := fullState(1, awgInb("inb_a", 51842, "10.66.4", "direct", awgCred("c1")))
	if expectedHash(t, noWarp) == r.StateHash {
		t.Error("the WARP configuration is not in the state hash")
	}
}

// A new agent under a panel from before the tunnel protocols: the state it is sent has no tunnel and no WARP message, and its hash is the
// hash that panel computes (statehash.State, the formula that existed before the L3 additions), byte for byte.
func TestNewAgentWithAnOldPanelKeepsTheOldHash(t *testing.T) {
	x := newL3(t, 3, true)
	x.waitConnected()
	ds := fullState(1, inb("inb_h", 20000, 29999, cred("c1")))
	r := mustApply(t, x.panel, ds)
	old := statehash.State([]statehash.Inbound{{
		Spec: plugin.InboundSpec{ID: "inb_h", Protocol: "fake", ProfileID: "prf_1", Version: 1, Enabled: true,
			Listen: plugin.Listen{Network: "udp", Port: 443, HopFrom: 20000, HopTo: 29999},
			TLS:    plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: "example.com"}, Egress: "direct", Settings: []byte(`{"obfs":"x"}`)},
		Creds: []plugin.UserCred{{CredID: "c1", UserID: "usr_c1", DeviceID: "dev_c1", Data: []byte(`{"auth_sha256":"c1"}`)}},
	}})
	if r.StateHash != old {
		t.Errorf("a state without L3 hashes as %s, the pre-L3 formula says %s", r.StateHash, old)
	}
	// WARP is told on every reconcile (syncWarp), and a sweep tick may add one on a loaded machine: at least once, always with nothing.
	if n := x.warp.applyCount(); x.tun.setCount() != 0 || n < 1 || x.warp.lastSpec() != nil {
		t.Errorf("an old panel's state touched the L3 host side: tunnel sets=%d warp applies=%d spec=%v", x.tun.setCount(), n, x.warp.lastSpec())
	}
}

func TestWarpSpecLifecycleAndPersistence(t *testing.T) {
	x := newL3(t, 3, true)
	x.waitConnected()
	full := fullState(1, inb("inb_h", 0, 0, cred("c1")))
	full.Warp = warpSpecPB(true)
	r := mustApply(t, x.panel, full)
	if r.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || r.StateHash != expectedHash(t, full) {
		t.Fatalf("apply = %v", r)
	}
	sp := x.warp.lastSpec()
	if sp == nil || !sp.Enabled || sp.PrivateKey != "cHJpdmF0ZQ==" || sp.EndpointV4 != "203.0.113.10" || len(sp.Ports) != 4 || sp.Ports[1] != 500 ||
		string(sp.Reserved) != "\x01\x02\x03" || sp.MTU != 1280 || sp.AddressV6 != "fd00:16::2/128" || sp.Backend != "auto" {
		t.Fatalf("manager got %+v", sp)
	}

	// A delta without the WARP message keeps it (absence in a delta = unchanged).
	delta := &pb.DesiredState{Revision: 2, BaseRevision: 1, Inbounds: []*pb.InboundState{{InboundId: "inb_h", Creds: []*pb.Credential{cred("c2")}}}}
	r = mustApply(t, x.panel, delta)
	if r.StateHash != expectedHash(t, full, delta) || x.warp.lastSpec() == nil {
		t.Fatalf("a delta without warp changed it: spec=%v hash=%s", x.warp.lastSpec(), r.StateHash)
	}

	// A delta with the message replaces the whole configuration; enabled=false is a pause, not a removal.
	pause := &pb.DesiredState{Revision: 3, BaseRevision: 2, Warp: warpSpecPB(false)}
	r = mustApply(t, x.panel, pause)
	if sp := x.warp.lastSpec(); sp == nil || sp.Enabled {
		t.Fatalf("paused spec = %+v", sp)
	}
	if r.StateHash != expectedHash(t, full, delta, pause) {
		t.Error("hash after the pause")
	}

	// It survives an agent restart: the persisted state brings it back before the panel says anything.
	if err := x.stop(); err != nil {
		t.Fatal(err)
	}
	x.warp.mu.Lock()
	x.warp.spec = nil
	x.warp.mu.Unlock()
	x.agentRestart()
	x.waitConnected()
	eventually(t, func() bool { sp := x.warp.lastSpec(); return sp != nil && !sp.Enabled }, "the paused spec restored from disk")

	// A full state without it says "this node has no WARP".
	r = mustApply(t, x.panel, fullState(10, inb("inb_h", 0, 0, cred("c1"))))
	if x.warp.lastSpec() != nil {
		t.Errorf("a full state without warp left the manager with %+v", x.warp.lastSpec())
	}
}

// agentRestart builds and starts a new agent over the same state directory (a process restart).
func (x *l3) agentRestart() {
	x.a = x.newAgent()
	x.start()
}

func TestEgressWarpWithoutWarpIsNeverStarted(t *testing.T) {
	x := newL3(t, 3, true)
	x.waitConnected()
	viaWarp := func() *pb.InboundState {
		in := inb("inb_w", 0, 0, cred("c1"))
		in.Spec.Egress = "warp"
		return in
	}
	r := mustApply(t, x.panel, fullState(1, viaWarp()))
	if r.Status != pb.ApplyStatus_APPLY_STATUS_PARTIAL || r.Inbounds[0].Error != "egress warp: not configured on this node" ||
		r.Inbounds[0].State != pb.InboundRunState_INBOUND_RUN_STATE_FAILED {
		t.Fatalf("apply = %v", r)
	}
	if x.eng.has("inb_w") || x.eng.applyCount("inb_w") != 0 {
		t.Fatal("an inbound with egress warp started on a node without WARP")
	}

	// The account arrives: the inbound starts.
	ds := fullState(2, viaWarp())
	ds.Warp = warpSpecPB(true)
	r = mustApply(t, x.panel, ds)
	if r.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || !x.eng.has("inb_w") {
		t.Fatalf("with WARP: %v", r)
	}

	// The account is deleted (a full state without it): the running inbound is stopped, not left to leave directly.
	r = mustApply(t, x.panel, fullState(3, viaWarp()))
	if x.eng.has("inb_w") || r.Inbounds[0].Error != "egress warp: not configured on this node" {
		t.Fatalf("after the removal: has=%v %v", x.eng.has("inb_w"), r)
	}
	if x.warp.lastSpec() != nil {
		t.Error("the manager still holds the account")
	}
}

func TestAwgViaWarpIsRoutedBeforeItServes(t *testing.T) {
	x := newL3(t, 3, true)
	x.waitConnected()
	ds := fullState(1, awgInb("inb_a", 51842, "10.66.4", "warp", awgCred("c1")))
	ds.Warp = warpSpecPB(true)
	r := mustApply(t, x.panel, ds)
	if r.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED {
		t.Fatalf("apply = %v", r)
	}
	i := func(s string) int { return x.ord.index(s) }
	if !(i("warp.apply") >= 0 && i("warp.apply") < i("warp.routes:10.66.4.0/22,fd66:66:0:1::/64") &&
		i("warp.routes:10.66.4.0/22,fd66:66:0:1::/64") < i("tunnels") && i("tunnels") < i("awg.apply:inb_a")) {
		t.Fatalf("order must be WARP, its routes, the firewall, the interface: %v", x.ord.all())
	}
	if ts := x.tun.lastSet(); len(ts) != 1 || !ts[0].ViaWarp {
		t.Errorf("a WARP tunnel must not be masqueraded by the tunnel table: %+v", ts)
	}

	// The routes cannot be installed: the interface is taken down, never left serving with a direct exit.
	x.warp.mu.Lock()
	x.warp.routeErr = errors.New("rule pref 110 is taken")
	x.warp.mu.Unlock()
	ds2 := fullState(2, awgInb("inb_a", 51842, "10.66.4", "warp", awgCred("c1"), awgCred("c2")))
	ds2.Warp = warpSpecPB(true)
	r = mustApply(t, x.panel, ds2)
	if x.awg.has("inb_a") || !strings.Contains(r.Inbounds[0].Error, "warp routing") || r.Inbounds[0].State != pb.InboundRunState_INBOUND_RUN_STATE_FAILED {
		t.Fatalf("routes failed but the inbound serves: has=%v %v", x.awg.has("inb_a"), r)
	}
	if !x.panel.hasEventEventually("warp_needs_attention") {
		t.Error("no warp_needs_attention event")
	}
	// And it comes back by itself once the routes can be installed (the sweep reconciles every few seconds).
	x.warp.mu.Lock()
	x.warp.routeErr = nil
	x.warp.mu.Unlock()
	eventually(t, func() bool { return x.awg.has("inb_a") }, "the inbound after the routes recovered")
}

func (p *fakePanel) hasEventEventually(code string) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p.hasEvent(code) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestTunnelFirewallFailureBlocksTheInterface(t *testing.T) {
	x := newL3(t, 3, false)
	x.waitConnected()
	x.tun.setErr(errors.New("nft: Operation not permitted"))
	r := mustApply(t, x.panel, fullState(1, awgInb("inb_a", 51842, "10.66.4", "direct", awgCred("c1"))))
	if x.awg.has("inb_a") || !strings.Contains(r.Inbounds[0].Error, "tunnel firewall") || r.Status != pb.ApplyStatus_APPLY_STATUS_PARTIAL {
		t.Fatalf("an interface started without its isolation rules: has=%v %v", x.awg.has("inb_a"), r)
	}
	if !x.panel.hasEventEventually("tunnel_failed") {
		t.Error("no tunnel_failed event")
	}
	x.tun.setErr(nil)
	eventually(t, func() bool { return x.awg.has("inb_a") }, "the inbound after the firewall recovered")

	// No tunnel left: the table is removed, once.
	n := x.tun.setCount()
	mustApply(t, x.panel, fullState(2))
	eventually(t, func() bool { return x.tun.setCount() == n+1 && len(x.tun.lastSet()) == 0 }, "SetTunnels(nil) when the last tunnel goes")
	time.Sleep(150 * time.Millisecond)
	if x.tun.setCount() != n+1 {
		t.Errorf("the empty set is sent again and again: %d calls", x.tun.setCount()-n)
	}
}

// Two inbounds whose client subnets overlap cannot both be isolated from each other: the later one is blocked, the first
// one and the firewall of the node are not affected.
func TestOverlappingTunnelsBlockOnlyTheLaterInbound(t *testing.T) {
	x := newL3(t, 3, false)
	x.waitConnected()
	first := awgInb("inb_a", 51842, "10.66.4", "direct", awgCred("c1"))
	second := awgInb("inb_b", 40001, "10.66.5", "direct", awgCred("c2")) // 10.66.5.1/22 is inside 10.66.4.0/22
	second.Spec.Tunnel.AddrV6 = "fd66:66:0:2::1/64"
	r := mustApply(t, x.panel, fullState(1, first, second))
	if r.Status != pb.ApplyStatus_APPLY_STATUS_PARTIAL {
		t.Fatalf("apply = %v", r)
	}
	by := map[string]*pb.InboundResult{}
	for _, in := range r.Inbounds {
		by[in.InboundId] = in
	}
	if by["inb_a"].Error != "" || !x.awg.has("inb_a") {
		t.Errorf("the first inbound was hurt: %v", by["inb_a"])
	}
	if !strings.Contains(by["inb_b"].Error, "overlaps mgawg51842") || x.awg.has("inb_b") {
		t.Errorf("the overlapping inbound runs or says nothing useful: %v has=%v", by["inb_b"], x.awg.has("inb_b"))
	}
	if ts := x.tun.lastSet(); len(ts) != 1 || ts[0].Iface != "mgawg51842" {
		t.Errorf("tunnels = %+v", ts)
	}
}

func TestNodeSettingsResetReappliesTheEngine(t *testing.T) {
	x := newL3(t, 3, false)
	x.waitConnected()
	ds := fullState(1, awgInb("inb_a", 51842, "10.66.4", "direct", awgCred("c1")))
	ds.Settings = &pb.NodeSettings{AwgBackend: "auto"}
	mustApply(t, x.panel, ds)
	if x.awg.applyCount("inb_a") != 1 {
		t.Fatalf("applies = %d", x.awg.applyCount("inb_a"))
	}
	// The same backend setting again: nothing is re-applied.
	mustApply(t, x.panel, &pb.DesiredState{Revision: 2, BaseRevision: 1, Settings: &pb.NodeSettings{AwgBackend: "auto"}})
	if x.awg.applyCount("inb_a") != 1 {
		t.Fatalf("an unchanged setting re-applied the inbound: %d", x.awg.applyCount("inb_a"))
	}
	// A changed backend: the engine drops its inbounds and the agent applies them again in the same reconcile.
	mustApply(t, x.panel, &pb.DesiredState{Revision: 3, BaseRevision: 2, Settings: &pb.NodeSettings{AwgBackend: "kernel"}})
	if x.awg.applyCount("inb_a") != 2 || x.awg.resets != 1 {
		t.Fatalf("after the change: applies=%d resets=%d", x.awg.applyCount("inb_a"), x.awg.resets)
	}
	if got := x.a.AwgBackend(); got != "kernel" {
		t.Errorf("AwgBackend() = %q", got)
	}
}

func TestWarpHealthAndEventsReachThePanel(t *testing.T) {
	x := newL3(t, 3, true)
	x.waitConnected()
	// No WARP, no health.
	eventually(t, func() bool { return x.panel.statsSeen() != nil }, "first stats batch")
	if w := x.panel.statsSeen().Warp; w != nil {
		t.Fatalf("a node without WARP reports %v", w)
	}
	hs := time.Unix(1700000000, 0)
	x.warp.mu.Lock()
	x.warp.health = &warp.Health{State: warp.StateStarting, Backend: "kernel", Endpoint: "203.0.113.10:2408", LastHandshake: hs,
		ProbeCloudflareOK: false, ProbeOtherOK: true, Failures: 0, RxBytes: 10, TxBytes: 20, LastError: "probe_cloudflare_failed",
		ProbeCloudflare: &warp.ProbeResult{OK: false, Latency: 420 * time.Millisecond, At: hs, FailureCode: "http_502"}, CheckedAt: hs}
	x.warp.mu.Unlock()
	var wh *pb.WarpHealth
	eventually(t, func() bool {
		if st := x.panel.statsSeen(); st != nil && st.Warp != nil {
			wh = st.Warp
			return true
		}
		return false
	}, "a stats batch with the WARP health")
	if wh.State != pb.WarpState_WARP_STATE_STARTING || wh.Backend != "kernel" || wh.Colo != "" || wh.WarpFlag != "" || wh.ProbeCloudflareOk || !wh.ProbeOtherOk ||
		wh.RxBytes != 10 || wh.TxBytes != 20 || wh.Endpoint != "203.0.113.10:2408" || wh.LastHandshakeUnix < hs.Unix()-2 || wh.LastHandshakeUnix > hs.Unix()+2 {
		t.Errorf("warp health = %v", wh)
	}
	// the per-probe results travel too; a probe the round did not run stays unset
	if p := wh.ProbeCloudflare; p == nil || p.Ok || p.LatencyMs != 420 || p.FailureCode != "http_502" || p.AtUnix < hs.Unix()-2 || p.AtUnix > hs.Unix()+2 ||
		wh.ProbeOther != nil || wh.CheckedUnix < hs.Unix()-2 || wh.CheckedUnix > hs.Unix()+2 {
		t.Errorf("probe results = %v / %v, checked %d", wh.ProbeCloudflare, wh.ProbeOther, wh.CheckedUnix)
	}
	// The manager's events become agent events.
	x.a.WarpEvent(warp.Event{Code: "warp_state", Params: map[string]string{"state": "down", "from": "up"}})
	x.a.WarpEvent(warp.Event{Code: "warp_needs_attention", Warn: true, Params: map[string]string{"reason": "refresh_requested"}})
	if !x.panel.hasEventEventually("warp_state") || !x.panel.hasEventEventually("warp_needs_attention") {
		t.Errorf("events = %v", x.panel.eventCodes())
	}
}

func TestAwgBackendUnavailableIsAnEventOfItsOwn(t *testing.T) {
	x := newL3(t, 2, false)
	x.waitConnected()
	x.awg.applyErr["inb_a"] = fmt.Errorf(`awg inbound "inb_a": %s: open /dev/net/tun: no such file or directory`, awg.ReasonBackendUnavailable)
	r := mustApply(t, x.panel, fullState(1, awgInb("inb_a", 51842, "10.66.4", "direct", awgCred("c1"))))
	if !strings.Contains(r.Inbounds[0].Error, "awg_backend_unavailable") {
		t.Fatalf("result = %v", r)
	}
	if !x.panel.hasEventEventually("awg_backend_unavailable") || x.panel.hasEvent("engine_failed") {
		t.Errorf("events = %v", x.panel.eventCodes())
	}
}

func TestRejectsMalformedL3(t *testing.T) {
	x := newL3(t, 3, true)
	x.waitConnected()
	good := fullState(1, awgInb("inb_a", 51842, "10.66.4", "direct", awgCred("c1")))
	mustApply(t, x.panel, good)
	for name, mut := range map[string]func(*pb.DesiredState){
		"warp reserved of 2 bytes": func(ds *pb.DesiredState) { ds.Warp = warpSpecPB(true); ds.Warp.Reserved = []byte{1, 2} },
		"warp port 0":              func(ds *pb.DesiredState) { ds.Warp = warpSpecPB(true); ds.Warp.Ports = []uint32{0} },
		"warp port 70000":          func(ds *pb.DesiredState) { ds.Warp = warpSpecPB(true); ds.Warp.Ports = []uint32{70000} },
		"warp mtu 70000":           func(ds *pb.DesiredState) { ds.Warp = warpSpecPB(true); ds.Warp.Mtu = 70000 },
		"tunnel garbage":           func(ds *pb.DesiredState) { ds.Inbounds[0].Spec.Tunnel.AddrV4 = "not-an-address" },
		"tunnel mtu 70000":         func(ds *pb.DesiredState) { ds.Inbounds[0].Spec.Tunnel.Mtu = 70000 },
	} {
		ds := fullState(2, awgInb("inb_a", 51842, "10.66.4", "direct", awgCred("c1")))
		mut(ds)
		r := mustApply(t, x.panel, ds)
		if r.Status != pb.ApplyStatus_APPLY_STATUS_REJECTED {
			t.Errorf("%s: status %v", name, r.Status)
		}
		if r.StateHash != expectedHash(t, good) {
			t.Errorf("%s: a rejected message changed the state", name)
		}
	}
	if x.warp.lastSpec() != nil {
		t.Error("a rejected spec reached the manager")
	}
}

func TestRetireCleansWarpAndHost(t *testing.T) {
	x := newL3(t, 3, true)
	x.waitConnected()
	ds := fullState(1, awgInb("inb_a", 51842, "10.66.4", "direct", awgCred("c1")))
	ds.Warp = warpSpecPB(true)
	mustApply(t, x.panel, ds)
	x.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_Retire{Retire: &pb.Retire{RequestId: "req_r"}}})
	if r := x.panel.nextCmd(); !r.Ok {
		t.Fatalf("retire = %v", r)
	}
	select {
	case err := <-x.done:
		if !errors.Is(err, ErrRetired) {
			t.Fatalf("Run returned %v", err)
		}
		x.cancel = nil
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Retire")
	}
	x.warp.mu.Lock()
	cleaned := x.warp.cleaned
	x.warp.mu.Unlock()
	x.host.mu.Lock()
	hostCleaned := x.host.cleaned
	x.host.mu.Unlock()
	if !cleaned || !hostCleaned {
		t.Errorf("warp cleaned=%v host cleaned=%v", cleaned, hostCleaned)
	}
}

// A node that never had WARP does not run the WARP cleanup on retire: it deletes by table number, and another tool on the host
// may own that table.
func TestRetireWithoutWarpLeavesTheWarpTableAlone(t *testing.T) {
	x := newL3(t, 3, true)
	x.waitConnected()
	mustApply(t, x.panel, fullState(1, awgInb("inb_a", 51842, "10.66.4", "direct", awgCred("c1"))))
	x.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_Retire{Retire: &pb.Retire{RequestId: "req_r"}}})
	if r := x.panel.nextCmd(); !r.Ok {
		t.Fatalf("retire = %v", r)
	}
	select {
	case <-x.done:
		x.cancel = nil
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Retire")
	}
	x.warp.mu.Lock()
	cleaned := x.warp.cleaned
	x.warp.mu.Unlock()
	x.host.mu.Lock()
	hostCleaned := x.host.cleaned
	x.host.mu.Unlock()
	if cleaned || !hostCleaned {
		t.Errorf("warp cleaned=%v (want false), host cleaned=%v (want true)", cleaned, hostCleaned)
	}
}

func TestWarpManagerRunsWithTheAgent(t *testing.T) {
	x := newL3(t, 3, true)
	select {
	case <-x.warp.ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the WARP manager's health loop was never started")
	}
}

// A WARP state change must not wait for the doctor's 10 minute schedule: warp_path is re-run at once and sent as a
// partial report (so a red "WARP is down" does not stay for minutes after WARP is back).
func TestWarpStateChangeRerunsWarpPath(t *testing.T) {
	x := newL3(t, 2, true)
	x.waitConnected()
	ds := fullState(1, awgInb("inb_a", 51842, "10.66.4", "warp", awgCred("c1")))
	ds.Warp = warpSpecPB(true)
	mustApply(t, x.panel, ds)
	x.warp.mu.Lock()
	x.warp.health = &warp.Health{State: warp.StateUp, Backend: "kernel"}
	x.warp.mu.Unlock()
	x.a.WarpEvent(warp.Event{Code: "warp_state", Params: map[string]string{"state": "up", "from": "down"}})
	r := nextDoctor(t, x.harness)
	if !r.Partial || r.RequestId != "" || resultIDs(r) != "warp_path" {
		t.Fatalf("report = %+v", r)
	}
	if got := r.Results[0].DetailCode; got != "warp_path.up" {
		t.Errorf("warp_path code = %q", got)
	}
	// other events (needs attention) do not trigger a run
	x.a.WarpEvent(warp.Event{Code: "warp_needs_attention", Warn: true, Params: map[string]string{"reason": "down_after_ladder"}})
	select {
	case r := <-x.panel.doctors:
		t.Fatalf("an unexpected report: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestDoctorSeesTheL3Path(t *testing.T) {
	x := newL3(t, 2, true)
	x.waitConnected()
	via := awgInb("inb_a", 51842, "10.66.4", "warp", awgCred("c1"))
	ds := fullState(1, via)
	ds.Warp = warpSpecPB(true)
	mustApply(t, x.panel, ds)
	x.warp.mu.Lock()
	x.warp.health = &warp.Health{State: warp.StateDown, LastError: "probe_other_failed"}
	x.warp.findings = []warp.Finding{{ID: "forward_drop", Detail: "iptables FORWARD policy is DROP"}}
	x.warp.mu.Unlock()
	rep, err := x.a.doc.Run(context.Background(), []string{"awg_backend", "warp_path"})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]string{}
	for _, r := range rep.Results {
		by[r.ID] = r.Status.String() + " " + r.Params["hint"] + r.Params["error"]
	}
	if by["warp_path"] != "FAIL probe_other_failed" {
		t.Errorf("warp_path = %q", by["warp_path"])
	}
	if by["awg_backend"] != "WARN docker_forward_drop" {
		t.Errorf("awg_backend = %q", by["awg_backend"])
	}
	// A backend that is not there, on a generation 2 unit, is the unit's fault and says so.
	x.awg.hmu.Lock()
	x.awg.status = awg.BackendStatus{Mode: "auto", Reason: "open /dev/net/tun: no such file or directory"}
	x.awg.hmu.Unlock()
	rep, _ = x.a.doc.Run(context.Background(), []string{"awg_backend"})
	if h := rep.Results[0].Params["hint"]; rep.Results[0].Status.String() != "FAIL" || h != "unit_outdated" {
		t.Errorf("awg_backend = %v %v", rep.Results[0].Status, rep.Results[0].Params)
	}
}
