//go:build linux

package hostctl

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	udpLabAddr        = "192.0.2.1"
	udpLabBoundPort   = 40443
	udpLabUnboundPort = 40444
)

func TestLabUDPCheckListenerHelper(t *testing.T) {
	if os.Getenv("MG_UDP_CHECK_LISTENER") == "" {
		t.Skip("lab helper")
	}
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP(udpLabAddr), Port: udpLabBoundPort})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = fmt.Fprintln(os.Stdout, "UDP_CHECK_LISTENING")
	buf := make([]byte, 2048)
	for {
		_, addr, err := c.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if _, err := c.WriteToUDP([]byte("seen"), addr); err != nil {
			return
		}
	}
}

func TestLabUDPCheckHoldHelper(t *testing.T) {
	if os.Getenv("MG_UDP_CHECK_HOLD") == "" {
		t.Skip("lab helper")
	}
	h := New(slog.New(slog.DiscardHandler)).(UDPCounter)
	if err := h.CountUDP([8]byte{1, 2, 3, 4, 5, 6, 7, 8}, []uint16{udpLabBoundPort}); err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintln(os.Stdout, "UDP_CHECK_ARMED")
	<-time.After(time.Second)
	if err := CleanupUDPCount(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintln(os.Stdout, "UDP_CHECK_EXPIRED")
}

func TestLabUDPCheckNftCountsAndDropsTaggedDatagrams(t *testing.T) {
	if os.Getenv("MG_ROOT_TESTS") == "" || os.Geteuid() != 0 {
		t.Skip("set MG_ROOT_TESTS=1 and run as root")
	}
	for _, tool := range []string{"ip", "nft", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	id := fmt.Sprintf("mg3-udp-%d", os.Getpid())
	nsNode, nsSender := id+"-n", id+"-s"
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
	for _, ns := range []string{nsNode, nsSender} {
		sh("ip", "netns", "add", ns)
		defer exec.Command("ip", "netns", "del", ns).Run()
	}
	sh("ip", "-n", nsNode, "link", "add", "mg3-udp", "type", "veth", "peer", "name", "mg3-peer", "netns", nsSender)
	for _, c := range [][]string{{nsNode, "mg3-udp", "192.0.2.1/24"}, {nsSender, "mg3-peer", "192.0.2.2/24"}} {
		sh("ip", "-n", c[0], "addr", "add", c[2], "dev", c[1])
		sh("ip", "-n", c[0], "link", "set", c[1], "up")
		sh("ip", "-n", c[0], "link", "set", "lo", "up")
	}
	in := func(ns string, args ...string) []string { return append([]string{"ip", "netns", "exec", ns}, args...) }

	listener := exec.Command("ip", "netns", "exec", nsNode, os.Args[0], "-test.run=^TestLabUDPCheckListenerHelper$")
	listener.Env = append(os.Environ(), "MG_UDP_CHECK_LISTENER=1")
	stdout, err := listener.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Process.Kill(); _ = listener.Wait() }()
	listenerReady := make(chan string, 1)
	go func() {
		s := bufio.NewScanner(stdout)
		for s.Scan() {
			if s.Text() == "UDP_CHECK_LISTENING" {
				listenerReady <- s.Text()
				return
			}
		}
	}()
	select {
	case <-listenerReady:
	case <-time.After(5 * time.Second):
		t.Fatal("UDP listener did not start")
	}

	const tagHex = "0102030405060708"
	arm := exec.Command("ip", "netns", "exec", nsNode, os.Args[0], "-test.run=^TestLabUDPCheckArmHelper$")
	arm.Env = append(os.Environ(), "MG_UDP_CHECK_ARM=1")
	if out, err := arm.CombinedOutput(); err != nil || !strings.Contains(string(out), "PASS") {
		t.Fatalf("arm helper: %v\n%s", err, out)
	}
	send := func(port int, payloadHex string, fill bool) string {
		t.Helper()
		fillExpr := "b''"
		if fill {
			fillExpr = "b'x' * 56"
		}
		code := `import socket,sys
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); s.settimeout(0.3)
s.sendto(bytes.fromhex(sys.argv[1])+` + fillExpr + `,("192.0.2.1",int(sys.argv[2])))
try: print(s.recvfrom(16)[0].decode())
except socket.timeout: print("none")`
		out := sh(in(nsSender, "python3", "-c", code, payloadHex, fmt.Sprint(port))...)
		return strings.TrimSpace(out)
	}
	if got := send(udpLabUnboundPort, tagHex, true); got != "none" {
		t.Fatalf("tagged packet to unbound port got %q", got)
	}
	if got := send(udpLabBoundPort, tagHex, true); got != "none" {
		t.Fatalf("bound listener saw tagged packet: %q", got)
	}
	if got := send(udpLabBoundPort, "ffffffffffffffff", true); got != "seen" {
		t.Fatalf("untagged packet did not reach listener: %q", got)
	}

	take := exec.Command("ip", "netns", "exec", nsNode, os.Args[0], "-test.run=^TestLabUDPCheckTakeHelper$")
	take.Env = append(os.Environ(), "MG_UDP_CHECK_TAKE=1")
	out, err := take.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "UDP_CHECK_COUNT 40443 1 92") || !strings.Contains(string(out), "UDP_CHECK_COUNT 40444 1 92") {
		t.Fatalf("take helper: %v\n%s", err, out)
	}
	if _, err := try(in(nsNode, "nft", "list", "table", "inet", NftUDPCheckTable)...); err == nil {
		t.Fatal("table still exists after take")
	}

	hold := exec.Command("ip", "netns", "exec", nsNode, os.Args[0], "-test.run=^TestLabUDPCheckHoldHelper$")
	hold.Env = append(os.Environ(), "MG_UDP_CHECK_HOLD=1")
	holdOut, err := hold.CombinedOutput()
	if err != nil || !strings.Contains(string(holdOut), "UDP_CHECK_EXPIRED") {
		t.Fatalf("hold helper: %v\n%s", err, holdOut)
	}
	if _, err := try(in(nsNode, "nft", "list", "table", "inet", NftUDPCheckTable)...); err == nil {
		t.Fatal("table still exists after hold expiry")
	}
}

func TestLabUDPCheckArmHelper(t *testing.T) {
	if os.Getenv("MG_UDP_CHECK_ARM") == "" {
		t.Skip("lab helper")
	}
	h := New(slog.New(slog.DiscardHandler)).(UDPCounter)
	if err := h.CountUDP([8]byte{1, 2, 3, 4, 5, 6, 7, 8}, []uint16{udpLabBoundPort, udpLabUnboundPort}); err != nil {
		t.Fatal(err)
	}
}

func TestLabUDPCheckTakeHelper(t *testing.T) {
	if os.Getenv("MG_UDP_CHECK_TAKE") == "" {
		t.Skip("lab helper")
	}
	h := New(slog.New(slog.DiscardHandler)).(UDPCounter)
	counts, err := h.TakeUDPCount()
	if err != nil {
		t.Fatal(err)
	}
	for _, port := range []uint16{udpLabBoundPort, udpLabUnboundPort} {
		c := counts[port]
		_, _ = fmt.Fprintf(os.Stdout, "UDP_CHECK_COUNT %d %d %d\n", port, c.Packets, c.Bytes)
	}
}
