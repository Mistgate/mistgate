//go:build linux

package hostctl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The relay forwards against real packets: a client, an entry node and an exit in network namespaces of their own
// (mgrly-<pid>-*). The entry's rules are installed INSIDE its namespace by this very test binary, with the real host
// owner, so nothing touches the host. It shows what a unit test cannot: the exit sees the entry as the source (masquerade),
// the reply finds its way back, TCP and UDP both pass, a replaced set stops what it no longer lists, a FORWARD chain
// that drops makes the install fail closed, and Cleanup removes the lot.
//
// Opt-in: MG_ROOT_TESTS=1 as root with ip, nft and python3.
//
//	MG_ROOT_TESTS=1 go test ./internal/node/hostctl -run TestLabRelay -v

const (
	labRelayEntry  = "198.51.100.1" // the entry, as the client sees it
	labRelayExit   = "192.0.2.2"    // the exit
	labRelayBehind = "203.0.113.2"  // another host behind the entry, which the client reaches THROUGH it
)

// TestLabRelayHelper is the process the lab runs inside the entry namespace. MG3_LAB_RELAY is "cleanup" or the forwards as
// "id:network:port:to_ip:to_port,...". It prints "RESULT ok" or "RESULT err <message>". Skipped in a normal run.
func TestLabRelayHelper(t *testing.T) {
	spec := os.Getenv("MG3_LAB_RELAY")
	if spec == "" {
		t.Skip("lab helper")
	}
	dir, err := os.MkdirTemp("", "mgrly-helper")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	// The real host owner, with its files in a temp dir: Cleanup must not touch the machine's own sysctl and journald drop-ins.
	h := &linuxHost{
		log: slog.New(slog.DiscardHandler), run: execRunner,
		sysctlFile: filepath.Join(dir, "sysctl.conf"), journaldFile: filepath.Join(dir, "journald.conf"),
		procSys: "/proc/sys", procRoot: "/proc", sshdConfDir: dir,
	}
	if spec == "cleanup" {
		if err := h.Cleanup(context.Background()); err != nil {
			fmt.Println("RESULT err", err)
			return
		}
		fmt.Println("RESULT ok")
		return
	}
	var fs []Forward
	for _, item := range strings.Split(spec, ",") {
		p := strings.Split(item, ":")
		port, _ := strconv.Atoi(p[2])
		toPort, _ := strconv.Atoi(p[4])
		fs = append(fs, Forward{ID: p[0], Network: p[1], Port: uint16(port), ToIP: netip.MustParseAddr(p[3]), ToPort: uint16(toPort)})
	}
	if err := h.SetRelays(context.Background(), fs); err != nil {
		fmt.Println("RESULT err", err, "drop="+strconv.FormatBool(errors.Is(err, ErrForwardDrop)))
		return
	}
	fmt.Println("RESULT ok")
}

func TestLabRelay(t *testing.T) {
	if os.Getenv("MG_ROOT_TESTS") == "" || os.Geteuid() != 0 {
		t.Skip("set MG_ROOT_TESTS=1 and run as root")
	}
	for _, tool := range []string{"ip", "nft", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	id := fmt.Sprintf("mgrly-%d", os.Getpid())
	nsC, nsE, nsX, nsB := id+"-c", id+"-e", id+"-x", id+"-b"
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
	for _, ns := range []string{nsC, nsE, nsX, nsB} {
		sh("ip", "netns", "add", ns)
		defer exec.Command("ip", "netns", "del", ns).Run()
	}
	in := func(ns string, args ...string) []string { return append([]string{"ip", "netns", "exec", ns}, args...) }
	sh("ip", "-n", nsE, "link", "add", "mgrly-ec", "type", "veth", "peer", "name", "mgrly-ce", "netns", nsC)
	sh("ip", "-n", nsE, "link", "add", "mgrly-ex", "type", "veth", "peer", "name", "mgrly-xe", "netns", nsX)
	sh("ip", "-n", nsE, "link", "add", "mgrly-eb", "type", "veth", "peer", "name", "mgrly-be", "netns", nsB)
	for _, c := range [][]string{
		{nsE, "mgrly-ec", labRelayEntry + "/24"}, {nsC, "mgrly-ce", "198.51.100.2/24"},
		{nsE, "mgrly-ex", "192.0.2.1/24"}, {nsX, "mgrly-xe", labRelayExit + "/24"},
		{nsE, "mgrly-eb", "203.0.113.1/24"}, {nsB, "mgrly-be", labRelayBehind + "/24"},
	} {
		sh("ip", "-n", c[0], "addr", "add", c[2], "dev", c[1])
		sh("ip", "-n", c[0], "link", "set", c[1], "up")
		sh("ip", "-n", c[0], "link", "set", "lo", "up")
	}

	// The client reaches the host behind the entry through the entry, which routes (once forwarding is on).
	sh("ip", "-n", nsC, "route", "add", "203.0.113.0/24", "via", labRelayEntry)
	sh("ip", "-n", nsB, "route", "add", "198.51.100.0/24", "via", "203.0.113.1")

	// The exit: a TCP and a UDP service that answer with the address they saw. It has no route to the client's network, so a
	// reply to an unmasqueraded source could not come back.
	echo := `import socket, threading
def tcp():
    s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(("192.0.2.2", 443)); s.listen(8)
    while True:
        c, a = s.accept(); c.send(b"tcp:" + a[0].encode()); c.close()
threading.Thread(target=tcp, daemon=True).start()
u = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); u.bind(("192.0.2.2", 5353))
while True:
    _, a = u.recvfrom(64); u.sendto(b"udp:" + a[0].encode(), a)`
	// The host behind the entry runs the same services on the same ports under another name and another address.
	behind := strings.NewReplacer("192.0.2.2", labRelayBehind, "b\"tcp:\"", "b\"behind-tcp:\"", "b\"udp:\"", "b\"behind-udp:\"", "443)", "8443)", "5353)", "9443)").Replace(echo)
	for _, p := range []struct{ ns, code string }{{nsX, echo}, {nsB, behind}} {
		cmd := exec.Command("ip", "netns", "exec", p.ns, "python3", "-c", p.code)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	}
	time.Sleep(400 * time.Millisecond)

	tcpTo := func(addr string, port int) string {
		out, err := try(in(nsC, "python3", "-c", fmt.Sprintf(`import socket,sys
s=socket.socket(); s.settimeout(1.5)
try:
    s.connect(("%s",%d)); print(s.recv(64).decode())
except OSError:
    print("none")`, addr, port))...)
		if err != nil {
			t.Fatalf("tcp client: %v %s", err, out)
		}
		return strings.TrimSpace(out)
	}
	tcp := func(port int) string { return tcpTo(labRelayEntry, port) }
	udpTo := func(addr string, port int) string {
		out, err := try(in(nsC, "python3", "-c", fmt.Sprintf(`import socket
s=socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.settimeout(1.5)
s.sendto(b"hi", ("%s",%d))
try:
    print(s.recv(64).decode())
except OSError:
    print("none")`, addr, port))...)
		if err != nil {
			t.Fatalf("udp client: %v %s", err, out)
		}
		return strings.TrimSpace(out)
	}
	udp := func(port int) string { return udpTo(labRelayEntry, port) }
	helper := func(spec string) string {
		t.Helper()
		cmd := exec.Command("ip", "netns", "exec", nsE, os.Args[0], "-test.run=^TestLabRelayHelper$", "-test.v")
		cmd.Env = append(os.Environ(), "MG3_LAB_RELAY="+spec)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("helper: %v\n%s", err, out)
		}
		for _, l := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(l, "RESULT ") {
				return strings.TrimPrefix(l, "RESULT ")
			}
		}
		t.Fatalf("helper printed no result:\n%s", out)
		return ""
	}
	entryTables := func() string { return sh(in(nsE, "nft", "list", "ruleset")...) }
	entryForward := func() string { return strings.TrimSpace(sh(in(nsE, "cat", "/proc/sys/net/ipv4/ip_forward")...)) }

	// Nothing is forwarded yet, and the entry does not forward packets (a new namespace may inherit 1 from the machine: Docker).
	sh(in(nsE, "sysctl", "-w", "net.ipv4.ip_forward=0")...)
	if got := tcp(8443); got != "none" {
		t.Fatalf("the lab is wrong: 8443/tcp answers before any forward: %q", got)
	}
	if entryForward() != "0" {
		t.Fatal("the lab is wrong: the entry forwards before the agent asked for it")
	}

	both := "rly_t:tcp:8443:" + labRelayExit + ":443,rly_u:udp:9443:" + labRelayExit + ":5353"
	if got := helper(both); got != "ok" {
		t.Fatalf("install: %s", got)
	}
	if entryForward() != "1" {
		t.Error("the agent did not switch IPv4 forwarding on")
	}
	// Through the entry to the exit; the exit saw the entry (masquerade), not the client.
	if got := tcp(8443); got != "tcp:192.0.2.1" {
		t.Errorf("TCP through the forward: %q, want the exit's answer naming the entry 192.0.2.1", got)
	}
	if got := udp(9443); got != "udp:192.0.2.1" {
		t.Errorf("UDP through the forward: %q, want the exit's answer naming the entry 192.0.2.1", got)
	}
	// A packet that only passes through the entry, to a host behind it on the very ports that are forwarded, is not for the entry:
	// it reaches that host as it is (same source, no masquerade), not the exit.
	if got := tcpTo(labRelayBehind, 8443); got != "behind-tcp:198.51.100.2" {
		t.Errorf("TCP through the entry to a host behind it: %q, want that host answering with the client's own address", got)
	}
	if got := udpTo(labRelayBehind, 9443); got != "behind-udp:198.51.100.2" {
		t.Errorf("UDP through the entry to a host behind it: %q, want that host answering with the client's own address", got)
	}
	// What is not forwarded stays closed: another port, and the other network on a forwarded port.
	if got := tcp(8444); got != "none" {
		t.Errorf("an unforwarded TCP port answers: %q", got)
	}
	if got := udp(8443); got != "none" {
		t.Errorf("UDP on a TCP-only forward answers: %q", got)
	}
	// The counters count what was forwarded, each its own.
	out := sh(in(nsE, "nft", "list", "counters", "table", "inet", NftRelayTable)...)
	for _, name := range []string{"tcp_8443", "udp_9443"} {
		if m := regexp.MustCompile(`counter ` + name + ` \{\s+packets (\d+)`).FindStringSubmatch(out); m == nil || m[1] == "0" {
			t.Errorf("counter %s did not count:\n%s", name, out)
		}
	}

	// A replaced set stops what it no longer lists.
	if got := helper("rly_t:tcp:8443:" + labRelayExit + ":443"); got != "ok" {
		t.Fatalf("reinstall: %s", got)
	}
	if got := udp(9443); got != "none" {
		t.Errorf("a removed UDP forward still answers: %q", got)
	}
	if got := tcp(8443); got != "tcp:192.0.2.1" {
		t.Errorf("TCP after the replacement: %q", got)
	}

	// A FORWARD chain that drops (Docker, UFW): fail closed, the table is gone, the port is not hijacked.
	sh(in(nsE, "nft", "add", "table", "ip", "filter")...)
	sh(in(nsE, "nft", "add", "chain", "ip", "filter", "FORWARD", "{ type filter hook forward priority 0 ; policy drop ; }")...)
	if got := helper(both); !strings.Contains(got, "forward_drop") || !strings.Contains(got, "drop=true") {
		t.Errorf("a dropping FORWARD chain: %q", got)
	}
	if strings.Contains(entryTables(), NftRelayTable) {
		t.Error("the relay table is installed behind a dropping FORWARD chain")
	}
	if got := tcp(8443); got != "none" {
		t.Errorf("a port is forwarded behind a dropping FORWARD chain: %q", got)
	}
	sh(in(nsE, "nft", "delete", "table", "ip", "filter")...)

	// And it works again once the drop is gone.
	if got := helper(both); got != "ok" {
		t.Fatalf("install after the drop is gone: %s", got)
	}
	if got := tcp(8443); got != "tcp:192.0.2.1" {
		t.Errorf("TCP after the drop is gone: %q", got)
	}

	// Cleanup removes everything.
	if got := helper("cleanup"); got != "ok" {
		t.Fatalf("cleanup: %s", got)
	}
	if strings.Contains(entryTables(), NftRelayTable) || strings.Contains(entryTables(), "dnat") {
		t.Errorf("Cleanup left rules behind:\n%s", entryTables())
	}
	if got := tcp(8443); got != "none" {
		t.Errorf("TCP after Cleanup: %q", got)
	}
	if got := udp(9443); got != "none" {
		t.Errorf("UDP after Cleanup: %q", got)
	}
}
