//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amnezia-vpn/amneziawg-go/v3/tun"
	"github.com/vishvananda/netlink"

	"github.com/mistgate/mistgate/internal/node/hostctl"
)

// TestMain lets this test binary stand in for the agent binary inside a transient systemd unit (the helper pattern): with
// MG3_UNIT_HELPER set, "probe" runs the sandbox probe below and anything else is the real command line of
// mistgate-node (the unit's ExecStopPost runs "cleanup-net" through it).
func TestMain(m *testing.M) {
	if os.Getenv("MG3_UNIT_HELPER") != "" {
		if len(os.Args) > 1 && os.Args[1] == "probe" {
			os.Exit(unitProbe())
		}
		os.Exit(dispatch(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// probeResult is what the probe saw from inside the sandbox; "ok" or the error text per capability.
type probeResult struct {
	DevNetTun    string `json:"dev_net_tun"`   // open /dev/net/tun
	CreateTUN    string `json:"create_tun"`    // what the userspace AWG and WARP backends do
	NetlinkLink  string `json:"netlink_link"`  // a persistent link named mgawg* (what the kernel AWG backend does)
	KernelWG     string `json:"kernel_wg"`     // a link of kind wireguard named mgwarp (the WARP kernel backend)
	SetTunnels   string `json:"set_tunnels"`   // hostctl: the tunnel nft table and the forwarding sysctl
	IPForwarding string `json:"ip_forwarding"` // net.ipv4.ip_forward after SetTunnels
}

func unitProbe() int {
	var r probeResult
	res := func(err error) string {
		if err == nil {
			return "ok"
		}
		return err.Error()
	}
	f, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	r.DevNetTun = res(err)
	if err == nil {
		f.Close()
	}
	// The same call the amneziawg-go backends make: a TUN device named like ours.
	if td, err := tun.CreateTUN("mgawg51842", 1280); err != nil {
		r.CreateTUN = res(err)
	} else {
		r.CreateTUN = "ok"
		td.Close()
	}
	// A persistent link with our prefix, so that ExecStopPost has something to remove (a TUN dies with its file descriptor).
	la := netlink.NewLinkAttrs()
	la.Name = "mgawg40001"
	d := &netlink.Dummy{LinkAttrs: la}
	if err := netlink.LinkAdd(d); err != nil {
		r.NetlinkLink = res(err)
	} else {
		r.NetlinkLink = "ok"
		_ = netlink.LinkSetUp(d)
	}
	wa := netlink.NewLinkAttrs()
	wa.Name = "mgwarp"
	r.KernelWG = res(netlink.LinkAdd(&netlink.Wireguard{LinkAttrs: wa}))

	h := hostctl.New(slog.New(slog.DiscardHandler))
	th, _ := h.(hostctl.TunnelHost)
	err = th.SetTunnels(context.Background(), []hostctl.Tunnel{{
		Iface: "mgawg40001", Subnet4: netip.MustParsePrefix("10.66.8.0/22"), Addr4: netip.MustParseAddr("10.66.8.1"), UDPPort: 40001,
	}})
	r.SetTunnels = res(err)
	if b, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward"); err == nil {
		r.IPForwarding = strings.TrimSpace(string(b))
	} else {
		r.IPForwarding = err.Error()
	}
	out, _ := json.Marshal(r)
	if dir := os.Getenv("MG3_PROBE_OUT"); dir != "" {
		_ = os.WriteFile(filepath.Join(dir, "probe.json"), out, 0o644)
	}
	return 0
}

// TestUnitGen3UnderRealSystemd proves the unit change of generation 3 with the RENDERED unit (hardening included) on a real
// systemd, inside a network namespace made for the test (NetworkNamespacePath, so nothing touches the network of the host):
//
//  1. under generation 3 the service can open /dev/net/tun, create a TUN, a persistent link, a kernel WireGuard link and
//     the tunnel nft table, with the capability set it has (CAP_NET_ADMIN, CAP_NET_BIND_SERVICE only);
//  2. the same unit with PrivateDevices=yes (what generation 2 had) cannot open /dev/net/tun, which is the reason for the
//     generation, and the same cannot be cured by anything but the unit;
//  3. ExecStopPost ("cleanup-net", run through this binary) leaves neither a link named mgawg* / mgwarp nor the tunnel table
//     behind, also after a service that did not clean up after itself.
//
// It needs root, ip, nft and a running systemd, so it is opt-in:
//
//	MISTGATE_SYSTEMD_TESTS=1 go test -run UnitGen3UnderRealSystemd ./cmd/mistgate-node   (as root, in WSL or a disposable VM)
//
// Everything it creates is named mg3-unit-* and lives in /run and in its own namespace; all of it is removed at the end.
func TestUnitGen3UnderRealSystemd(t *testing.T) {
	if os.Getenv("MISTGATE_SYSTEMD_TESTS") == "" {
		t.Skip("set MISTGATE_SYSTEMD_TESTS=1 to run against the real systemd (root only)")
	}
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	if out, _ := exec.Command("systemctl", "is-system-running").Output(); !strings.Contains(string(out), "running") && !strings.Contains(string(out), "degraded") {
		t.Skip("systemd is not running")
	}
	for _, tool := range []string{"ip", "nft"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	for _, d := range []string{"/etc/sysctl.d", "/etc/systemd/journald.conf.d", "/run/systemd/system"} {
		if _, err := os.Stat(d); err != nil {
			t.Skipf("%s is missing (ReadWritePaths would fail the unit)", d)
		}
	}
	id := fmt.Sprintf("mg3-unit-%d", os.Getpid())
	root := filepath.Join("/run", id)
	state, binDir := filepath.Join(root, "state"), filepath.Join(root, "bin")
	for _, d := range []string{state, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	defer os.RemoveAll(root)
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "mistgate-node")
	if err := os.WriteFile(bin, self, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("ip", "netns", "add", id).CombinedOutput(); err != nil {
		t.Fatalf("ip netns add: %v: %s", err, out)
	}
	defer exec.Command("ip", "netns", "del", id).Run()

	name := id + ".service"
	unitPath := filepath.Join("/run/systemd/system", name)
	defer func() {
		_ = exec.Command("systemctl", "stop", name).Run()
		_ = os.Remove(unitPath)
		_ = exec.Command("systemctl", "daemon-reload").Run()
	}()

	runUnit := func(mut func(line string) string) (probeResult, string) {
		t.Helper()
		_ = os.Remove(filepath.Join(state, "probe.json"))
		u, err := renderUnit(bin, state, 0)
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		for _, l := range strings.Split(u, "\n") {
			switch {
			case strings.HasPrefix(l, "ExecStart="):
				l = "ExecStart=" + bin + " probe\nNetworkNamespacePath=/run/netns/" + id +
					"\nEnvironment=MG3_UNIT_HELPER=1\nEnvironment=MG3_PROBE_OUT=" + state
			case strings.HasPrefix(l, "Type="):
				l = "Type=oneshot"
			case strings.HasPrefix(l, "ExecStartPre="), l == "WantedBy=multi-user.target":
				continue
			}
			if mut != nil {
				l = mut(l)
			}
			lines = append(lines, l)
		}
		if err := os.WriteFile(unitPath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
			t.Fatalf("daemon-reload: %v: %s", err, out)
		}
		out, err := exec.Command("systemctl", "start", name).CombinedOutput()
		journal, _ := exec.Command("journalctl", "-u", name, "--no-pager", "-n", "30").CombinedOutput()
		if err != nil {
			t.Fatalf("start: %v: %s\n%s", err, out, journal)
		}
		b, err := os.ReadFile(filepath.Join(state, "probe.json"))
		if err != nil {
			t.Fatalf("the probe wrote nothing: %v\n%s", err, journal)
		}
		var r probeResult
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		return r, string(journal)
	}
	ns := func(args ...string) string {
		out, _ := exec.Command("ip", append([]string{"netns", "exec", id}, args...)...).CombinedOutput()
		return string(out)
	}

	// (1) generation 3 as rendered.
	r, journal := runUnit(nil)
	t.Logf("generation 3 probe: %+v", r)
	if r.DevNetTun != "ok" || r.CreateTUN != "ok" {
		t.Errorf("generation 3 cannot use /dev/net/tun: open=%q create=%q\n%s", r.DevNetTun, r.CreateTUN, journal)
	}
	if r.NetlinkLink != "ok" || r.SetTunnels != "ok" || r.IPForwarding != "1" {
		t.Errorf("netlink / nft / sysctl under the hardening: link=%q tunnels=%q forwarding=%q\n%s", r.NetlinkLink, r.SetTunnels, r.IPForwarding, journal)
	}
	if r.KernelWG != "ok" {
		t.Logf("kernel WireGuard link: %q (the kernel of this host may have no wireguard module; informational)", r.KernelWG)
	}
	// (3) ExecStopPost ran after the service ended (oneshot, so it is done when start returned): nothing of ours is left.
	if links := ns("ip", "-o", "link"); strings.Contains(links, "mgawg") || strings.Contains(links, "mgwarp") {
		t.Errorf("ExecStopPost left links behind:\n%s", links)
	}
	if tables := ns("nft", "list", "tables"); strings.Contains(tables, "mistgate_awg") {
		t.Errorf("ExecStopPost left the tunnel table behind:\n%s", tables)
	}

	// (2) the generation 2 device policy: PrivateDevices=yes hides /dev/net/tun.
	r2, journal2 := runUnit(func(l string) string {
		switch {
		case l == "PrivateDevices=no":
			return "PrivateDevices=yes"
		case l == "DevicePolicy=closed" || strings.HasPrefix(l, "DeviceAllow="):
			return ""
		}
		return l
	})
	t.Logf("generation 2 probe: %+v", r2)
	if r2.DevNetTun == "ok" || r2.CreateTUN == "ok" {
		t.Errorf("with PrivateDevices=yes /dev/net/tun is reachable, so generation 3 would change nothing: %+v\n%s", r2, journal2)
	}
	if r2.NetlinkLink != "ok" {
		t.Errorf("a generation 2 unit must still manage netlink links (the kernel backends need nothing new): %q", r2.NetlinkLink)
	}
}
