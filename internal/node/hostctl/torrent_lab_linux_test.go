//go:build linux

package hostctl

import (
	"context"
	"encoding/hex"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The torrent guard against real packets and the real kernel: a client behind an interface named like an AWG one and an
// "internet" behind another link, each in a network namespace of its own, with the node's rules and NFQUEUE runtime
// installed by this test binary inside a throw-away user and network namespace (`unshare -Urn`): nothing touches the host,
// and no root is needed. It checks what only the kernel can answer: the block verdict (repeat + mark) turns into a ct mark
// that drops the rest of the connection, and ordinary traffic passes.
//
// Opt-in: MG_USERNS_TESTS=1 with unshare, nsenter, ip, nft and python3, and unprivileged user namespaces.
//
//	MG_USERNS_TESTS=1 go test ./internal/node/hostctl -run TestLabTorrentGuard -v
func TestLabTorrentGuard(t *testing.T) {
	if os.Getenv("MG_USERNS_TESTS") == "" {
		t.Skip("set MG_USERNS_TESTS=1")
	}
	for _, tool := range []string{"unshare", "nsenter", "ip", "nft", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	cmd := exec.Command("unshare", "-Urn", "--fork", os.Args[0], "-test.run", "^TestLabTorrentGuardNode$", "-test.v")
	cmd.Env = append(os.Environ(), "MG3_TG_LAB=1")
	out, err := cmd.CombinedOutput()
	t.Logf("%s", out)
	if err != nil || !strings.Contains(string(out), "--- PASS: TestLabTorrentGuardNode") {
		t.Fatalf("lab: %v", err)
	}
}

// TestLabTorrentGuardNode runs inside the node namespace of TestLabTorrentGuard. Skipped in a normal run.
func TestLabTorrentGuardNode(t *testing.T) {
	if os.Getenv("MG3_TG_LAB") == "" {
		t.Skip("lab helper")
	}
	ctx := context.Background()
	sh := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	netns := func() string { // a namespace kept alive by a sleeping process; nsenter -t <pid> -n enters it
		t.Helper()
		c := exec.Command("unshare", "-n", "sleep", "120")
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
		time.Sleep(100 * time.Millisecond)
		return strconv.Itoa(c.Process.Pid)
	}
	client, inet := netns(), netns()
	in := func(pid string, args ...string) string {
		return sh(append([]string{"nsenter", "-t", pid, "-n"}, args...)...)
	}

	sh("ip", "link", "set", "lo", "up")
	sh("ip", "link", "add", "mgawg51820", "type", "veth", "peer", "name", "c0", "netns", client)
	sh("ip", "link", "add", "up0", "type", "veth", "peer", "name", "i0", "netns", inet)
	sh("ip", "addr", "add", "10.66.4.1/24", "dev", "mgawg51820")
	sh("ip", "addr", "add", "198.51.100.1/24", "dev", "up0")
	sh("ip", "link", "set", "mgawg51820", "up")
	sh("ip", "link", "set", "up0", "up")
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644); err != nil {
		t.Skipf("no forwarding in this namespace: %v", err)
	}
	for pid, cfg := range map[string][2]string{client: {"10.66.4.2/24", "10.66.4.1"}, inet: {"198.51.100.7/24", "198.51.100.1"}} {
		dev := map[string]string{client: "c0", inet: "i0"}[pid]
		in(pid, "ip", "link", "set", "lo", "up")
		in(pid, "ip", "addr", "add", cfg[0], "dev", dev)
		in(pid, "ip", "link", "set", dev, "up")
		in(pid, "ip", "route", "add", "default", "via", cfg[1])
	}

	var mu sync.Mutex
	var detections []TorrentDetection
	h := New(slog.New(slog.DiscardHandler)).(TorrentGuardHost)
	if err := h.SetTorrentGuard(ctx, []string{"mgawg51820"}, func(d TorrentDetection) {
		mu.Lock()
		detections = append(detections, d)
		mu.Unlock()
	}); err != nil {
		if strings.Contains(err.Error(), "netfilter queue") {
			t.Skipf("no NFQUEUE here: %v", err)
		}
		t.Fatal(err)
	}
	defer h.SetTorrentGuard(ctx, nil, nil)

	// The internet side: a UDP and a TCP server on 6881 that write every payload they get, one line each, to a file.
	got := t.TempDir() + "/got"
	server := exec.Command("nsenter", "-t", inet, "-n", "python3", "-c", `
import socket, sys, threading
out = open(sys.argv[1], "a", buffering=1)
u = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); u.bind(("0.0.0.0", 6881))
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(("0.0.0.0", 6881)); s.listen(8)
def udp():
    while True:
        d, _ = u.recvfrom(2048); out.write("udp " + d.hex() + "\n")
def conn(c):
    while True:
        d = c.recv(2048)
        if not d: return
        out.write("tcp " + d.hex() + "\n"); c.sendall(b"reply")
threading.Thread(target=udp, daemon=True).start()
while True:
    c, _ = s.accept(); threading.Thread(target=conn, args=(c,), daemon=True).start()
`, got)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Process.Kill(); _ = server.Wait() }()
	time.Sleep(300 * time.Millisecond)

	// The client: UDP datagrams, then TCP streams that each send their chunks with a pause and report what came back.
	clientRun := func(script string, args ...string) string {
		return in(client, append([]string{"python3", "-c", script}, args...)...)
	}
	const udpSend = `
import socket, sys
for h in sys.argv[1:]:
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.sendto(bytes.fromhex(h), ("198.51.100.7", 6881)); s.close()
`
	const tcpSend = `
import socket, sys, time
s = socket.create_connection(("198.51.100.7", 6881), timeout=3)
s.settimeout(1)
replies = 0
for h in sys.argv[1:]:
    try:
        s.sendall(bytes.fromhex(h)); time.sleep(0.3); replies += len(s.recv(100))
    except Exception:
        pass
print("replies", replies)
`
	hexOf := func(s string) string { return hex.EncodeToString([]byte(s)) }
	dhtQuery := hexOf("d1:ad2:id20:01234567890123456789e1:q4:ping1:t2:aa1:y1:qe")
	clientRun(udpSend, hexOf("ordinary-udp"), dhtQuery, hexOf("after-dht"))
	torrent := clientRun(tcpSend, hexOf("\x13BitTorrent protocol")+"00", hexOf("more-after-handshake"))
	ordinary := clientRun(tcpSend, hexOf("GET / HTTP/1.1\r\n\r\n"), hexOf("second-request"))
	time.Sleep(500 * time.Millisecond)

	b, _ := os.ReadFile(got)
	received := string(b)
	t.Logf("received:\n%sordinary: %storrent: %s", received, ordinary, torrent)
	for _, want := range []string{"udp " + hexOf("ordinary-udp"), "udp " + hexOf("after-dht"), "tcp " + hexOf("GET / HTTP/1.1\r\n\r\n"), "tcp " + hexOf("second-request")} {
		if !strings.Contains(received, want) {
			t.Errorf("ordinary traffic did not arrive: %q\nreceived:\n%s", want, received)
		}
	}
	// The runtime forgets a connection once it decided it, so only the ct mark can drop the retransmissions of the
	// handshake segment that come after it (and the rest of the connection).
	for _, blocked := range []string{dhtQuery, hexOf("\x13BitTorrent"), hexOf("more-after-handshake")} {
		if strings.Contains(received, blocked) {
			t.Errorf("blocked traffic arrived: %q\nreceived:\n%s", blocked, received)
		}
	}
	if !strings.Contains(ordinary, "replies 10") || !strings.Contains(torrent, "replies 0") {
		t.Errorf("replies: ordinary %q, torrent %q", ordinary, torrent)
	}
	// The blocked connection is remembered by the kernel, not by the runtime.
	if out := sh("nft", "list", "table", "inet", NftTorrentTable); !strings.Contains(out, "ct mark set 0x4d475442 drop") {
		t.Errorf("table: %s", out)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Logf("detections: %+v", detections)
	kinds := map[string]bool{}
	for _, d := range detections {
		kinds[string(d.Signature)] = true
		if d.TunnelIface != "mgawg51820" || d.TunnelIP.String() != "10.66.4.2" {
			t.Errorf("detection = %+v", d)
		}
	}
	if !kinds["bittorrent_dht"] || !kinds["bittorrent_tcp"] {
		t.Errorf("detections = %+v", detections)
	}
}
