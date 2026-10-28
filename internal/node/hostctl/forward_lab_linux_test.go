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
	"strconv"
	"strings"
	"testing"
	"time"
)

// The doctor's allow_forwarding fix against real packets and real firewall tools. A host namespace (mgfwd-<pid>-h) holds a
// fake tunnel interface mgawg0 with a client behind it, a WAN side with an "internet" host, an exit for the relay, a LAN
// host and a docker0 with a "container" behind it. The host's FORWARD path drops (UFW-like and Docker-like iptables chains,
// a native nft base chain, iptables-legacy). The fix is applied by the real host owner inside the namespace (this test
// binary, re-run there), so nothing touches the machine.
//
// What it shows: before the fix the client and the relay are dropped; after it the client reaches the internet and the relay
// works both ways (TCP and UDP) while a NEW connection from the internet to the client, forwarded traffic of another
// interface and the client's way to a container bridge stay dropped; a second apply and a sweep add nothing; a firewall
// reload is repaired by the sweep; Cleanup leaves the host's own rules exactly as they were.
//
// Opt-in: MG_ROOT_TESTS=1 as root with ip, nft, iptables, iptables-legacy and python3.
//
//	MG_ROOT_TESTS=1 go test ./internal/node/hostctl -run TestLabForward -v

const (
	labFwdWAN   = "198.51.100.1" // the host as the internet sees it (relay entry)
	labFwdInet  = "198.51.100.2" // the internet host
	labFwdExit  = "192.0.2.2"    // the relay exit
	labFwdLAN   = "203.0.113.2"  // a host on another interface of the machine
	labFwdBox   = "172.17.0.2"   // a container behind docker0
	labFwdCli   = "10.9.0.2"     // the tunnel client
	labFwdRelay = "rly_t:tcp:8443:" + labFwdExit + ":443,rly_u:udp:9443:" + labFwdExit + ":5353"
)

// TestLabForwardHelper is the process the lab runs inside the host namespace. MG3_LAB_FWD is a ";"-separated list of
// operations (relay=<spec>|relay=none, plan, apply, reassert, cleanupfwd, cleanup); each prints one "RESULT ..." line.
func TestLabForwardHelper(t *testing.T) {
	spec := os.Getenv("MG3_LAB_FWD")
	if spec == "" {
		t.Skip("lab helper")
	}
	dir, err := os.MkdirTemp("", "mgfwd-helper")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	h := &linuxHost{
		log: slog.New(slog.DiscardHandler), run: execRunner,
		sysctlFile: filepath.Join(dir, "sysctl.conf"), journaldFile: filepath.Join(dir, "journald.conf"),
		procSys: "/proc/sys", procRoot: "/proc", sshdConfDir: dir,
	}
	h.SetStateDir(os.Getenv("MG3_LAB_FWD_STATE"))
	ctx := context.Background()
	for _, op := range strings.Split(spec, ";") {
		name, arg, _ := strings.Cut(op, "=")
		switch name {
		case "relay":
			var fs []Forward
			if arg != "none" {
				for _, item := range strings.Split(arg, ",") {
					p := strings.Split(item, ":")
					port, _ := strconv.Atoi(p[2])
					toPort, _ := strconv.Atoi(p[4])
					fs = append(fs, Forward{ID: p[0], Network: p[1], Port: uint16(port), ToIP: netip.MustParseAddr(p[3]), ToPort: uint16(toPort)})
				}
			}
			if err := h.SetRelays(ctx, fs); err != nil {
				fmt.Println("RESULT relay err", err, "drop="+strconv.FormatBool(errors.Is(err, ErrForwardDrop)))
			} else {
				fmt.Println("RESULT relay ok")
			}
		case "plan":
			p, err := h.PlanForwardExceptions(ctx)
			if err != nil {
				fmt.Println("RESULT plan err", err)
				continue
			}
			fmt.Printf("RESULT plan needed=%v add=%d remove=%d chains=%d relays=%d backend=%s\n", p.Needed, p.AddRules, p.RemoveRules, p.DropChains, p.RelayForwards, p.Backend)
		case "apply":
			n, err := h.ApplyForwardExceptions(ctx)
			if err != nil {
				fmt.Println("RESULT apply err", err)
				continue
			}
			fmt.Printf("RESULT apply changed=%d\n", n)
		case "reassert":
			if err := h.ReassertForwardExceptions(ctx); err != nil {
				fmt.Println("RESULT reassert err", err)
				continue
			}
			fmt.Println("RESULT reassert ok")
		case "cleanupfwd":
			if err := h.cleanupForwardExceptions(ctx); err != nil {
				fmt.Println("RESULT cleanupfwd err", err)
				continue
			}
			fmt.Println("RESULT cleanupfwd ok")
		case "cleanup":
			if err := h.Cleanup(ctx); err != nil {
				fmt.Println("RESULT cleanup err", err)
				continue
			}
			fmt.Println("RESULT cleanup ok")
		default:
			fmt.Println("RESULT unknown op", op)
		}
	}
}

// labFwdScenario is one way a host drops forwarded packets.
type labFwdScenario struct {
	name   string
	legacy bool   // iptables and ip6tables are the legacy binaries for the helper
	setup  string // shell, run inside the host namespace; safe to run again after a flush ("firewall reload")
	// reload removes everything the host's firewall owns and sets it up again (what ufw reload or a Docker restart does),
	// which also removes the fix's rules from the chains it rewrites. Empty = setup.
	reload string
	// estab says that the host's own rules already accept ESTABLISHED,RELATED for everything (UFW does): replies are then
	// not a proof of the fix's own reply rules.
	estab bool
}

const labFwdHostRule = "host-owned"

var labFwdScenarios = []labFwdScenario{
	{
		name: "iptables-nft FORWARD policy DROP",
		setup: `iptables -F FORWARD; iptables -P FORWARD DROP
iptables -A FORWARD -i lan0 -o eth0 -m comment --comment ` + labFwdHostRule + ` -j ACCEPT`,
	},
	{
		name:  "iptables-nft UFW-like chains",
		estab: true,
		setup: `iptables -F FORWARD; iptables -P FORWARD DROP
for c in ufw-before-forward ufw-user-forward ufw-reject-forward; do iptables -F $c 2>/dev/null || iptables -N $c; done
iptables -A ufw-before-forward -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
iptables -A ufw-before-forward -p icmp --icmp-type echo-request -j ACCEPT
iptables -A FORWARD -j ufw-before-forward
iptables -A FORWARD -j ufw-user-forward
iptables -A FORWARD -j ufw-reject-forward
iptables -A ufw-user-forward -i lan0 -o eth0 -m comment --comment ` + labFwdHostRule + ` -j ACCEPT`,
	},
	{
		name: "iptables-nft Docker-like chains",
		setup: `iptables -F FORWARD; iptables -P FORWARD DROP
for c in DOCKER-USER DOCKER DOCKER-ISOLATION-STAGE-1; do iptables -F $c 2>/dev/null || iptables -N $c; done
iptables -A DOCKER-USER -j RETURN
iptables -A DOCKER-ISOLATION-STAGE-1 -j RETURN
iptables -A FORWARD -j DOCKER-USER
iptables -A FORWARD -j DOCKER-ISOLATION-STAGE-1
iptables -A FORWARD -o docker0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
iptables -A FORWARD -o docker0 -j DOCKER
iptables -A FORWARD -i docker0 ! -o docker0 -j ACCEPT
iptables -A FORWARD -i lan0 -o eth0 -m comment --comment ` + labFwdHostRule + ` -j ACCEPT`,
	},
	{
		name:   "iptables-legacy FORWARD policy DROP",
		legacy: true,
		setup: `iptables-legacy -F FORWARD; iptables-legacy -P FORWARD DROP
iptables-legacy -A FORWARD -i lan0 -o eth0 -m comment --comment ` + labFwdHostRule + ` -j ACCEPT`,
		reload: `iptables-legacy -F FORWARD; iptables-legacy -P FORWARD DROP
iptables-legacy -A FORWARD -i lan0 -o eth0 -m comment --comment ` + labFwdHostRule + ` -j ACCEPT`,
	},
	{
		name: "native nft inet base chain, policy drop, no established accept",
		setup: `nft delete table inet hostfw 2>/dev/null || true
nft -f - <<'EOT'
table inet hostfw {
	chain forward {
		type filter hook forward priority filter; policy drop;
		iifname "lan0" oifname "eth0" accept comment "` + labFwdHostRule + `"
	}
}
EOT`,
	},
	{
		name:  "native nft ip base chain with established accept",
		estab: true,
		setup: `nft delete table ip hostfw 2>/dev/null || true
nft -f - <<'EOT'
table ip hostfw {
	chain forward {
		type filter hook forward priority filter - 10; policy drop;
		ct state established,related accept
		iifname "lan0" oifname "eth0" accept comment "` + labFwdHostRule + `"
	}
}
EOT`,
	},
	{
		name: "iptables-nft Docker-like plus a native inet drop chain",
		setup: `iptables -F FORWARD; iptables -P FORWARD DROP
for c in DOCKER-USER DOCKER; do iptables -F $c 2>/dev/null || iptables -N $c; done
iptables -A DOCKER-USER -j RETURN
iptables -A FORWARD -j DOCKER-USER
iptables -A FORWARD -o docker0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
iptables -A FORWARD -o docker0 -j DOCKER
iptables -A FORWARD -i docker0 ! -o docker0 -j ACCEPT
nft delete table inet hostfw 2>/dev/null || true
nft -f - <<'EOT'
table inet hostfw {
	chain forward {
		type filter hook forward priority filter + 5; policy drop;
		iifname "lan0" oifname "eth0" accept comment "` + labFwdHostRule + `"
	}
}
EOT`,
	},
}

func TestLabForward(t *testing.T) {
	if os.Getenv("MG_ROOT_TESTS") == "" || os.Geteuid() != 0 {
		t.Skip("set MG_ROOT_TESTS=1 and run as root")
	}
	for _, tool := range []string{"ip", "nft", "iptables", "iptables-legacy", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	// The helper's iptables must be the nf_tables one unless a scenario says otherwise: the lab reads what is on PATH.
	if out, _ := exec.Command("iptables", "-V").CombinedOutput(); !strings.Contains(string(out), "nf_tables") {
		t.Skip("the iptables on PATH is not iptables-nft: " + strings.TrimSpace(string(out)))
	}
	shim := t.TempDir()
	for name, target := range map[string]string{"iptables": "iptables-legacy", "ip6tables": "ip6tables-legacy"} {
		path, err := exec.LookPath(target)
		if err != nil {
			t.Skip("no " + target)
		}
		if err := os.Symlink(path, filepath.Join(shim, name)); err != nil {
			t.Fatal(err)
		}
	}
	for i, sc := range labFwdScenarios {
		t.Run(fmt.Sprintf("%d_%s", i, strings.Join(strings.Fields(sc.name)[:2], "_")), func(t *testing.T) {
			runLabForward(t, fmt.Sprintf("mgfwd-%d-%d", os.Getpid(), i), shim, sc)
		})
	}
}

func runLabForward(t *testing.T, id, shim string, sc labFwdScenario) {
	nsH, nsC, nsI, nsX, nsU, nsD := id+"-h", id+"-c", id+"-i", id+"-x", id+"-u", id+"-d"
	sh := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	for _, ns := range []string{nsH, nsC, nsI, nsX, nsU, nsD} {
		sh("ip", "netns", "add", ns)
		defer exec.Command("ip", "netns", "del", ns).Run()
	}
	in := func(ns string, args ...string) []string { return append([]string{"ip", "netns", "exec", ns}, args...) }
	hostShell := func(script string) string {
		t.Helper()
		return sh(in(nsH, "bash", "-ec", script)...)
	}
	// host side name, peer namespace, peer side name, host address, peer address
	for _, l := range []struct{ host, ns, peer, haddr, paddr string }{
		{"mgawg0", nsC, "tun0", "10.9.0.1/24", labFwdCli + "/24"},
		{"eth0", nsI, "wan0", labFwdWAN + "/24", labFwdInet + "/24"},
		{"exit0", nsX, "e0", "192.0.2.1/24", labFwdExit + "/24"},
		{"lan0", nsU, "l0", "203.0.113.1/24", labFwdLAN + "/24"},
		{"docker0", nsD, "c0", "172.17.0.1/24", labFwdBox + "/24"},
	} {
		sh("ip", "-n", nsH, "link", "add", l.host, "type", "veth", "peer", "name", l.peer, "netns", l.ns)
		for _, c := range [][]string{{nsH, l.host, l.haddr}, {l.ns, l.peer, l.paddr}} {
			sh("ip", "-n", c[0], "addr", "add", c[2], "dev", c[1])
			sh("ip", "-n", c[0], "link", "set", c[1], "up")
			sh("ip", "-n", c[0], "link", "set", "lo", "up")
		}
	}
	sh("ip", "-n", nsH, "link", "set", "lo", "up")
	sh("ip", "-n", nsC, "route", "add", "default", "via", "10.9.0.1")
	sh("ip", "-n", nsU, "route", "add", "default", "via", "203.0.113.1")
	sh("ip", "-n", nsD, "route", "add", "default", "via", "172.17.0.1")
	// The internet host can route to every other network through the host: only the firewall may stop it.
	for _, n := range []string{"10.9.0.0/24", "203.0.113.0/24", "172.17.0.0/24"} {
		sh("ip", "-n", nsI, "route", "add", n, "via", labFwdWAN)
	}
	// The machine forwards (Docker and UFW hosts do) and masquerades its client's traffic like the tunnel table does.
	hostShell(`sysctl -qw net.ipv4.ip_forward=1
nft -f - <<'EOT'
table ip labnat {
	chain post {
		type nat hook postrouting priority srcnat; policy accept;
		ip saddr 10.9.0.0/24 oifname != "mgawg0" masquerade
	}
}
EOT`)

	// Servers answer "<proto>:<address they saw>".
	server := func(addr string, tcpPort, udpPort int) string {
		return fmt.Sprintf(`import socket, threading
def tcp():
    s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(("%[1]s", %[2]d)); s.listen(16)
    while True:
        c, a = s.accept(); c.send(b"tcp:" + a[0].encode()); c.close()
threading.Thread(target=tcp, daemon=True).start()
u = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); u.bind(("%[1]s", %[3]d))
while True:
    _, a = u.recvfrom(64); u.sendto(b"udp:" + a[0].encode(), a)`, addr, tcpPort, udpPort)
	}
	for _, s := range []struct {
		ns, code string
	}{
		{nsI, server(labFwdInet, 80, 5300)}, {nsX, server(labFwdExit, 443, 5353)}, {nsC, server(labFwdCli, 7000, 7000)},
		{nsU, server(labFwdLAN, 7001, 7001)}, {nsD, server(labFwdBox, 7002, 7002)},
	} {
		cmd := exec.Command("ip", "netns", "exec", s.ns, "python3", "-c", s.code)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	}
	time.Sleep(500 * time.Millisecond)

	// probe runs the probes of one namespace at once (a dropped one takes its timeout, in parallel) and returns name -> answer.
	type probe struct {
		name, proto, addr string
		port              int
	}
	run := func(ns string, probes ...probe) map[string]string {
		t.Helper()
		var list []string
		for _, p := range probes {
			list = append(list, fmt.Sprintf(`("%s","%s","%s",%d)`, p.name, p.proto, p.addr, p.port))
		}
		code := `import socket, threading
res = {}
def go(n, proto, addr, port):
    try:
        if proto == "tcp":
            s = socket.socket(); s.settimeout(1.2); s.connect((addr, port)); res[n] = s.recv(64).decode()
        else:
            s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.settimeout(1.2); s.sendto(b"hi", (addr, port)); res[n] = s.recv(64).decode()
    except OSError:
        res[n] = "none"
ts = [threading.Thread(target=go, args=p) for p in [` + strings.Join(list, ",") + `]]
[x.start() for x in ts]
[x.join() for x in ts]
for k, v in res.items():
    print(k + "=" + v)`
		out := sh(in(ns, "python3", "-c", code)...)
		m := map[string]string{}
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			k, v, _ := strings.Cut(l, "=")
			m[k] = v
		}
		return m
	}
	fromClient := func() map[string]string {
		return run(nsC,
			probe{"inet-tcp", "tcp", labFwdInet, 80}, probe{"inet-udp", "udp", labFwdInet, 5300},
			probe{"lan-tcp", "tcp", labFwdLAN, 7001}, probe{"box-tcp", "tcp", labFwdBox, 7002})
	}
	fromInternet := func() map[string]string {
		return run(nsI,
			probe{"relay-tcp", "tcp", labFwdWAN, 8443}, probe{"relay-udp", "udp", labFwdWAN, 9443}, probe{"unforwarded", "tcp", labFwdWAN, 8444},
			probe{"client-tcp", "tcp", labFwdCli, 7000}, probe{"client-udp", "udp", labFwdCli, 7000},
			probe{"lan-tcp", "tcp", labFwdLAN, 7001}, probe{"box-tcp", "tcp", labFwdBox, 7002})
	}
	expect := func(phase string, got map[string]string, want map[string]string) {
		t.Helper()
		for k, w := range want {
			if got[k] != w {
				t.Errorf("%s: %s = %q, want %q", phase, k, got[k], w)
			}
		}
	}
	none := "none"
	allNone := map[string]string{"inet-tcp": none, "inet-udp": none, "lan-tcp": none, "box-tcp": none}
	allNoneI := map[string]string{"relay-tcp": none, "relay-udp": none, "unforwarded": none, "client-tcp": none, "client-udp": none, "lan-tcp": none, "box-tcp": none}

	helper := func(ops string) map[string]string {
		t.Helper()
		cmd := exec.Command("ip", "netns", "exec", nsH, os.Args[0], "-test.run=^TestLabForwardHelper$", "-test.v")
		cmd.Env = append(os.Environ(), "MG3_LAB_FWD="+ops, "MG3_LAB_FWD_STATE="+filepath.Join(stateDir(t, id), "state"))
		if sc.legacy {
			cmd.Env = append(cmd.Env, "PATH="+shim+":"+os.Getenv("PATH"))
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("helper %q: %v\n%s", ops, err, out)
		}
		res := map[string]string{}
		for _, l := range strings.Split(string(out), "\n") {
			if rest, ok := strings.CutPrefix(l, "RESULT "); ok {
				op, tail, _ := strings.Cut(rest, " ")
				res[op] = tail
			}
		}
		return res
	}
	// snapshot is the firewall as the HOST owns it (counters stripped): what must be identical after Cleanup.
	snapshot := func() string {
		return hostShell(`nft list ruleset | sed -E 's/counter packets [0-9]+ bytes [0-9]+/counter/' | grep -v 'Warning: table'
iptables-legacy-save | grep -v '^#' | sed -E 's/\[[0-9]+:[0-9]+\]/[0:0]/'`)
	}
	tagged := func() int {
		out := hostShell(`(nft list ruleset; iptables-save; iptables-legacy-save; ip6tables-save; ip6tables-legacy-save) 2>/dev/null | grep -c '` + forwardExceptionPrefix + `' || true`)
		n, _ := strconv.Atoi(strings.TrimSpace(out))
		return n
	}

	hostShell(sc.setup)
	baseline := snapshot()
	if tagged() != 0 {
		t.Fatal("the lab is wrong: tagged rules before the fix")
	}

	// ---- before the fix
	expect("before: client", fromClient(), allNone)
	if r := helper("relay=" + labFwdRelay)["relay"]; !strings.Contains(r, "forward_drop") || !strings.Contains(r, "drop=true") {
		t.Errorf("before: relay install = %q, want fail-closed forward_drop", r)
	}
	expect("before: internet", fromInternet(), allNoneI)
	if r := helper("plan")["plan"]; !strings.Contains(r, "needed=true") {
		t.Errorf("before: plan = %q, want needed=true", r)
	}

	// ---- apply (the agent: sweep tries the relay, the owner confirms the fix, the next sweep installs the relay)
	res := helper("apply;relay=" + labFwdRelay + ";plan")
	if !strings.HasPrefix(res["apply"], "changed=") || strings.Contains(res["apply"], "changed=0") {
		t.Fatalf("apply = %q", res["apply"])
	}
	if res["relay"] != "ok" {
		t.Fatalf("relay after the fix = %q", res["relay"])
	}
	if !strings.Contains(res["plan"], "needed=false") {
		t.Errorf("plan after apply = %q, want needed=false", res["plan"])
	}
	afterC := map[string]string{"inet-tcp": "tcp:" + labFwdWAN, "inet-udp": "udp:" + labFwdWAN, "lan-tcp": "tcp:203.0.113.1", "box-tcp": none}
	afterI := map[string]string{"relay-tcp": "tcp:192.0.2.1", "relay-udp": "udp:192.0.2.1", "unforwarded": none,
		"client-tcp": none, "client-udp": none, "lan-tcp": none, "box-tcp": none}
	expect("after: client", fromClient(), afterC)
	expect("after: internet", fromInternet(), afterI)
	appliedRules := tagged()
	if appliedRules == 0 {
		t.Fatal("no tagged rules after apply")
	}
	if sc.estab {
		t.Log("the host's own rules accept ESTABLISHED,RELATED: replies are not a proof of the fix's reply rules here")
	}

	// ---- twice adds nothing, and neither does a sweep
	res = helper("apply;reassert;relay=" + labFwdRelay + ";reassert;plan")
	if res["apply"] != "changed=0" || res["reassert"] != "ok" || res["relay"] != "ok" || !strings.Contains(res["plan"], "needed=false") {
		t.Errorf("second round = %v", res)
	}
	if got := tagged(); got != appliedRules {
		t.Errorf("tagged rules after a second apply: %d, want %d", got, appliedRules)
	}

	// ---- a firewall reload removes the host chain contents; the sweep restores the approved exceptions
	if sc.reload != "" {
		hostShell(sc.reload)
	} else {
		hostShell(sc.setup)
	}
	if tagged() != 0 {
		t.Fatalf("the lab is wrong: the reload left %d tagged rules", tagged())
	}
	expect("after reload: client", fromClient(), allNone)
	res = helper("reassert;relay=" + labFwdRelay)
	if res["reassert"] != "ok" || res["relay"] != "ok" {
		t.Errorf("sweep after reload = %v", res)
	}
	if got := tagged(); got != appliedRules {
		t.Errorf("tagged rules after the sweep: %d, want %d", got, appliedRules)
	}
	expect("after sweep: client", fromClient(), afterC)
	expect("after sweep: internet", fromInternet(), afterI)

	// ---- Cleanup removes only the tagged rules
	res = helper("relay=none;cleanupfwd")
	if res["cleanupfwd"] != "ok" {
		t.Errorf("cleanupfwd = %v", res)
	}
	if got := tagged(); got != 0 {
		t.Errorf("tagged rules left after Cleanup: %d", got)
	}
	if now := snapshot(); now != baseline {
		t.Errorf("the host's own firewall changed:\n--- before\n%s\n--- after\n%s", baseline, now)
	}
	if _, err := os.Stat(filepath.Join(stateDir(t, id), "state", forwardStateFileName)); !os.IsNotExist(err) {
		t.Errorf("the approved-state file remains after Cleanup: %v", err)
	}
	expect("after cleanup: client", fromClient(), allNone)
	expect("after cleanup: internet", fromInternet(), allNoneI)
	// a sweep after Cleanup must not bring anything back
	if res := helper("reassert"); res["reassert"] != "ok" || tagged() != 0 {
		t.Errorf("a sweep after Cleanup re-added rules: %v tagged=%d", res, tagged())
	}
	// and the whole agent Cleanup (retire) is clean too
	if res := helper("apply;relay=" + labFwdRelay + ";cleanup"); res["cleanup"] != "ok" || tagged() != 0 {
		t.Errorf("full Cleanup: %v tagged=%d", res, tagged())
	}
}

var labFwdStateDirs = map[string]string{}

// stateDir is the persistent state directory of the agent in one lab (it survives the helper processes).
func stateDir(t *testing.T, id string) string {
	t.Helper()
	if d, ok := labFwdStateDirs[id]; ok {
		return d
	}
	d := t.TempDir()
	labFwdStateDirs[id] = d
	if err := os.MkdirAll(filepath.Join(d, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	return d
}
