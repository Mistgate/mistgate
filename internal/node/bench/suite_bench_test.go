//go:build bench && linux

package bench

// The engine benchmark suite. Run it as root on Linux (WSL or a throwaway VM, never on a fleet host):
//
//	MISTGATE_BENCH=1 go test -tags bench ./internal/node/bench/ -run '^TestBench$' -count=1 -v -timeout 0
//
// scripts/bench.sh does that in WSL. Without MISTGATE_BENCH=1, without root, or without network namespaces (or tc for the netem
// variants, nft for AmneziaWG) it skips with a message. Everything it creates is a namespace named mg3-awg-b-*, inside which the
// engines run as re-executed copies of this test binary (roles_bench_test.go); nothing of the host's network is touched.
//
//	MISTGATE_BENCH_ENGINES   comma list of hysteria2, vless-tcp, vless-xhttp, awg   (default: all)
//	MISTGATE_BENCH_VARIANTS  comma list of clean, rtt50-loss1, mtu1280              (default: all)
//	MISTGATE_BENCH_SECONDS   length of each throughput phase                        (default: 4)
//	MISTGATE_BENCH_REPEAT    runs per case, the median is reported (at most 9)      (default: 1; 3 for a baseline)
//	MISTGATE_BENCH_JSON      where the report goes                                  (default: bench-report.json)
//	MISTGATE_BENCH_BASELINE  the baseline to compare with                           (default: baseline.json)
//	MISTGATE_BENCH_UPDATE=1  write the report as the new baseline instead of comparing
//	MISTGATE_BENCH_THRESHOLD how much worse than the baseline fails: 15, 15% or 0.15 (default: 15%)

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/awg"
	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/node/awg/awgtest"
	"github.com/mistgate/mistgate/internal/plugin"
)

const (
	srvIP  = "203.0.113.10"
	cliIP  = "203.0.113.2"
	siteIP = "198.51.100.2"
)

func TestBench(t *testing.T) {
	if os.Getenv("MISTGATE_BENCH") != "1" {
		t.Skip("the engine benchmarks are off: set MISTGATE_BENCH=1 (Linux, root, a throwaway machine; see docs/en/reference/benchmarks.md)")
	}
	if os.Geteuid() != 0 {
		t.Skip("the engine benchmarks need root for network namespaces")
	}
	for _, tool := range []string{"ip"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("the engine benchmarks need %s (iproute2)", tool)
		}
	}
	if out, err := exec.Command("ip", "netns", "list").CombinedOutput(); err != nil {
		t.Skipf("network namespaces are not usable here: %v: %s", err, out)
	}
	_, tcErr := exec.LookPath("tc")
	_, nftErr := exec.LookPath("nft")
	threshold, err := ThresholdFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	secs := 4
	if v, err := strconv.Atoi(os.Getenv("MISTGATE_BENCH_SECONDS")); err == nil && v > 0 {
		secs = v
	}
	repeat := 1
	if v, err := strconv.Atoi(os.Getenv("MISTGATE_BENCH_REPEAT")); err == nil && v > 0 {
		repeat = min(v, 9)
	}
	wantEngines, err := selectBenchmarkNames(engines, os.Getenv("MISTGATE_BENCH_ENGINES"))
	if err != nil {
		t.Fatalf("MISTGATE_BENCH_ENGINES: %v", err)
	}
	wantVariants, err := selectBenchmarkNames(variants, os.Getenv("MISTGATE_BENCH_VARIANTS"))
	if err != nil {
		t.Fatalf("MISTGATE_BENCH_VARIANTS: %v", err)
	}

	report := Report{Schema: SchemaVersion, When: time.Now().UTC().Format(time.RFC3339), Host: hostDescription(), Cases: map[string]Metrics{}}
	for _, eng := range wantEngines {
		for _, variant := range wantVariants {
			switch {
			case variant != "clean" && tcErr != nil:
				t.Logf("skip %s/%s: tc (iproute2) is not installed, so there is no netem", eng, variant)
				continue
			case eng == "awg" && nftErr != nil:
				t.Logf("skip %s/%s: nft is not installed (the node masquerades the tunnel's traffic with it)", eng, variant)
				continue
			}
			var m Metrics
			ok := t.Run(eng+"/"+variant, func(t *testing.T) {
				var runs []Metrics
				for i := 0; i < repeat; i++ {
					runs = append(runs, runCase(t, eng, variant, secs))
				}
				m = MedianOf(runs)
			})
			if ok && m != (Metrics{}) {
				report.Cases[eng+"/"+variant] = m
				t.Logf("%s/%s: %+v", eng, variant, m)
			}
		}
	}
	if len(report.Cases) == 0 {
		t.Skip("no case ran")
	}
	b, err := report.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	out := envOr("MISTGATE_BENCH_JSON", "bench-report.json")
	if err := os.WriteFile(out, b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("report written to %s:\n%s", out, b)

	basePath := envOr("MISTGATE_BENCH_BASELINE", "baseline.json")
	if os.Getenv("MISTGATE_BENCH_UPDATE") == "1" {
		merged := Report{Schema: SchemaVersion, Cases: map[string]Metrics{}}
		if old, err := LoadReport(basePath); err == nil && old.Schema == SchemaVersion {
			merged = old
		}
		for k, v := range report.Cases {
			merged.Cases[k] = v
		}
		merged.When, merged.Host = report.When, report.Host
		nb, _ := merged.Marshal()
		if err := os.WriteFile(basePath, nb, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("baseline %s updated with %d cases", basePath, len(report.Cases))
		return
	}
	base, err := LoadReport(basePath)
	if err != nil {
		t.Logf("no baseline to compare with (%v): the report is all there is", err)
		return
	}
	res := Compare(base, report, threshold)
	t.Logf("compared with %s at a threshold of %.0f%% (baseline taken on: %s)\n%s", basePath, threshold*100, base.Host, res.Summary())
	if res.Failed() {
		t.Errorf("%d metrics are more than %.0f%% worse than the baseline", len(res.Regressions), threshold*100)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func hostDescription() string {
	kernel, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return fmt.Sprintf("kernel %s, %d CPUs, %s %s", strings.TrimSpace(string(kernel)), runtime.NumCPU(), runtime.Version(), runtime.GOARCH)
}

// ---- one case ----

type topology struct{ srv, cli, inet awgtest.NS }

// newTopology is client - veth - node - veth - internet. The netem and the MTU of the variant sit on the first link, on both of
// its ends; the node and the internet are joined by a clean link.
func newTopology(t *testing.T, variant string) topology {
	t.Helper()
	tp := topology{srv: awgtest.NewNS(t, "b-s"), cli: awgtest.NewNS(t, "b-c"), inet: awgtest.NewNS(t, "b-i")}
	awgtest.Veth(t, tp.cli, "bc0", []string{cliIP + "/24"}, tp.srv, "bs0", []string{srvIP + "/24"})
	awgtest.Veth(t, tp.srv, "bs1", []string{"198.51.100.1/24"}, tp.inet, "bi1", []string{siteIP + "/24"})
	tp.srv.Must(t, "sysctl", "-qw", "net.ipv4.ip_forward=1")
	for _, end := range []struct {
		ns  awgtest.NS
		dev string
	}{{tp.cli, "bc0"}, {tp.srv, "bs0"}} {
		switch variant {
		case "rtt50-loss1": // 25 ms and 0.5% each way: a round trip of 50 ms with about 1% lost
			end.ns.Must(t, "tc", "qdisc", "add", "dev", end.dev, "root", "netem", "delay", "25ms", "loss", "0.5%")
		case "mtu1280":
			end.ns.Must(t, "ip", "link", "set", "dev", end.dev, "mtu", "1280")
		}
	}
	return tp
}

const masqRules = `
table inet mgbench {
  chain forward {
    type filter hook forward priority filter; policy accept;
    iifname "mgawg*" tcp flags syn tcp option maxseg size set rt mtu
    oifname "mgawg*" tcp flags syn tcp option maxseg size set rt mtu
  }
  chain postrouting {
    type nat hook postrouting priority srcnat; policy accept;
    ip saddr 10.66.0.0/16 oifname != "mgawg*" masquerade
  }
}
`

func startRole(t *testing.T, ns awgtest.NS, role string, a roleArgs) (*awgtest.Proc, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(a)
	out := fmt.Sprintf("%s/mg-bench-%d-%s-%d.json", os.TempDir(), os.Getpid(), role, time.Now().UnixNano()%1_000_000)
	_ = os.Remove(out)
	t.Cleanup(func() { _ = os.Remove(out) })
	p := ns.Start(t, []string{roleEnv + "=" + role, argsEnv + "=" + string(args), outEnv + "=" + out},
		exe, "-test.run=^TestBenchRole$", "-test.timeout=0", "-test.count=1")
	return p, out
}

// await waits for a helper's output file and decodes it into v.
func await(t *testing.T, p *awgtest.Proc, path string, within time.Duration, v any) {
	t.Helper()
	var raw []byte
	if !awgtest.Wait(within, func() bool {
		b, err := os.ReadFile(path)
		if err == nil && len(b) > 0 {
			raw = b
			return true
		}
		return false
	}) {
		t.Fatalf("a helper did not answer within %v:\n%s", within, p.Output())
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("a helper's answer %q: %v", raw, err)
	}
}

func runCase(t *testing.T, eng, variant string, secs int) Metrics {
	tp := newTopology(t, variant)
	siteDest := fmt.Sprintf("%s:%d", siteIP, destPort)
	a := roleArgs{Engine: eng, Dest: siteDest, Target: fmt.Sprintf("%s:%d", siteIP, targetPort), Server: srvIP, Seconds: secs, ListenIP: siteIP}

	dest, destOut := startRole(t, tp.inet, "dest", a)
	var destReady readyInfo
	await(t, dest, destOut, 20*time.Second, &destReady)
	if destReady.Error != "" {
		t.Fatalf("the internet helper: %s", destReady.Error)
	}

	var awgClient *awgtest.Client
	var awgCfg awgtest.ClientCfg
	switch eng {
	case "hysteria2":
		a.Port = 4433
	case "vless-tcp", "vless-xhttp":
		a.Port = 4443
	case "awg":
		a.Port = 51842
		cmd := tp.srv.Cmd("nft", "-f", "-")
		cmd.Stdin = strings.NewReader(masqRules)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("nft is not usable here: %v: %s", err, out)
		}
		a.Spec, a.Creds, awgCfg = awgProfile(t, a.Port, variant)
	}
	server, serverOut := startRole(t, tp.srv, "server", a)
	var ready readyInfo
	await(t, server, serverOut, 60*time.Second, &ready)
	if ready.Error != "" {
		t.Fatalf("the engine: %s", ready.Error)
	}
	a.Pin = ready.Pin
	if eng == "awg" {
		awgClient = awgtest.StartClient(t, tp.cli, awgCfg)
		if !awgtest.Wait(15*time.Second, awgClient.Handshaken) {
			t.Fatalf("no AmneziaWG handshake.\nclient: %s\nserver: %s", awgClient.Output(), server.Output())
		}
	}

	// the throughput phases, with the engine process's CPU and memory read around them
	cpu0 := cpuSeconds(t, server.Pid())
	a.Phase = "load"
	client, clientOut := startRole(t, tp.cli, "client", a)
	var load clientResult
	await(t, client, clientOut, time.Duration(2*secs+90)*time.Second, &load)
	cpu1 := cpuSeconds(t, server.Pid())
	if load.Error != "" {
		if variant == "mtu1280" {
			// not a failure of the suite: an engine that cannot work over a 1280-byte link has no figure for it (QUIC sends
			// 1280-byte datagrams with the do-not-fragment flag, which a 1280-byte link drops), and the report has no such case
			t.Skipf("%s does not carry traffic over a 1280-byte link: %s", eng, load.Error)
		}
		t.Fatalf("the load: %s\nserver: %s", load.Error, server.Output())
	}
	m := Metrics{Tput1Mbit: round1(load.Tput1Mbit), Tput8Mbit: round1(load.Tput8Mbit), P50MS: round1(load.P50MS), P99MS: round1(load.P99MS),
		RSSMB: round1(peakRSSMB(t, server.Pid()))}
	if gb := float64(load.Bytes) / 1e9; gb > 0 {
		m.CPUSecPerGB = math.Round(100*(cpu1-cpu0)/gb) / 100
	}

	// handshakes per second
	if eng == "awg" {
		m.HandshakesPerS = round1(awgHandshakes(t, tp.cli, awgClient, awgCfg, secs))
	} else {
		a.Phase = "handshakes"
		hc, hOut := startRole(t, tp.cli, "client", a)
		var hs clientResult
		await(t, hc, hOut, time.Duration(secs+60)*time.Second, &hs)
		if hs.Error != "" {
			t.Fatalf("the handshake count: %s", hs.Error)
		}
		m.HandshakesPerS = round1(hs.HandshakesPerS)
	}
	return m
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// awgHandshakes counts how many times the client can have its peer forgotten and be answered again in secs seconds: each round is
// a fresh handshake, forced by the next ping.
func awgHandshakes(t *testing.T, cli awgtest.NS, c *awgtest.Client, cfg awgtest.ClientCfg, secs int) float64 {
	t.Helper()
	deadline := time.Now().Add(time.Duration(secs) * time.Second)
	start := time.Now()
	n, tries := 0, 0
	for time.Now().Before(deadline) {
		// WireGuard's own flood limit: a server ignores a peer's handshake initiation less than 20 ms after the last one, and the
		// client then waits for its retry timer (5 s), so this is as fast as a handshake may legitimately be asked for
		time.Sleep(25 * time.Millisecond)
		if err := c.ReplacePeer(cfg); err != nil {
			t.Fatalf("reset the client's peer: %v", err)
		}
		t0 := time.Now()
		out, err := cli.Run("ping", "-c", "1", "-W", "2", "-n", "-q", "10.66.0.1")
		if err == nil {
			n++
		} else if n == 0 {
			t.Fatalf("no handshake after the peer was reset: %v: %s", err, out)
		}
		if tries++; tries <= 3 {
			t.Logf("awg handshake round %d: %v in %v", tries, err, time.Since(t0))
		}
	}
	return float64(n) / time.Since(start).Seconds()
}

// awgProfile is one AmneziaWG inbound without obfuscation (the engine's speed, not the parameters', is the point) and the client
// that matches it. The tunnel MTU follows the variant.
func awgProfile(t *testing.T, port uint16, variant string) (*plugin.InboundSpec, []plugin.UserCred, awgtest.ClientCfg) {
	t.Helper()
	one := func(s string) awgcfg.Range { v, _ := awgcfg.ParseRange(s); return v }
	obf := awgcfg.Obfuscation{H1: one("1"), H2: one("2"), H3: one("3"), H4: one("4")}
	priv, pub := awgtest.NewKey(t)
	cpriv, cpub := awgtest.NewKey(t)
	psk, _ := awgtest.NewKey(t)
	settings := awgcfg.Settings{Version: awgcfg.Version31, PrivateKey: awgtest.B64(priv), Obfuscation: obf}
	mtu := 1280
	if variant == "mtu1280" {
		mtu = 1200 // the link's 1280 less the tunnel's own headers
	}
	if r := awgcfg.Validate(settings, awgcfg.Options{MTU: mtu}); !r.OK() {
		t.Fatalf("the benchmark's own AmneziaWG profile is invalid: %v", r.Errors)
	}
	nj, err := settings.NodeJSON()
	if err != nil {
		t.Fatal(err)
	}
	spec := &plugin.InboundSpec{ID: "inb_bench", Protocol: awg.Protocol, ProfileID: "prf_bench", Version: 1, Enabled: true,
		Listen: plugin.Listen{Network: "udp", Port: port}, Egress: "direct", Settings: nj,
		Tunnel: plugin.Tunnel{AddrV4: netip.MustParsePrefix("10.66.0.1/22"), MTU: uint16(mtu)}}
	data, _ := json.Marshal(struct {
		PublicKey  string   `json:"public_key"`
		AllowedIPs []string `json:"allowed_ips"`
		PSK        string   `json:"psk"`
	}{awgtest.B64(cpub), []string{"10.66.0.2/32"}, awgtest.B64(psk)})
	creds := []plugin.UserCred{{CredID: "crd_bench", UserID: "usr_bench", DeviceID: "dev_bench", Data: data}}
	cfg := awgtest.ClientCfg{Flavor: awgtest.Flavor31, Addrs: []string{"10.66.0.2/22"}, Routes: []string{"198.51.100.0/24"},
		Priv: cpriv, ServerPub: pub, PSK: &psk, Endpoint: fmt.Sprintf("%s:%d", srvIP, port), Obf: obf, MTU: mtu, Keepalive: "25"}
	return spec, creds, cfg
}

// ---- /proc ----

// cpuSeconds is the user plus system CPU time of a process.
func cpuSeconds(t *testing.T, pid int) float64 {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatalf("read the engine process's CPU time: %v", err)
	}
	s := string(b)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+2:]) // after "pid (comm) ": state is field 3, utime and stime are 14 and 15
	if len(f) < 13 {
		t.Fatalf("unexpected /proc/%d/stat: %q", pid, s)
	}
	ut, _ := strconv.ParseFloat(f[11], 64)
	st, _ := strconv.ParseFloat(f[12], 64)
	return (ut + st) / clockTicks()
}

func clockTicks() float64 {
	if out, err := exec.CommandContext(context.Background(), "getconf", "CLK_TCK").Output(); err == nil {
		if v, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil && v > 0 {
			return v
		}
	}
	return 100
}

// peakRSSMB is the process's peak resident memory (VmHWM).
func peakRSSMB(t *testing.T, pid int) float64 {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatalf("read the engine process's memory: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			kb, _ := strconv.ParseFloat(strings.Fields(rest)[0], 64)
			return kb / 1024
		}
	}
	return 0
}
