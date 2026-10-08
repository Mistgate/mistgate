//go:build linux

package hostctl

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type call struct{ stdin, name, args string }

// testHost is a linuxHost whose files live in a temp dir and whose commands are recorded, never run:
// the real firewall and sysctls of the machine running the tests are not touched.
func testHost(t *testing.T) (*linuxHost, *[]call) {
	t.Helper()
	dir := t.TempDir()
	var calls []call
	h := &linuxHost{
		log:          slog.New(slog.DiscardHandler),
		sysctlFile:   filepath.Join(dir, "sysctl.d", "90-mistgate.conf"),
		journaldFile: filepath.Join(dir, "journald.conf.d", "90-mistgate.conf"),
		procSys:      filepath.Join(dir, "proc-sys"),
		procRoot:     "/proc",
		run: func(_ context.Context, stdin, name string, args ...string) ([]byte, error) {
			calls = append(calls, call{stdin, name, strings.Join(args, " ")})
			return nil, nil
		},
	}
	for _, f := range []string{"net/core/default_qdisc", "net/ipv4/tcp_congestion_control", "net/core/rmem_max", "net/core/wmem_max"} {
		p := filepath.Join(h.procSys, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		value := "fq_codel\n"
		if f == "net/core/rmem_max" || f == "net/core/wmem_max" {
			value = "4096\n"
		}
		if err := os.WriteFile(p, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return h, &calls
}

func TestApplyBaselineAndCleanup(t *testing.T) {
	h, calls := testHost(t)
	ctx := context.Background()
	if err := h.ApplyBaseline(ctx); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]string{
		"net/core/default_qdisc":          "fq",
		"net/ipv4/tcp_congestion_control": "bbr",
		"net/core/rmem_max":               "16777216",
		"net/core/wmem_max":               "16777216",
	} {
		if b, _ := os.ReadFile(filepath.Join(h.procSys, f)); string(b) != want {
			t.Errorf("%s = %q, want %q", f, b, want)
		}
	}
	if b, _ := os.ReadFile(h.sysctlFile); string(b) != sysctlFileBody {
		t.Errorf("sysctl.d: %q", b)
	}
	if b, _ := os.ReadFile(h.journaldFile); !strings.Contains(string(b), "SystemMaxUse=200M") {
		t.Errorf("journald: %q", b)
	}
	restarts := func() (n int) {
		for _, c := range *calls {
			if c.name == "systemctl" && c.args == "restart systemd-journald" {
				n++
			}
		}
		return
	}
	if restarts() != 1 {
		t.Fatalf("calls after first apply: %+v", *calls)
	}
	if nft := (*calls)[len(*calls)-1]; nft.name != "nft" || !strings.Contains(nft.stdin, "tcp dport { 22 } ct state new") {
		t.Fatalf("baseline must end with the ssh guard on the default port: %+v", nft)
	}
	// Idempotent: second run changes nothing, so journald is not restarted again.
	if err := h.ApplyBaseline(ctx); err != nil {
		t.Fatal(err)
	}
	if restarts() != 1 {
		t.Fatalf("second apply restarted journald: %+v", *calls)
	}

	before := len(*calls)
	if err := h.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{h.sysctlFile, h.journaldFile} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived cleanup", p)
		}
	}
	var nft *call
	for i := before; i < len(*calls); i++ {
		if c := &(*calls)[i]; c.name == "nft" {
			nft = c
		}
	}
	if nft == nil || nft.args != "-f -" || nft.stdin != "add table inet mistgate_node\ndelete table inet mistgate_node\n" {
		t.Fatalf("cleanup script: %+v", nft)
	}
	// the cap goes with the drop-in: journald reads it only on start
	if restarts() != 2 {
		t.Fatalf("cleanup did not restart journald once: %+v", (*calls)[before:])
	}
	if err := h.Cleanup(ctx); err != nil { // nothing left to remove is fine
		t.Fatal(err)
	}
	if restarts() != 2 {
		t.Fatalf("a cleanup with no drop-in restarted journald: %+v", *calls)
	}
}

func TestApplyBaselinePreservesHigherUDPBufferValues(t *testing.T) {
	h, _ := testHost(t)
	for f, want := range map[string]string{"net/core/rmem_max": "33554432", "net/core/wmem_max": "67108864"} {
		if err := os.WriteFile(filepath.Join(h.procSys, f), []byte(want+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.ApplyBaseline(context.Background()); err != nil {
		t.Fatal(err)
	}
	for f, want := range map[string]string{"net/core/rmem_max": "33554432", "net/core/wmem_max": "67108864"} {
		// left as the kernel shows it (with its newline): a higher value is not rewritten
		if b, err := os.ReadFile(filepath.Join(h.procSys, f)); err != nil || strings.TrimSpace(string(b)) != want {
			t.Errorf("%s = %q, %v; want %q", f, b, err, want)
		}
	}
	wantFile := strings.Replace(sysctlFileBody, "net.core.rmem_max = 16777216", "net.core.rmem_max = 33554432", 1)
	wantFile = strings.Replace(wantFile, "net.core.wmem_max = 16777216", "net.core.wmem_max = 67108864", 1)
	if b, err := os.ReadFile(h.sysctlFile); err != nil || string(b) != wantFile {
		t.Errorf("sysctl.d = %q, %v; want %q", b, err, wantFile)
	}
}

func TestApplyBaselineToleratesUnwritableUDPBufferKey(t *testing.T) {
	h, _ := testHost(t)
	wmem := filepath.Join(h.procSys, "net/core/wmem_max")
	if err := os.Remove(wmem); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(wmem, 0o755); err != nil {
		t.Fatal(err)
	}
	err := h.ApplyBaseline(context.Background())
	if err == nil || !strings.Contains(err.Error(), "net.core.wmem_max=16777216") {
		t.Fatalf("err = %v", err)
	}
	if b, readErr := os.ReadFile(filepath.Join(h.procSys, "net/core/rmem_max")); readErr != nil || string(b) != "16777216" {
		t.Errorf("writable sibling key was skipped: %q, %v", b, readErr)
	}
	if _, statErr := os.Stat(h.sysctlFile); statErr != nil {
		t.Errorf("sysctl drop-in was not written: %v", statErr)
	}
}

func TestApplyBaselineReportsReadOnlySysctl(t *testing.T) {
	h, _ := testHost(t)
	h.procSys = filepath.Join(t.TempDir(), "missing") // like OpenVZ: /proc/sys/net/... not writable
	err := h.ApplyBaseline(context.Background())
	if err == nil || !strings.Contains(err.Error(), "net.core.default_qdisc=fq") {
		t.Fatalf("err = %v", err)
	}
	// The drop-ins were still written: a failure in one step does not skip the others.
	if _, statErr := os.Stat(h.journaldFile); statErr != nil {
		t.Fatal("journald drop-in not written")
	}
}

func TestSetPortHopsPipesRenderedScript(t *testing.T) {
	h, calls := testHost(t)
	hops := []Hop{{InboundID: "inb_a", Network: "udp", From: 20000, To: 39999, Port: 443}}
	if err := h.SetPortHops(context.Background(), hops); err != nil {
		t.Fatal(err)
	}
	want, _ := RenderHops(hops)
	if len(*calls) != 1 || (*calls)[0].stdin != want || (*calls)[0].args != "-f -" {
		t.Fatalf("calls: %+v", *calls)
	}
	if err := h.SetPortHops(context.Background(), []Hop{{InboundID: "a b", Network: "udp", From: 1, To: 2, Port: 3}}); err == nil {
		t.Fatal("bad hop accepted")
	}
	if len(*calls) != 1 {
		t.Fatal("nft ran for an invalid ruleset")
	}
}

func TestFactsAndMetricsOnThisHost(t *testing.T) {
	h := New(slog.New(slog.DiscardHandler))
	f := h.Facts(context.Background())
	if f.CPUCount < 1 || f.Kernel == "" || f.RAMTotal == 0 || f.DiskTotal == 0 || f.Hostname == "" || f.Arch == "" || f.Virt == "" || f.OS == "" {
		t.Fatalf("facts incomplete: %+v", f)
	}
	if f.Boot.IsZero() {
		t.Error("boot time missing")
	}
	h.Metrics() // first sample primes the rate counters
	m := h.Metrics()
	if m.RAMTotal == 0 || m.RAMUsed > m.RAMTotal || m.UptimeS == 0 || m.DiskTotal == 0 || m.CPUPct < 0 || m.CPUPct > 100 {
		t.Fatalf("metrics: %+v", m)
	}
}

// TestRulesetLoadsInPrivateNetns feeds the rendered script to a real nft inside a throw-away user+net
// namespace (`unshare -Urn`), so syntax and kernel support are checked without touching the host's firewall.
// Skipped where nft, unshare or unprivileged user namespaces are unavailable.
func TestRulesetLoadsInPrivateNetns(t *testing.T) {
	for _, bin := range []string{"nft", "unshare"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	script, err := RenderHops([]Hop{
		{InboundID: "inb_a", Network: "udp", From: 20000, To: 29999, Port: 443},
		{InboundID: "inb_t", Network: "tcp", From: 30000, To: 31000, Port: 8443},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	load, cleanup := filepath.Join(dir, "load.nft"), filepath.Join(dir, "cleanup.nft")
	if err := os.WriteFile(load, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	clean, _ := RenderHops(nil)
	if err := os.WriteFile(cleanup, []byte(clean), 0o600); err != nil {
		t.Fatal(err)
	}
	// Load twice (the replace path), list, clean up, list again.
	sh := "nft -f " + load + " && nft -f " + load + " && nft list ruleset && echo AFTER-CLEANUP && nft -f " + cleanup + " && nft list ruleset"
	out, err := execRunner(context.Background(), "", "unshare", "-Urn", "bash", "-c", sh)
	if err != nil {
		if strings.Contains(string(out), "not permitted") || strings.Contains(string(out), "unshare failed") {
			t.Skipf("no unprivileged namespaces: %s", out)
		}
		t.Fatalf("%v\n%s", err, out)
	}
	before, after, _ := strings.Cut(string(out), "AFTER-CLEANUP")
	for _, want := range []string{`udp dport 20000-29999 redirect to :443 comment "hop:inb_a"`, `tcp dport 30000-31000 redirect to :8443 comment "hop:inb_t"`} {
		if !strings.Contains(before, want) {
			t.Errorf("listed ruleset lacks %q:\n%s", want, before)
		}
	}
	if strings.Contains(after, "mistgate_node") {
		t.Errorf("table survived cleanup:\n%s", after)
	}
}

// TestTorrentGuardRulesetLoadsInPrivateNetns checks the torrent guard table (ct mark, ct original packets, queue bypass)
// against a real nft and kernel the same way.
func TestTorrentGuardRulesetLoadsInPrivateNetns(t *testing.T) {
	for _, bin := range []string{"nft", "unshare"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	script, err := RenderTorrentGuard([]string{"mgawg51820", "mgawg51821"})
	if err != nil {
		t.Fatal(err)
	}
	load := filepath.Join(t.TempDir(), "load.nft")
	if err := os.WriteFile(load, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := execRunner(context.Background(), "", "unshare", "-Urn", "bash", "-c", "nft -f "+load+" && nft -f "+load+" && nft list table inet "+NftTorrentTable)
	if err != nil {
		if strings.Contains(string(out), "not permitted") || strings.Contains(string(out), "unshare failed") {
			t.Skipf("no unprivileged namespaces: %s", out)
		}
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"ct mark set 0x4d475442 drop", "ct original packets <= 6", "ct state new", "bypass to 4242"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("listed table lacks %q:\n%s", want, out)
		}
	}
}
