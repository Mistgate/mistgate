package warp

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/plugin"
)

// fakePlane is a dataplane that records calls and lets a test script what Stat reports.
type fakePlane struct {
	mu          sync.Mutex
	ups         []linkSpec
	setEps      []netip.AddrPort
	reasserts   []routeSpec
	downs       int
	cleanups    int
	upErr       error
	present     bool
	linkUp      bool
	hs          time.Time
	rx, tx      uint64
	ep          netip.AddrPort
	findings    []Finding
	preflights  int
	reassertErr error
}

func (f *fakePlane) Up(_ context.Context, ls linkSpec) (string, netip.AddrPort, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ups = append(f.ups, ls)
	if f.upErr != nil {
		return "", netip.AddrPort{}, f.upErr
	}
	f.present, f.linkUp, f.ep = true, true, ls.Endpoint
	return "kernel", ls.Endpoint, nil
}

func (f *fakePlane) SetEndpoint(_ context.Context, ep netip.AddrPort) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setEps = append(f.setEps, ep)
	f.ep = ep
	return nil
}

func (f *fakePlane) Stat(context.Context) (stat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return stat{LinkPresent: f.present, LinkUp: f.linkUp, Handshake: f.hs, Rx: f.rx, Tx: f.tx, Endpoint: f.ep}, nil
}

func (f *fakePlane) Reassert(_ context.Context, rs routeSpec) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reasserts = append(f.reasserts, rs)
	return false, f.reassertErr
}

func (f *fakePlane) DownLink(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downs++
	f.present, f.linkUp = false, false
	return nil
}

func (f *fakePlane) Cleanup(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleanups++
	f.present, f.linkUp = false, false
	return nil
}

func (f *fakePlane) Preflight(context.Context) []Finding {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preflights++
	return f.findings
}

func (f *fakePlane) lastRoute() routeSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reasserts[len(f.reasserts)-1]
}

type fakeProbe struct {
	mu   sync.Mutex
	flag string
	colo string
	a, b error
}

func (p *fakeProbe) A(context.Context) (string, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.flag, p.colo, p.a
}

func (p *fakeProbe) B(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.b
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var (
	key1 = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))
	key2 = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))
)

func spec() *plugin.WarpSpec {
	return &plugin.WarpSpec{
		Enabled: true, PrivateKey: key1, PeerPublicKey: key2,
		EndpointV4: "198.51.100.7", Ports: []uint16{2408, 500, 1701},
		AddressV4: "172.16.0.2/32", MTU: 1280,
	}
}

type rig struct {
	m   *Manager
	pl  *fakePlane
	pr  *fakeProbe
	clk *clock
	ev  *[]Event
}

func newRig(t *testing.T) *rig {
	t.Helper()
	pl := &fakePlane{}
	pr := &fakeProbe{flag: "on", colo: "FRA"}
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	var evs []Event
	var mu sync.Mutex
	m := newManager(Options{
		Now: clk.now, AllowPrivate: true,
		Emit: func(e Event) { mu.Lock(); evs = append(evs, e); mu.Unlock() },
	}, pl, pr)
	return &rig{m: m, pl: pl, pr: pr, clk: clk, ev: &evs}
}

// healthy makes the fake peer look alive: a fresh handshake at the current fake time.
func (r *rig) healthy() {
	r.pl.mu.Lock()
	r.pl.hs = r.clk.now()
	r.pl.mu.Unlock()
}

// tick advances to the next check and runs whatever is due.
func (r *rig) tick(d time.Duration) {
	r.clk.add(d)
	r.m.poll(context.Background())
}

func (r *rig) events(code string) []Event {
	var out []Event
	for _, e := range *r.ev {
		if e.Code == code {
			out = append(out, e)
		}
	}
	return out
}

func TestPreflightClashRefusesToTouchTheHost(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.pl.findings = []Finding{{ID: "forward_drop", Detail: "docker"}, {ID: "rule_pref_in_use", Detail: "pref 90 belongs to table 100"}}
	err := r.m.Apply(ctx, spec())
	if err == nil || !strings.Contains(err.Error(), "rule_pref_in_use") {
		t.Fatalf("%v", err)
	}
	if len(r.pl.ups) != 0 || len(r.pl.reasserts) != 0 || r.m.Configured() {
		t.Fatal("the host was touched despite the clash")
	}
	if att := r.events("warp_needs_attention"); len(att) != 1 || att[0].Params["reason"] != "rule_pref_in_use" {
		t.Fatalf("%+v", att)
	}
	if err := r.m.SetRoutedSubnets(ctx, []netip.Prefix{netip.MustParsePrefix("10.66.0.0/24")}); err == nil {
		t.Fatal("subnets accepted despite the clash")
	}
	// the clash is resolved: the next reconcile goes through, and only a FORWARD drop remains as a warning
	r.pl.findings = []Finding{{ID: "forward_drop", Detail: "docker"}}
	if err := r.m.Apply(ctx, spec()); err != nil {
		t.Fatal(err)
	}
	n := r.pl.preflights
	r.m.Apply(ctx, spec())
	if r.pl.preflights != n {
		t.Fatal("the preflight must run once, not on every reconcile")
	}
}

func TestNodeWithoutWarpDoesNotTouchTheHost(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 3; i++ {
		if err := r.m.Apply(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
		if err := r.m.SetRoutedSubnets(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.pl.reasserts) != 0 || r.pl.downs != 0 || r.pl.preflights != 0 {
		t.Fatalf("reasserts=%d downs=%d preflights=%d", len(r.pl.reasserts), r.pl.downs, r.pl.preflights)
	}
}

func TestApplyNilAndHealth(t *testing.T) {
	r := newRig(t)
	if _, ok := r.m.Health(); ok {
		t.Fatal("health reported without a spec")
	}
	if err := r.m.Apply(context.Background(), spec()); err != nil {
		t.Fatal(err)
	}
	if err := r.m.Apply(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if r.m.Configured() {
		t.Fatal("configured after nil")
	}
	rs := r.pl.lastRoute()
	if rs.Configured {
		t.Fatalf("nil spec without subnets must leave nothing behind: %+v", rs)
	}
}

func TestApplyBringsUpOnceAndIsIdempotent(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.m.Apply(ctx, spec()); err != nil {
		t.Fatal(err)
	}
	if len(r.pl.ups) != 1 {
		t.Fatalf("ups=%d", len(r.pl.ups))
	}
	ls := r.pl.ups[0]
	if ls.Endpoint != netip.MustParseAddrPort("198.51.100.7:2408") || ls.MTU != 1280 || ls.KeepaliveSeconds != 25 || ls.Backend != "auto" {
		t.Fatalf("link spec: %+v", ls)
	}
	h, _ := r.m.Health()
	if h.State != StateStarting || h.Backend != "kernel" || h.Endpoint != "198.51.100.7:2408" {
		t.Fatalf("health: %+v", h)
	}
	// the same spec again (a reconcile): no Up, routing re-asserted
	n := len(r.pl.reasserts)
	if err := r.m.Apply(ctx, spec()); err != nil {
		t.Fatal(err)
	}
	if len(r.pl.ups) != 1 || len(r.pl.reasserts) != n+1 {
		t.Fatalf("ups=%d reasserts=%d->%d", len(r.pl.ups), n, len(r.pl.reasserts))
	}
	// a different key is a new identity
	s2 := spec()
	s2.PrivateKey = key2
	if err := r.m.Apply(ctx, s2); err != nil {
		t.Fatal(err)
	}
	if len(r.pl.ups) != 2 {
		t.Fatalf("ups=%d", len(r.pl.ups))
	}
}

func TestApplyInvalidSpecKeepsTheTunnel(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.m.Apply(ctx, spec()); err != nil {
		t.Fatal(err)
	}
	bad := spec()
	bad.EndpointV4 = "engage.example.com"
	err := r.m.Apply(ctx, bad)
	if err == nil {
		t.Fatal("hostname endpoint accepted")
	}
	if strings.Contains(err.Error(), key1) || len(r.pl.ups) != 1 || r.pl.downs != 0 {
		t.Fatalf("err=%v ups=%d downs=%d", err, len(r.pl.ups), r.pl.downs)
	}
	if !r.m.Configured() {
		t.Fatal("the old spec was dropped")
	}
}

func TestStartingBecomesUpAfterTwoGoodChecks(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.m.Apply(ctx, spec())
	r.healthy()
	r.tick(5 * time.Second) // watch deadline is 15 s away, the first check is due (FastInterval)
	if h, _ := r.m.Health(); h.State != StateStarting {
		t.Fatalf("one good check is not enough: %v", h.State)
	}
	r.healthy()
	r.tick(5 * time.Second)
	h, _ := r.m.Health()
	if h.State != StateUp || !h.ProbeCloudflareOK || !h.ProbeOtherOK || h.WarpFlag != "on" || h.Colo != "FRA" || h.LastError != "" {
		t.Fatalf("health: %+v", h)
	}
	evs := r.events("warp_state")
	if len(evs) != 2 || evs[0].Params["state"] != "starting" || evs[1].Params["state"] != "up" || evs[1].Params["from"] != "starting" {
		t.Fatalf("events: %+v", evs)
	}
}

// A slow Cloudflare edge: the probe fails, the next one passes, and so on. The state stays "starting" (no two passes in a
// row, no three failures in a row), so Health must describe the LATEST check, not the last failure or the last success.
func TestHealthDescribesTheLatestCheck(t *testing.T) {
	r := newRig(t)
	r.m.Apply(context.Background(), spec())
	r.healthy()
	r.pr.mu.Lock()
	r.pr.a = context.DeadlineExceeded
	r.pr.mu.Unlock()
	r.tick(5 * time.Second)
	h, _ := r.m.Health()
	if h.State != StateStarting || h.LastError != "probe_cloudflare_failed" || h.ProbeCloudflare == nil || h.ProbeCloudflare.OK || h.ProbeCloudflare.FailureCode != "timeout" ||
		h.ProbeOther == nil || !h.ProbeOther.OK || !h.ProbeCloudflare.At.Equal(r.clk.now()) || !h.CheckedAt.Equal(r.clk.now()) {
		t.Fatalf("after a failed check: %+v", h)
	}
	r.pr.mu.Lock()
	r.pr.a = nil
	r.pr.mu.Unlock()
	r.healthy()
	r.tick(5 * time.Second)
	h, _ = r.m.Health()
	if h.State != StateStarting || h.LastError != "" || !h.ProbeCloudflare.OK || h.ProbeCloudflare.FailureCode != "" || h.Failures != 0 {
		t.Fatalf("after a good check the failure of the previous one is stale: %+v", h)
	}
	// the link is down: nothing was probed, so no probe result (not a fake "failed")
	r.pl.mu.Lock()
	r.pl.linkUp = false
	r.pl.mu.Unlock()
	r.tick(5 * time.Second)
	h, _ = r.m.Health()
	if h.ProbeCloudflare != nil || h.ProbeOther != nil || h.LastError == "" || h.CheckedAt.IsZero() {
		t.Fatalf("without a link: %+v", h)
	}
}

func TestProbeFailureCodesAreSafeAndSpecific(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		flag string
		want string
	}{
		{"timeout", context.DeadlineExceeded, "", "timeout"},
		{"http status", &probeHTTPStatusError{probe: "probe A", status: 502}, "", "http_502"},
		{"dns", &net.DNSError{Err: "no such host", Name: "probe.example"}, "", "dns"},
		{"connection", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}, "", "connection"},
		{"invalid trace", errProbeTraceMissing, "", "invalid_trace"},
		{"warp off", nil, "off", "warp_off"},
		{"unexpected flag", nil, "unknown", "invalid_trace"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeFailureCode(tc.err, tc.flag); got != tc.want {
				t.Fatalf("probeFailureCode() = %q, want %q", got, tc.want)
			}
		})
	}
	if got := (&httpProber{}).limit(); got != 12*time.Second {
		t.Fatalf("default probe timeout = %s, want 12s", got)
	}
}

func bringUp(t *testing.T, r *rig) {
	t.Helper()
	if err := r.m.Apply(context.Background(), spec()); err != nil {
		t.Fatal(err)
	}
	r.healthy()
	r.tick(5 * time.Second)
	r.healthy()
	r.tick(5 * time.Second)
	if h, _ := r.m.Health(); h.State != StateUp {
		t.Fatalf("not up: %+v", h)
	}
}

func TestThreeFailuresGoDownTwoSuccessesComeBack(t *testing.T) {
	r := newRig(t)
	bringUp(t, r)
	r.pr.mu.Lock()
	r.pr.b = errors.New("timeout")
	r.pr.mu.Unlock()
	for i := 1; i <= 3; i++ {
		r.healthy()
		r.tick(30 * time.Second)
		h, _ := r.m.Health()
		wantDown := i == 3
		if (h.State == StateDown) != wantDown {
			t.Fatalf("after %d failures state=%v", i, h.State)
		}
		if h.ProbeOtherOK || !h.ProbeCloudflareOK {
			t.Fatalf("probe flags: %+v", h)
		}
	}
	h, _ := r.m.Health()
	if !strings.HasPrefix(h.LastError, "probe_other_failed") || h.Failures != 3 {
		t.Fatalf("health: %+v", h)
	}
	r.pr.mu.Lock()
	r.pr.b = nil
	r.pr.mu.Unlock()
	r.healthy()
	r.tick(30 * time.Second)
	if h, _ := r.m.Health(); h.State != StateDown {
		t.Fatalf("one success must not bring it up: %v", h.State)
	}
	r.healthy()
	r.tick(30 * time.Second)
	if h, _ := r.m.Health(); h.State != StateUp || h.LastError != "" || h.Failures != 0 {
		t.Fatalf("health: %+v", h)
	}
	if ev := r.events("warp_state"); ev[len(ev)-2].Params["state"] != "down" || !ev[len(ev)-2].Warn {
		t.Fatalf("events: %+v", ev)
	}
}

func TestWarpFlagOffAndStaleHandshakeAreFailures(t *testing.T) {
	r := newRig(t)
	bringUp(t, r)
	r.pr.mu.Lock()
	r.pr.flag = "off"
	r.pr.mu.Unlock()
	r.healthy()
	r.tick(30 * time.Second)
	if h, _ := r.m.Health(); h.LastError != "warp_flag_off" || h.ProbeCloudflareOK {
		t.Fatalf("%+v", h)
	}
	r.pr.mu.Lock()
	r.pr.flag = "plus"
	r.pr.mu.Unlock()
	// no fresh handshake for more than 180 s
	r.tick(200 * time.Second)
	if h, _ := r.m.Health(); !strings.HasPrefix(h.LastError, "handshake_stale") {
		t.Fatalf("%+v", h)
	}
}

func TestLadderOrderAndCooldown(t *testing.T) {
	r := newRig(t)
	bringUp(t, r)
	// the peer stopped answering: no handshake at all (a fresh one would make a rotation pointless)
	r.pl.mu.Lock()
	r.pl.hs = time.Time{}
	r.pl.mu.Unlock()
	r.pr.mu.Lock()
	r.pr.a = errors.New("timeout")
	r.pr.mu.Unlock()
	reasserts := len(r.pl.reasserts)
	fail := func() {
		r.tick(30 * time.Second)
	}
	fail()
	fail()
	if len(r.pl.setEps) != 0 {
		t.Fatal("rotated before DOWN")
	}
	fail() // DOWN + step 0: re-assert
	h, _ := r.m.Health()
	if h.State != StateDown || !strings.Contains(h.LastError, "ladder: reassert") || len(r.pl.reasserts) <= reasserts {
		t.Fatalf("%+v reasserts %d->%d", h, reasserts, len(r.pl.reasserts))
	}
	fail() // rotate 500
	fail() // rotate 1701
	want := []string{"198.51.100.7:500", "198.51.100.7:1701"}
	for i, w := range want {
		if r.pl.setEps[i].String() != w {
			t.Fatalf("rotation %d: %v want %s", i, r.pl.setEps, w)
		}
	}
	if len(r.events("warp_needs_attention")) != 0 {
		t.Fatal("attention raised before the ladder ran out")
	}
	fail() // refresh requested
	fail() // owner action needed
	att := r.events("warp_needs_attention")
	if len(att) != 2 || att[0].Params["reason"] != "refresh_requested" || att[1].Params["reason"] != "down_after_ladder" {
		t.Fatalf("attention: %+v", att)
	}
	// cooling down: no more events; the endpoints keep being walked, one per failed check
	nEps := len(r.pl.setEps)
	for i := 0; i < 5; i++ {
		fail()
	}
	if len(r.pl.setEps) != nEps+5 || len(r.events("warp_needs_attention")) != 2 {
		t.Fatalf("cooldown: %d rotations, %d events", len(r.pl.setEps)-nEps, len(r.events("warp_needs_attention")))
	}
	// after the cooldown it starts over
	r.clk.add(ladderCooldown)
	att0 := len(r.events("warp_needs_attention"))
	fail() // restart: step 0 re-asserts, nothing rotates
	if len(r.pl.setEps) != nEps+5 {
		t.Fatal("step 0 must not rotate")
	}
	fail()
	fail()
	fail() // refresh requested again
	if len(r.events("warp_needs_attention")) != att0+1 {
		t.Fatalf("ladder did not restart after the cooldown: %d events", len(r.events("warp_needs_attention")))
	}
	// recovery resets the ladder
	r.pr.mu.Lock()
	r.pr.a = nil
	r.pr.mu.Unlock()
	r.healthy() // the peer answers again
	r.tick(30 * time.Second)
	r.healthy()
	r.tick(30 * time.Second)
	if h, _ := r.m.Health(); h.State != StateUp || h.LastError != "" {
		t.Fatalf("%+v", h)
	}
}

func TestFreshHandshakeMeansNoRotation(t *testing.T) {
	r := newRig(t)
	bringUp(t, r)
	r.pr.mu.Lock()
	r.pr.a = errors.New("timeout")
	r.pr.mu.Unlock()
	for i := 0; i < 12; i++ {
		r.healthy() // the tunnel handshakes fine, the probes do not: a different port cannot help
		r.tick(30 * time.Second)
	}
	if h, _ := r.m.Health(); h.State != StateDown {
		t.Fatalf("%+v", h)
	}
	if len(r.pl.setEps) != 0 {
		t.Fatalf("rotated although the handshake is fresh: %v", r.pl.setEps)
	}
	if len(r.events("warp_needs_attention")) != 0 {
		t.Fatal("the ladder must not run past a step it cannot take")
	}
}

func TestHandshakeWatchRotatesThenGivesUp(t *testing.T) {
	r := newRig(t)
	if err := r.m.Apply(context.Background(), spec()); err != nil {
		t.Fatal(err)
	}
	// no handshake: after HandshakeWait (15 s) the next endpoint
	r.tick(14 * time.Second)
	if len(r.pl.setEps) != 0 {
		t.Fatal("rotated early")
	}
	r.tick(2 * time.Second)
	if len(r.pl.setEps) != 1 || r.pl.setEps[0].Port() != 500 {
		t.Fatalf("%v", r.pl.setEps)
	}
	r.tick(16 * time.Second)
	if len(r.pl.setEps) != 2 || r.pl.setEps[1].Port() != 1701 {
		t.Fatalf("%v", r.pl.setEps)
	}
	// all three tried: the watch stops
	r.tick(16 * time.Second)
	if len(r.pl.setEps) != 2 {
		t.Fatalf("rotated past the last candidate: %v", r.pl.setEps)
	}
	h, _ := r.m.Health()
	if !strings.HasPrefix(h.LastError, "handshake_never") {
		t.Fatalf("%+v", h)
	}
}

func TestHandshakeWatchStopsOnHandshake(t *testing.T) {
	r := newRig(t)
	r.m.Apply(context.Background(), spec())
	r.tick(10 * time.Second)
	r.healthy() // the peer answered on the first port
	r.tick(6 * time.Second)
	if len(r.pl.setEps) != 0 {
		t.Fatalf("rotated although the handshake came: %v", r.pl.setEps)
	}
}

func TestPauseKeepsRulesAndFailsClosed(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.m.SetRoutedSubnets(ctx, []netip.Prefix{netip.MustParsePrefix("10.66.4.1/22"), netip.MustParsePrefix("10.66.4.9/22")}); err != nil {
		t.Fatal(err)
	}
	rs := r.pl.lastRoute()
	if !rs.Configured || len(rs.Subnets) != 1 || rs.Subnets[0] != netip.MustParsePrefix("10.66.4.0/22") {
		t.Fatalf("subnets must be masked and deduplicated: %+v", rs)
	}
	bringUp(t, r)
	s := spec()
	s.Enabled = false
	if err := r.m.Apply(ctx, s); err != nil {
		t.Fatal(err)
	}
	h, _ := r.m.Health()
	if h.State != StateDisabled || r.pl.downs != 1 {
		t.Fatalf("%+v downs=%d", h, r.pl.downs)
	}
	rs = r.pl.lastRoute()
	if !rs.Configured || rs.LinkUp || len(rs.Subnets) != 1 {
		t.Fatalf("paused: the table and the subnet rules stay (closed): %+v", rs)
	}
	if _, err := r.m.Egress().TCP("203.0.113.10:80"); !errors.Is(err, ErrNotActive) {
		t.Fatalf("egress while paused: %v", err)
	}
	if err := r.m.Egress().CheckUDP("203.0.113.10:53"); !errors.Is(err, ErrNotActive) {
		t.Fatalf("egress while paused: %v", err)
	}
	// removing WARP while a subnet still routes through it keeps the closed table
	if err := r.m.Apply(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if rs = r.pl.lastRoute(); !rs.Configured || rs.LinkUp {
		t.Fatalf("%+v", rs)
	}
	if err := r.m.SetRoutedSubnets(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if rs = r.pl.lastRoute(); rs.Configured {
		t.Fatalf("nothing should remain: %+v", rs)
	}
}

func TestCleanupResetsEverything(t *testing.T) {
	r := newRig(t)
	bringUp(t, r)
	if err := r.m.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.pl.cleanups != 1 || r.m.Configured() {
		t.Fatalf("cleanups=%d configured=%v", r.pl.cleanups, r.m.Configured())
	}
	if _, err := r.m.Egress().TCP("203.0.113.10:80"); !errors.Is(err, ErrNotActive) {
		t.Fatalf("%v", err)
	}
}

func TestUnavailableThenRecovers(t *testing.T) {
	r := newRig(t)
	r.pl.upErr = ErrUnavailable
	err := r.m.Apply(context.Background(), spec())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	h, _ := r.m.Health()
	if h.State != StateUnavailable || h.LastError != "backend_unavailable" {
		t.Fatalf("%+v", h)
	}
	if !r.pl.lastRoute().Configured {
		t.Fatal("routing must stay closed while unavailable")
	}
	if _, err := r.m.Egress().TCP("203.0.113.10:80"); !errors.Is(err, ErrNotActive) {
		t.Fatalf("%v", err)
	}
	r.pl.mu.Lock()
	r.pl.upErr = nil
	r.pl.mu.Unlock()
	r.tick(31 * time.Second) // the regular check retries
	if h, _ := r.m.Health(); h.State != StateStarting {
		t.Fatalf("%+v", h)
	}
}

func TestVanishedDeviceIsRecreated(t *testing.T) {
	r := newRig(t)
	bringUp(t, r)
	r.pl.mu.Lock()
	r.pl.present, r.pl.linkUp = false, false
	r.pl.mu.Unlock()
	r.healthy()
	r.tick(30 * time.Second)
	if len(r.pl.ups) != 2 {
		t.Fatalf("ups=%d", len(r.pl.ups))
	}
	// a reconcile with the same spec repairs it too
	r.pl.mu.Lock()
	r.pl.present, r.pl.linkUp = false, false
	r.pl.mu.Unlock()
	if err := r.m.Apply(context.Background(), spec()); err != nil {
		t.Fatal(err)
	}
	if len(r.pl.ups) != 3 {
		t.Fatalf("ups=%d", len(r.pl.ups))
	}
}

func TestLinkDownIsAFailureAndRoutesFollowTheLink(t *testing.T) {
	r := newRig(t)
	bringUp(t, r)
	if !r.pl.lastRoute().LinkUp {
		t.Fatal("route spec must say the link is up")
	}
	r.pl.mu.Lock()
	r.pl.linkUp = false
	r.pl.mu.Unlock()
	r.tick(30 * time.Second)
	h, _ := r.m.Health()
	if h.LastError != "link_down" || r.pl.lastRoute().LinkUp {
		t.Fatalf("%+v %+v", h, r.pl.lastRoute())
	}
}

func TestIPv6SpecFlowsToTheDataplane(t *testing.T) {
	r := newRig(t)
	s := spec()
	s.AddressV6 = "2001:db8::2/128"
	s.EndpointV6 = "2001:db8::1"
	s.Reserved = []byte{1, 2, 3}
	if err := r.m.Apply(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	ls := r.pl.ups[0]
	if !ls.AddrV6.IsValid() || len(ls.Endpoints) != 6 || ls.Endpoints[3].Addr().Is4() || len(ls.Reserved) != 3 {
		t.Fatalf("%+v", ls)
	}
	rs := r.pl.lastRoute()
	if !rs.HasV6 || !rs.Kernel || len(rs.Reserved) != 3 {
		t.Fatalf("%+v", rs)
	}
}

func TestRunStopsWithContext(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.m.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// A failed reassert must not be remembered as applied: the next reconcile with the same subnets retries it and keeps
// failing (so the agent keeps the dependent inbounds blocked) until the routes really are installed.
func TestRoutedSubnetsFailClosedUntilReassertSucceeds(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	subs := []netip.Prefix{netip.MustParsePrefix("10.66.4.0/22")}
	r.pl.reassertErr = errors.New("netlink: busy")
	if err := r.m.SetRoutedSubnets(ctx, subs); err == nil {
		t.Fatal("a failed reassert was reported as success")
	}
	n := len(r.pl.reasserts)
	if err := r.m.SetRoutedSubnets(ctx, subs); err == nil {
		t.Fatal("the same list after a failed reassert returned success without retrying")
	}
	if len(r.pl.reasserts) != n+1 {
		t.Fatalf("no retry: %d reasserts, want %d", len(r.pl.reasserts), n+1)
	}
	r.pl.reassertErr = nil
	if err := r.m.SetRoutedSubnets(ctx, subs); err != nil {
		t.Fatal(err)
	}
	n = len(r.pl.reasserts)
	if err := r.m.SetRoutedSubnets(ctx, subs); err != nil || len(r.pl.reasserts) != n {
		t.Fatalf("a clean, unchanged list must not touch the host again: err=%v reasserts=%d->%d", err, n, len(r.pl.reasserts))
	}
	// the same for removing the last subnet
	r.pl.reassertErr = errors.New("netlink: busy")
	if err := r.m.SetRoutedSubnets(ctx, nil); err == nil {
		t.Fatal("failed removal reported as success")
	}
	if err := r.m.SetRoutedSubnets(ctx, nil); err == nil {
		t.Fatal("a failed removal was forgotten")
	}
}
