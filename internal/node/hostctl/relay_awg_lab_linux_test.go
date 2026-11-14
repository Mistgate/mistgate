//go:build linux

package hostctl

import (
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/node/awg/awgtest"
)

// The AmneziaWG relay lab (step 4 of plans/awg-relay-plan.md). The entry node of relay_lab_linux_test.go forwards one UDP port with the
// rules RenderRelays writes, and behind it sits an exit with a real AmneziaWG interface (amneziawg-go in userspace). Real
// amneziawg-go clients connect straight and through the entry. It settles, with packets, what the plan rests on:
//
//   - two keys of one device (a direct key and a path key, different tunnel addresses) are alive at the same time, and the
//     exit sees the path key's peer at the ENTRY's address and the direct key's at the client's own;
//   - ONE key used from both paths at once makes the exit's endpoint of that peer jump between the addresses (so a second
//     key for the path is needed, not a second endpoint for the same key);
//   - the same jump when the second endpoint is the exit's own IPv6 address (§2.6, the "· IPv6" twin);
//   - what an entry name with A and AAAA does (the forward is IPv4 only);
//   - what a conntrack entry that has expired does to a client that stays silent.
//
// Namespaces are named mgarl-<pid>-*; the AWG harness (internal/node/awg/awgtest) is used without its Require, which would
// delete every "mg3-awg-" namespace of another session's run. Addresses are documentation ranges. Opt-in as TestLabRelay:
//
//	MG_ROOT_TESTS=1 go test ./internal/node/hostctl -run TestLabRelayAWG -v -count=1

const (
	awgLabExitPort   = 51842 // the exit's AmneziaWG port
	awgLabEntryPort  = 51843 // the port the entry listens on; the forward rewrites it to the exit's
	awgLabEntry      = "198.51.100.1"
	awgLabEntry6     = "2001:db8:5::1"
	awgLabExit       = "192.0.2.2"   // as the entry reaches it (the forward's target)
	awgLabExitDirect = "203.0.113.2" // the same exit as a direct client reaches it
	awgLabExit6      = "2001:db8:6::2"
	awgLabTun        = "10.70.0.1" // the exit's address inside the tunnel
)

// awgLabObf is the "3.1 full" parameter set of the engine stand (full31 in internal/node/awg/stand_linux_test.go: the
// same Jc, S1-S4, H1-H4, I1, timers, random trailers), with a header protection key of its own for each run.
func awgLabObf(t *testing.T) awgcfg.Obfuscation {
	r := func(s string) awgcfg.Range {
		v, err := awgcfg.ParseRange(s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	hpk, _ := awgtest.NewKey(t)
	return awgcfg.Obfuscation{
		Jc: 6, Jmin: 10, Jmax: 50, S1: 24, S2: 24, S3: 24, S4: 24,
		H1: r("1"), H2: r("2"), H3: r("3"), H4: r("4"),
		I1:                  "<b 0xc70000000108><r 8><t><r 40>",
		HeaderProtectionKey: awgtest.B64(hpk), RandomTrailers: true,
		ContentPaddingAddition: r("2-10"), RekeyAfterTime: r("100-120"), RekeyTimeout: r("3-7"), RejectAfterTime: r("150-180"),
		KeepaliveTimeout: r("5-15"), MaxHandshakeAttempts: r("15-20"),
	}
}

type awgLab struct {
	nsA, nsB, nsE, nsX, nsA6 awgtest.NS // direct client, client behind the entry, entry, exit, direct client over IPv6
	obf                      awgcfg.Obfuscation
}

func TestLabRelayAWG(t *testing.T) {
	if os.Getenv("MG_ROOT_TESTS") != "1" || os.Geteuid() != 0 {
		t.Skip("set MG_ROOT_TESTS=1 and run as root")
	}
	for _, tool := range []string{"ip", "nft", "python3", "ping", "unshare"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	id := fmt.Sprintf("mgarl-%d", os.Getpid())
	mk := func(suffix string) awgtest.NS {
		ns := awgtest.NS{Name: id + "-" + suffix}
		if out, err := exec.Command("ip", "netns", "add", ns.Name).CombinedOutput(); err != nil {
			t.Fatalf("ip netns add %s: %v: %s", ns.Name, err, out)
		}
		t.Cleanup(func() { // only our own namespace, its processes first
			if pids, err := exec.Command("ip", "netns", "pids", ns.Name).Output(); err == nil {
				for _, p := range strings.Fields(string(pids)) {
					if n, err := strconv.Atoi(p); err == nil && n > 1 {
						_ = syscall.Kill(n, syscall.SIGKILL)
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
			_ = exec.Command("ip", "netns", "del", ns.Name).Run()
		})
		ns.Must(t, "ip", "link", "set", "lo", "up")
		return ns
	}
	l := &awgLab{nsA: mk("a"), nsB: mk("b"), nsE: mk("e"), nsX: mk("x"), nsA6: mk("a6"), obf: awgLabObf(t)}

	// The exit has an address on each link and NO route to the client behind the entry: a reply to an unmasqueraded source
	// could not come back.
	awgtest.Veth(t, l.nsA, "a0", []string{"203.0.113.3/24"}, l.nsX, "xa", []string{awgLabExitDirect + "/24"})
	awgtest.Veth(t, l.nsB, "b0", []string{"198.51.100.2/24", "2001:db8:5::2/64"}, l.nsE, "eb", []string{awgLabEntry + "/24", awgLabEntry6 + "/64"})
	awgtest.Veth(t, l.nsE, "ex", []string{"192.0.2.1/24"}, l.nsX, "xe", []string{awgLabExit + "/24"})
	awgtest.Veth(t, l.nsA6, "a6", []string{"2001:db8:6::3/64"}, l.nsX, "x6", []string{awgLabExit6 + "/64"})
	l.nsE.Must(t, "sysctl", "-qw", "net.ipv4.ip_forward=0") // a new namespace may inherit 1 (Docker); the agent must turn it on

	// The entry's table is what the agent installs: its own host code, run inside the entry's namespace.
	spec := fmt.Sprintf("rly_awg:udp:%d:%s:%d", awgLabEntryPort, awgLabExit, awgLabExitPort)
	cmd := l.nsE.Cmd(os.Args[0], "-test.run=^TestLabRelayHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "MG3_LAB_RELAY="+spec)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "RESULT ok") {
		t.Fatalf("install the relay on the entry: %v\n%s", err, out)
	}
	rules := l.nsE.Must(t, "nft", "list", "chain", "inet", NftRelayTable, "pre")
	t.Logf("the entry's DNAT rule as RenderRelays wrote it:\n%s", rules)
	if want := fmt.Sprintf("udp dport %d dnat ip to %s:%d", awgLabEntryPort, awgLabExit, awgLabExitPort); !strings.Contains(rules, want) {
		t.Fatalf("RenderRelays does not produce %q for a UDP forward:\n%s", want, rules)
	}
	if got := strings.TrimSpace(l.nsE.Must(t, "cat", "/proc/sys/net/ipv4/ip_forward")); got != "1" {
		t.Fatalf("the agent did not turn IPv4 forwarding on in the entry: %q", got)
	}

	t.Run("two_keys_one_device_both_live", l.twoKeys)
	t.Run("one_key_two_paths_endpoint_jumps", func(t *testing.T) {
		l.oneKeyJumps(t, []awgLabPath{
			{l.nsA, fmt.Sprintf("%s:%d", awgLabExitDirect, awgLabExitPort)},
			{l.nsB, fmt.Sprintf("%s:%d", awgLabEntry, awgLabEntryPort)},
		}, "203.0.113.3", awgLabEntry4Seen)
	})
	t.Run("one_key_ipv4_and_ipv6_endpoint_of_one_exit_jumps", func(t *testing.T) {
		l.oneKeyJumps(t, []awgLabPath{
			{l.nsA, fmt.Sprintf("%s:%d", awgLabExitDirect, awgLabExitPort)},
			{l.nsA6, fmt.Sprintf("[%s]:%d", awgLabExit6, awgLabExitPort)},
		}, "203.0.113.3", "2001:db8:6::3")
	})
	t.Run("entry_name_with_aaaa", l.entryName)
	t.Run("keepalive_holds_the_mapping", l.keepalive)
	t.Run("silent_client_after_conntrack_expiry", l.idle)
}

// awgLabEntry4Seen is the address the exit sees for the path through the entry: the entry's side of the exit link.
const awgLabEntry4Seen = "192.0.2.1"

type awgLabPath struct {
	ns       awgtest.NS
	endpoint string // host:port the client sends to
}

// server starts the exit with the given peers.
func (l *awgLab) server(t *testing.T, priv [32]byte, peers ...awgcfg.Peer) *awgtest.Client {
	return awgtest.StartServer(t, l.nsX, awgtest.ServerCfg{
		Flavor: awgtest.Flavor31, Addrs: []string{awgLabTun + "/24"}, Priv: priv, Port: awgLabExitPort, Obf: l.obf, Peers: peers,
	})
}

func (l *awgLab) client(t *testing.T, ns awgtest.NS, endpoint, addr string, priv, serverPub [32]byte, psk *[32]byte, keepalive string) *awgtest.Client {
	return awgtest.StartClient(t, ns, awgtest.ClientCfg{
		Flavor: awgtest.Flavor31, Addrs: []string{addr + "/24"}, Priv: priv, ServerPub: serverPub, PSK: psk,
		Endpoint: endpoint, Obf: l.obf, MTU: 1280, Keepalive: keepalive,
	})
}

func awgLabPeer(pub [32]byte, psk *[32]byte, ip string) awgcfg.Peer {
	return awgcfg.Peer{PublicKey: pub, PSK: psk, AllowedIPs: []netip.Prefix{netip.MustParsePrefix(ip + "/32")}}
}

// seen is the address the exit has for the peer ("" = none yet).
func awgLabSeen(srv *awgtest.Client, pub [32]byte) string {
	p, ok := srv.Peer(pub)
	if !ok || !p.Endpoint.IsValid() {
		return ""
	}
	return p.Endpoint.Addr().Unmap().String()
}

func awgLabHandshaken(srv *awgtest.Client, pub [32]byte) bool {
	p, ok := srv.Peer(pub)
	return ok && !p.LastHS.IsZero()
}

func (l *awgLab) counter(t *testing.T) int {
	out := l.nsE.Must(t, "nft", "list", "counters", "table", "inet", NftRelayTable)
	m := regexp.MustCompile(fmt.Sprintf(`counter udp_%d \{\s+packets (\d+)`, awgLabEntryPort)).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no counter for the forward:\n%s", out)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// Claim 1: a direct key and a path key of one device are both alive at once.
func (l *awgLab) twoKeys(t *testing.T) {
	srvPriv, srvPub := awgtest.NewKey(t)
	privA, pubA := awgtest.NewKey(t) // alice, straight to de1
	privB, pubB := awgtest.NewKey(t) // alice, the path key: through msk1
	pskA, _ := awgtest.NewKey(t)
	pskB, _ := awgtest.NewKey(t)
	srv := l.server(t, srvPriv, awgLabPeer(pubA, &pskA, "10.70.0.2"), awgLabPeer(pubB, &pskB, "10.70.0.3"))
	before := l.counter(t)

	cA := l.client(t, l.nsA, fmt.Sprintf("%s:%d", awgLabExitDirect, awgLabExitPort), "10.70.0.2", privA, srvPub, &pskA, "5")
	cB := l.client(t, l.nsB, fmt.Sprintf("%s:%d", awgLabEntry, awgLabEntryPort), "10.70.0.3", privB, srvPub, &pskB, "5")
	start := time.Now()
	if !awgtest.Wait(10*time.Second, func() bool { return awgLabHandshaken(srv, pubA) && awgLabHandshaken(srv, pubB) }) {
		t.Fatalf("the two keys of one device do not both handshake at the exit (direct: %v, path: %v)\nclient direct: %s\nclient path: %s",
			awgLabHandshaken(srv, pubA), awgLabHandshaken(srv, pubB), cA.Output(), cB.Output())
	}
	t.Logf("both handshakes done %v after the clients started", time.Since(start).Round(10*time.Millisecond))
	if !cA.Handshaken() || !cB.Handshaken() {
		t.Errorf("a client does not see its own handshake (direct %v, path %v)", cA.Handshaken(), cB.Handshaken())
	}

	// Ping inside the tunnel from both at once, while the exit's view of both peers is sampled.
	var wg sync.WaitGroup
	loss := make([]int, 2)
	for i, ns := range []awgtest.NS{l.nsA, l.nsB} {
		wg.Add(1)
		go func() { defer wg.Done(); loss[i], _ = awgtest.Ping(ns, awgLabTun, 20, 0, false, 100*time.Millisecond) }()
	}
	seenA, seenB := map[string]bool{}, map[string]bool{}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
sample:
	for {
		select {
		case <-done:
			break sample
		case <-time.After(50 * time.Millisecond):
			if a := awgLabSeen(srv, pubA); a != "" {
				seenA[a] = true
			}
			if b := awgLabSeen(srv, pubB); b != "" {
				seenB[b] = true
			}
		}
	}
	if loss[0] != 0 || loss[1] != 0 {
		t.Errorf("ping inside the tunnel at the same time: direct lost %d%%, through the entry lost %d%%, want 0 and 0", loss[0], loss[1])
	}
	if len(seenA) != 1 || !seenA["203.0.113.3"] {
		t.Errorf("the exit sees the direct key at %v, want exactly the client's own 203.0.113.3", seenA)
	}
	if len(seenB) != 1 || !seenB[awgLabEntry4Seen] {
		t.Errorf("the exit sees the path key at %v, want exactly the entry's address %s (and never the client's 198.51.100.2)", seenB, awgLabEntry4Seen)
	}
	// And the other way: the exit reaches both tunnel addresses (the reply path through the masquerade works too).
	for _, ip := range []string{"10.70.0.2", "10.70.0.3"} {
		if l, out := awgtest.Ping(l.nsX, ip, 3, 0, false, 200*time.Millisecond); l != 0 {
			t.Errorf("the exit cannot ping %s inside the tunnel (loss %d%%): %s", ip, l, out)
		}
	}
	if n := l.counter(t) - before; n < 20 { // 20 echo requests alone go that way; the counter counts only the direction to the exit
		t.Errorf("the entry's counter udp_%d grew by %d packets: the path key's traffic did not go through the RenderRelays rules", awgLabEntryPort, n)
	} else {
		t.Logf("packets through the entry's forward during the subtest: %d", n)
	}
	t.Logf("exit's endpoints: direct key %v, path key %v", keys(seenA), keys(seenB))
}

func keys(m map[string]bool) []string {
	var o []string
	for k := range m {
		o = append(o, k)
	}
	return o
}

// Claims 2 and 4: ONE key at the exit, two live clients that reach the exit by different routes: the exit's endpoint of
// that peer jumps between the two source addresses, and each client's replies go to whoever spoke last.
// The first path is direct (alice at 203.0.113.3); the second is either the entry or the exit's IPv6 address.
func (l *awgLab) oneKeyJumps(t *testing.T, paths []awgLabPath, first, second string) {
	srvPriv, srvPub := awgtest.NewKey(t)
	priv, pub := awgtest.NewKey(t)
	psk, _ := awgtest.NewKey(t)
	srv := l.server(t, srvPriv, awgLabPeer(pub, &psk, "10.70.0.2"))
	var cs []*awgtest.Client
	for _, p := range paths {
		cs = append(cs, l.client(t, p.ns, p.endpoint, "10.70.0.2", priv, srvPub, &psk, "1"))
	}
	if !awgtest.Wait(10*time.Second, func() bool { return awgLabHandshaken(srv, pub) }) {
		t.Fatalf("no handshake at all: %s / %s", cs[0].Output(), cs[1].Output())
	}

	var wg sync.WaitGroup
	loss := make([]int, len(paths))
	for i, p := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			loss[i], _ = awgtest.Ping(p.ns, awgLabTun, 60, 0, false, 100*time.Millisecond)
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	var seq []string // consecutive distinct addresses the exit had for the peer
	seen := map[string]int{}
sample:
	for {
		select {
		case <-done:
			break sample
		case <-time.After(20 * time.Millisecond):
			if a := awgLabSeen(srv, pub); a != "" {
				seen[a]++
				if len(seq) == 0 || seq[len(seq)-1] != a {
					seq = append(seq, a)
				}
			}
		}
	}
	t.Logf("exit's endpoint of the one peer, in order of change: %v (samples per address %v); ping loss per client, in order: %v%%", seq, seen, loss)
	if seen[first] == 0 || seen[second] == 0 {
		t.Errorf("the exit never had both addresses for the one key: %v", seen)
	}
	if flips := len(seq) - 1; flips < 2 {
		t.Errorf("the endpoint of a key used from two routes changed %d times in 6 s (%v); the plan says it jumps, so the plan's reason for a separate path key needs a second look", flips, seq)
	} else {
		t.Logf("the endpoint changed %d times in 6 s: one key cannot serve two live clients", flips)
	}
}

// getaddrinfo resolves the entry's name with glibc's order inside the client's namespace, with the name defined by a hosts file that only
// a private mount namespace sees (the host's /etc/hosts stays as it is).
func (l *awgLab) getaddrinfo(t *testing.T, ns awgtest.NS, hosts string) string {
	f, err := os.CreateTemp("", "mgarl-hosts")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	_, _ = f.WriteString(hosts)
	f.Close()
	out, err := ns.Run("unshare", "-m", "sh", "-c", `mount --bind "$1" /etc/hosts && exec python3 -c 'import socket
for a in socket.getaddrinfo("msk1.example.test", 51843, type=socket.SOCK_DGRAM): print(a[4][0])'`, "sh", f.Name())
	if err != nil {
		t.Skipf("cannot give a namespace its own hosts file: %v: %s", err, out)
	}
	return strings.TrimSpace(out)
}

// Claim 3: an entry name that has A and AAAA. The forward is IPv4 only.
func (l *awgLab) entryName(t *testing.T) {
	srvPriv, srvPub := awgtest.NewKey(t)
	priv4, pub4 := awgtest.NewKey(t)
	priv6, pub6 := awgtest.NewKey(t)
	srv := l.server(t, srvPriv, awgLabPeer(pub4, nil, "10.70.0.4"), awgLabPeer(pub6, nil, "10.70.0.5"))

	// The daemon never resolves a name: a "host:port" endpoint is refused by the UAPI. The name is resolved by whatever writes the config
	// (the app, awg-quick, Mihomo), so the choice between A and AAAA is made there, not in amneziawg-go.
	c4 := l.client(t, l.nsB, fmt.Sprintf("%s:%d", awgLabEntry, awgLabEntryPort), "10.70.0.4", priv4, srvPub, nil, "2")
	if msg := c4.SetEndpointText(srvPub, fmt.Sprintf("msk1.example.test:%d", awgLabEntryPort)); msg == "" {
		t.Errorf("amneziawg-go accepted a host name as an endpoint: the plan's \"the client resolves the name\" would be settled inside the daemon after all")
	} else {
		t.Logf("amneziawg-go refuses a name as an endpoint: %s", msg)
	}

	// The same entry by its IPv4 address works, by its IPv6 address does not: the DNAT is IPv4 only.
	c6 := l.client(t, l.nsB, fmt.Sprintf("[%s]:%d", awgLabEntry6, awgLabEntryPort), "10.70.0.5", priv6, srvPub, nil, "2")
	if !awgtest.Wait(8*time.Second, func() bool { return awgLabHandshaken(srv, pub4) }) {
		t.Fatalf("control: the client that uses the entry's IPv4 address does not handshake: %s", c4.Output())
	}
	time.Sleep(4 * time.Second)
	if awgLabHandshaken(srv, pub6) || c6.Handshaken() {
		t.Errorf("a client that picked the entry's IPv6 address got through: the forward is not IPv4 only (exit saw it: %v)", awgLabSeen(srv, pub6))
	} else {
		t.Logf("a client that sends to the entry's AAAA address gets no handshake in 12 s while the A address works: only the IPv4 side is forwarded")
	}

	// How a libc-style client orders the two answers: a diagnostic, not a check (Go's ResolveUDPAddr prefers IPv4, getaddrinfo prefers
	// the family the client can reach).
	both := awgLabEntry + " msk1.example.test\n" + awgLabEntry6 + " msk1.example.test\n"
	t.Logf("getaddrinfo order in a client namespace that has IPv4 and IPv6 addresses: %s", strings.ReplaceAll(l.getaddrinfo(t, l.nsB, both), "\n", ", "))
}

// shortenConntrack makes the entry's UDP mappings live 3 s (unreplied) and 6 s (assured) instead of 30 and 120, inside its own namespace.
func (l *awgLab) shortenConntrack(t *testing.T) (unreplied, assured int) {
	unreplied, assured = 3, 6
	if out, err := l.nsE.Run("sysctl", "-w", fmt.Sprintf("net.netfilter.nf_conntrack_udp_timeout=%d", unreplied), fmt.Sprintf("net.netfilter.nf_conntrack_udp_timeout_stream=%d", assured)); err != nil {
		t.Skipf("cannot shorten the entry's conntrack timeouts: %v: %s", err, out)
	}
	return
}

// Claim 5a: a keepalive that comes more often than the assured timeout (the real ones: 25 s against 120 s) keeps the mapping, so the
// exit can reach a client that has nothing to say. Here: keepalive 5 s against the shortened 6 s (and 3 s unreplied), idle for 14 s.
func (l *awgLab) keepalive(t *testing.T) {
	_, assured := l.shortenConntrack(t)
	srvPriv, srvPub := awgtest.NewKey(t)
	priv, pub := awgtest.NewKey(t)
	srv := l.server(t, srvPriv, awgLabPeer(pub, nil, "10.70.0.7"))
	c := l.client(t, l.nsB, fmt.Sprintf("%s:%d", awgLabEntry, awgLabEntryPort), "10.70.0.7", priv, srvPub, nil, "5")
	if loss, out := awgtest.Ping(l.nsB, awgLabTun, 5, 0, false, 200*time.Millisecond); loss != 0 {
		t.Fatalf("no traffic at the start (loss %d%%): %s\n%s", loss, out, c.Output())
	}
	p0, _ := srv.Peer(pub)
	time.Sleep(time.Duration(2*assured+2) * time.Second)
	if loss, out := awgtest.Ping(l.nsX, "10.70.0.7", 3, 0, false, 200*time.Millisecond); loss != 0 {
		t.Errorf("a client with keepalive 5 s (assured timeout %d s) is unreachable from the exit after %d s of silence (loss %d%%): keepalive does not hold the mapping: %s", assured, 2*assured+2, loss, out)
	}
	p1, _ := srv.Peer(pub)
	t.Logf("exit's endpoint of the client: %v before, %v after %d s idle", p0.Endpoint, p1.Endpoint, 2*assured+2)
}

// Claim 5b (the plan's note on conntrack in section 3): a client without keepalive that stays silent loses its mapping at the entry;
// the exit cannot reach it until the client speaks, and when it does, the exit follows the new mapping by itself.
func (l *awgLab) idle(t *testing.T) {
	_, assured := l.shortenConntrack(t)
	srvPriv, srvPub := awgtest.NewKey(t)
	priv, pub := awgtest.NewKey(t)
	srv := l.server(t, srvPriv, awgLabPeer(pub, nil, "10.70.0.6"))
	c := l.client(t, l.nsB, fmt.Sprintf("%s:%d", awgLabEntry, awgLabEntryPort), "10.70.0.6", priv, srvPub, nil, "")
	if loss, out := awgtest.Ping(l.nsB, awgLabTun, 5, 0, false, 200*time.Millisecond); loss != 0 {
		t.Fatalf("no traffic at the start (loss %d%%): %s\n%s", loss, out, c.Output())
	}
	if loss, out := awgtest.Ping(l.nsX, "10.70.0.6", 3, 0, false, 200*time.Millisecond); loss != 0 {
		t.Fatalf("the exit cannot reach the client at the start (loss %d%%): %s", loss, out)
	}
	p0, _ := srv.Peer(pub)
	time.Sleep(time.Duration(assured+3) * time.Second)
	if loss, _ := awgtest.Ping(l.nsX, "10.70.0.6", 3, 0, false, 200*time.Millisecond); loss != 100 {
		t.Errorf("after %d s of silence the exit still reaches the client (loss %d%%): the entry's mapping did not expire, the lab's timeouts did not take", assured+3, loss)
	} else {
		t.Logf("after the mapping expired the exit cannot reach the client (loss 100%%): until the client speaks, the exit's packets are lost")
	}
	if loss, out := awgtest.Ping(l.nsB, awgLabTun, 5, 0, false, 200*time.Millisecond); loss != 0 {
		t.Errorf("the client's next packet does not get through after the mapping expired (loss %d%%): %s", loss, out)
	}
	if loss, out := awgtest.Ping(l.nsX, "10.70.0.6", 3, 0, false, 200*time.Millisecond); loss != 0 {
		t.Errorf("the exit cannot reach the client after the client spoke again (loss %d%%): the exit did not follow the new mapping: %s", loss, out)
	}
	p1, _ := srv.Peer(pub)
	t.Logf("the exit's endpoint of the client: %v before, %v after", p0.Endpoint, p1.Endpoint)
}
