//go:build linux

package hostctl

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// The tunnel firewall against real packets: two tunnel interfaces named like the engine's, one client
// behind each, and an "internet" behind a third link, all in network namespaces of their own (mg3-n3-*, the node's rules are
// installed INSIDE the node namespace by this very test binary, so nothing touches the host). It answers what a test stand
// found open: client A reaching client B through the node, and a client reaching the node address of another profile.
//
// Opt-in: MG_ROOT_TESTS=1 as root with ip, nft and python3.
//
//	MG_ROOT_TESTS=1 go test ./internal/node/hostctl -run TestLabTunnelFirewall -v

const (
	labIfA, labIfB     = "mgawg51842", "mgawg40001"
	labPortA, labPortB = 51842, 40001
)

// TestLabNodeHelper is the process the lab runs inside the node namespace: it installs the tunnel rules with the REAL host
// owner and exits (the nft table and the sysctls stay in the namespace). Skipped in a normal run.
func TestLabNodeHelper(t *testing.T) {
	if os.Getenv("MG3_LAB_NODE") == "" {
		t.Skip("lab helper")
	}
	h := New(slog.New(slog.DiscardHandler)).(TunnelHost)
	a := Tunnel{Iface: labIfA, Subnet4: netip.MustParsePrefix("10.66.4.0/22"), Addr4: netip.MustParseAddr("10.66.4.1"), UDPPort: labPortA}
	b := Tunnel{Iface: labIfB, Subnet4: netip.MustParsePrefix("10.66.8.0/22"), Addr4: netip.MustParseAddr("10.66.8.1"), UDPPort: labPortB}
	if err := h.SetTunnels(context.Background(), []Tunnel{a, b}); err != nil {
		t.Fatal(err)
	}
}

func TestLabTunnelFirewall(t *testing.T) {
	if os.Getenv("MG_ROOT_TESTS") == "" || os.Geteuid() != 0 {
		t.Skip("set MG_ROOT_TESTS=1 and run as root")
	}
	for _, tool := range []string{"ip", "nft", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	id := fmt.Sprintf("mg3-n3-%d", os.Getpid())
	nsN, nsA, nsB, nsI := id+"-n", id+"-a", id+"-b", id+"-i"
	sh := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	try := func(args ...string) (string, error) {
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		return string(out), err
	}
	for _, ns := range []string{nsN, nsA, nsB, nsI} {
		sh("ip", "netns", "add", ns)
		defer exec.Command("ip", "netns", "del", ns).Run()
	}
	in := func(ns string, args ...string) []string { return append([]string{"ip", "netns", "exec", ns}, args...) }
	// Links are created inside the namespaces: nothing named mgawg* ever exists in the namespace of the host.
	sh("ip", "-n", nsN, "link", "add", labIfA, "type", "veth", "peer", "name", "va", "netns", nsA)
	sh("ip", "-n", nsN, "link", "add", labIfB, "type", "veth", "peer", "name", "vb", "netns", nsB)
	sh("ip", "-n", nsN, "link", "add", "mg3-up", "type", "veth", "peer", "name", "mg3-dn", "netns", nsI)
	for _, c := range [][]string{
		{nsN, labIfA, "10.66.4.1/22"}, {nsN, labIfB, "10.66.8.1/22"}, {nsN, "mg3-up", "192.0.2.1/24"},
		{nsA, "va", "10.66.4.5/22"}, {nsB, "vb", "10.66.8.5/22"}, {nsI, "mg3-dn", "192.0.2.2/24"},
	} {
		sh("ip", "-n", c[0], "addr", "add", c[2], "dev", c[1])
		sh("ip", "-n", c[0], "link", "set", c[1], "up")
		sh("ip", "-n", c[0], "link", "set", "lo", "up")
	}
	sh("ip", "-n", nsA, "route", "add", "default", "via", "10.66.4.1")
	sh("ip", "-n", nsB, "route", "add", "default", "via", "10.66.8.1")
	sh("ip", "-n", nsI, "route", "add", "10.66.0.0/16", "via", "192.0.2.1") // so that, without NAT, a reply could find its way back

	// The "internet": one web server that answers with the address it saw, and a service of the node itself.
	web := `import http.server as h
class H(h.BaseHTTPRequestHandler):
    def do_GET(s):
        s.send_response(200); s.end_headers(); s.wfile.write(s.client_address[0].encode())
    def log_message(s, *a): pass
h.HTTPServer(("192.0.2.2", 8080), H).serve_forever()`
	own := `import socket
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(("0.0.0.0", 2222)); s.listen(8)
while True:
    c, _ = s.accept(); c.send(b"node"); c.close()`
	for _, p := range []struct{ ns, code string }{{nsI, web}, {nsN, own}} {
		cmd := exec.Command("ip", "netns", "exec", p.ns, "python3", "-c", p.code)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	}
	time.Sleep(400 * time.Millisecond)

	ping := func(ns, to string) bool {
		_, err := try(in(ns, "ping", "-c", "2", "-W", "1", "-i", "0.2", to)...)
		return err == nil
	}
	fetch := func(ns, url string) (string, bool) {
		out, err := try(in(ns, "curl", "-s", "--max-time", "2", url)...)
		return strings.TrimSpace(out), err == nil && out != ""
	}

	// The lab reproduces the hole first: a node that only forwards connects clients to each other and to the node of the
	// other profile (N1 found exactly this with the nft text of the first draft of the plan).
	sh(in(nsN, "sysctl", "-w", "net.ipv4.ip_forward=1")...)
	if !ping(nsA, "10.66.8.5") || !ping(nsA, "10.66.8.1") {
		t.Fatal("the lab is wrong: without rules client A should reach client B and the node of profile B")
	}
	if got, ok := fetch(nsA, "http://192.0.2.2:8080/"); !ok || got != "10.66.4.5" {
		t.Fatalf("the lab is wrong: without masquerade the server should see the client address, saw %q ok=%v", got, ok)
	}

	// Install the rules with the real host owner, inside the node namespace.
	cmd := exec.Command("ip", "netns", "exec", nsN, os.Args[0], "-test.run=^TestLabNodeHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "MG3_LAB_NODE=1")
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "PASS") {
		t.Fatalf("helper: %v\n%s", err, out)
	}

	// Allowed: the internet, with the node's address as the source; the node's own address by ping.
	if got, ok := fetch(nsA, "http://192.0.2.2:8080/"); !ok || got != "192.0.2.1" {
		t.Errorf("client A to the internet: %q ok=%v (want a connection masqueraded to 192.0.2.1)", got, ok)
	}
	if got, ok := fetch(nsB, "http://192.0.2.2:8080/"); !ok || got != "192.0.2.1" {
		t.Errorf("client B to the internet: %q ok=%v", got, ok)
	}
	if !ping(nsA, "10.66.4.1") || !ping(nsB, "10.66.8.1") {
		t.Error("a client cannot ping its own gateway")
	}
	// Closed: another client, the node of the other profile, a service of the node.
	if ping(nsA, "10.66.8.5") || ping(nsB, "10.66.4.5") {
		t.Error("client-to-client traffic across profiles is open")
	}
	if ping(nsA, "10.66.8.1") || ping(nsB, "10.66.4.1") {
		t.Error("a client reaches the node address of ANOTHER profile")
	}
	if _, ok := fetch(nsA, "http://10.66.4.1:2222/"); ok {
		t.Error("a client reached a TCP service of the node itself through the tunnel")
	}
	if out, err := try(in(nsA, "python3", "-c", `import socket,sys
s=socket.socket(); s.settimeout(1)
try:
    s.connect(("10.66.4.1",2222)); sys.exit(1)
except OSError:
    sys.exit(0)`)...); err != nil {
		t.Errorf("TCP to the node's own listener is not dropped: %v %s", err, out)
	}
	// The UDP counter counts datagrams to the port from the outside, whether or not anything listens.
	for i := 0; i < 5; i++ {
		_, _ = try(in(nsI, "python3", "-c", `import socket
socket.socket(socket.AF_INET, socket.SOCK_DGRAM).sendto(b"x", ("192.0.2.1", 51842))`)...)
	}
	out := sh(in(nsN, "nft", "list", "counters", "table", "inet", NftTunnelTable)...)
	if !strings.Contains(out, "packets 5 ") {
		t.Errorf("the UDP counter of the port did not count 5 datagrams:\n%s", out)
	}
}
