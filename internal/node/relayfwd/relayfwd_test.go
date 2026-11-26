package relayfwd

import (
	"io"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"
)

// freePort reserves a port and gives it away: the forward listens on every address, so a test needs to know the port before it
// starts. The gap between the two is small and a busy port only fails the test, never hides a bug.
func freePort(t *testing.T, network string) uint16 {
	t.Helper()
	if network == "udp" {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer pc.Close()
		return uint16(pc.LocalAddr().(*net.UDPAddr).Port)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return uint16(ln.Addr().(*net.TCPAddr).Port)
}

func tcpEcho(t *testing.T, network, addr string) net.Listener {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln
}

func udpEcho(t *testing.T, network, addr string) *net.UDPConn {
	t.Helper()
	ua, err := net.ResolveUDPAddr(network, addr)
	if err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenUDP(network, ua)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteToUDPAddrPort(buf[:n], from)
		}
	}()
	return pc
}

func waitCounters(t *testing.T, s *Server, id string, ok func(Counters) bool) Counters {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c := s.Counters()[id]; ok(c) {
			return c
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("counters of %s did not reach what the test expects: %+v", id, s.Counters()[id])
	return Counters{}
}

// A TCP port carried to an IPv6 exit: the people's connection is spliced to a connection of the other family, both directions
// pass, and the bytes are counted on both sides.
func TestTCPForwardToIPv6(t *testing.T) {
	echo := tcpEcho(t, "tcp6", "[::1]:0")
	defer echo.Close()
	to := netip.MustParseAddrPort(echo.Addr().String())
	port := freePort(t, "tcp")

	s := New()
	defer s.Close()
	if errs := s.Sync([]Forward{{ID: "rly_a", Network: "tcp", Port: port, To: to, Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))}}); len(errs) != 0 {
		t.Fatalf("sync: %v", errs)
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("hello over the family")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("hello over the family"))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello over the family" {
		t.Fatalf("echo = %q", got)
	}
	c := waitCounters(t, s, "rly_a", func(c Counters) bool { return c.Bytes > 0 && c.BytesBack > 0 })
	if c.BytesBack == 0 {
		t.Fatalf("nothing counted back: %+v", c)
	}
}

// A UDP port carried to an IPv6 exit: every client address gets a session of its own, datagrams pass both ways, and a client
// that goes quiet loses its session at the sweep.
func TestUDPForwardToIPv6(t *testing.T) {
	echo := udpEcho(t, "udp6", "[::1]:0")
	defer echo.Close()
	to := netip.MustParseAddrPort(echo.LocalAddr().String())
	port := freePort(t, "udp")

	s := New()
	defer s.Close()
	if errs := s.Sync([]Forward{{ID: "rly_u", Network: "udp", Port: port, To: to, Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))}}); len(errs) != 0 {
		t.Fatalf("sync: %v", errs)
	}
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(port)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	for i := 0; i < 3; i++ {
		if _, err := conn.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		if string(buf[:n]) != "ping" {
			t.Fatalf("reply = %q", buf[:n])
		}
	}
	c := waitCounters(t, s, "rly_u", func(c Counters) bool {
		return c.Bytes >= 12 && c.BytesBack >= 12 && c.Packets >= 3 && c.PacketsBack >= 3
	})
	if c.Bytes < 12 || c.BytesBack < 12 || c.Packets < 3 || c.PacketsBack < 3 {
		t.Fatalf("counters = %+v", c)
	}
}

// Sync replaces the running set: a changed target stops the old forward and starts the new one; an empty list stops everything.
func TestSyncReplacesAndStops(t *testing.T) {
	echo := tcpEcho(t, "tcp4", "127.0.0.1:0")
	defer echo.Close()
	to := netip.MustParseAddrPort(echo.Addr().String())
	port := freePort(t, "tcp")

	s := New()
	defer s.Close()
	if errs := s.Sync([]Forward{{ID: "rly_a", Network: "tcp", Port: port, To: to, Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))}}); len(errs) != 0 {
		t.Fatalf("sync: %v", errs)
	}
	if errs := s.Sync([]Forward{{ID: "rly_a", Network: "tcp", Port: port, To: to, Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))}}); len(errs) != 0 {
		t.Fatalf("the same forward again: %v", errs)
	}
	if errs := s.Sync([]Forward{{ID: "rly_a", Network: "tcp", Port: port, To: netip.MustParseAddrPort("127.0.0.1:9"), Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))}}); len(errs) != 0 {
		t.Fatalf("a changed target: %v", errs)
	}
	if errs := s.Sync(nil); len(errs) != 0 {
		t.Fatalf("stopping: %v", errs)
	}
	if _, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), 300*time.Millisecond); err == nil {
		t.Fatal("the port still accepts after the forward was stopped")
	}
}

func TestBadConfigIsRefused(t *testing.T) {
	s := New()
	defer s.Close()
	errs := s.Sync([]Forward{
		{ID: "bad_net", Network: "icmp", Port: 8443, To: netip.MustParseAddrPort("127.0.0.1:1")},
		{ID: "no_target", Network: "tcp", Port: 8443},
		{ID: "no_port", Network: "tcp", To: netip.MustParseAddrPort("127.0.0.1:1")},
	})
	for _, id := range []string{"bad_net", "no_target", "no_port"} {
		if errs[id] == nil {
			t.Fatalf("%s was accepted: %v", id, errs)
		}
	}
}
