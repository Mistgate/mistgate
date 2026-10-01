//go:build linux

package warp

// The WARP lab: the real dataplane against a fake WARP peer (kernel WireGuard in a network namespace).
// Needs root and `ip`, `nft`, `curl`, `ethtool`; skipped without MG_ROOT_TESTS=1.
//
//	GOOS=linux go test -c -o mg3warp.test ./internal/node/warp/
//	MG_ROOT_TESTS=1 ./mg3warp.test -test.run '^TestLab$' -test.v          (as root, in WSL or a disposable VM)
//
// Everything the lab creates is named mg3-warp-*: four namespaces (node, inet, two clients) and the veth pairs in
// them; a namespace takes its links, routes, rules and nft tables along when it is deleted, so the root
// namespace of the machine is never touched. Stale mg3-warp-* namespaces of an earlier crashed run are removed first.
// The test binary re-executes itself inside the namespaces (TestLabInner in the node one, TestLabPeer in the
// "internet" one), so the manager under test runs exactly as it would on a node, only in its own netns.
//
//	node ns:  mg3-wn0 192.0.2.1/24 (default route via the inet ns), mg3-wn1 10.66.0.1/24 + fd00:66::1/64 (client A, goes
//	          through WARP), mg3-wn2 10.67.0.1/24 (client B, goes direct, masqueraded by the lab)
//	inet ns:  mg3-wi0 192.0.2.2/24, the fake WARP peer mg3-wp0 (accepts 172.16.0.2/32 and fd00:16::2/128 like WARP),
//	          and the "internet": 203.0.113.10 (trace: warp=on when the source is a WARP address), 203.0.113.11, 2001:db8:1::10

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/mistgate/mistgate/internal/plugin"
)

const (
	nsNode = "mg3-warp-node"
	nsInet = "mg3-warp-inet"
	nsCA   = "mg3-warp-ca"
	nsCB   = "mg3-warp-cb"

	probeA = "http://203.0.113.10/cdn-cgi/trace"
	probeB = "http://203.0.113.11/generate_204"
)

func run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func mustRun(t testing.TB, name string, args ...string) string {
	t.Helper()
	out, err := run(name, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

func labCleanup() {
	out, _ := run("ip", "netns", "list")
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) > 0 && strings.HasPrefix(f[0], "mg3-warp-") {
			run("ip", "netns", "del", f[0])
		}
	}
}

func labSetup(t *testing.T) {
	t.Helper()
	ip := func(args ...string) { mustRun(t, "ip", args...) }
	for _, ns := range []string{nsNode, nsInet, nsCA, nsCB} {
		ip("netns", "add", ns)
		ip("-n", ns, "link", "set", "lo", "up")
	}
	pair := func(a, nsA, b, nsB string) {
		ip("link", "add", a, "type", "veth", "peer", "name", b)
		ip("link", "set", a, "netns", nsA)
		ip("link", "set", b, "netns", nsB)
		ip("-n", nsA, "link", "set", a, "up")
		ip("-n", nsB, "link", "set", b, "up")
	}
	pair("mg3-wn0", nsNode, "mg3-wi0", nsInet)
	pair("mg3-wn1", nsNode, "mg3-wc0", nsCA)
	pair("mg3-wn2", nsNode, "mg3-wc1", nsCB)
	ip("-n", nsNode, "addr", "add", "192.0.2.1/24", "dev", "mg3-wn0")
	ip("-n", nsInet, "addr", "add", "192.0.2.2/24", "dev", "mg3-wi0")
	ip("-n", nsNode, "route", "add", "default", "via", "192.0.2.2")
	ip("-n", nsNode, "addr", "add", "10.66.0.1/24", "dev", "mg3-wn1")
	ip("-n", nsNode, "addr", "add", "fd00:66::1/64", "dev", "mg3-wn1", "nodad")
	ip("-n", nsCA, "addr", "add", "10.66.0.2/24", "dev", "mg3-wc0")
	ip("-n", nsCA, "addr", "add", "fd00:66::2/64", "dev", "mg3-wc0", "nodad")
	ip("-n", nsCA, "route", "add", "default", "via", "10.66.0.1")
	ip("-n", nsCA, "-6", "route", "add", "default", "via", "fd00:66::1")
	ip("-n", nsNode, "addr", "add", "10.67.0.1/24", "dev", "mg3-wn2")
	ip("-n", nsCB, "addr", "add", "10.67.0.2/24", "dev", "mg3-wc1")
	ip("-n", nsCB, "route", "add", "default", "via", "10.67.0.1")
	mustRun(t, "ip", "netns", "exec", nsNode, "sysctl", "-qw", "net.ipv4.ip_forward=1", "net.ipv6.conf.all.forwarding=1")
	// real UDP checksums on the veth ends: the reserved-bytes stamping must leave a valid one (research: offload
	// would hide a bad checksum, because the receiver skips verification of CHECKSUM_PARTIAL packets)
	run("ip", "netns", "exec", nsNode, "ethtool", "-K", "mg3-wn0", "tx", "off", "rx", "off")
	run("ip", "netns", "exec", nsInet, "ethtool", "-K", "mg3-wi0", "tx", "off", "rx", "off")
	nftIn(t, nsNode, "table ip mg3_lab {\n chain post {\n  type nat hook postrouting priority srcnat;\n  oifname \"mg3-wn0\" ip saddr 10.67.0.0/24 masquerade\n }\n}\n")
}

func nftIn(t testing.TB, ns, script string) {
	t.Helper()
	cmd := exec.Command("ip", "netns", "exec", ns, "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nft in %s: %v\n%s", ns, err, out)
	}
}

// TestLab is the outer test: builds the namespaces and runs the inner test in the node one.
func TestLab(t *testing.T) {
	if os.Getenv("MG3_WARP_INNER") != "" {
		t.Skip("inner process")
	}
	if os.Getenv("MG_ROOT_TESTS") != "1" || os.Geteuid() != 0 {
		t.Skip("needs root and MG_ROOT_TESTS=1")
	}
	for _, tool := range []string{"ip", "nft", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	labCleanup()
	t.Cleanup(labCleanup)
	labSetup(t)
	args := []string{"netns", "exec", nsNode, os.Args[0], "-test.run", "^TestLabInner$", "-test.v", "-test.timeout", "10m"}
	if testing.Short() {
		args = append(args, "-test.short")
	}
	cmd := exec.Command("ip", args...)
	cmd.Env = append(os.Environ(), "MG3_WARP_INNER=node")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("inner lab failed: %v", err)
	}
}

// --- the fake peer (runs in the inet namespace) -----------------------------------------------------------------

func TestLabPeer(t *testing.T) {
	if os.Getenv("MG3_WARP_INNER") != "peer" {
		t.Skip("peer process")
	}
	say := func(f string, a ...any) { fmt.Printf("peer: "+f+"\n", a...) }
	priv, err := wgtypes.ParseKey(os.Getenv("MG3_PEER_PRIV"))
	if err != nil {
		t.Fatal(err)
	}
	clientPub, err := wgtypes.ParseKey(os.Getenv("MG3_CLIENT_PUB"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(os.Getenv("MG3_PEER_PORT"))

	if err := netlink.LinkAdd(&netlink.Wireguard{LinkAttrs: netlink.LinkAttrs{Name: "mg3-wp0"}}); err != nil {
		t.Fatal(err)
	}
	wc, err := wgctrl.New()
	if err != nil {
		t.Fatal(err)
	}
	defer wc.Close()
	_, c4, _ := net.ParseCIDR("172.16.0.2/32")
	_, c6, _ := net.ParseCIDR("fd00:16::2/128")
	curPort := port
	cfgPort := func(p int) error {
		curPort = p
		return wc.ConfigureDevice("mg3-wp0", wgtypes.Config{PrivateKey: &priv, ListenPort: &p,
			Peers: []wgtypes.PeerConfig{{PublicKey: clientPub, ReplaceAllowedIPs: true, AllowedIPs: []net.IPNet{*c4, *c6}}}})
	}
	if err := cfgPort(port); err != nil {
		t.Fatal(err)
	}
	wp, _ := netlink.LinkByName("mg3-wp0")
	for _, a := range []string{"172.16.0.1/32", "fd00:16::1/128"} {
		ad, _ := netlink.ParseAddr(a)
		if err := netlink.AddrAdd(wp, ad); err != nil {
			t.Fatal(err)
		}
	}
	if err := netlink.LinkSetUp(wp); err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		dst string
	}{{"172.16.0.2/32"}, {"fd00:16::2/128"}} {
		_, dst, _ := net.ParseCIDR(r.dst)
		if err := netlink.RouteAdd(&netlink.Route{LinkIndex: wp.Attrs().Index, Dst: dst, Scope: netlink.SCOPE_LINK}); err != nil {
			t.Fatal(err)
		}
	}
	lo, _ := netlink.LinkByName("lo")
	for _, a := range []string{"203.0.113.10/32", "203.0.113.11/32", "2001:db8:1::10/128"} {
		ad, _ := netlink.ParseAddr(a)
		if err := netlink.AddrAdd(lo, ad); err != nil {
			t.Fatal(err)
		}
	}

	warpNets := []netip.Prefix{netip.MustParsePrefix("172.16.0.0/24"), netip.MustParsePrefix("fd00:16::/64")}
	mux := http.NewServeMux()
	mux.HandleFunc("/cdn-cgi/trace", func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		a, _ := netip.ParseAddr(host)
		flag := "off"
		for _, p := range warpNets {
			if p.Contains(a.Unmap()) {
				flag = "on"
			}
		}
		mss, _ := r.Context().Value(mssKey{}).(int)
		fmt.Fprintf(w, "fl=lab\nip=%s\ncolo=TST\nwarp=%s\nmss=%d\n", a.Unmap(), flag, mss)
	})
	mux.HandleFunc("/generate_204", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	ln, err := net.Listen("tcp", ":80")
	if err != nil {
		t.Fatal(err)
	}
	go (&http.Server{Handler: mux, ConnContext: func(ctx context.Context, c net.Conn) context.Context {
		return context.WithValue(ctx, mssKey{}, tcpMSS(c))
	}}).Serve(ln)
	pc, err := net.ListenPacket("udp", ":5353")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_ = n
			h, _, _ := net.SplitHostPort(from.String())
			a, _ := netip.ParseAddr(h)
			pc.WriteTo([]byte("src="+a.Unmap().String()), from)
		}
	}()

	var rawConn net.PacketConn
	say("ready")
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "port":
			p, _ := strconv.Atoi(f[1])
			if err := cfgPort(p); err != nil {
				say("error %v", err)
				continue
			}
			say("ok")
		case "wgdown": // the peer forgets the client: nothing is answered, no error anywhere
			p := curPort
			if err := wc.ConfigureDevice("mg3-wp0", wgtypes.Config{ReplacePeers: true}); err != nil {
				say("error %v", err)
				continue
			}
			curPort = p
			say("ok")
		case "wgup":
			if err := cfgPort(curPort); err != nil {
				say("error %v", err)
				continue
			}
			say("ok")
		case "raw": // a plain UDP listener on the endpoint address: shows the bytes the client really sends
			if rawConn != nil {
				rawConn.Close()
			}
			c, err := net.ListenPacket("udp", "192.0.2.2:"+f[1])
			if err != nil {
				say("error %v", err)
				continue
			}
			rawConn = c
			go func() {
				buf := make([]byte, 2048)
				for {
					n, _, err := c.ReadFrom(buf)
					if err != nil {
						return
					}
					say("raw %s %d", hex.EncodeToString(buf[:4]), n)
				}
			}()
			say("ok")
		case "rawstop":
			if rawConn != nil {
				rawConn.Close()
				rawConn = nil
			}
			say("ok")
		case "quit":
			return
		}
	}
}

type mssKey struct{}

// tcpMSS is the effective MSS the server side of c negotiated (what an MSS clamp on the path changes).
func tcpMSS(c net.Conn) int {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return 0
	}
	rc, err := tc.SyscallConn()
	if err != nil {
		return 0
	}
	v := 0
	rc.Control(func(fd uintptr) { v, _ = unix.GetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_MAXSEG) })
	return v
}

type peer struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	lines chan string
}

func startPeer(t *testing.T, peerPriv, clientPub wgtypes.Key, port int) *peer {
	t.Helper()
	cmd := exec.Command("ip", "netns", "exec", nsInet, os.Args[0], "-test.run", "^TestLabPeer$", "-test.timeout", "30m")
	cmd.Env = append(os.Environ(), "MG3_WARP_INNER=peer", "MG3_PEER_PRIV="+peerPriv.String(), "MG3_CLIENT_PUB="+clientPub.String(), "MG3_PEER_PORT="+strconv.Itoa(port))
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &peer{cmd: cmd, stdin: in, lines: make(chan string, 256)}
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if l, ok := strings.CutPrefix(sc.Text(), "peer: "); ok {
				select {
				case p.lines <- l:
				default:
				}
			}
		}
		close(p.lines)
	}()
	t.Cleanup(func() {
		fmt.Fprintln(in, "quit")
		in.Close()
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
		}
	})
	p.expect(t, "ready", 20*time.Second)
	return p
}

func (p *peer) expect(t testing.TB, prefix string, d time.Duration) string {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case l, ok := <-p.lines:
			if !ok {
				t.Fatalf("peer exited while waiting for %q", prefix)
			}
			if strings.HasPrefix(l, prefix) {
				return l
			}
		case <-deadline:
			t.Fatalf("peer: no %q within %v", prefix, d)
		}
	}
}

func (p *peer) cmdOK(t testing.TB, line string) {
	t.Helper()
	fmt.Fprintln(p.stdin, line)
	if l := p.expect(t, "", 5*time.Second); l != "ok" {
		t.Fatalf("peer %q: %s", line, l)
	}
}

// --- the inner test (runs in the node namespace) ------------------------------------------------------------------

type lab struct {
	clientPriv, clientPub, peerPub wgtypes.Key
	peer                           *peer
}

func (l *lab) spec(backend string, mod ...func(*plugin.WarpSpec)) *plugin.WarpSpec {
	s := &plugin.WarpSpec{
		Enabled: true, PrivateKey: l.clientPriv.String(), PeerPublicKey: l.peerPub.String(),
		EndpointV4: "192.0.2.2", Ports: []uint16{2408, 500, 1701},
		AddressV4: "172.16.0.2/32", AddressV6: "fd00:16::2/128", Backend: backend,
	}
	for _, m := range mod {
		m(s)
	}
	return s
}

func (l *lab) manager(t *testing.T) *Manager {
	t.Helper()
	m, err := New(Options{
		Log: testLogger(t), Interval: time.Second, FastInterval: 300 * time.Millisecond,
		HandshakeWait: 2500 * time.Millisecond, ProbeTimeout: 1500 * time.Millisecond,
		ProbeAURL: probeA, ProbeBURL: probeB,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestLabInner(t *testing.T) {
	if os.Getenv("MG3_WARP_INNER") != "node" {
		t.Skip("inner process")
	}
	l := &lab{}
	var err error
	if l.clientPriv, err = wgtypes.GeneratePrivateKey(); err != nil {
		t.Fatal(err)
	}
	peerPriv, _ := wgtypes.GeneratePrivateKey()
	l.clientPub, l.peerPub = l.clientPriv.PublicKey(), peerPriv.PublicKey()
	l.peer = startPeer(t, peerPriv, l.clientPub, 2408)
	assertClean(t)

	t.Run("nft syntax", func(t *testing.T) {
		for _, txt := range []string{
			renderNft(NftTable, DefaultIface, nil, netip.AddrPort{}),
			renderNft(NftTable, DefaultIface, []byte{1, 2, 3}, netip.MustParseAddrPort("192.0.2.2:2408")),
			renderNft(NftTable, DefaultIface, []byte{1, 2, 3}, netip.MustParseAddrPort("[2001:db8::1]:500")),
		} {
			cmd := exec.Command("nft", "-c", "-f", "-")
			cmd.Stdin = strings.NewReader(txt)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("nft -c: %v\n%s\n%s", err, out, txt)
			}
		}
	})
	t.Run("preflight", l.preflight)
	for _, backend := range []string{"kernel", "userspace"} {
		backend := backend
		t.Run(backend+"/table", func(t *testing.T) { l.table(t, backend) })
		t.Run(backend+"/pause", func(t *testing.T) { l.pause(t, backend) })
		t.Run(backend+"/reserved", func(t *testing.T) { l.reserved(t, backend) })
		t.Run(backend+"/crash cleanup", func(t *testing.T) { l.crashCleanup(t, backend) })
	}
	t.Run("kernel/no replace peers", l.noReplacePeers)
	t.Run("kernel/account without IPv6", l.noV6)
	t.Run("kernel/port rotation", l.rotation)
	t.Run("kernel/peer dead", l.peerDead)
	t.Run("auto backend", l.autoBackend)
}

func testLogger(testing.TB) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// --- helpers ------------------------------------------------------------------------------------------------------

func readFile(p string) string {
	b, _ := os.ReadFile(p)
	return strings.TrimSpace(string(b))
}

// assertClean: the WARP path left nothing in this namespace.
func assertClean(t testing.TB) {
	t.Helper()
	var bad []string
	for _, args := range [][]string{{"ip", "rule", "show"}, {"ip", "-6", "rule", "show"}} {
		out, _ := run(args[0], args[1:]...)
		if strings.Contains(out, "lookup "+strconv.Itoa(DefaultTable)) {
			bad = append(bad, strings.Join(args, " ")+":\n"+out)
		}
	}
	for _, args := range [][]string{{"ip", "route", "show", "table", "51820"}, {"ip", "-6", "route", "show", "table", "51820"}} {
		// an empty table is not even listed: "FIB table does not exist"
		if out, _ := run(args[0], args[1:]...); out != "" && !strings.Contains(out, "FIB table does not exist") {
			bad = append(bad, strings.Join(args, " ")+":\n"+out)
		}
	}
	if out, err := run("ip", "link", "show", DefaultIface); err == nil {
		bad = append(bad, "link still there:\n"+out)
	}
	if out, err := run("nft", "list", "table", "inet", NftTable); err == nil {
		bad = append(bad, "nft table still there:\n"+out)
	}
	if len(bad) > 0 {
		t.Fatalf("not clean:\n%s", strings.Join(bad, "\n"))
	}
}

func waitState(t testing.TB, m *Manager, want State, d time.Duration) Health {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		h, _ := m.Health()
		if h.State == want {
			return h
		}
		if time.Now().After(deadline) {
			dumpState(t)
			t.Fatalf("state %v, want %v after %v: %+v", h.State, want, d, h)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func dumpState(t testing.TB) {
	for _, args := range [][]string{{"ip", "rule", "show"}, {"ip", "-6", "rule", "show"}, {"ip", "route", "show", "table", "51820"}, {"ip", "-6", "route", "show", "table", "51820"}, {"ip", "-br", "addr"}, {"nft", "list", "ruleset"}} {
		out, _ := run(args[0], args[1:]...)
		t.Logf("%s:\n%s", strings.Join(args, " "), out)
	}
}

// httpGet does a plain HTTP/1.0 GET over dial and returns the body.
func httpGet(dial func(string) (net.Conn, error), addr, path string) (string, error) {
	c, err := dial(addr)
	if err != nil {
		return "", err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := fmt.Fprintf(c, "GET %s HTTP/1.0\r\nHost: lab\r\n\r\n", path); err != nil {
		return "", err
	}
	b, err := io.ReadAll(c)
	if err != nil && len(b) == 0 {
		return "", err
	}
	_, body, _ := bytes.Cut(b, []byte("\r\n\r\n"))
	return string(body), nil
}

func direct(addr string) (net.Conn, error) { return net.DialTimeout("tcp", addr, 3*time.Second) }

func traceOf(t testing.TB, body string) (ip, warp string) {
	t.Helper()
	w, _ := parseTrace(body)
	for _, l := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(l, "ip="); ok {
			ip = v
		}
	}
	return ip, w
}

func wantTrace(t testing.TB, what string, body string, err error, wantIP, wantWarp string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if ip, w := traceOf(t, body); ip != wantIP || w != wantWarp {
		t.Fatalf("%s: ip=%q warp=%q, want ip=%q warp=%q\n%s", what, ip, w, wantIP, wantWarp, body)
	}
}

func curlIn(ns, url string, v6 bool) (string, error) {
	args := []string{"netns", "exec", ns, "curl", "-sS", "-m", "4", "--connect-timeout", "3"}
	if v6 {
		args = append(args, "-6", "-g")
	}
	out, err := exec.Command("ip", append(args, url)...).CombinedOutput()
	return string(out), err
}

func mssOf(body string) int {
	for _, l := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(l, "mss="); ok {
			n, _ := strconv.Atoi(v)
			return n
		}
	}
	return 0
}

// pingGateway: a client must reach the node's own address of its subnet (replies and ICMP errors the node originates
// from that address are not captured by the "from subnet" rule), whatever state the tunnel is in.
func pingGateway(t testing.TB, state string) {
	t.Helper()
	if out, err := exec.Command("ip", "netns", "exec", nsCA, "ping", "-c", "1", "-W", "2", "10.66.0.1").CombinedOutput(); err != nil {
		t.Fatalf("ping the gateway (%s): %v\n%s", state, err, out)
	}
	if out, err := exec.Command("ip", "netns", "exec", nsCA, "ping", "-6", "-c", "1", "-W", "2", "fd00:66::1").CombinedOutput(); err != nil {
		t.Fatalf("ping6 the gateway (%s): %v\n%s", state, err, out)
	}
}

func route(t testing.TB, args ...string) string {
	out, _ := run("ip", args...)
	return out
}

// --- scenarios ----------------------------------------------------------------------------------------------------

func (l *lab) table(t *testing.T, backend string) {
	m := l.manager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); m.Cleanup(context.Background()); assertClean(t) }()

	if err := m.SetRoutedSubnets(ctx, []netip.Prefix{netip.MustParsePrefix("10.66.0.0/24"), netip.MustParsePrefix("fd00:66::/64")}); err != nil {
		t.Fatal(err)
	}
	// before the tunnel exists the client subnets already fail closed
	if out, err := curlIn(nsCA, probeA, false); err == nil {
		t.Fatalf("client A reached the internet before WARP was up: %s", out)
	}
	if err := m.Apply(ctx, l.spec(backend)); err != nil {
		t.Fatal(err)
	}
	go m.Run(ctx)
	h := waitState(t, m, StateUp, 25*time.Second)
	if h.Backend != backend || h.Colo != "TST" || h.WarpFlag != "on" || !h.ProbeCloudflareOK || !h.ProbeOtherOK || time.Since(h.LastHandshake) > 30*time.Second {
		t.Fatalf("health: %+v", h)
	}
	if h.Endpoint != "192.0.2.2:2408" {
		t.Fatalf("endpoint %s", h.Endpoint)
	}

	if out := route(t, "route", "get", "192.0.2.2"); !strings.Contains(out, "dev mg3-wn0") {
		t.Fatalf("the WARP endpoint must stay on the direct path: %s", out)
	}
	if out := route(t, "route", "get", "203.0.113.10", "oif", "mgwarp"); !strings.Contains(out, "dev mgwarp") {
		t.Fatalf("oif lookup: %s", out)
	}
	body, err := httpGet(direct, "203.0.113.10:80", "/cdn-cgi/trace")
	wantTrace(t, "unmarked socket", body, err, "192.0.2.1", "off")

	eg := m.Egress()
	body, err = httpGet(eg.TCP, "203.0.113.10:80", "/cdn-cgi/trace")
	wantTrace(t, "bound TCP v4", body, err, "172.16.0.2", "on")
	body, err = httpGet(eg.TCP, "[2001:db8:1::10]:80", "/cdn-cgi/trace")
	wantTrace(t, "bound TCP v6", body, err, "fd00:16::2", "on")
	u, err := eg.UDP("203.0.113.10:5353")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.WriteTo([]byte("hi"), "203.0.113.10:5353"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	done := make(chan string, 1)
	go func() { n, _, err := u.ReadFrom(buf); done <- fmt.Sprint(string(buf[:n]), err) }()
	select {
	case got := <-done:
		if !strings.HasPrefix(got, "src=172.16.0.2") {
			t.Fatalf("bound UDP: %s", got)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("bound UDP: no answer")
	}
	u.Close()

	pingGateway(t, "tunnel up")
	out, err := curlIn(nsCA, probeA, false)
	wantTrace(t, "client A v4 (AWG subnet through WARP)", out, err, "172.16.0.2", "on")
	// the MSS clamp: the client's SYN said 1460 (its veth has MTU 1500), the WARP device has MTU 1280
	// (the server reports the effective MSS, 12 bytes less for the timestamp option: 1228; unclamped it would be ~1368)
	if mss := mssOf(out); mss < 1000 || mss > 1240 {
		t.Fatalf("forwarded TCP through the WARP device was not MSS-clamped:\n%s", out)
	}
	out, err = curlIn(nsCA, "http://[2001:db8:1::10]/cdn-cgi/trace", true)
	wantTrace(t, "client A v6", out, err, "fd00:16::2", "on")
	out, err = curlIn(nsCB, probeA, false)
	wantTrace(t, "client B (not routed) stays direct", out, err, "192.0.2.1", "off")

	rules4, rules6 := route(t, "rule", "show"), route(t, "-6", "rule", "show")
	for _, w := range []string{"90:\tfrom all oif mgwarp lookup 51820", "110:\tfrom 10.66.0.0/24 lookup 51820"} {
		if !strings.Contains(rules4, w) {
			t.Fatalf("missing rule %q:\n%s", w, rules4)
		}
	}
	for _, w := range []string{"90:\tfrom all oif mgwarp lookup 51820", "110:\tfrom fd00:66::/64 lookup 51820"} {
		if !strings.Contains(rules6, w) {
			t.Fatalf("missing v6 rule %q:\n%s", w, rules6)
		}
	}
	for fam, args := range map[string][]string{"v4": {"route", "show", "table", "51820"}, "v6": {"-6", "route", "show", "table", "51820"}} {
		tab := route(t, args...)
		if want := map[string]string{"v4": "throw 10.66.0.0/24", "v6": "throw fd00:66::/64"}[fam]; !strings.Contains(tab, want) {
			t.Fatalf("%s table lacks %q:\n%s", fam, want, tab)
		}
		if !strings.Contains(tab, "default dev mgwarp") || !strings.Contains(tab, "unreachable default") || !strings.Contains(tab, "metric 4096") {
			t.Fatalf("%s table:\n%s", fam, tab)
		}
	}
	if v := readFile("/proc/sys/net/ipv4/conf/mgwarp/rp_filter"); v != "2" {
		t.Fatalf("rp_filter=%q", v)
	}
	if nft := mustRun(t, "nft", "list", "table", "inet", NftTable); !strings.Contains(nft, "masquerade") || !strings.Contains(nft, "maxseg size set rt mtu") {
		t.Fatalf("nft:\n%s", nft)
	}
	// MTU on the device
	if out := route(t, "link", "show", "mgwarp"); !strings.Contains(out, "mtu 1280") {
		t.Fatalf("link: %s", out)
	}

	// --- the tunnel link goes down: everything selected fails closed, the rest stays direct ---
	cancel() // stop the health loop, so this checks Apply (a reconcile) and not the loop's own repair
	time.Sleep(300 * time.Millisecond)
	mustRun(t, "ip", "link", "set", "mgwarp", "down")
	start := time.Now()
	_, err = eg.TCP("203.0.113.10:80")
	// a bound socket on a down device gets ENETUNREACH from the kernel before any routing table is consulted
	if err == nil || !(strings.Contains(err.Error(), "network is unreachable") || strings.Contains(err.Error(), "no route to host")) {
		t.Fatalf("bound dial with the link down: %v (want network is unreachable or no route to host)", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("a closed egress must fail fast, took %v", time.Since(start))
	}
	if out, err := curlIn(nsCA, probeA, false); err == nil {
		t.Fatalf("client A leaked with the link down: %s", out)
	}
	body, err = httpGet(direct, "203.0.113.10:80", "/cdn-cgi/trace")
	wantTrace(t, "unmarked socket with the link down", body, err, "192.0.2.1", "off")
	out, err = curlIn(nsCB, probeA, false)
	wantTrace(t, "client B with the link down", out, err, "192.0.2.1", "off")
	if tab := route(t, "route", "show", "table", "51820"); strings.Contains(tab, "default dev") || !strings.Contains(tab, "unreachable default") {
		t.Fatalf("table after link down:\n%s", tab)
	}
	pingGateway(t, "link down")

	// link up + reconcile: the default route comes back
	mustRun(t, "ip", "link", "set", "mgwarp", "up")
	if err := m.Apply(context.Background(), l.spec(backend)); err != nil {
		t.Fatal(err)
	}
	if tab := route(t, "route", "show", "table", "51820"); !strings.Contains(tab, "default dev mgwarp") {
		t.Fatalf("table after reconcile:\n%s", tab)
	}
	if tab := route(t, "-6", "route", "show", "table", "51820"); !strings.Contains(tab, "default dev mgwarp") {
		t.Fatalf("v6 table after reconcile:\n%s", tab)
	}
	var lastErr error
	for i := 0; i < 20; i++ { // the session needs a moment after the flap
		body, lastErr = httpGet(eg.TCP, "203.0.113.10:80", "/cdn-cgi/trace")
		if lastErr == nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	wantTrace(t, "bound TCP after reconcile", body, lastErr, "172.16.0.2", "on")
}

func (l *lab) pause(t *testing.T, backend string) {
	m := l.manager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); m.Cleanup(context.Background()); assertClean(t) }()
	m.SetRoutedSubnets(ctx, []netip.Prefix{netip.MustParsePrefix("10.66.0.0/24")})
	if err := m.Apply(ctx, l.spec(backend)); err != nil {
		t.Fatal(err)
	}
	go m.Run(ctx)
	waitState(t, m, StateUp, 25*time.Second)
	out, err := curlIn(nsCA, probeA, false)
	wantTrace(t, "client A before the pause", out, err, "172.16.0.2", "on")

	if err := m.Apply(ctx, l.spec(backend, func(s *plugin.WarpSpec) { s.Enabled = false })); err != nil {
		t.Fatal(err)
	}
	if h, _ := m.Health(); h.State != StateDisabled {
		t.Fatalf("%+v", h)
	}
	if _, err := run("ip", "link", "show", DefaultIface); err == nil {
		t.Fatal("the device must be gone while paused")
	}
	if _, err := m.Egress().TCP("203.0.113.10:80"); !errors.Is(err, ErrNotActive) {
		t.Fatalf("paused egress: %v", err)
	}
	if out, err := curlIn(nsCA, probeA, false); err == nil {
		t.Fatalf("client A leaked while WARP is paused: %s", out)
	}
	if r := route(t, "rule", "show"); !strings.Contains(r, "from 10.66.0.0/24 lookup 51820") {
		t.Fatalf("the subnet rule must stay while paused:\n%s", r)
	}
	pingGateway(t, "paused")
	if out, err := curlIn(nsCB, probeA, false); err != nil {
		t.Fatalf("client B: %v %s", err, out)
	}

	if err := m.Apply(ctx, l.spec(backend)); err != nil {
		t.Fatal(err)
	}
	waitState(t, m, StateUp, 25*time.Second)
	out, err = curlIn(nsCA, probeA, false)
	wantTrace(t, "client A after resume", out, err, "172.16.0.2", "on")
}

// reserved: the peer moves its WireGuard port away and a raw listener takes the endpoint port, so the test sees the
// handshake initiation bytes exactly as they leave the node.
func (l *lab) reserved(t *testing.T, backend string) {
	l.peer.cmdOK(t, "port 7000")
	l.peer.cmdOK(t, "raw 2408")
	defer func() { l.peer.cmdOK(t, "rawstop"); l.peer.cmdOK(t, "port 2408") }()

	for _, tc := range []struct {
		reserved []byte
		want     string
	}{{[]byte{0x0a, 0x0b, 0x0c}, "010a0b0c"}, {nil, "01000000"}} {
		m := l.manager(t)
		ctx, cancel := context.WithCancel(context.Background())
		if err := m.Apply(ctx, l.spec(backend, func(s *plugin.WarpSpec) { s.Ports = []uint16{2408}; s.Reserved = tc.reserved })); err != nil {
			t.Fatal(err)
		}
		got := l.peer.expect(t, "raw ", 10*time.Second)
		f := strings.Fields(got)
		if f[1] != tc.want || f[2] != "148" {
			t.Fatalf("%s reserved=%x: first bytes %s len %s, want %s len 148", backend, tc.reserved, f[1], f[2], tc.want)
		}
		if backend == "kernel" && tc.reserved != nil {
			if nft := mustRun(t, "nft", "list", "table", "inet", NftTable); !strings.Contains(nft, "0xa0b0c") { // nft prints the value without the leading zero
				t.Fatalf("nft:\n%s", nft)
			}
		}
		cancel()
		m.Cleanup(context.Background())
		assertClean(t)
	}
}

func (l *lab) crashCleanup(t *testing.T, backend string) {
	m := l.manager(t)
	ctx := context.Background()
	m.SetRoutedSubnets(ctx, []netip.Prefix{netip.MustParsePrefix("10.66.0.0/24"), netip.MustParsePrefix("fd00:66::/64")})
	if err := m.Apply(ctx, l.spec(backend)); err != nil {
		t.Fatal(err)
	}
	// the agent dies: no Cleanup. The unit's ExecStopPost runs the stateless cleanup.
	if _, err := run("ip", "link", "show", DefaultIface); err != nil {
		t.Fatal("no device before the crash cleanup")
	}
	if err := CleanupHost(ctx, Settings{}); err != nil {
		t.Fatal(err)
	}
	assertClean(t)
	if err := CleanupHost(ctx, Settings{}); err != nil {
		t.Fatalf("a second cleanup must be a no-op: %v", err)
	}
	m.Cleanup(ctx) // releases the userspace device the process still holds
	assertClean(t)
}

func (l *lab) preflight(t *testing.T) {
	m := l.manager(t)
	ctx := context.Background()
	if f := m.Preflight(ctx); len(f) != 0 {
		t.Fatalf("clean host, findings: %+v", f)
	}
	mustRun(t, "ip", "rule", "add", "pref", "90", "from", "198.18.0.0/16", "lookup", "100")
	mustRun(t, "ip", "route", "add", "unreachable", "198.19.0.0/16", "table", "51820")
	mustRun(t, "ip", "rule", "add", "pref", "50", "from", "198.18.1.0/24", "lookup", "51820")
	mustRun(t, "nft", "add", "table", "ip", "mg3_fake_docker")
	mustRun(t, "nft", "add", "chain", "ip", "mg3_fake_docker", "forward", "{ type filter hook forward priority 0 ; policy drop ; }")
	f := m.Preflight(ctx)
	ids := map[string]bool{}
	for _, x := range f {
		ids[x.ID] = true
	}
	for _, id := range []string{"rule_pref_in_use", "table_in_use", "forward_drop"} {
		if !ids[id] {
			t.Errorf("missing finding %s: %+v", id, f)
		}
	}
	// with a clash the manager refuses to touch the host, instead of flushing another tool's table
	m2 := l.manager(t)
	if err := m2.Apply(ctx, l.spec("kernel")); err == nil || !(strings.Contains(err.Error(), "rule_pref_in_use") || strings.Contains(err.Error(), "table_in_use")) {
		t.Fatalf("Apply with a clash: %v", err)
	}
	if _, err := run("ip", "link", "show", DefaultIface); err == nil {
		t.Fatal("the device was created despite the clash")
	}
	if out := route(t, "route", "show", "table", "51820"); !strings.Contains(out, "unreachable 198.19.0.0/16") {
		t.Fatalf("the foreign route in the table was touched: %s", out)
	}
	run("ip", "rule", "del", "pref", "90")
	run("ip", "rule", "del", "pref", "50")
	run("ip", "route", "flush", "table", "51820")
	run("nft", "delete", "table", "ip", "mg3_fake_docker")
	if f := m.Preflight(ctx); len(f) != 0 {
		t.Fatalf("after removing the clashes: %+v", f)
	}
	assertClean(t)
}

func (l *lab) noReplacePeers(t *testing.T) {
	m := l.manager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); m.Cleanup(context.Background()); assertClean(t) }()
	if err := m.Apply(ctx, l.spec("kernel")); err != nil {
		t.Fatal(err)
	}
	go m.Run(ctx)
	waitState(t, m, StateUp, 25*time.Second)
	pl := m.plane.(*linuxPlane)
	st0, err := pl.Stat(ctx)
	if err != nil || st0.Handshake.IsZero() {
		t.Fatalf("%+v %v", st0, err)
	}
	ls := linkSpec{Backend: "kernel", PrivateKey: l.clientPriv.String(), PeerKey: l.peerPub.String(),
		Endpoints: []netip.AddrPort{netip.MustParseAddrPort("192.0.2.2:2408"), netip.MustParseAddrPort("192.0.2.2:500")},
		Endpoint:  netip.MustParseAddrPort("192.0.2.2:2408"), AddrV4: netip.MustParsePrefix("172.16.0.2/32"),
		AddrV6: netip.MustParsePrefix("fd00:16::2/128"), MTU: 1280, KeepaliveSeconds: 25}
	for i := 0; i < 3; i++ {
		if err := m.Apply(ctx, l.spec("kernel")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := pl.Up(ctx, ls); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	st1, _ := pl.Stat(ctx)
	if !st1.Handshake.Equal(st0.Handshake) {
		t.Fatalf("the session was reset by a no-op reconcile: %v -> %v", st0.Handshake, st1.Handshake)
	}
	// a live endpoint that is one of ours is kept even when Up is asked for another one (a restart after a rotation)
	if err := pl.SetEndpoint(ctx, netip.MustParseAddrPort("192.0.2.2:500")); err != nil {
		t.Fatal(err)
	}
	_, ep, err := pl.Up(ctx, ls)
	if err != nil || ep.Port() != 500 {
		t.Fatalf("Up moved the live endpoint: %v %v", ep, err)
	}
	if err := pl.SetEndpoint(ctx, netip.MustParseAddrPort("192.0.2.2:2408")); err != nil {
		t.Fatal(err)
	}

	// an agent restart: a second manager on the same host, no cleanup in between, keeps the live session
	cancel()
	time.Sleep(300 * time.Millisecond)
	m2 := l.manager(t)
	if err := m2.Apply(context.Background(), l.spec("kernel")); err != nil {
		t.Fatal(err)
	}
	st2, _ := m2.plane.(*linuxPlane).Stat(context.Background())
	if !st2.Handshake.Equal(st0.Handshake) {
		t.Fatalf("a restarted manager reset the live session: %v -> %v", st0.Handshake, st2.Handshake)
	}
	m2.Cleanup(context.Background())
}

func (l *lab) rotation(t *testing.T) {
	l.peer.cmdOK(t, "port 500")
	defer l.peer.cmdOK(t, "port 2408")
	m := l.manager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); m.Cleanup(context.Background()); assertClean(t) }()
	if err := m.Apply(ctx, l.spec("kernel")); err != nil {
		t.Fatal(err)
	}
	go m.Run(ctx)
	h := waitState(t, m, StateUp, 30*time.Second)
	if h.Endpoint != "192.0.2.2:500" {
		t.Fatalf("endpoint after rotation: %s", h.Endpoint)
	}
}

func (l *lab) peerDead(t *testing.T) {
	m := l.manager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); l.peer.cmdOK(t, "wgup"); m.Cleanup(context.Background()); assertClean(t) }()
	if err := m.Apply(ctx, l.spec("kernel")); err != nil {
		t.Fatal(err)
	}
	go m.Run(ctx)
	waitState(t, m, StateUp, 25*time.Second)
	// the peer stops answering: the tunnel is "up", probes time out (a black hole), no error anywhere
	l.peer.cmdOK(t, "wgdown")
	h := waitState(t, m, StateDown, 40*time.Second)
	if !strings.Contains(h.LastError, "probe_") {
		t.Fatalf("%+v", h)
	}
	if _, err := httpGet(m.Egress().TCP, "203.0.113.10:80", "/cdn-cgi/trace"); err == nil {
		t.Fatal("a dial through a dead tunnel succeeded")
	}
	// it comes back without anybody touching it (the ladder may have rotated the port meanwhile)
	l.peer.cmdOK(t, "wgup")
	waitState(t, m, StateUp, 60*time.Second)
}

// noV6: new accounts often have no IPv6. The table then holds only the unreachable default for v6, clients of a v6
// subnet fail at once instead of hanging in a black hole, bound v6 dials are refused, and v4 works.
func (l *lab) noV6(t *testing.T) {
	m := l.manager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); m.Cleanup(context.Background()); assertClean(t) }()
	m.SetRoutedSubnets(ctx, []netip.Prefix{netip.MustParsePrefix("10.66.0.0/24"), netip.MustParsePrefix("fd00:66::/64")})
	if err := m.Apply(ctx, l.spec("kernel", func(s *plugin.WarpSpec) { s.AddressV6 = "" })); err != nil {
		t.Fatal(err)
	}
	go m.Run(ctx)
	waitState(t, m, StateUp, 25*time.Second)
	if tab := route(t, "-6", "route", "show", "table", "51820"); strings.Contains(tab, "default dev mgwarp") || !strings.Contains(tab, "unreachable default") {
		t.Fatalf("v6 table without a v6 account:\n%s", tab)
	}
	if out := route(t, "-br", "addr", "show", "dev", "mgwarp"); strings.Contains(out, "fd00:16::2") {
		t.Fatalf("a v6 address on the device: %s", out)
	}
	body, err := httpGet(m.Egress().TCP, "203.0.113.10:80", "/cdn-cgi/trace")
	wantTrace(t, "bound v4", body, err, "172.16.0.2", "on")
	if c, err := m.Egress().TCP("[2001:db8:1::10]:80"); err == nil {
		c.Close()
		t.Fatal("a v6 dial through an account without IPv6 succeeded")
	}
	start := time.Now()
	if out, err := curlIn(nsCA, "http://[2001:db8:1::10]/cdn-cgi/trace", true); err == nil {
		t.Fatalf("v6 client reached the internet: %s", out)
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("v6 client should be refused at once (ICMP unreachable), took %v", time.Since(start))
	}
	out, err := curlIn(nsCA, probeA, false)
	wantTrace(t, "client A v4", out, err, "172.16.0.2", "on")
}

// autoBackend: "auto" takes the kernel when it can. (The fallback to userspace when the WireGuard module is missing
// cannot be staged here; kernelUnsupported is unit-tested.)
func (l *lab) autoBackend(t *testing.T) {
	m := l.manager(t)
	ctx := context.Background()
	defer func() { m.Cleanup(ctx); assertClean(t) }()
	if err := m.Apply(ctx, l.spec("auto")); err != nil {
		t.Fatal(err)
	}
	if h, _ := m.Health(); h.Backend != "kernel" {
		t.Fatalf("%+v", h)
	}
}
