//go:build linux

package agent

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/awg"
	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/node/warp"
)

// The glue of the L3 side against the REAL pieces: the agent with the real AWG engine (userspace), the
// real WARP manager (kernel WireGuard), the real tunnel firewall of hostctl, all inside a network namespace made for the
// test (mg3-n3-live-*), the panel being the fake one of this package. It checks what the fakes cannot: that the order of the
// reconcile leaves no moment without the firewall or without the WARP route, that a failed WARP takes the interface down, and
// that retire leaves nothing on the host.
//
// Opt-in: MG_ROOT_TESTS=1, as root, with ip, nft and /dev/net/tun.
//
//	MG_ROOT_TESTS=1 go test ./internal/node/agent -run TestLiveL3Glue -v
//
// The test process (in the namespace of the host) creates the namespace and runs this same binary inside it, so every
// link, rule and table the agent makes exists only there and goes with the namespace.

func TestLiveL3Glue(t *testing.T) {
	if os.Getenv("MG_ROOT_TESTS") == "" || os.Geteuid() != 0 {
		t.Skip("set MG_ROOT_TESTS=1 and run as root")
	}
	for _, tool := range []string{"ip", "nft"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Skip("no /dev/net/tun")
	}
	ns := fmt.Sprintf("mg3-n3-live-%d", os.Getpid())
	if out, err := exec.Command("ip", "netns", "add", ns).CombinedOutput(); err != nil {
		t.Fatalf("ip netns add: %v: %s", err, out)
	}
	defer exec.Command("ip", "netns", "del", ns).Run()
	if out, err := exec.Command("ip", "-n", ns, "link", "set", "lo", "up").CombinedOutput(); err != nil { // the fake panel listens on loopback
		t.Fatalf("lo up: %v: %s", err, out)
	}
	cmd := exec.Command("ip", "netns", "exec", ns, os.Args[0], "-test.run=^TestLiveL3Helper$", "-test.v", "-test.timeout=120s")
	cmd.Env = append(os.Environ(), "MG3_LIVE_HELPER=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestLiveL3Helper") {
		t.Fatalf("the glue test inside the namespace failed: %v\n%s", err, out)
	}
}

// liveHost is the fake host for everything except the tunnel firewall, which is the real one: the fake never writes the
// sysctl and journald files of the machine.
type liveHost struct {
	*fakeHost
	real hostctl.TunnelHost
}

// Cleanup is the tunnel part of the real Cleanup (the table and our links), inside the namespace: the real one also deletes
// the sysctl and journald files of the machine, which a test must not touch.
func (h liveHost) Cleanup(ctx context.Context) error {
	_ = h.fakeHost.Cleanup(ctx)
	return hostctl.CleanupTunnels(ctx)
}

func (h liveHost) SetTunnels(ctx context.Context, ts []hostctl.Tunnel) error {
	return h.real.SetTunnels(ctx, ts)
}
func (h liveHost) TunnelCounters(ctx context.Context) (map[uint16]uint64, error) {
	return h.real.TunnelCounters(ctx)
}

func sh(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func try(args ...string) (string, bool) {
	out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
	return string(out), err == nil
}

func b64key() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func rangeOf(s string) awgcfg.Range { r, _ := awgcfg.ParseRange(s); return r }

func TestLiveL3Helper(t *testing.T) {
	if os.Getenv("MG3_LIVE_HELPER") == "" {
		t.Skip("runs inside the namespace of TestLiveL3Glue")
	}
	// The AWG settings the engine takes: the server key, the 3.1 parameters.
	settings, err := (awgcfg.Settings{Version: awgcfg.Version31, PrivateKey: b64key(), Obfuscation: awgcfg.Obfuscation{
		Jc: 6, Jmin: 10, Jmax: 50, S1: 24, S2: 24, S3: 24, S4: 24,
		H1: rangeOf("1"), H2: rangeOf("2"), H3: rangeOf("3"), H4: rangeOf("4"),
		HeaderProtectionKey: b64key(), RandomTrailers: true, ContentPaddingAddition: rangeOf("2-10"),
	}}).NodeJSON()
	if err != nil {
		t.Fatal(err)
	}
	awgInbound := func(egress string) *pb.InboundState {
		return &pb.InboundState{InboundId: "inb_awg", CredsReplace: true,
			Creds: []*pb.Credential{{CredId: "crd_a", UserId: "usr_a", DeviceId: "dev_a",
				DataJson: fmt.Sprintf(`{"public_key":%q,"allowed_ips":["10.66.4.5/32","fd66:66:0:1::5/128"],"psk":%q}`, b64key(), b64key())}},
			Spec: &pb.InboundSpec{InboundId: "inb_awg", Protocol: awg.Protocol, ProfileId: "prf_awg", SpecVersion: 1, Enabled: true,
				Listen: &pb.Listen{Network: "udp", Port: 51842}, Egress: egress, SettingsJson: string(settings),
				Tunnel: &pb.Tunnel{AddrV4: "10.66.4.1/22", AddrV6: "fd66:66:0:1::1/64", Mtu: 1280}}}
	}
	warpSpec := func() *pb.WarpSpec {
		return &pb.WarpSpec{Enabled: true, PrivateKey: b64key(), PeerPublicKey: b64key(), EndpointV4: "203.0.113.10",
			Ports: []uint32{2408, 500}, AddressV4: "172.16.0.2/32", AddressV6: "fd00:16::2/128", Mtu: 1280, Backend: "kernel"}
	}

	var wm *warp.Manager
	h := newHarness(t, harnessOpts{
		extra: map[string]engine.Factory{awg.Protocol: awg.FactoryFor("userspace")},
		wrapHost: func(f *fakeHost) hostctl.Host {
			return liveHost{fakeHost: f, real: hostctl.New(slog.New(slog.DiscardHandler)).(hostctl.TunnelHost)}
		},
		cfg: func(c *Config) {
			var err error
			if wm, err = warp.New(warp.Options{Interval: time.Second, FastInterval: time.Second}); err != nil {
				t.Fatal(err)
			}
			c.Warp, c.UnitGen = wm, 3
		},
	})
	h.waitConnected()

	linkExists := func(name string) bool { _, ok := try("ip", "-o", "link", "show", name); return ok }
	rules := func() string { return sh(t, "ip", "rule", "show") }

	// (1) An awg inbound whose egress is WARP, with the WARP account.
	ds := fullState(1, awgInbound("warp"))
	ds.Warp = warpSpec()
	h.panel.push(ds)
	res := h.panel.nextApply()
	if res.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || res.Inbounds[0].State != pb.InboundRunState_INBOUND_RUN_STATE_RUNNING {
		t.Fatalf("apply: %v", res)
	}
	if !linkExists("mgawg51842") {
		t.Fatal("the interface of the inbound does not exist")
	}
	if a := sh(t, "ip", "-o", "addr", "show", "dev", "mgawg51842"); !strings.Contains(a, "10.66.4.1/22") {
		t.Errorf("addresses: %s", a)
	}
	if l := sh(t, "ip", "-o", "link", "show", "mgawg51842"); !strings.Contains(l, "mtu 1280") {
		t.Errorf("mtu: %s", l)
	}
	tbl := sh(t, "nft", "list", "table", "inet", "mistgate_awg")
	for _, want := range []string{`iifname "mgawg*" drop`, `iifname "mgawg*" oifname "mgawg*" drop`, "counter udp_51842"} {
		if !strings.Contains(tbl, want) {
			t.Errorf("tunnel table lacks %q:\n%s", want, tbl)
		}
	}
	if strings.Contains(tbl, "masquerade") {
		t.Errorf("a WARP tunnel is masqueraded by the tunnel table:\n%s", tbl)
	}
	if r := rules(); !strings.Contains(r, "from 10.66.4.0/22 lookup 51820") {
		t.Errorf("no rule routing the client subnet into the WARP table:\n%s", r)
	}
	if rt := sh(t, "ip", "route", "show", "table", "51820"); !strings.Contains(rt, "unreachable default") {
		t.Errorf("the WARP table does not fail closed:\n%s", rt)
	}
	if !linkExists("mgwarp") {
		t.Error("the WARP device does not exist")
	}
	eventually(t, func() bool { st := h.panel.statsSeen(); return st != nil && st.Warp != nil }, "WARP health in a stats batch")
	if got := h.panel.statsSeen().Warp.State; got == pb.WarpState_WARP_STATE_UNSPECIFIED {
		t.Errorf("WARP state %v", got)
	}
	if ip, _ := try("sysctl", "-n", "net.ipv4.ip_forward"); strings.TrimSpace(ip) != "1" {
		t.Errorf("ip_forward = %q", ip)
	}

	// (2) The same inbound goes direct: the subnet rule goes, the tunnel table masquerades.
	ds = fullState(2, awgInbound("direct"))
	ds.Warp = warpSpec()
	h.panel.push(ds)
	h.panel.nextApply()
	if r := rules(); strings.Contains(r, "10.66.4.0/22") {
		t.Errorf("the subnet rule of a direct inbound stayed:\n%s", r)
	}
	if tbl := sh(t, "nft", "list", "table", "inet", "mistgate_awg"); !strings.Contains(tbl, "masquerade") {
		t.Errorf("a direct tunnel is not masqueraded:\n%s", tbl)
	}

	// (3) WARP is switched back on for the inbound, and then the account is deleted (a full state without it): the inbound is
	// stopped and its interface is gone, never left serving with a direct exit.
	ds = fullState(3, awgInbound("warp"))
	ds.Warp = warpSpec()
	h.panel.push(ds)
	h.panel.nextApply()
	h.panel.push(fullState(4, awgInbound("warp")))
	res = h.panel.nextApply()
	if res.Inbounds[0].Error != errWarpNotConfigured || res.Inbounds[0].State != pb.InboundRunState_INBOUND_RUN_STATE_FAILED {
		t.Fatalf("inbound without WARP: %v", res.Inbounds[0])
	}
	if linkExists("mgawg51842") {
		t.Error("the interface of an inbound that may not run is still up")
	}
	if linkExists("mgwarp") {
		t.Error("the WARP device stayed after the account was deleted")
	}
	if r := rules(); strings.Contains(r, "10.66.4.0/22") || strings.Contains(r, "lookup 51820") {
		t.Errorf("WARP rules stayed:\n%s", r)
	}

	// (4) Back to a working state, then retire: nothing of ours is left on the host.
	ds = fullState(5, awgInbound("direct"))
	h.panel.push(ds)
	h.panel.nextApply()
	if !linkExists("mgawg51842") {
		t.Fatal("the interface did not come back")
	}
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_Retire{Retire: &pb.Retire{RequestId: "req_r"}}})
	if r := h.panel.nextCmd(); !r.Ok {
		t.Fatalf("retire: %v", r)
	}
	select {
	case <-h.done:
		h.cancel = nil
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after Retire")
	}
	if linkExists("mgawg51842") || linkExists("mgwarp") {
		t.Error("a link of ours survived the retire")
	}
	if _, ok := try("nft", "list", "table", "inet", "mistgate_awg"); ok {
		t.Error("the tunnel table survived the retire")
	}
	if r := rules(); strings.Contains(r, "lookup 51820") {
		t.Errorf("a WARP rule survived the retire:\n%s", r)
	}
}
