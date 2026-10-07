package fleet

import (
	"context"
	"encoding/json"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// The L3 additions of the fleet (agent.proto "AWG AND WARP"): the capability gates, the
// tunnel and the WarpSpec in the desired state with the hash an agent computes, the health and event intake, the settings
// the agent follows. A fake agent over real TLS, the real store, a fake WARP module.

// fakeWarpMod is the WARP module as the fleet sees it.
type fakeWarpMod struct {
	mu        sync.Mutex
	spec      *plugin.WarpSpec
	health    []*agentv1.WarpHealth
	attn      []string
	refresh   int
	rereg     int
	refreshed chan struct{}
	summary   adminv1.WarpState
	specErr   error
	storeErr  error
}

func (w *fakeWarpMod) Spec(context.Context, string) (*plugin.WarpSpec, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.spec == nil {
		return nil, w.specErr
	}
	cp := *w.spec
	return &cp, w.specErr
}

func (w *fakeWarpMod) setSpec(s *plugin.WarpSpec) {
	w.mu.Lock()
	w.spec = s
	w.mu.Unlock()
}

func (w *fakeWarpMod) StoreHealth(_ context.Context, _ string, h *agentv1.WarpHealth) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.health = append(w.health, h)
	return w.storeErr
}

func (w *fakeWarpMod) lastHealth() *agentv1.WarpHealth {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.health) == 0 {
		return nil
	}
	return w.health[len(w.health)-1]
}

func (w *fakeWarpMod) NeedsAttention(_ context.Context, _, reason string) error {
	w.mu.Lock()
	w.attn = append(w.attn, reason)
	w.mu.Unlock()
	return nil
}

func (w *fakeWarpMod) RefreshByNode(context.Context, string) error {
	w.mu.Lock()
	w.refresh++
	refreshed := w.refreshed
	w.mu.Unlock()
	if refreshed != nil {
		select {
		case refreshed <- struct{}{}:
		default:
		}
	}
	return nil
}

func (w *fakeWarpMod) AutoReregister(context.Context, string) (bool, error) {
	w.mu.Lock()
	w.rereg++
	w.mu.Unlock()
	return false, nil
}

func (w *fakeWarpMod) Summary(a *store.WarpAccountRow, online bool, _ time.Time) *adminv1.WarpSummary {
	if a == nil {
		return &adminv1.WarpSummary{State: adminv1.WarpState_WARP_STATE_NOT_CONFIGURED}
	}
	st := w.summary
	if !online {
		st = adminv1.WarpState_WARP_STATE_UNKNOWN
	}
	return &adminv1.WarpSummary{State: st, Source: adminv1.WarpSource_WARP_SOURCE_IMPORTED, Colo: "FRA"}
}

func (w *fakeWarpMod) counts() (health, refresh, rereg int, attn []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.health), w.refresh, w.rereg, append([]string(nil), w.attn...)
}

func warpSpec(enabled bool) *plugin.WarpSpec {
	return &plugin.WarpSpec{Enabled: enabled, PrivateKey: "cHJpdmF0ZQ==", PeerPublicKey: "cGVlcg==", EndpointV4: "203.0.113.10",
		Ports: []uint16{2408, 500, 1701, 4500}, AddressV4: "172.16.0.2/32", AddressV6: "fd00:16::2/128", MTU: 1280,
		Reserved: []byte{1, 2, 3}, Backend: "auto"}
}

// l3Source is a replaceable desired-state source: one fakehy inbound and one awg inbound with a tunnel.
type l3Source struct {
	mu    sync.Mutex
	awgOn bool
	creds []plugin.UserCred
}

func (s *l3Source) set(creds ...plugin.UserCred) {
	s.mu.Lock()
	s.creds = creds
	s.mu.Unlock()
}

func (s *l3Source) get(_ context.Context, _ string) ([]statehash.Inbound, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hy := plugin.InboundSpec{ID: "inb_hy", Protocol: "fakehy", ProfileID: "prf_hy", Version: 1, Enabled: true,
		Listen: plugin.Listen{Network: "udp", Port: 4443}, TLS: plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: "x"}, Egress: "direct", Settings: json.RawMessage(`{}`)}
	out := []statehash.Inbound{{Spec: hy, Creds: []plugin.UserCred{{CredID: "crd_hy", Data: json.RawMessage(`{"a":1}`)}}}}
	if s.awgOn {
		awg := plugin.InboundSpec{ID: "inb_awg", Protocol: "awg", ProfileID: "prf_awg", Version: 1, Enabled: true,
			Listen: plugin.Listen{Network: "udp", Port: 51842}, Egress: "warp", Settings: json.RawMessage(`{"version":"3.1"}`),
			Tunnel: plugin.Tunnel{AddrV4: netip.MustParsePrefix("10.66.4.1/22"), AddrV6: netip.MustParsePrefix("fd66:66:0:1::1/64"), MTU: 1280}}
		out = append(out, statehash.Inbound{Spec: awg, Creds: append([]plugin.UserCred(nil), s.creds...)})
	}
	return out, nil
}

func awgCred(id string) plugin.UserCred {
	return plugin.UserCred{CredID: id, UserID: "usr_" + id, DeviceID: "dev_" + id,
		Data: json.RawMessage(`{"public_key":"AAAA","allowed_ips":["10.66.4.5/32"],"psk":"BBBB"}`)}
}

// l3Env is an env with the awg rows in the database, the source and the WARP module attached, and a clock the test moves.
type l3Env struct {
	*env
	src   *l3Source
	wm    *fakeWarpMod
	clock *testClock
}

type testClock struct{ ns atomic.Int64 }

func (c *testClock) now() time.Time      { return time.Unix(0, c.ns.Load()) }
func (c *testClock) add(d time.Duration) { c.ns.Add(int64(d)) }

func newL3Env(t *testing.T) (*l3Env, *agent) {
	t.Helper()
	e := newEnv(t)
	x := &l3Env{env: e, src: &l3Source{awgOn: true}, wm: &fakeWarpMod{summary: adminv1.WarpState_WARP_STATE_UP}, clock: &testClock{}}
	x.clock.ns.Store(time.Now().UnixNano())
	x.src.creds = []plugin.UserCred{awgCred("crd_a")}
	e.f.cfg.Desired = x.src.get
	e.f.now = x.clock.now
	e.f.SetWarp(x.wm)
	a := e.enroll("nodea")
	now := time.Now().Unix()
	e.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at) VALUES ('prf_awg', 'awg', 'awg1', '{}', 1, ?, ?)`, now, now)
	e.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, created_at, updated_at) VALUES ('inb_awg', 'prf_awg', ?, 1, ?, ?)`, a.nodeID, now, now)
	e.run()
	return x, a
}

// connectCaps is connectFull with a Hello that lists capabilities.
func connectCaps(a *agent, instance string, caps ...string) (*conn, *model, *agentv1.DesiredState) {
	a.e.t.Helper()
	c := a.open()
	h := hello(instance, 0, "")
	h.GetHello().Capabilities = caps
	c.send(0, h)
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	ds := c.desired()
	m := newModel()
	if !m.apply(ds) {
		a.e.t.Fatal("full state did not apply")
	}
	if m.hash() != ds.StateHash {
		a.e.t.Fatalf("hash of the full state: the agent computes %s, the panel announced %s", m.hash(), ds.StateHash)
	}
	c.send(0, applied(ds, m.hash()))
	return c, m, ds
}

func (x *l3Env) inboundRow(id string) store.FleetInboundRow {
	x.t.Helper()
	rows, err := x.st.FleetInbounds(x.ctx, x.nodeIDOf(), false)
	if err != nil {
		x.t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	x.t.Fatalf("no inbound %s", id)
	return store.FleetInboundRow{}
}

func (x *l3Env) nodeIDOf() string {
	var id string
	if err := x.st.R.QueryRowContext(x.ctx, `SELECT id FROM node WHERE name = 'nodea'`).Scan(&id); err != nil {
		x.t.Fatal(err)
	}
	return id
}

func within(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOldAgentNeverGetsAwgOrWarp(t *testing.T) {
	x, a := newL3Env(t)
	x.wm.setSpec(warpSpec(true))

	// An agent that lists no capabilities (every agent before the tunnel protocols): it gets the hysteria2-style inbound and nothing else.
	c, m, ds := connectFull(a, "old")
	if len(m.in) != 1 || m.in["inb_hy"] == nil || ds.Warp != nil || len(ds.Inbounds) != 1 {
		t.Fatalf("an old agent was sent L3 state: inbounds=%v warp=%v", keys(m.in), ds.Warp)
	}
	if ds.Settings.AwgBackend != "" {
		t.Errorf("an old agent was sent awg_backend %q", ds.Settings.AwgBackend)
	}
	for _, is := range ds.Inbounds {
		if is.Spec.Protocol == "awg" || is.Spec.Tunnel != nil {
			t.Fatalf("awg inbound sent to an old agent: %v", is)
		}
	}
	// The panel says on the inbound why it is not running there, in words the node page can show.
	within(t, "the withheld inbound is marked failed", func() bool {
		r := x.inboundRow("inb_awg")
		return r.State == "failed" && len(r.LastError) > 0 && r.LastError[:13] == "agent_too_old"
	})

	// Changes that only concern L3 are invisible to it: no message, not even a settings change.
	x.src.set(awgCred("crd_a"), awgCred("crd_b"))
	x.wm.setSpec(warpSpec(false))
	x.f.StateChanged()
	c.quiet(400 * time.Millisecond)
	node, _ := x.st.Node(x.ctx, a.nodeID)
	if _, err := x.st.UpdateNode(x.ctx, a.nodeID, store.NodePatch{AwgBackend: ptr("kernel")}); err != nil {
		t.Fatal(err)
	}
	_ = node
	x.f.StateChanged()
	c.quiet(400 * time.Millisecond)
}

func ptr[T any](v T) *T { return &v }

func TestNewAgentGetsTunnelAndWarpWithTheHashItComputes(t *testing.T) {
	x, a := newL3Env(t)
	x.wm.setSpec(warpSpec(true))
	if _, err := x.st.UpdateNode(x.ctx, a.nodeID, store.NodePatch{AwgBackend: ptr("userspace")}); err != nil {
		t.Fatal(err)
	}
	c, m, ds := connectCaps(a, "new", "doctor/1", "awg/1", "warp/1")
	if len(m.in) != 2 || m.in["inb_awg"] == nil || ds.Warp == nil || m.warp == nil {
		t.Fatalf("a new agent did not get L3 state: inbounds=%v warp=%v", keys(m.in), ds.Warp)
	}
	tn := m.in["inb_awg"].spec.Tunnel
	if tn.AddrV4 != netip.MustParsePrefix("10.66.4.1/22") || tn.AddrV6 != netip.MustParsePrefix("fd66:66:0:1::1/64") || tn.MTU != 1280 {
		t.Errorf("tunnel on the wire = %+v", tn)
	}
	if ds.Settings.AwgBackend != "userspace" {
		t.Errorf("awg_backend = %q", ds.Settings.AwgBackend)
	}
	if w := m.warp; w.PrivateKey != "cHJpdmF0ZQ==" || !w.Enabled || len(w.Ports) != 4 || string(w.Reserved) != "\x01\x02\x03" || w.Backend != "auto" {
		t.Errorf("warp on the wire = %+v", w)
	}
	// An agent that restores this exact state (WARP included) from disk after a restart and says so is left alone, and the
	// deltas that follow are based on the revision it announced.
	c2 := a.open()
	h := hello("new2", m.rev, m.hash())
	h.GetHello().Capabilities = []string{"doctor/1", "awg/1", "warp/1"}
	c2.send(0, h)
	c2.wait(func(r *agentv1.ConnectResponse) bool { return r.GetHelloAck() != nil })
	c2.quiet(300 * time.Millisecond)
	c = c2

	// Only the credentials of the awg inbound change: a delta with those, and no WARP in it.
	x.src.set(awgCred("crd_a"), awgCred("crd_b"))
	x.f.StateChanged()
	d := c.desired()
	if d.BaseRevision != m.rev || d.Warp != nil || len(d.Inbounds) != 1 || d.Inbounds[0].InboundId != "inb_awg" || len(d.Inbounds[0].Creds) != 1 || d.Inbounds[0].Spec != nil {
		t.Fatalf("credential delta = %v", d)
	}
	if !m.apply(d) || m.hash() != d.StateHash {
		t.Fatal("the delta does not reproduce the announced hash")
	}
	c.send(0, applied(d, m.hash()))

	// The WARP configuration changes (paused): a delta that carries the WHOLE message and nothing else.
	x.wm.setSpec(warpSpec(false))
	x.f.StateChanged()
	d = c.desired()
	if d.BaseRevision != m.rev || d.Warp == nil || d.Warp.Enabled || len(d.Inbounds) != 0 {
		t.Fatalf("warp delta = %v", d)
	}
	if !m.apply(d) || m.hash() != d.StateHash || m.warp.Enabled {
		t.Fatal("warp delta does not reproduce the hash")
	}
	c.send(0, applied(d, m.hash()))

	// The account is deleted: a delta cannot say "remove", so the panel sends a FULL state without the message.
	x.wm.setSpec(nil)
	x.f.StateChanged()
	d = c.desired()
	if d.BaseRevision != 0 || d.Warp != nil || len(d.Inbounds) != 2 {
		t.Fatalf("removal must be a full state: base=%d warp=%v inbounds=%d", d.BaseRevision, d.Warp, len(d.Inbounds))
	}
	if !m.apply(d) || m.warp != nil || m.hash() != d.StateHash {
		t.Fatalf("full state after the removal: warp=%v", m.warp)
	}
	c.send(0, applied(d, m.hash()))

	// The owner changes the AWG backend of the node: a settings-only delta.
	c.quiet(200 * time.Millisecond)
	if _, err := (nodeService{x.f}).UpdateNode(x.ctx, connect.NewRequest(&adminv1.UpdateNodeRequest{NodeId: a.nodeID, AwgBackend: ptr("kernel")})); err != nil {
		t.Fatal(err)
	}
	d = c.desired()
	if d.Settings == nil || d.Settings.AwgBackend != "kernel" || len(d.Inbounds) != 0 {
		t.Fatalf("settings delta = %v", d)
	}
}

// RestartWarp's proof that the tunnel went down: the node confirmed a state that pauses WARP. Another change it applied
// meanwhile, with WARP still on, is not it; nor is the paused state before the node confirms it.
func TestWarpPauseAppliedNeedsThePausedStateConfirmed(t *testing.T) {
	x, a := newL3Env(t)
	x.wm.setSpec(warpSpec(true))
	c, m, ds := connectCaps(a, "new", "awg/1", "warp/1")
	appliedIs := func(h string) func() bool {
		return func() bool { n, err := x.st.Node(x.ctx, a.nodeID); return err == nil && n.AppliedHash == h }
	}
	within(t, "the connect state is applied", appliedIs(ds.StateHash))
	if x.f.WarpPauseApplied(x.ctx, a.nodeID) {
		t.Fatal("WARP runs, yet the pause is reported")
	}

	x.src.set(awgCred("crd_a"), awgCred("crd_b")) // something else changes and is applied
	x.f.StateChanged()
	d := c.desired()
	if !m.apply(d) {
		t.Fatal("delta did not apply")
	}
	c.send(0, applied(d, m.hash()))
	within(t, "the other change is applied", appliedIs(d.StateHash))
	if x.f.WarpPauseApplied(x.ctx, a.nodeID) {
		t.Fatal("a change that kept WARP on counted as the pause")
	}

	x.wm.setSpec(warpSpec(false)) // the pause: sent, then confirmed
	x.f.StateChanged()
	d = c.desired()
	if d.Warp == nil || d.Warp.Enabled {
		t.Fatalf("pause delta = %v", d)
	}
	if x.f.WarpPauseApplied(x.ctx, a.nodeID) {
		t.Fatal("the pause counted before the node confirmed it")
	}
	if !m.apply(d) {
		t.Fatal("pause delta did not apply")
	}
	c.send(0, applied(d, m.hash()))
	within(t, "the pause is confirmed", func() bool { return x.f.WarpPauseApplied(x.ctx, a.nodeID) })
}

func TestUpdateNodeAwgBackendRules(t *testing.T) {
	x, a := newL3Env(t)
	call := func(v string) error {
		_, err := (nodeService{x.f}).UpdateNode(x.ctx, connect.NewRequest(&adminv1.UpdateNodeRequest{NodeId: a.nodeID, AwgBackend: &v}))
		return err
	}
	if err := call("quantum"); code(err) != connect.CodeInvalidArgument {
		t.Errorf("a bad value: %v", err)
	}
	// The node has an awg inbound and its agent (no Hello yet, no capabilities) cannot follow the setting.
	if err := call("kernel"); code(err) != connect.CodeFailedPrecondition || err.Error() != "failed_precondition: agent too old" {
		t.Errorf("an agent without awg/1: %v", err)
	}
	// With an agent that lists it, it goes through, and the node view shows it.
	connectCaps(a, "new", "awg/1")
	if err := call("userspace"); err != nil {
		t.Fatal(err)
	}
	n, _ := x.st.Node(x.ctx, a.nodeID)
	if n.AwgBackend != "userspace" {
		t.Errorf("stored %q", n.AwgBackend)
	}
	// A node without awg inbounds may hold the setting whatever its agent is.
	x.exec(`DELETE FROM inbound WHERE id = 'inb_awg'`)
	if err := call("auto"); err != nil {
		t.Errorf("no awg inbound, yet: %v", err)
	}
}

func awgBatch(seq uint64, end int64, inbound string, peersOnline uint32, warp *agentv1.WarpHealth) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Seq: seq, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: end - 10, IntervalEndUnix: end,
		Health: []*agentv1.InboundHealth{{InboundId: inbound, State: agentv1.InboundRunState_INBOUND_RUN_STATE_RUNNING,
			Awg: &agentv1.AwgHealth{Backend: "userspace", BackendVersion: "amneziawg-go v3.1.20260828", IfaceUp: true, Peers: 4,
				PeersHandshaken: 3, PeersOnline: peersOnline, NewestHandshakeUnix: end - 5, UdpRxPackets: 99, UnknownPeerEvents: 1}}},
		Warp: warp,
	}}}
}

func TestAwgAndWarpHealthIntakeIsStoredAndThrottled(t *testing.T) {
	x, a := newL3Env(t)
	c, _, _ := connectCaps(a, "new", "awg/1", "warp/1")
	now := x.clock.now().Unix()
	up := &agentv1.WarpHealth{State: agentv1.WarpState_WARP_STATE_UP, Backend: "kernel", Colo: "FRA", WarpFlag: "on", ProbeCloudflareOk: true, ProbeOtherOk: true}

	c.send(1, awgBatch(1, now, "inb_awg", 2, up))
	within(t, "the awg health is stored", func() bool { return x.inboundRow("inb_awg").AwgHealthJSON != "" })
	within(t, "the warp health reaches the module", func() bool { h, _, _, _ := x.wm.counts(); return h == 1 })
	row := x.inboundRow("inb_awg")
	msg := awgStatusMsg(row)
	if msg == nil || msg.Backend != "userspace" || msg.Peers != 4 || msg.PeersOnline != 2 || msg.UdpRxPackets != 99 || !msg.IfaceUp || msg.ReportedUnix == 0 {
		t.Fatalf("awg status = %v", msg)
	}

	// The same report again within the gap changes nothing; a changed one still waits for the gap.
	c.send(2, awgBatch(2, now+10, "inb_awg", 2, up))
	c.send(3, awgBatch(3, now+20, "inb_awg", 3, up))
	c.ack()
	time.Sleep(150 * time.Millisecond)
	if h, _, _, _ := x.wm.counts(); h != 1 {
		t.Errorf("warp health written %d times inside the gap", h)
	}
	if got := awgStatusMsg(x.inboundRow("inb_awg")); got.PeersOnline != 2 {
		t.Errorf("a changed report overwrote inside the gap: %v", got)
	}
	// After the gap a changed one is written, and an unchanged one only after the refresh gap.
	x.clock.add(healthMinGap + time.Second)
	c.send(4, awgBatch(4, now+31, "inb_awg", 3, up))
	within(t, "the changed report after the gap", func() bool { return awgStatusMsg(x.inboundRow("inb_awg")).PeersOnline == 3 })
	x.clock.add(healthMinGap + time.Second)
	c.send(5, awgBatch(5, now+62, "inb_awg", 3, up))
	time.Sleep(150 * time.Millisecond)
	if h, _, _, _ := x.wm.counts(); h != 1 {
		t.Errorf("an unchanged report was written again before the refresh gap: %d", h)
	}
	x.clock.add(healthRefreshGap)
	c.send(6, awgBatch(6, now+200, "inb_awg", 3, up))
	within(t, "the refresh of an unchanged report", func() bool { h, _, _, _ := x.wm.counts(); return h == 2 })

	// A report for an inbound that is not this node's changes nothing (the node id is part of the update).
	x.exec(`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_other', 'other', 'o.example.com', 'active', 1)`)
	x.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, created_at, updated_at) VALUES ('inb_foreign', 'prf_awg', 'nod_other', 1, 1, 1)`)
	x.clock.add(healthRefreshGap)
	c.send(7, awgBatch(7, now+400, "inb_foreign", 1, nil))
	c.ack()
	var foreign string
	_ = x.st.R.QueryRowContext(x.ctx, `SELECT awg_health_json FROM inbound WHERE id = 'inb_foreign'`).Scan(&foreign)
	if foreign != "" {
		t.Error("a node wrote the health of another node's inbound")
	}

	// The admin views: the awg block on the inbound, the badge and the backend on the node.
	resp, err := (nodeService{x.f}).GetNode(x.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: a.nodeID}))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, in := range resp.Msg.Inbounds {
		if in.Id == "inb_awg" {
			found = in.Awg != nil && in.Awg.PeersHandshaken == 3
		}
	}
	if !found {
		t.Error("Inbound.awg missing from GetNode")
	}
	if resp.Msg.Node.AwgBackend != "auto" || resp.Msg.Node.Warp == nil || resp.Msg.Node.Warp.State != adminv1.WarpState_WARP_STATE_NOT_CONFIGURED {
		t.Errorf("node view: backend=%q warp=%v", resp.Msg.Node.AwgBackend, resp.Msg.Node.Warp)
	}
}

// The pill of the WARP card sat on "starting" for half a minute after the tunnel was up: the first good check of the
// node (fresh handshake, green probes, state still "starting") was written, and the report that said "up" waited for the
// write gap. A change of state is written at once; counters that only moved still wait.
func TestWarpStateChangeIsWrittenAtOnce(t *testing.T) {
	x, a := newL3Env(t)
	c, _, _ := connectCaps(a, "new", "awg/1", "warp/1")
	now := x.clock.now().Unix()
	health := func(state agentv1.WarpState, rx uint64) *agentv1.WarpHealth {
		return &agentv1.WarpHealth{State: state, Backend: "kernel", Colo: "FRA", WarpFlag: "on", ProbeCloudflareOk: true, ProbeOtherOk: true, RxBytes: rx}
	}
	writes := func() int { h, _, _, _ := x.wm.counts(); return h }

	c.send(1, awgBatch(1, now, "inb_awg", 1, health(agentv1.WarpState_WARP_STATE_STARTING, 10)))
	within(t, "the first report", func() bool { return writes() == 1 })

	c.send(2, awgBatch(2, now+10, "inb_awg", 1, health(agentv1.WarpState_WARP_STATE_STARTING, 20)))
	c.ack()
	time.Sleep(150 * time.Millisecond)
	if n := writes(); n != 1 {
		t.Fatalf("moving counters were written inside the gap: %d", n)
	}

	c.send(3, awgBatch(3, now+20, "inb_awg", 1, health(agentv1.WarpState_WARP_STATE_UP, 30)))
	within(t, "starting -> up inside the gap", func() bool { return writes() == 2 })
	if got := x.wm.lastHealth(); got.GetState() != agentv1.WarpState_WARP_STATE_UP {
		t.Errorf("the stored report says %v", got.GetState())
	}
}

func TestWarpBadgeFollowsTheAccount(t *testing.T) {
	x, a := newL3Env(t)
	connectCaps(a, "new", "warp/1")
	now := time.Now().Unix()
	if err := x.st.CreateWarpAccount(x.ctx, store.WarpAccountRow{NodeID: a.nodeID, Source: store.WarpImported, SecretEnc: []byte("x"), PeerPublicKey: "cGVlcg==",
		EndpointV4: "203.0.113.10", Ports: []uint16{2408}, AddressV4: "172.16.0.2/32", MTU: 1280, Enabled: true,
		CreatedAt: time.Unix(now, 0), UpdatedAt: time.Unix(now, 0)}); err != nil {
		t.Fatal(err)
	}
	resp, err := (nodeService{x.f}).ListNodes(x.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range resp.Msg.Nodes {
		if n.Id == a.nodeID && (n.Warp == nil || n.Warp.State != adminv1.WarpState_WARP_STATE_UP || n.Warp.Colo != "FRA") {
			t.Errorf("node badge = %v", n.Warp)
		}
	}
	// Without a module the badge is a plain "unknown" for a node that has an account, "not configured" for the others.
	x.f.SetWarp(nil)
	if s := x.f.warpSummary(x.ctx, a.nodeID, true, time.Now()); s.State != adminv1.WarpState_WARP_STATE_UNKNOWN {
		t.Errorf("no module, account present: %v", s)
	}
	if s := x.f.warpSummary(x.ctx, "nod_none", true, time.Now()); s.State != adminv1.WarpState_WARP_STATE_NOT_CONFIGURED {
		t.Errorf("no module, no account: %v", s)
	}
}

func warpEvent(seq uint64, reason string) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Seq: seq, Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
		Severity: agentv1.Severity_SEVERITY_WARNING, Code: "warp_needs_attention", TimeUnix: time.Now().Unix(), Params: map[string]string{"reason": reason}}}}
}

func TestWarpEventsReachTheModuleWithoutBlockingTheStream(t *testing.T) {
	x, a := newL3Env(t)
	c, _, _ := connectCaps(a, "new", "awg/1", "warp/1")

	// The ladder ran out of endpoints: the panel reads the account (read only), at most once per gap, and it is NOT an
	// alarm for the owner.
	c.send(1, warpEvent(1, "refresh_requested"))
	within(t, "the refresh", func() bool { _, r, _, _ := x.wm.counts(); return r == 1 })
	c.send(2, warpEvent(2, "refresh_requested"))
	c.ack()
	time.Sleep(100 * time.Millisecond)
	if _, r, _, attn := x.wm.counts(); r != 1 || len(attn) != 0 {
		t.Errorf("refresh asked %d times, attention %v", r, attn)
	}
	x.clock.add(warpRefreshGap + time.Second)
	c.send(3, warpEvent(3, "refresh_requested"))
	within(t, "the refresh after the gap", func() bool { _, r, _, _ := x.wm.counts(); return r == 2 })

	// A clash on the host is the owner's to resolve and is recorded; a ladder that gave up also tries the automatic
	// re-registration (the module decides whether the owner switched it on).
	c.send(4, warpEvent(4, "table_in_use"))
	within(t, "attention for a clash", func() bool { _, _, _, attn := x.wm.counts(); return len(attn) == 1 && attn[0] == "table_in_use" })
	c.send(5, warpEvent(5, "down_after_ladder"))
	within(t, "attention and re-registration", func() bool {
		_, _, rr, attn := x.wm.counts()
		return rr == 1 && len(attn) == 2 && attn[1] == "down_after_ladder"
	})

	// Other events do not reach the module.
	c.send(6, &agentv1.ConnectRequest{Seq: 6, Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{Code: "warp_state", TimeUnix: time.Now().Unix()}}})
	c.ack()
	if _, _, _, attn := x.wm.counts(); len(attn) != 2 {
		t.Errorf("a warp_state event became attention: %v", attn)
	}
	// And the events themselves are stored like any event.
	if n := x.count(`SELECT count(*) FROM event WHERE code = 'warp_needs_attention'`); n != 5 {
		t.Errorf("stored warp_needs_attention events: %d", n)
	}
}

func TestWarpRefreshThrottleUsesCoreEventTimeAndSurvivesAckQueueFull(t *testing.T) {
	e, core, ctx, state, sidecar, now := coreFixture(t, "warp-refresh")
	h := hello("instance-warp-refresh", 0, "")
	h.GetHello().Capabilities = []string{capWarp}
	started := stepHello(t, core, ctx, state, sidecar, now, h.GetHello())
	sctx, cancel := context.WithCancelCause(ctx)
	s := &session{f: e.f, nodeID: state.NodeID, owner: state.OwnerGeneration, caps: started.State.Capabilities,
		ctx: sctx, cancel: cancel, done: make(chan struct{}), out: make(chan *agentv1.ConnectResponse, outQueue),
		core: core, coreState: started.State, coreSidecar: started.Sidecar}
	defer func() {
		cancel(nil)
		close(s.done)
	}()
	warpEvent := func(seq uint64, at time.Time) *agentv1.ConnectRequest {
		return &agentv1.ConnectRequest{Seq: seq, Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
			Code: eventWarpAttention, TimeUnix: at.Unix(), Params: map[string]string{"reason": warpReasonRefresh},
		}}}
	}
	_, err := s.stepCore(sctx, SessionEvent{Kind: EventAgentFrame, At: now, Frame: warpEvent(1, now)})
	if err != nil {
		t.Fatal(err)
	}
	if !s.coreSidecar.L3.warpAsk.IsZero() {
		t.Fatalf("missing module consumed the refresh throttle at %v", s.coreSidecar.L3.warpAsk)
	}
	<-s.out // Ack for the first event.

	w := &fakeWarpMod{refreshed: make(chan struct{}, 1)}
	e.f.SetWarp(w)
	eventAt := now.Add(ackEvery)
	for i := 0; i < cap(s.out); i++ {
		s.out <- &agentv1.ConnectResponse{}
	}
	_, err = s.stepCore(sctx, SessionEvent{Kind: EventAgentFrame, At: eventAt, Frame: warpEvent(2, eventAt)})
	if err != errAgentQueueFull {
		t.Fatalf("full Ack queue error = %v, want %v", err, errAgentQueueFull)
	}
	if !s.coreSidecar.L3.warpAsk.Equal(eventAt) {
		t.Fatalf("refresh throttle timestamp = %v, want core event time %v", s.coreSidecar.L3.warpAsk, eventAt)
	}
	select {
	case <-w.refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh was skipped after Ack queue overflow")
	}
}

func TestWarpUpClearsWhatTheNodeRaised(t *testing.T) {
	x, a := newL3Env(t)
	c, _, _ := connectCaps(a, "new", "warp/1")
	now := time.Now()
	if err := x.st.CreateWarpAccount(x.ctx, store.WarpAccountRow{NodeID: a.nodeID, Source: store.WarpRegistered, SecretEnc: []byte("x"), PeerPublicKey: "cGVlcg==",
		EndpointV4: "203.0.113.10", Ports: []uint16{2408}, AddressV4: "172.16.0.2/32", MTU: 1280, Enabled: true, Attention: "down_after_ladder",
		CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	down := &agentv1.WarpHealth{State: agentv1.WarpState_WARP_STATE_DOWN, LastError: "probe_other_failed"}
	c.send(1, awgBatch(1, now.Unix(), "inb_awg", 0, down))
	within(t, "the down report", func() bool { h, _, _, _ := x.wm.counts(); return h == 1 })
	if _, _, _, attn := x.wm.counts(); len(attn) != 0 {
		t.Fatalf("a DOWN report cleared the attention: %v", attn)
	}
	x.clock.add(healthMinGap + time.Second)
	c.send(2, awgBatch(2, now.Unix()+31, "inb_awg", 0, &agentv1.WarpHealth{State: agentv1.WarpState_WARP_STATE_UP}))
	within(t, "the attention is cleared when the tunnel works", func() bool {
		_, _, _, attn := x.wm.counts()
		return len(attn) == 1 && attn[0] == ""
	})
	// Only once per recovery, not with every UP report.
	x.clock.add(healthRefreshGap + time.Second)
	c.send(3, awgBatch(3, now.Unix()+200, "inb_awg", 0, &agentv1.WarpHealth{State: agentv1.WarpState_WARP_STATE_UP}))
	c.ack()
	time.Sleep(100 * time.Millisecond)
	if _, _, _, attn := x.wm.counts(); len(attn) != 1 {
		t.Errorf("attention cleared again: %v", attn)
	}
}

// A check that starts failing while the state stays "starting" (a slow WARP edge) is written at once, a counter change is not.
func TestWarpStateChangedSeesAFlippedCheck(t *testing.T) {
	st := agentv1.WarpState_WARP_STATE_STARTING
	ok := &agentv1.WarpHealth{State: st, ProbeCloudflareOk: true, ProbeOtherOk: true, RxBytes: 1}
	same := &agentv1.WarpHealth{State: st, ProbeCloudflareOk: true, ProbeOtherOk: true, RxBytes: 2}
	failed := &agentv1.WarpHealth{State: st, ProbeCloudflareOk: false, ProbeOtherOk: true, LastError: "probe_cloudflare_failed"}
	if warpStateChanged(ok, same) {
		t.Error("a byte counter is not urgent")
	}
	if !warpStateChanged(ok, failed) || !warpStateChanged(failed, ok) {
		t.Error("a check that flipped must be written at once")
	}
	if !warpStateChanged(
		&agentv1.WarpHealth{State: st, ProbeCloudflare: &agentv1.WarpProbeResult{FailureCode: "timeout"}},
		&agentv1.WarpHealth{State: st, ProbeCloudflare: &agentv1.WarpProbeResult{FailureCode: "http_502"}},
	) {
		t.Error("a probe diagnostic change must be written at once")
	}
	if !warpStateChanged(ok, &agentv1.WarpHealth{State: agentv1.WarpState_WARP_STATE_UP, ProbeCloudflareOk: true, ProbeOtherOk: true}) {
		t.Error("a state change must be written at once")
	}
}

func TestWarpModuleErrorsDoNotBreakTheStream(t *testing.T) {
	x, a := newL3Env(t)
	x.wm.mu.Lock()
	x.wm.storeErr = context.DeadlineExceeded
	x.wm.mu.Unlock()
	c, _, _ := connectCaps(a, "new", "awg/1", "warp/1")
	c.send(1, awgBatch(1, time.Now().Unix(), "inb_awg", 1, &agentv1.WarpHealth{State: agentv1.WarpState_WARP_STATE_UP}))
	ack := c.ack()
	if ack.UpToSeq != 1 {
		t.Errorf("the batch was not acknowledged because the module failed: %v", ack)
	}
}
