//go:build linux

package hostctl

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apernet/quic-go"
	"golang.org/x/net/ipv6"
)

// The node's own IPv6 address against real packets, in network namespaces of their own (mg3-v6-*): the real host owner puts
// a /128 on the uplink of the node namespace, and a client namespace on the same link talks to it. The test binary runs
// INSIDE the namespaces (the helper roles below), so nothing touches the host or a real interface.
//
// It answers the §4.8 risk of the IPv6 plan: an address with preferred_lft 0 (deprecated) is not picked by the kernel as a
// source for new outgoing connections; do incoming TCP and UDP and, above all, the replies to them still work? For UDP the
// reply source is what matters: a server on a wildcard socket that does not pin the source lets the kernel choose, and a
// client with a connected socket (WireGuard, QUIC) drops a reply from another address. The test runs three UDP servers (a
// naive one, one that answers from the address the packet came to with IPV6_PKTINFO, and a quic-go one, the library of the
// Hysteria2 engine) against the deprecated address of the agent and, for comparison, against a plain /128 added by hand.
//
// Opt-in: MG_ROOT_TESTS=1 as root with ip.
//
//	MG_ROOT_TESTS=1 go test ./internal/node/hostctl -run TestLabOwnedIPv6 -v

const (
	labV6Hoster = "2001:db8:1::10" // the node's own address, from the "hoster"
	labV6Gate   = "2001:db8:1::1"  // the client
	labV6Owned  = "2001:db8:1::beef"
	labV6Plain  = "2001:db8:1::cafe"
	labV6Near   = "2001:db8:1::2"
	labV6IfN    = "de1"
	labV6IfC    = "cl1"
	labPortTCP  = 7000
	labPortUDP  = 7001 // answers with the plain WriteTo
	labPortInfo = 7002 // answers from the address the packet came to
	labPortQUIC = 7003
)

// TestLabOwnedIPv6Helper is the process the lab runs inside a namespace; MG3_LAB_V6 says which role. Skipped in a normal run.
func TestLabOwnedIPv6Helper(t *testing.T) {
	role := os.Getenv("MG3_LAB_V6")
	if role == "" {
		t.Skip("lab helper")
	}
	dir := os.Getenv("MG3_LAB_DIR")
	h := New(slog.New(slog.DiscardHandler)).(*linuxHost)
	h.SetStateDir(dir)
	ctx := context.Background()
	switch role {
	case "sync", "server":
		var res OwnedIPv6Result
		readded := 0
		for i := 0; i < 100; i++ { // DAD takes about a second
			var err error
			res, err = h.SyncOwnedIPv6(ctx, []netip.Addr{netip.MustParseAddr(labV6Owned)})
			if err != nil {
				t.Fatal(err)
			}
			readded += len(res.Readded)
			if len(res.States) == 1 && res.States[0].State == IPv6AddrOK {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Printf("STATE %v readded=%d\n", res.States, readded)
		if role == "server" {
			labServe(t)
		}
	case "cleanup":
		if err := CleanupOwnedIPv6(ctx, dir); err != nil {
			t.Fatal(err)
		}
		fmt.Println("CLEANED")
	case "client":
		labClient(t, os.Getenv("MG3_LAB_TARGET"))
	}
}

func labServe(t *testing.T) {
	go func() { // TCP: say which local address the connection came to
		ln, err := net.Listen("tcp6", fmt.Sprintf("[::]:%d", labPortTCP))
		if err != nil {
			t.Error(err)
			return
		}
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			fmt.Fprint(c, c.LocalAddr().(*net.TCPAddr).IP.String())
			c.Close()
		}
	}()
	go func() { // UDP, naive: a wildcard socket, plain WriteTo; the kernel picks the source
		c, err := net.ListenUDP("udp6", &net.UDPAddr{Port: labPortUDP})
		if err != nil {
			t.Error(err)
			return
		}
		buf := make([]byte, 64)
		for {
			n, from, err := c.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = c.WriteToUDP(buf[:n], from)
		}
	}()
	go func() { // UDP with IPV6_PKTINFO: the reply leaves from the address the packet came to
		uc, err := net.ListenUDP("udp6", &net.UDPAddr{Port: labPortInfo})
		if err != nil {
			t.Error(err)
			return
		}
		pc := ipv6.NewPacketConn(uc)
		if err := pc.SetControlMessage(ipv6.FlagDst|ipv6.FlagInterface, true); err != nil {
			t.Error(err)
			return
		}
		buf := make([]byte, 64)
		for {
			n, cm, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], &ipv6.ControlMessage{Src: cm.Dst, IfIndex: cm.IfIndex}, from)
		}
	}()
	go func() { // QUIC (the library of the Hysteria2 engine) on a wildcard socket
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
		der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
		uc, err := net.ListenUDP("udp6", &net.UDPAddr{Port: labPortQUIC})
		if err != nil {
			t.Error(err)
			return
		}
		l, err := quic.Listen(uc, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, NextProtos: []string{"lab"}}, nil)
		if err != nil {
			t.Error(err)
			return
		}
		for {
			conn, err := l.Accept(context.Background())
			if err != nil {
				return
			}
			go func() {
				s, err := conn.AcceptStream(context.Background())
				if err != nil {
					return
				}
				buf := make([]byte, 4)
				_, _ = s.Read(buf)
				_, _ = s.Write([]byte("pong"))
				time.Sleep(200 * time.Millisecond)
			}()
		}
	}()
	fmt.Println("READY")
	select {} // killed by the test
}

// srcLog is a UDP socket that remembers where its packets came from.
type srcLog struct {
	net.PacketConn // only the plain interface: quic-go must read through ReadFrom, not through ReadMsgUDP
	mu             sync.Mutex
	seen           map[string]bool
}

func (s *srcLog) ReadFrom(b []byte) (int, net.Addr, error) {
	n, a, err := s.PacketConn.ReadFrom(b)
	if a != nil {
		s.mu.Lock()
		s.seen[a.(*net.UDPAddr).IP.String()] = true
		s.mu.Unlock()
	}
	return n, a, err
}

func (s *srcLog) sources() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var l []string
	for a := range s.seen {
		l = append(l, a)
	}
	sort.Strings(l)
	return strings.Join(l, ",")
}

func labClient(t *testing.T, target string) {
	// TCP: which address of the node the connection reached.
	c, err := net.DialTimeout("tcp6", fmt.Sprintf("[%s]:%d", target, labPortTCP), 3*time.Second)
	if err != nil {
		fmt.Println("tcp error", err)
	} else {
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		b := make([]byte, 64)
		n, _ := c.Read(b)
		fmt.Printf("tcp local=%s\n", b[:n])
		c.Close()
	}
	// UDP: from which address the reply came. The socket is not connected, so a wrong source is visible instead of lost.
	for _, p := range []struct {
		name string
		port int
	}{{"udp-plain", labPortUDP}, {"udp-pktinfo", labPortInfo}} {
		uc, err := net.ListenUDP("udp6", &net.UDPAddr{})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = uc.WriteToUDP([]byte("hi"), &net.UDPAddr{IP: net.ParseIP(target), Port: p.port})
		_ = uc.SetReadDeadline(time.Now().Add(2 * time.Second))
		b := make([]byte, 16)
		_, from, err := uc.ReadFromUDP(b)
		if err != nil {
			fmt.Printf("%s reply=none\n", p.name)
		} else {
			fmt.Printf("%s reply-from=%s\n", p.name, from.IP)
		}
		uc.Close()
	}
	// QUIC: does the handshake finish, and from which addresses did its packets come.
	uc, err := net.ListenUDP("udp6", &net.UDPAddr{})
	if err != nil {
		t.Fatal(err)
	}
	sl := &srcLog{PacketConn: uc, seen: map[string]bool{}}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	conn, err := quic.Dial(ctx, sl, &net.UDPAddr{IP: net.ParseIP(target), Port: labPortQUIC},
		&tls.Config{InsecureSkipVerify: true, NextProtos: []string{"lab"}}, nil)
	if err != nil {
		fmt.Printf("quic handshake=failed sources=%s\n", sl.sources())
		return
	}
	s, _ := conn.OpenStreamSync(ctx)
	_, _ = s.Write([]byte("ping"))
	buf := make([]byte, 4)
	_, rerr := s.Read(buf)
	fmt.Printf("quic handshake=ok exchange=%v sources=%s\n", rerr == nil && string(buf) == "pong", sl.sources())
}

func TestLabOwnedIPv6(t *testing.T) {
	if os.Getenv("MG_ROOT_TESTS") != "1" || os.Geteuid() != 0 {
		t.Skip("set MG_ROOT_TESTS=1 and run as root")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("no ip")
	}
	id := fmt.Sprintf("mg3-v6-%d", os.Getpid())
	nsN, nsC := id+"-n", id+"-c"
	sh := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	for _, ns := range []string{nsN, nsC} {
		sh("ip", "netns", "add", ns)
		defer exec.Command("ip", "netns", "del", ns).Run()
		sh("ip", "-n", ns, "link", "set", "lo", "up")
	}
	// The links are created inside the namespaces: nothing of this lab ever exists in the namespace of the host.
	sh("ip", "-n", nsN, "link", "add", labV6IfN, "type", "veth", "peer", "name", labV6IfC, "netns", nsC)
	sh("ip", "-n", nsN, "addr", "add", labV6Hoster+"/64", "dev", labV6IfN)
	sh("ip", "-n", nsC, "addr", "add", labV6Gate+"/64", "dev", labV6IfC)
	sh("ip", "-n", nsN, "link", "set", labV6IfN, "up")
	sh("ip", "-n", nsC, "link", "set", labV6IfC, "up")
	sh("ip", "-n", nsN, "route", "add", "default", "via", labV6Gate, "dev", labV6IfN)
	in := func(ns string, args ...string) []string { return append([]string{"ip", "netns", "exec", ns}, args...) }
	waitDAD := func(ns, dev, addr string) {
		t.Helper()
		for i := 0; i < 60; i++ {
			out := sh("ip", "-n", ns, "-6", "addr", "show", "dev", dev)
			if strings.Contains(out, addr) && !strings.Contains(out, "tentative") {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("%s on %s is still tentative", addr, dev)
	}
	waitDAD(nsN, labV6IfN, labV6Hoster)
	waitDAD(nsC, labV6IfC, labV6Gate)

	state := t.TempDir()
	helper := func(ns, role string, env ...string) *exec.Cmd {
		cmd := exec.Command("ip", "netns", "exec", ns, os.Args[0], "-test.run=^TestLabOwnedIPv6Helper$", "-test.v")
		cmd.Env = append(os.Environ(), append([]string{"MG3_LAB_V6=" + role, "MG3_LAB_DIR=" + state}, env...)...)
		return cmd
	}

	// The node's server: it asks the real host owner for the address first, and waits until DAD is over.
	srv := helper(nsN, "server")
	pipe, err := srv.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Process.Kill(); _ = srv.Wait() }()
	ready := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(pipe)
		for sc.Scan() {
			if l := sc.Text(); strings.HasPrefix(l, "STATE") || l == "READY" {
				if l == "READY" {
					ready <- l
					return
				}
				t.Log(l)
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		t.Fatal("the server did not come up")
	}

	// What the host owner did: a /128, deprecated, noprefixroute, valid forever, next to the hoster's address.
	addrs := sh("ip", "-n", nsN, "-6", "addr", "show", "dev", labV6IfN)
	t.Log("\n" + addrs)
	var line string
	for _, l := range strings.Split(addrs, "\n") {
		if strings.Contains(l, "inet6 "+labV6Owned+"/128") {
			line = l
		}
	}
	for _, want := range []string{"/128", "deprecated", "noprefixroute"} {
		if !strings.Contains(line, want) {
			t.Errorf("owned address line %q lacks %q", line, want)
		}
	}
	if !strings.Contains(addrs, "inet6 "+labV6Hoster+"/64") {
		t.Error("the hoster's address is gone")
	}
	if !strings.Contains(sh("ip", "-n", nsN, "-6", "addr", "show", "dev", labV6IfN, "scope", "global", "deprecated"), labV6Owned) {
		t.Error("the owned address is not listed as deprecated")
	}
	if !strings.Contains(sh("ip", "netns", "exec", nsN, "ip", "-6", "addr", "show", "dev", labV6IfN), "preferred_lft 0sec") {
		t.Error("preferred_lft 0sec not seen")
	}
	// No leak into egress: a new outgoing connection does not use the address.
	if got := sh(in(nsN, "ip", "-6", "route", "get", labV6Gate)...); !strings.Contains(got, "src "+labV6Hoster) {
		t.Errorf("egress source: %s", got)
	}

	probe := func(target string) map[string]string {
		t.Helper()
		out, err := helper(nsC, "client", "MG3_LAB_TARGET="+target).CombinedOutput()
		if err != nil {
			t.Fatalf("client: %v\n%s", err, out)
		}
		res := map[string]string{}
		for _, l := range strings.Split(string(out), "\n") {
			l = strings.TrimSpace(l)
			for _, k := range []string{"tcp", "udp-plain", "udp-pktinfo", "quic"} {
				if strings.HasPrefix(l, k+" ") {
					res[k] = strings.TrimPrefix(l, k+" ")
				}
			}
		}
		t.Logf("to %s: %v", target, res)
		return res
	}
	owned := probe(labV6Owned)
	// For comparison, a plain /128 added by hand (inside the namespace), probed after the first so that it does not change
	// what the kernel can choose for the first.
	sh("ip", "-n", nsN, "addr", "add", labV6Plain+"/128", "dev", labV6IfN, "noprefixroute")
	waitDAD(nsN, labV6IfN, labV6Plain)
	// And with a preferred address that the kernel likes better as a source for this client: a naive server then answers
	// from that one even though nothing is deprecated, so the naive server is no argument about preferred_lft 0.
	sh("ip", "-n", nsN, "addr", "add", labV6Near+"/128", "dev", labV6IfN, "noprefixroute")
	waitDAD(nsN, labV6IfN, labV6Near)
	plain := probe(labV6Plain)

	// Incoming TCP and UDP reach the deprecated address, and the replies leave from it where the server follows the packet.
	if owned["tcp"] != "local="+labV6Owned {
		t.Errorf("TCP to the deprecated address: %q", owned["tcp"])
	}
	if owned["udp-pktinfo"] != "reply-from="+labV6Owned {
		t.Errorf("UDP server that follows the packet: %q", owned["udp-pktinfo"])
	}
	if !strings.Contains(owned["quic"], "handshake=ok exchange=true") || !strings.Contains(owned["quic"], "sources="+labV6Owned) || strings.Contains(owned["quic"], ",") {
		t.Errorf("QUIC to the deprecated address: %q", owned["quic"])
	}
	// The naive server is the finding, not a failure: report where its reply comes from for both kinds of address.
	t.Logf("naive UDP server (plain WriteTo on a wildcard socket): deprecated address answers %q, plain address answers %q",
		owned["udp-plain"], plain["udp-plain"])
	t.Logf("QUIC: deprecated address %q, plain address %q", owned["quic"], plain["quic"])

	// Kernel WireGuard (the AmneziaWG kernel backend is its fork): the tunnel comes up, and the endpoint the client learns
	// for the node is the source address of the node's last authenticated packet, that is, where its replies leave from.
	if _, err := exec.LookPath("wg"); err != nil {
		t.Log("no wg: the kernel WireGuard check is skipped")
	} else if out, err := exec.Command("ip", "-n", nsN, "link", "add", "wgk1", "type", "wireguard").CombinedOutput(); err != nil {
		t.Logf("no kernel WireGuard (%v: %s): its check is skipped", err, strings.TrimSpace(string(out)))
	} else {
		sh("ip", "-n", nsC, "link", "add", "wgk2", "type", "wireguard")
		key := func() (priv, pub string) {
			priv = strings.TrimSpace(sh("wg", "genkey"))
			cmd := exec.Command("wg", "pubkey")
			cmd.Stdin = strings.NewReader(priv)
			b, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			return priv, strings.TrimSpace(string(b))
		}
		keyFile := func(priv string) string {
			p := t.TempDir() + "/key"
			if err := os.WriteFile(p, []byte(priv), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}
		nodePriv, nodePub := key()
		clientPriv, clientPub := key()
		sh("ip", "-n", nsN, "addr", "add", "10.99.0.1/24", "dev", "wgk1")
		sh("ip", "-n", nsC, "addr", "add", "10.99.0.2/24", "dev", "wgk2")
		sh(in(nsN, "wg", "set", "wgk1", "listen-port", "7004", "private-key", keyFile(nodePriv), "peer", clientPub, "allowed-ips", "10.99.0.2/32")...)
		sh(in(nsC, "wg", "set", "wgk2", "private-key", keyFile(clientPriv), "peer", nodePub, "allowed-ips", "10.99.0.1/32",
			"endpoint", "["+labV6Owned+"]:7004")...)
		sh("ip", "-n", nsN, "link", "set", "wgk1", "up")
		sh("ip", "-n", nsC, "link", "set", "wgk2", "up")
		pingOut, pingErr := exec.Command("ip", "netns", "exec", nsC, "ping", "-c", "2", "-W", "3", "10.99.0.1").CombinedOutput()
		endpoints := strings.TrimSpace(sh(in(nsC, "wg", "show", "wgk2", "endpoints")...))
		t.Logf("kernel WireGuard to the deprecated address: ping err=%v, endpoint the client learned: %s", pingErr, endpoints)
		if pingErr != nil {
			t.Errorf("no tunnel through the deprecated address:\n%s", pingOut)
		}
		if !strings.HasSuffix(endpoints, "["+labV6Owned+"]:7004") {
			t.Errorf("WireGuard replies leave from %q, want %s", endpoints, labV6Owned)
		}
	}

	// A network manager removes the address: the next sync puts it back and says so.
	sh("ip", "-n", nsN, "addr", "del", labV6Owned+"/128", "dev", labV6IfN)
	out, err := helper(nsN, "sync").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "readded=1") {
		t.Fatalf("sync after removal: %v\n%s", err, out)
	}
	waitDAD(nsN, labV6IfN, labV6Owned)

	// Cleanup removes only the address of the file; the hoster's and the hand-made one stay.
	if out, err := helper(nsN, "cleanup").CombinedOutput(); err != nil || !strings.Contains(string(out), "CLEANED") {
		t.Fatalf("cleanup: %v\n%s", err, out)
	}
	after := sh("ip", "-n", nsN, "-6", "addr", "show", "dev", labV6IfN)
	if strings.Contains(after, labV6Owned) || !strings.Contains(after, labV6Hoster) || !strings.Contains(after, labV6Plain) {
		t.Errorf("after cleanup:\n%s", after)
	}
	if _, err := os.Stat(state + "/" + OwnedIPv6File); err == nil {
		t.Error("the owned file is still there")
	}
}
