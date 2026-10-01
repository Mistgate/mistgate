package awg

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

const p1 = "mgawg51842"

func TestApplyBuildsInterfaceAndPeers(t *testing.T) {
	e, fb, _ := newTestEngine(t)
	s := spec(t, "inb1", 51842, nil)
	rep := mustApply(t, e, s, cred("c1", 0x11, 5), cred("c2", 0x12, 6))

	if rep.SpecHash != statehash.Spec(s) || rep.CredCount != 2 || rep.Restarted {
		t.Errorf("report %+v", rep)
	}
	if got := fb.take(); !slices.Equal(got, []string{"create:" + p1, "peers:" + p1 + ":+11,+12"}) {
		t.Errorf("calls: %v", got)
	}
	d := fb.devs[p1]
	if d.cfg.Port != 51842 || d.cfg.Version != "3.1" || d.cfg.PrivateKey != pk(1) || d.cfg.Tunnel.AddrV4 != netip.MustParsePrefix("10.66.4.1/22") || d.cfg.Obf.S1 != 24 {
		t.Errorf("device config %+v", d.cfg)
	}
	if h := e.Health(); len(h) != 1 || h[0].State != plugin.RunRunning || h[0].InboundID != "inb1" {
		t.Errorf("health %+v", h)
	}
	if e.Protocol() != "awg" || e.Version() != "fake userspace" || e.Capabilities().RateLimitPerCred {
		t.Errorf("identity: %q %q %+v", e.Protocol(), e.Version(), e.Capabilities())
	}
}

func TestUsersOnlyChangeIsLiveAndNeverReAddsAnUnchangedPeer(t *testing.T) {
	e, fb, _ := newTestEngine(t)
	s := spec(t, "inb1", 51842, nil)
	a, b, c := cred("a", 0x0a, 5), cred("b", 0x0b, 6), cred("c", 0x0c, 7)
	mustApply(t, e, s, a, b)
	fb.take()

	s2 := s
	s2.Version = 2
	rep := mustApply(t, e, s2, a, c)
	if rep.Restarted {
		t.Error("a users-only change must not restart the interface")
	}
	if got := fb.take(); !slices.Equal(got, []string{"peers:" + p1 + ":-0b,+0c"}) {
		t.Errorf("want one call, removal first, and nothing for the unchanged peer: %v", got)
	}
	// the very same state again: no calls at all
	mustApply(t, e, s2, a, c)
	if got := fb.take(); len(got) != 0 {
		t.Errorf("an idempotent Apply touched the device: %v", got)
	}
}

func TestChangedAddressOrPSKIsUpdateOnly(t *testing.T) {
	e, fb, _ := newTestEngine(t)
	s := spec(t, "inb1", 51842, nil)
	a := cred("a", 0x0a, 5)
	mustApply(t, e, s, a)
	fb.take()

	moved := credData(a, func(j *credJSON) { j.AllowedIPs = []string{"10.66.4.9/32"} })
	mustApply(t, e, s, moved)
	if got := fb.take(); !slices.Equal(got, []string{"peers:" + p1 + ":~0a"}) {
		t.Errorf("address change: %v", got)
	}
	if ips := fb.peer(t, p1, 0x0a).allowed; len(ips) != 1 || ips[0] != netip.MustParsePrefix("10.66.4.9/32") {
		t.Errorf("replace_allowed_ips did not replace: %v", ips)
	}

	rekeyed := credData(moved, func(j *credJSON) { j.PSK = b64(0x77) })
	mustApply(t, e, s, rekeyed)
	if got := fb.take(); !slices.Equal(got, []string{"peers:" + p1 + ":~0a"}) {
		t.Errorf("psk change: %v", got)
	}
	if psk := fb.peer(t, p1, 0x0a).psk; psk == nil || *psk != pk(0x77) {
		t.Errorf("psk not updated: %v", psk)
	}

	nopsk := credData(rekeyed, func(j *credJSON) { j.PSK = "" })
	mustApply(t, e, s, nopsk)
	if got := fb.take(); !slices.Equal(got, []string{"peers:" + p1 + ":~0a"}) {
		t.Errorf("psk removal: %v", got)
	}
	if fb.peer(t, p1, 0x0a).psk != nil {
		t.Error("an absent psk must clear the old one (zero key), update_only alone would keep it")
	}
}

func TestRotationRemovesTheOldKeyFirst(t *testing.T) {
	e, fb, _ := newTestEngine(t)
	s := spec(t, "inb1", 51842, nil)
	mustApply(t, e, s, cred("dev", 0x0a, 5), cred("other", 0x0b, 6))
	fb.take()
	// same device, new key, same address: the old public key goes and the new one comes in the same message
	mustApply(t, e, s, cred("dev2", 0x1a, 5), cred("other", 0x0b, 6))
	if got := fb.take(); !slices.Equal(got, []string{"peers:" + p1 + ":-0a,+1a"}) {
		t.Errorf("rotation: %v", got)
	}
	if fb.hasPeer(p1, 0x0a) || !fb.hasPeer(p1, 0x1a) || !fb.hasPeer(p1, 0x0b) {
		t.Error("peer set after rotation")
	}
}

func TestIdentityChangeRecreatesButMTUDoesNot(t *testing.T) {
	e, fb, _ := newTestEngine(t)
	s := spec(t, "inb1", 51842, nil)
	a := cred("a", 0x0a, 5)
	mustApply(t, e, s, a)
	fb.take()

	mtu := s
	mtu.Tunnel.MTU = 1380
	if rep := mustApply(t, e, mtu, a); rep.Restarted {
		t.Error("an MTU-only change is applied live")
	}
	if got := fb.take(); !slices.Equal(got, []string{"mtu:" + p1 + ":1380"}) {
		t.Errorf("mtu: %v", got)
	}

	for name, mut := range map[string]func(*awgcfg.Settings){
		"s1":            func(s *awgcfg.Settings) { s.Obfuscation.S1 = 25 },
		"hpk":           func(s *awgcfg.Settings) { s.Obfuscation.HeaderProtectionKey = b64(9) },
		"server key":    func(s *awgcfg.Settings) { s.PrivateKey = b64(2) },
		"random trails": func(s *awgcfg.Settings) { s.Obfuscation.RandomTrailers = false },
		"version": func(s *awgcfg.Settings) {
			s.Version = "2.0"
			s.Obfuscation = awgcfg.Obfuscation{S1: 20, S2: 30, S3: 10, S4: 5, H1: rng("100-200"), H2: rng("300-400"), H3: rng("500-600"), H4: rng("700-800")}
		},
		"jc": func(s *awgcfg.Settings) { s.Obfuscation.Jc = 7 },
	} {
		ns := spec(t, "inb1", 51842, mut)
		ns.Tunnel.MTU = 1380
		rep := mustApply(t, e, ns, a)
		if !rep.Restarted {
			t.Errorf("%s: must recreate the interface", name)
		}
		if got := fb.take(); !slices.Equal(got, []string{"destroy:" + p1, "create:" + p1, "peers:" + p1 + ":+0a"}) {
			t.Errorf("%s: calls %v", name, got)
		}
		mustApply(t, e, spec(t, "inb1", 51842, nil), a) // back to the base settings
		fb.take()
	}

	addr := spec(t, "inb1", 51842, nil)
	addr.Tunnel.AddrV4 = netip.MustParsePrefix("10.66.8.1/22")
	if _, err := e.Apply(context.Background(), addr, []plugin.UserCred{a}); err == nil {
		t.Fatal("a peer outside the new subnet must be refused") // a is 10.66.4.5, the new subnet is 10.66.8.0/22
	}
	if h := e.Health()[0]; h.State != plugin.RunRunning {
		t.Errorf("a refused Apply must leave the running inbound alone: %+v", h)
	}

	// a port change moves the interface
	mustApply(t, e, spec(t, "inb1", 51843, nil), a)
	got := fb.take()
	if len(got) < 2 || got[0] != "destroy:"+p1 || got[1] != "create:mgawg51843" {
		t.Errorf("port change: %v", got)
	}
}

func TestRestartsCounter(t *testing.T) {
	e, _, _ := newTestEngine(t)
	mustApply(t, e, spec(t, "inb1", 51842, nil))
	mustApply(t, e, spec(t, "inb1", 51842, func(s *awgcfg.Settings) { s.Obfuscation.S2 = 30 }))
	if h := e.Health()[0]; h.Restarts != 1 {
		t.Errorf("restarts %d", h.Restarts)
	}
}

func TestObservedShowsAPeerThatVanished(t *testing.T) {
	e, fb, _ := newTestEngine(t)
	s := spec(t, "inb1", 51842, nil)
	creds := []plugin.UserCred{cred("a", 0x0a, 5), cred("b", 0x0b, 6)}
	mustApply(t, e, s, creds...)

	want := statehash.State([]statehash.Inbound{{Spec: s, Creds: creds}})
	if got := statehash.State(e.Observed()); got != want {
		t.Fatal("observed state differs right after Apply")
	}
	fb.mutate(p1, func(d *fakeDev) { delete(d.peers, pk(0x0b)) }) // deleted behind our back
	if statehash.State(e.Observed()) == want {
		t.Error("a vanished peer must show as a state-hash difference (state_drift)")
	}
	// a peer with the wrong address is also not "held"
	mustApply(t, e, s, creds...)
	if got := statehash.State(e.Observed()); got != want {
		t.Error("Apply must put the vanished peer back (the diff is against the device, not memory)")
	}
	fb.mutate(p1, func(d *fakeDev) { d.peers[pk(0x0a)].allowed = []netip.Prefix{netip.MustParsePrefix("10.66.4.99/32")} })
	if statehash.State(e.Observed()) == want {
		t.Error("a peer with other allowed ips is not what was asked for")
	}
	mustApply(t, e, s, creds...)
	if got := statehash.State(e.Observed()); got != want {
		t.Error("Apply must correct the allowed ips")
	}
}

func TestCollectDeltasSessionsAndCounterReset(t *testing.T) {
	e, fb, clk := newTestEngine(t)
	s := spec(t, "inb1", 51842, nil)
	mustApply(t, e, s, cred("a", 0x0a, 5), cred("b", 0x0b, 6))
	ctx := context.Background()

	set := func(b byte, rx, tx uint64, hsAgo time.Duration) {
		fb.mutate(p1, func(d *fakeDev) {
			p := d.peers[pk(b)]
			p.rx, p.tx = rx, tx
			if hsAgo >= 0 {
				p.hs = clk.Now().Add(-hsAgo)
				p.ep = netip.MustParseAddrPort("[::ffff:203.0.113.7]:40000")
			}
		})
	}
	set(0x0a, 1000, 4000, 5*time.Second)
	set(0x0b, 0, 0, -1)
	c, _ := e.Collect(ctx)
	if len(c.Traffic) != 1 || c.Traffic[0] != (plugin.UserTraffic{CredID: "a", InboundID: "inb1", Up: 1000, Down: 4000}) {
		t.Errorf("traffic %+v (Up = rx of the peer, Down = tx)", c.Traffic)
	}
	if len(c.Sessions) != 1 || c.Sessions[0].CredID != "a" || c.Sessions[0].RemoteIP != netip.MustParseAddr("203.0.113.7") || !c.Sessions[0].Since.Equal(clk.Now().Add(-5*time.Second)) {
		t.Errorf("sessions %+v (endpoint unmapped, since = last handshake)", c.Sessions)
	}

	set(0x0a, 1500, 4100, 5*time.Second)
	if c, _ = e.Collect(ctx); len(c.Traffic) != 1 || c.Traffic[0].Up != 500 || c.Traffic[0].Down != 100 {
		t.Errorf("second delta %+v", c.Traffic)
	}
	if c, _ = e.Collect(ctx); len(c.Traffic) != 0 {
		t.Errorf("nothing moved, nothing to report: %+v", c.Traffic)
	}
	set(0x0a, 30, 10, 5*time.Second) // the peer was recreated: smaller counters
	if c, _ = e.Collect(ctx); len(c.Traffic) != 1 || c.Traffic[0].Up != 30 || c.Traffic[0].Down != 10 {
		t.Errorf("after a counter reset the delta is the new value: %+v", c.Traffic)
	}
	clk.Add(200 * time.Second) // handshake now 205 s old: not a session any more
	if c, _ = e.Collect(ctx); len(c.Sessions) != 0 {
		t.Errorf("a stale handshake is not a session: %+v", c.Sessions)
	}
}

func TestRemovedPeerCountersAreFlushed(t *testing.T) {
	e, fb, _ := newTestEngine(t)
	s := spec(t, "inb1", 51842, nil)
	mustApply(t, e, s, cred("a", 0x0a, 5), cred("b", 0x0b, 6))
	fb.mutate(p1, func(d *fakeDev) { d.peers[pk(0x0b)].rx, d.peers[pk(0x0b)].tx = 700, 900 })
	mustApply(t, e, s, cred("a", 0x0a, 5)) // b removed before any Collect saw its bytes
	c, _ := e.Collect(context.Background())
	if len(c.Traffic) != 1 || c.Traffic[0] != (plugin.UserTraffic{CredID: "b", InboundID: "inb1", Up: 700, Down: 900}) {
		t.Fatalf("the last bytes of a removed peer are lost: %+v", c.Traffic)
	}
	// and a recreated interface flushes everyone
	fb.mutate(p1, func(d *fakeDev) { d.peers[pk(0x0a)].rx = 50 })
	mustApply(t, e, spec(t, "inb1", 51842, func(s *awgcfg.Settings) { s.Obfuscation.S3 = 26 }), cred("a", 0x0a, 5))
	c, _ = e.Collect(context.Background())
	if len(c.Traffic) != 1 || c.Traffic[0].CredID != "a" || c.Traffic[0].Up != 50 {
		t.Fatalf("a recreated interface must flush the old counters: %+v", c.Traffic)
	}
}

func TestKick(t *testing.T) {
	e, fb, clk := newTestEngine(t)
	e.kickDelay = 20 * time.Millisecond
	s := spec(t, "inb1", 51842, nil)
	mustApply(t, e, s, cred("a", 0x0a, 5), cred("b", 0x0b, 6), cred("idle", 0x0c, 7))
	fb.mutate(p1, func(d *fakeDev) {
		d.peers[pk(0x0a)].hs = clk.Now().Add(-3 * time.Second)
		d.peers[pk(0x0b)].hs = clk.Now().Add(-3 * time.Second)
	})
	fb.take()

	n, err := e.Kick(context.Background(), []string{"a", "idle", "nobody"})
	if err != nil || n != 1 {
		t.Fatalf("Kick = %d, %v: only credentials with a session count", n, err)
	}
	if fb.hasPeer(p1, 0x0a) || !fb.hasPeer(p1, 0x0b) || !fb.hasPeer(p1, 0x0c) {
		t.Error("only the kicked peer with a session is removed")
	}
	waitFor(t, func() bool { return fb.hasPeer(p1, 0x0a) }, "the kicked peer is put back")

	// a credential withdrawn during the second is not put back
	fb.mutate(p1, func(d *fakeDev) { d.peers[pk(0x0a)].hs = clk.Now() })
	if n, _ := e.Kick(context.Background(), []string{"a"}); n != 1 {
		t.Fatal("kick")
	}
	mustApply(t, e, s, cred("b", 0x0b, 6), cred("idle", 0x0c, 7))
	time.Sleep(80 * time.Millisecond)
	if fb.hasPeer(p1, 0x0a) {
		t.Error("a revoked credential must stay gone")
	}
}

func waitFor(t *testing.T, ok func() bool, what string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func TestDisabledInboundStopsAndComesBack(t *testing.T) {
	e, fb, _ := newTestEngine(t)
	s := spec(t, "inb1", 51842, nil)
	creds := []plugin.UserCred{cred("a", 0x0a, 5)}
	mustApply(t, e, s, creds...)
	fb.take()

	off := s
	off.Enabled = false
	rep := mustApply(t, e, off, creds...)
	if rep.Restarted {
		t.Error("stopping is not a restart")
	}
	if got := fb.take(); !slices.Equal(got, []string{"destroy:" + p1}) {
		t.Errorf("calls %v", got)
	}
	if h := e.Health(); h[0].State != plugin.RunStopped {
		t.Errorf("health %+v", h)
	}
	obs := e.Observed()
	if len(obs) != 1 || len(obs[0].Creds) != 1 || obs[0].Spec.Enabled {
		t.Errorf("a stopped inbound is reported as delivered: %+v", obs)
	}
	mustApply(t, e, s, creds...)
	if !fb.hasPeer(p1, 0x0a) {
		t.Error("re-enabled inbound has no peer")
	}
}

func TestNoBackend(t *testing.T) {
	e := newEngine(engine.Env{}, nil, BackendStatus{Mode: "auto", Reason: "no /dev/net/tun"})
	_, err := e.Apply(context.Background(), spec(t, "inb1", 51842, nil), nil)
	if err == nil || !strings.Contains(err.Error(), ReasonBackendUnavailable) || !strings.Contains(err.Error(), "no /dev/net/tun") {
		t.Fatalf("err = %v", err)
	}
	h := e.Health()
	if len(h) != 1 || h[0].State != plugin.RunFailed || !strings.HasPrefix(h[0].Detail, ReasonBackendUnavailable) {
		t.Errorf("health %+v", h)
	}
	if !strings.Contains(e.Version(), "no backend") || e.BackendStatus().Available {
		t.Errorf("version %q status %+v", e.Version(), e.BackendStatus())
	}
	// a disabled inbound needs no backend
	off := spec(t, "inb2", 51843, nil)
	off.Enabled = false
	if _, err := e.Apply(context.Background(), off, nil); err != nil {
		t.Errorf("disabled inbound: %v", err)
	}
	if err := e.Close(context.Background()); err != nil {
		t.Error(err)
	}
}

func TestModuleTooOldIsNotASilentFallback(t *testing.T) {
	e, fb, _ := newTestEngine(t)
	fb.is31 = false
	_, err := e.Apply(context.Background(), spec(t, "inb1", 51842, nil), nil)
	if err == nil || !strings.Contains(err.Error(), ReasonModuleTooOld) {
		t.Fatalf("err = %v", err)
	}
	if len(fb.devs) != 0 {
		t.Error("nothing may be created")
	}
	// a 2.0 profile runs on such a module
	v20 := spec(t, "inb2", 51843, func(s *awgcfg.Settings) {
		s.Version = "2.0"
		s.Obfuscation = awgcfg.Obfuscation{S1: 20, S2: 30, S3: 10, S4: 5, H1: rng("100-200"), H2: rng("300-400"), H3: rng("500-600"), H4: rng("700-800")}
	})
	mustApply(t, e, v20)
}

func TestRefusedInputLeavesEverythingAlone(t *testing.T) {
	e, fb, _ := newTestEngine(t)
	s := spec(t, "inb1", 51842, nil)
	good := cred("a", 0x0a, 5)
	mustApply(t, e, s, good)
	fb.take()
	ctx := context.Background()

	bad := map[string][]plugin.UserCred{
		"not json":          {{CredID: "x", Data: []byte("{")}},
		"bad public key":    {credData(good, func(j *credJSON) { j.PublicKey = "AAAA" })},
		"bad psk":           {credData(good, func(j *credJSON) { j.PSK = "AAAA" })},
		"no address":        {credData(good, func(j *credJSON) { j.AllowedIPs = nil })},
		"wide prefix":       {credData(good, func(j *credJSON) { j.AllowedIPs = []string{"10.66.4.0/24"} })},
		"default route":     {credData(good, func(j *credJSON) { j.AllowedIPs = []string{"0.0.0.0/0"} })},
		"outside subnet":    {credData(good, func(j *credJSON) { j.AllowedIPs = []string{"192.0.2.5/32"} })},
		"the node itself":   {credData(good, func(j *credJSON) { j.AllowedIPs = []string{"10.66.4.1/32"} })},
		"v6 without tunnel": {credData(good, func(j *credJSON) { j.AllowedIPs = []string{"2001:db8::5/128"} })},
		"same key twice":    {good, credData(cred("b", 0x0a, 6), func(*credJSON) {})},
		"same address":      {good, cred("b", 0x0b, 5)},
	}
	for name, creds := range bad {
		if _, err := e.Apply(ctx, s, creds); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if got := fb.take(); len(got) != 0 {
		t.Errorf("refused input touched the device: %v", got)
	}

	badSpec := map[string]func(*plugin.InboundSpec){
		"protocol":  func(s *plugin.InboundSpec) { s.Protocol = "hysteria2" },
		"tcp":       func(s *plugin.InboundSpec) { s.Listen.Network = "tcp" },
		"no port":   func(s *plugin.InboundSpec) { s.Listen.Port = 0 },
		"no tunnel": func(s *plugin.InboundSpec) { s.Tunnel = plugin.Tunnel{} },
		"v6 tunnel": func(s *plugin.InboundSpec) { s.Tunnel.AddrV4 = netip.MustParsePrefix("fd66::1/64") },
		"mtu":       func(s *plugin.InboundSpec) { s.Tunnel.MTU = 1600 },
		"tiny net":  func(s *plugin.InboundSpec) { s.Tunnel.AddrV4 = netip.MustParsePrefix("10.66.4.1/31") },
		"no key": func(s *plugin.InboundSpec) {
			s.Settings = settingsJSON(t, func(x *awgcfg.Settings) { x.PrivateKey = "" })
		},
		"short key": func(s *plugin.InboundSpec) {
			s.Settings = settingsJSON(t, func(x *awgcfg.Settings) { x.PrivateKey = "AAAA" })
		},
		"bad obf": func(s *plugin.InboundSpec) {
			s.Settings = settingsJSON(t, func(x *awgcfg.Settings) { x.Obfuscation.H2 = rng("1") })
		},
		"bad json": func(s *plugin.InboundSpec) { s.Settings = json.RawMessage("[") },
		"no id":    func(s *plugin.InboundSpec) { s.ID = "" },
		"2.0 + hpk": func(s *plugin.InboundSpec) {
			s.Settings = settingsJSON(t, func(x *awgcfg.Settings) { x.Version = "2.0" })
		},
		"low s + hpk": func(s *plugin.InboundSpec) {
			s.Settings = settingsJSON(t, func(x *awgcfg.Settings) { x.Obfuscation.S1 = 11 })
		},
	}
	for name, mut := range badSpec {
		ns := s
		mut(&ns)
		if _, err := e.Apply(ctx, ns, []plugin.UserCred{good}); err == nil {
			t.Errorf("spec %s: accepted", name)
		}
	}
	if got := fb.take(); len(got) != 0 {
		t.Errorf("refused specs touched the device: %v", got)
	}
}

func TestErrorsNeverCarrySecrets(t *testing.T) {
	e, fb, _ := newTestEngine(t)
	fb.createErr = fmt.Errorf("boom")
	s := spec(t, "inb1", 51842, nil)
	_, err := e.Apply(context.Background(), s, nil)
	if err == nil {
		t.Fatal("create error must surface")
	}
	for _, secret := range []string{b64(1), b64(7)} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the error carries a key: %v", err)
		}
	}
	if h := e.Health()[0]; h.State != plugin.RunFailed || !strings.Contains(h.Detail, "boom") {
		t.Errorf("health %+v", h)
	}
	fb.createErr = nil
	mustApply(t, e, s) // a retry of the same spec rebuilds it
	if h := e.Health()[0]; h.State != plugin.RunRunning {
		t.Errorf("after the retry: %+v", h)
	}
}

func TestPortClashBetweenInbounds(t *testing.T) {
	e, _, _ := newTestEngine(t)
	mustApply(t, e, spec(t, "inb1", 51842, nil))
	_, err := e.Apply(context.Background(), spec(t, "inb2", 51842, nil), nil)
	if err == nil || !strings.Contains(err.Error(), "already served") {
		t.Fatalf("err = %v", err)
	}
	if h := e.Health(); len(h) != 1 || h[0].InboundID != "inb1" || h[0].State != plugin.RunRunning {
		t.Errorf("the first inbound must be untouched: %+v", h)
	}
}

func TestTwoInboundsAreIndependent(t *testing.T) {
	e, fb, _ := newTestEngine(t)
	a := spec(t, "inb1", 51842, nil)
	b := spec(t, "inb2", 51843, func(s *awgcfg.Settings) { s.PrivateKey = b64(2); s.Obfuscation.S1 = 31 })
	b.Tunnel.AddrV4 = netip.MustParsePrefix("10.66.8.1/22")
	b.Tunnel.AddrV6 = netip.Prefix{}
	c1 := cred("a", 0x0a, 5)
	c2 := credData(cred("b", 0x0b, 5), func(j *credJSON) { j.AllowedIPs = []string{"10.66.8.5/32"} })
	mustApply(t, e, a, c1)
	mustApply(t, e, b, c2)
	if !fb.hasPeer("mgawg51842", 0x0a) || fb.hasPeer("mgawg51842", 0x0b) || !fb.hasPeer("mgawg51843", 0x0b) {
		t.Fatal("peers must stay on their own interface")
	}
	if err := e.Remove(context.Background(), "inb1"); err != nil {
		t.Fatal(err)
	}
	if fb.devs["mgawg51842"] != nil || fb.devs["mgawg51843"] == nil {
		t.Error("Remove stops only its inbound")
	}
	if err := e.Remove(context.Background(), "unknown"); err != nil {
		t.Errorf("Remove of an unknown id is not an error: %v", err)
	}
	if len(e.Observed()) != 1 {
		t.Error("a removed inbound is forgotten")
	}
}

func TestBatchesAndRemovalsFirst(t *testing.T) {
	e, fb, _ := newTestEngine(t)
	s := spec(t, "inb1", 51842, nil)
	s.Tunnel.AddrV4 = netip.MustParsePrefix("10.66.0.1/21") // 2046 hosts
	mk := func(from, n int) []plugin.UserCred {
		var out []plugin.UserCred
		for i := from; i < from+n; i++ {
			var k [32]byte
			k[0], k[1], k[2] = byte(i), byte(i>>8), 1
			d, _ := json.Marshal(credJSON{PublicKey: base64.StdEncoding.EncodeToString(k[:]), AllowedIPs: []string{fmt.Sprintf("10.66.%d.%d/32", 1+i/250, 2+i%250)}})
			out = append(out, plugin.UserCred{CredID: fmt.Sprintf("c%d", i), Data: d})
		}
		return out
	}
	mustApply(t, e, s, mk(0, 600)...)
	calls := fb.take()
	if len(calls) != 4 || calls[0] != "create:"+p1 { // create, then 250 + 250 + 100 peers
		t.Fatalf("calls: %d", len(calls))
	}

	// 300 peers leave, 300 arrive: every removal goes out before any addition, in batches of at most 250
	mustApply(t, e, s, mk(300, 600)...)
	calls = fb.take()
	var ops []string
	for _, c := range calls {
		list := strings.Split(strings.TrimPrefix(c, "peers:"+p1+":"), ",")
		if len(list) > peerBatch {
			t.Errorf("a batch of %d peers", len(list))
		}
		ops = append(ops, list...)
	}
	if len(ops) != 600 || len(calls) != 3 {
		t.Fatalf("%d ops in %d calls", len(ops), len(calls))
	}
	for i, op := range ops {
		if (i < 300) != strings.HasPrefix(op, "-") {
			t.Fatalf("op %d is %s: removals must all come first", i, op)
		}
	}
}

func TestHealthNoticesALostInterface(t *testing.T) {
	e, fb, _ := newTestEngine(t)
	s := spec(t, "inb1", 51842, nil)
	mustApply(t, e, s, cred("a", 0x0a, 5))
	fb.mutate(p1, func(d *fakeDev) { d.up = false })
	if h := e.Health()[0]; h.State != plugin.RunFailed || h.Detail != "iface_down" {
		t.Fatalf("health %+v", h)
	}
	fb.mutate(p1, func(d *fakeDev) { d.up = true })
	rep := mustApply(t, e, s, cred("a", 0x0a, 5)) // same spec: Apply rebuilds what Health found broken
	if !rep.Restarted {
		t.Error("rebuilding a lost interface counts as a restart")
	}
	if h := e.Health()[0]; h.State != plugin.RunRunning {
		t.Errorf("after rebuild %+v", h)
	}

	e.portListening = func(uint16) bool { return false }
	if h := e.Health()[0]; h.State != plugin.RunFailed || h.Detail != "port_not_listening" {
		t.Errorf("userspace: a port nobody listens on is a failure: %+v", h)
	}
	fb.name = "kernel"
	mustApply(t, e, s, cred("a", 0x0a, 5))
	if h := e.Health()[0]; h.State != plugin.RunRunning {
		t.Errorf("the kernel backend is not judged by /proc/net/udp: %+v", h)
	}
}

func TestAwgHealth(t *testing.T) {
	e, fb, clk := newTestEngine(t)
	mustApply(t, e, spec(t, "inb1", 51842, nil), cred("a", 0x0a, 5), cred("b", 0x0b, 6), cred("c", 0x0c, 7))
	fb.mutate(p1, func(d *fakeDev) {
		d.peers[pk(0x0a)].hs = clk.Now().Add(-10 * time.Second)
		d.peers[pk(0x0b)].hs = clk.Now().Add(-1000 * time.Second)
	})
	fb.unknown = 3
	var hr HealthReporter = e
	h := hr.AwgHealth()
	if len(h) != 1 {
		t.Fatalf("%+v", h)
	}
	x := h[0]
	if x.InboundID != "inb1" || x.Backend != "userspace" || !x.IfaceUp || x.Peers != 3 || x.PeersHandshaken != 2 || x.PeersOnline != 1 ||
		x.NewestHandshakeUnix != clk.Now().Add(-10*time.Second).Unix() || x.UnknownPeerEvents != 3 {
		t.Errorf("%+v", x)
	}
	if h = hr.AwgHealth(); h[0].UnknownPeerEvents != 0 {
		t.Error("the unknown-peer counter restarts after each report")
	}
}

func TestCloseDestroysEverythingAndRefusesFurtherWork(t *testing.T) {
	e, fb, _ := newTestEngine(t)
	mustApply(t, e, spec(t, "inb1", 51842, nil))
	mustApply(t, e, spec(t, "inb2", 51843, nil))
	if err := e.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fb.devs) != 0 {
		t.Errorf("interfaces left: %v", fb.devs)
	}
	if _, err := e.Apply(context.Background(), spec(t, "inb3", 51844, nil), nil); err == nil {
		t.Error("Apply after Close")
	}
}

func TestConcurrentUseIsRaceFree(t *testing.T) {
	e, fb, _ := newTestEngine(t)
	e.kickDelay = time.Millisecond
	s := spec(t, "inb1", 51842, nil)
	mustApply(t, e, s, cred("a", 0x0a, 5))
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, _ = e.Collect(context.Background())
				_ = e.Observed()
				_ = e.Health()
				_ = e.AwgHealth()
				_, _ = e.Kick(context.Background(), []string{"a"})
			}
		}()
	}
	for i := 0; i < 60; i++ {
		fb.mutate(p1, func(d *fakeDev) {
			if p := d.peers[pk(0x0a)]; p != nil {
				p.hs = time.Now()
			}
		})
		creds := []plugin.UserCred{cred("a", 0x0a, 5)}
		if i%2 == 0 {
			creds = append(creds, cred("b", 0x0b, 6))
		}
		sp := s
		if i%20 == 19 {
			sp = spec(t, "inb1", 51842, func(x *awgcfg.Settings) { x.Obfuscation.S2 = 30 + i })
		}
		if _, err := e.Apply(context.Background(), sp, creds); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestParsePeersAcceptsWhatThePanelSends(t *testing.T) {
	cfg := nodeConfig{tunnel: spec(t, "x", 1, nil).Tunnel}
	got, err := parsePeers(cfg, []plugin.UserCred{cred("a", 0x0a, 5)})
	if err != nil {
		t.Fatal(err)
	}
	w := got[pk(0x0a)]
	if w == nil || w.credID != "a" || len(w.allowed) != 2 || w.psk == nil || *w.psk != pk(0x0a+100) {
		t.Fatalf("%+v", w)
	}
	// the panel writes the fields in this order; the bytes are hashed, so a decoder must not care but must read them
	var c credJSON
	if err := json.Unmarshal(cred("a", 1, 5).Data, &c); err != nil || c.PublicKey == "" {
		t.Fatal(err)
	}
}
