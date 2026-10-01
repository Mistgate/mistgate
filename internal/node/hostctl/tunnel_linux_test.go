//go:build linux

package hostctl

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tunHost is testHost plus a fake /proc/sys for forwarding and a runner that answers `nft list counters`.
func tunHost(t *testing.T) (*linuxHost, *[]call, *string) {
	t.Helper()
	h, calls := testHost(t)
	counters := new(string)
	base := h.run
	h.run = func(ctx context.Context, stdin, name string, args ...string) ([]byte, error) {
		if name == "nft" && len(args) > 0 && args[0] == "-j" {
			*calls = append(*calls, call{stdin, name, strings.Join(args, " ")})
			return []byte(*counters), nil
		}
		return base(ctx, stdin, name, args...)
	}
	for f, v := range map[string]string{
		"net/ipv4/ip_forward":             "0\n",
		"net/ipv6/conf/all/forwarding":    "0\n",
		"net/ipv6/conf/eth0/accept_ra":    "1\n",
		"net/ipv6/conf/ens3/accept_ra":    "2\n",
		"net/ipv6/conf/default/accept_ra": "1\n",
	} {
		p := filepath.Join(h.procSys, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return h, calls, counters
}

func counterJSON(port string, packets int) string {
	return `{"nftables":[{"metainfo":{"version":"1.0.9"}},{"counter":{"family":"inet","name":"udp_` + port + `","table":"mistgate_awg","handle":2,"packets":` +
		itoa(packets) + `,"bytes":1}}]}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func readSys(t *testing.T, h *linuxHost, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.procSys, rel))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestSetTunnelsInstallsOnceAndEnablesForwarding(t *testing.T) {
	h, calls, _ := tunHost(t)
	ctx := context.Background()
	ts := []Tunnel{tun(51842, "10.66.4.0/22", "fd66:66:0:1::/64")}
	if err := h.SetTunnels(ctx, ts); err != nil {
		t.Fatal(err)
	}
	var nft []call
	for _, c := range *calls {
		if c.name == "nft" && c.args == "-f -" {
			nft = append(nft, c)
		}
	}
	if len(nft) != 1 || !strings.Contains(nft[0].stdin, "table inet mistgate_awg {") {
		t.Fatalf("calls: %+v", *calls)
	}
	if readSys(t, h, "net/ipv4/ip_forward") != "1" || readSys(t, h, "net/ipv6/conf/all/forwarding") != "1" {
		t.Error("forwarding is not on")
	}
	// The host's own RA acceptance survives IPv6 forwarding: accept_ra 1 -> 2, 2 stays.
	if readSys(t, h, "net/ipv6/conf/eth0/accept_ra") != "2" || readSys(t, h, "net/ipv6/conf/ens3/accept_ra") != "2" {
		t.Error("accept_ra was not raised to 2 before IPv6 forwarding")
	}
	// The same set again touches nothing.
	n := len(*calls)
	if err := h.SetTunnels(ctx, ts); err != nil || len(*calls) != n {
		t.Fatalf("second call: err=%v calls=%d->%d", err, n, len(*calls))
	}
}

func TestSetTunnelsIPv4OnlyLeavesIPv6Alone(t *testing.T) {
	h, _, _ := tunHost(t)
	if err := h.SetTunnels(context.Background(), []Tunnel{tun(51842, "10.66.4.0/22", "")}); err != nil {
		t.Fatal(err)
	}
	if readSys(t, h, "net/ipv4/ip_forward") != "1" || readSys(t, h, "net/ipv6/conf/all/forwarding") != "0" ||
		readSys(t, h, "net/ipv6/conf/eth0/accept_ra") != "1" {
		t.Error("an IPv4-only tunnel changed IPv6 sysctls")
	}
}

func TestTunnelCountersSurviveReplacement(t *testing.T) {
	h, _, counters := tunHost(t)
	ctx := context.Background()
	a := tun(51842, "10.66.4.0/22", "")
	if err := h.SetTunnels(ctx, []Tunnel{a}); err != nil {
		t.Fatal(err)
	}
	*counters = counterJSON("51842", 100)
	got, err := h.TunnelCounters(ctx)
	if err != nil || got[51842] != 100 {
		t.Fatalf("counters = %v, %v", got, err)
	}
	// A second tunnel replaces the table (the nft counters restart at 0): the 100 are carried over.
	if err := h.SetTunnels(ctx, []Tunnel{a, tun(40001, "10.66.8.0/22", "")}); err != nil {
		t.Fatal(err)
	}
	*counters = counterJSON("51842", 7)
	got, _ = h.TunnelCounters(ctx)
	if got[51842] != 107 || got[40001] != 0 {
		t.Fatalf("after replacement: %v", got)
	}
	// A port that is removed and comes back starts from zero.
	if err := h.SetTunnels(ctx, []Tunnel{tun(40001, "10.66.8.0/22", "")}); err != nil {
		t.Fatal(err)
	}
	if err := h.SetTunnels(ctx, []Tunnel{a, tun(40001, "10.66.8.0/22", "")}); err != nil {
		t.Fatal(err)
	}
	*counters = counterJSON("51842", 1)
	if got, _ = h.TunnelCounters(ctx); got[51842] != 1 {
		t.Fatalf("a re-added port kept the old count: %v", got)
	}
}

func TestSetTunnelsBadInputTouchesNothing(t *testing.T) {
	h, calls, _ := tunHost(t)
	bad := tun(51842, "10.66.4.0/22", "")
	bad.Iface = "eth0"
	if err := h.SetTunnels(context.Background(), []Tunnel{bad}); err == nil {
		t.Fatal("accepted")
	}
	if len(*calls) != 0 || readSys(t, h, "net/ipv4/ip_forward") != "0" {
		t.Errorf("a rejected set had effects: %+v", *calls)
	}
}

func TestSetTunnelsRetriesAfterSysctlFailure(t *testing.T) {
	h, calls, _ := tunHost(t)
	ts := []Tunnel{tun(51842, "10.66.4.0/22", "")}
	if err := os.Remove(filepath.Join(h.procSys, "net/ipv4/ip_forward")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(h.procSys, "net/ipv4/ip_forward"), 0o755); err != nil { // unwritable as a file
		t.Fatal(err)
	}
	if err := h.SetTunnels(context.Background(), ts); err == nil {
		t.Fatal("a sysctl failure was not reported")
	}
	n := len(*calls)
	_ = h.SetTunnels(context.Background(), ts)
	if len(*calls) == n {
		t.Error("the failed set was remembered as installed and never retried")
	}
}

func TestCleanupRemovesTunnelTableAndLinks(t *testing.T) {
	h, calls, _ := tunHost(t)
	linksDeleted := 0
	h.links = func() error { linksDeleted++; return nil }
	ctx := context.Background()
	if err := h.SetTunnels(ctx, []Tunnel{tun(51842, "10.66.4.0/22", "")}); err != nil {
		t.Fatal(err)
	}
	*calls = nil
	if err := h.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	var deleted bool
	for _, c := range *calls {
		if c.name == "nft" && c.args == "-f -" && c.stdin == "add table inet mistgate_awg\ndelete table inet mistgate_awg\n" {
			deleted = true
		}
	}
	if !deleted || linksDeleted != 1 {
		t.Errorf("tunnel table deleted=%v, link cleanups=%d, calls=%+v", deleted, linksDeleted, *calls)
	}
	// After Cleanup the same set is installed again (the agent restarts into a clean host).
	*calls = nil
	if err := h.SetTunnels(ctx, []Tunnel{tun(51842, "10.66.4.0/22", "")}); err != nil || len(*calls) == 0 {
		t.Errorf("not re-installed after cleanup: %v %+v", err, *calls)
	}
}

func TestOwnLink(t *testing.T) {
	for n, want := range map[string]bool{"mgawg51842": true, "mgawg": false, "mgwarp": true, "eth0": false, "wg0": false, "awg0": false, "mgwarp2": false, "lo": false} {
		if ownLink(n) != want {
			t.Errorf("ownLink(%q) = %v", n, !want)
		}
	}
}

// TestRenderedTunnelRulesetParses asks the real nft to parse the rendered script (-c: check only, nothing is installed).
// It needs nft and root (the kernel is consulted), so it is opt-in: MG_ROOT_TESTS=1, in WSL or a disposable VM.
func TestRenderedTunnelRulesetParses(t *testing.T) {
	if os.Getenv("MG_ROOT_TESTS") == "" || os.Geteuid() != 0 {
		t.Skip("set MG_ROOT_TESTS=1 and run as root")
	}
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("no nft")
	}
	a := tun(51842, "10.66.4.0/22", "fd66:66:0:1::/64")
	b := tun(40001, "10.66.8.0/22", "")
	b.ViaWarp = true
	for name, ts := range map[string][]Tunnel{"two": {a, b}, "one": {a}, "warp only": {b}, "none": nil} {
		script, err := RenderTunnels(ts)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("nft", "-c", "-f", "-")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: nft rejects the script: %v\n%s\n%s", name, err, out, script)
		}
	}
}
