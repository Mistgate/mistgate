package egress

import (
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// TestMain keeps the outgoing UDP sockets on loopback: a test binary that holds a wildcard socket makes Windows ask for a
// firewall rule on each run.
func TestMain(m *testing.M) {
	BindIP4 = net.IPv4(127, 0, 0, 1)
	os.Exit(m.Run())
}

// fakeDNS answers A queries for names in zone, NXDOMAIN for the rest, and counts queries.
func fakeDNS(t *testing.T, zone map[string]string) (addr string, queries *atomic.Int64) {
	t.Helper()
	return fakeDNSDrop(t, zone, false)
}

// fakeDNSDrop is fakeDNS that, with dropFirst, ignores the first query of each (name, type): a lost datagram.
func fakeDNSDrop(t *testing.T, zone map[string]string, dropFirst bool) (addr string, queries *atomic.Int64) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	queries = new(atomic.Int64)
	go func() {
		seen := map[string]bool{}
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var req dnsmessage.Message
			if req.Unpack(buf[:n]) != nil || len(req.Questions) != 1 {
				continue
			}
			q := req.Questions[0]
			queries.Add(1)
			if k := q.Name.String() + q.Type.String(); dropFirst && !seen[k] {
				seen[k] = true
				continue
			}
			resp := dnsmessage.Message{
				Header:    dnsmessage.Header{ID: req.ID, Response: true, Authoritative: true, RecursionAvailable: true},
				Questions: req.Questions,
			}
			if ip, ok := zone[q.Name.String()]; ok && q.Type == dnsmessage.TypeA {
				a := net.ParseIP(ip).To4()
				resp.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
					Body:   &dnsmessage.AResource{A: [4]byte(a)},
				}}
			} else if !ok {
				resp.Header.RCode = dnsmessage.RCodeNameError
			}
			out, _ := resp.Pack()
			pc.WriteTo(out, from)
		}
	}()
	return pc.LocalAddr().String(), queries
}

// One lost UDP datagram must cost about a second, not the whole per-server budget, and a lone resolver must survive it.
func TestLookupViaSurvivesLostDatagram(t *testing.T) {
	dnsAddr, queries := fakeDNSDrop(t, map[string]string{"lossy.example.test.": "127.0.0.1"}, true)
	start := time.Now()
	ips, err := lookupVia([]string{dnsAddr}, "lossy.example.test")
	if err != nil || len(ips) == 0 {
		t.Fatalf("lookup after a lost datagram: %v %v", ips, err)
	}
	if d := time.Since(start); d >= perServerTimeout {
		t.Fatalf("took %v, want a retry well inside %v", d, perServerTimeout)
	}
	if queries.Load() < 3 { // A and AAAA lost, then at least one of them again
		t.Fatalf("queries = %d, the retry never happened", queries.Load())
	}
}

// "No such host" stays final: no retry, no waiting.
func TestLookupViaNXDomainIsFinal(t *testing.T) {
	dnsAddr, queries := fakeDNS(t, nil)
	start := time.Now()
	if _, err := lookupVia([]string{dnsAddr}, "nope.example.test"); err == nil || time.Since(start) > attemptTimeout/2 {
		t.Fatalf("NXDOMAIN: err=%v after %v", err, time.Since(start))
	}
	if n := queries.Load(); n > 2 { // A + AAAA, once
		t.Fatalf("queries = %d, NXDOMAIN was retried", n)
	}
}

func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

func roundTrip(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != msg {
		t.Fatalf("echo: %q %v", got, err)
	}
}

func TestResolvesWithConfiguredServers(t *testing.T) {
	dnsAddr, queries := fakeDNS(t, map[string]string{"app.example.test.": "127.0.0.1"})
	echo := echoServer(t)
	_, port, _ := net.SplitHostPort(echo)

	// The first server is dead (nothing listens), the second answers: order and failover.
	dead, _ := net.ListenPacket("udp", "127.0.0.1:0")
	deadAddr := dead.LocalAddr().String()
	dead.Close()
	e := New(func() []string { return []string{deadAddr, dnsAddr} }, AllowPrivate())

	c, err := e.TCP("app.example.test:" + port)
	if err != nil {
		t.Fatalf("dial by name: %v", err)
	}
	roundTrip(t, c, "hello")
	if queries.Load() == 0 {
		t.Fatal("the configured resolver was never asked")
	}

	// Second dial is served from the cache.
	before := queries.Load()
	c, err = e.TCP("app.example.test:" + port)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, c, "again")
	if queries.Load() != before {
		t.Fatal("cache miss on the second dial")
	}

	live := New(func() []string { return []string{dnsAddr} }, AllowPrivate())
	if _, err := live.TCP("missing.example.test:" + port); err == nil {
		t.Fatal("NXDOMAIN must fail the dial")
	}
}

func TestUDPResolvesPerSessionNotPerDatagram(t *testing.T) {
	dnsAddr, queries := fakeDNS(t, map[string]string{"udp.example.test.": "127.0.0.1"})
	sink, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	_, port, _ := net.SplitHostPort(sink.LocalAddr().String())

	e := New(func() []string { return []string{dnsAddr} }, AllowPrivate())
	addr := "udp.example.test:" + port
	if err := e.CheckUDP(addr); err != nil {
		t.Fatal(err)
	}
	c, err := e.UDP(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i := 0; i < 20; i++ {
		if _, err := c.WriteTo([]byte("ping"), addr); err != nil {
			t.Fatal(err)
		}
	}
	buf := make([]byte, 16)
	sink.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, _, err := sink.ReadFrom(buf); err != nil || string(buf[:n]) != "ping" {
		t.Fatalf("sink got %q, %v", buf[:n], err)
	}
	// One lookup for the name (A, plus AAAA): far fewer than one per datagram.
	if q := queries.Load(); q == 0 || q > 4 {
		t.Fatalf("expected a handful of DNS queries for 20 datagrams, got %d", q)
	}
}

func TestBlocksNonPublicDestinations(t *testing.T) {
	dnsAddr, _ := fakeDNS(t, map[string]string{"sneaky.example.test.": "127.0.0.1", "meta.example.test.": "169.254.169.254"})
	echo := echoServer(t)
	_, port, _ := net.SplitHostPort(echo)
	e := New(func() []string { return []string{dnsAddr} })

	for _, addr := range []string{
		echo,                          // literal loopback
		"sneaky.example.test:" + port, // name that resolves to loopback
		"meta.example.test:80",        // link-local / cloud metadata
		"10.1.2.3:80", "192.168.0.1:80", "100.64.0.1:80", "[::1]:80", "[fe80::1]:80", "[fd00::1]:80", "0.0.0.0:80",
		"224.0.0.1:80", "[::ffff:127.0.0.1]:80",
	} {
		if _, err := e.TCP(addr); !errors.Is(err, ErrBlocked) {
			t.Errorf("TCP %s: want ErrBlocked, got %v", addr, err)
		}
		if err := e.CheckUDP(addr); !errors.Is(err, ErrBlocked) {
			t.Errorf("CheckUDP %s: want ErrBlocked, got %v", addr, err)
		}
	}
}

func TestVetAllowsPublic(t *testing.T) {
	for _, s := range []string{"8.8.8.8", "77.88.8.8", "2001:4860:4860::8888", "1.1.1.1"} {
		if vet(net.ParseIP(s)) == nil {
			t.Errorf("%s must be allowed", s)
		}
	}
}
