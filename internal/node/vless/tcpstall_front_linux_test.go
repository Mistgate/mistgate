//go:build linux

package vless

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/mistgate/mistgate/internal/node/front"
)

// TCP_INFO of a connection the front handed over is read through the socket under it: stalledOf takes the connection as
// Xray's tracker does when it closes it.
func TestTCPInfoThroughTheFrontsConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fr := front.New(ln, nil)
	defer fr.Close()
	fr.SetRoutes([]front.Route{{ID: "x"}}, "x")
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, _ = io.WriteString(client, "hello\r\n") // not TLS: the default route gets it
	conn, err := fr.Listener("x").Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	raw, err := conn.(syscall.Conn).SyscallConn()
	if err != nil {
		t.Fatalf("the front's connection has no raw socket: %v", err)
	}
	var info *unix.TCPInfo
	if err := raw.Control(func(fd uintptr) {
		info, err = unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
	}); err != nil || info == nil || info.State != unix.BPF_TCP_ESTABLISHED {
		t.Fatalf("TCP_INFO through the front's connection: %+v, %v", info, err)
	}
	if stalled, first := tcpStallOf(conn); stalled || first {
		t.Fatalf("a healthy connection reads as a stall: %v %v", stalled, first)
	}
}

// TestTCPStallOfAFreezeAfter16KThroughTheFront is TestTCPStallOfAFreezeAfter16K with the front between the client and the
// connection that is read as it closes: nftables drops everything the server sends past 16 KiB, the client gives up, and
// the stall is seen. Needs root and nft, so it runs in WSL:
//
//	MSYS_NO_PATHCONV=1 wsl -d Ubuntu -u root -- bash -c 'cd <repo> && MG_ROOT_TESTS=1 go test ./internal/node/vless/ -run TestTCPStallOfAFreezeAfter16KThroughTheFront -count=1 -v'
//
// It adds one table, mg3vless_stall_front, for its own port and removes it.
func TestTCPStallOfAFreezeAfter16KThroughTheFront(t *testing.T) {
	if os.Getenv("MG_ROOT_TESTS") != "1" || os.Geteuid() != 0 {
		t.Skip("needs root and MG_ROOT_TESTS=1")
	}
	smallMSS := func(_, _ string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) { _ = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_MAXSEG, 1400) })
	}
	ln, err := (&net.ListenConfig{Control: smallMSS}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fr := front.New(ln, nil)
	defer fr.Close()
	fr.SetRoutes([]front.Route{{ID: "x"}}, "x")
	routed := fr.Listener("x")
	rules := fmt.Sprintf(`table inet mg3vless_stall_front {
  chain input {
    type filter hook input priority filter; policy accept;
    tcp sport %d ct reply bytes > 16384 drop
  }
}
`, ln.Addr().(*net.TCPAddr).Port)
	nft := exec.Command("nft", "-f", "-")
	nft.Stdin = strings.NewReader(rules)
	if out, err := nft.CombinedOutput(); err != nil {
		t.Skipf("nft is not usable here: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("nft", "delete", "table", "inet", "mg3vless_stall_front").Run() })

	got := make(chan [2]bool, 1)
	go func() {
		conn, err := routed.Accept()
		if err != nil {
			got <- [2]bool{}
			return
		}
		go func() { _, _ = conn.Write(make([]byte, 1<<20)) }()
		_, _ = io.Copy(io.Discard, conn) // until the client's FIN, which still gets through
		time.Sleep(4 * time.Second)      // the retransmissions of the frozen data go on
		stalled, first := tcpStallOf(conn)
		_ = conn.Close()
		got <- [2]bool{stalled, first}
	}()
	dialer := &net.Dialer{Control: smallMSS}
	client, err := dialer.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(client, "hello\r\n") // not TLS: the default route gets it
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _ := io.Copy(io.Discard, client)
	_ = client.Close()
	r := <-got
	t.Logf("the client read %d bytes before the freeze; stalled %v, first16K %v", n, r[0], r[1])
	if !r[0] || !r[1] {
		t.Fatalf("a connection frozen after 16 KiB: stalled %v, first16K %v; want both", r[0], r[1])
	}
}
