//go:build linux

package vless

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestTCPStallOfAFreezeAfter16K drives the real TCP_INFO reading: on loopback, nftables drops everything the server sends
// past 16 KiB of its direction (how the TSPU's freeze looks), the client gives up and closes, and the server's connection
// is read as it closes. Needs root and nft, so it runs in WSL like the AmneziaWG stand:
//
//	MSYS_NO_PATHCONV=1 wsl -d Ubuntu -u root -- bash -c 'cd <repo> && MG_ROOT_TESTS=1 go test ./internal/node/vless/ -run TestTCPStallOf -count=1 -v'
//
// It adds one table, mg3vless_stall, for its own port and removes it.
func TestTCPStallOfAFreezeAfter16K(t *testing.T) {
	if os.Getenv("MG_ROOT_TESTS") != "1" || os.Geteuid() != 0 {
		t.Skip("needs root and MG_ROOT_TESTS=1")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	rules := fmt.Sprintf(`table inet mg3vless_stall { chain output { type filter hook output priority filter; tcp sport %d ct reply bytes > 16384 drop; } }`, port)
	nft := exec.Command("nft", "-f", "-")
	nft.Stdin = strings.NewReader(rules)
	if out, err := nft.CombinedOutput(); err != nil {
		t.Skipf("nft is not usable here: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("nft", "delete", "table", "inet", "mg3vless_stall").Run() })

	got := make(chan [2]bool, 1)
	go func() {
		conn, err := ln.Accept()
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
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _ := io.Copy(io.Discard, client)
	_ = client.Close()
	r := <-got
	t.Logf("the client read %d bytes before the freeze; stalled %v, first16K %v", n, r[0], r[1])
	if !r[0] || !r[1] {
		t.Fatalf("a connection frozen after 16 KiB: stalled %v, first16K %v; want both", r[0], r[1])
	}

	// the same transfer without the rule is no stall
	_ = exec.Command("nft", "delete", "table", "inet", "mg3vless_stall").Run()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			got <- [2]bool{true, true}
			return
		}
		_, _ = conn.Write(make([]byte, 256<<10))
		_ = conn.(*net.TCPConn).CloseWrite()
		_, _ = io.Copy(io.Discard, conn)
		stalled, first := tcpStallOf(conn)
		_ = conn.Close()
		got <- [2]bool{stalled, first}
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, client)
	_ = client.Close()
	if r := <-got; r[0] {
		t.Fatalf("a whole transfer counted as a stall: %v", r)
	}
}
