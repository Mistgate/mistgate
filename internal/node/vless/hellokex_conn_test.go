package vless

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	xnet "github.com/xtls/xray-core/common/net"

	"github.com/mistgate/mistgate/internal/plugin"
)

// tcpPair is a loopback TCP connection: the client end and the server end, which tracker.track wraps.
func tcpPair(t *testing.T, tr *tcpConnTracker) (client net.Conn, server *trackedTCPConn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	raw := <-accepted
	server = tr.track(raw).(*trackedTCPConn)
	t.Cleanup(func() { _ = server.Close() })
	return client, server
}

func newKexTracker(xhttp bool) *tcpConnTracker {
	tr := newTCPConnTracker("in", 1)
	tr.counters = &inboundCounters{}
	tr.reality = true
	if xhttp {
		tr.byAddr = make(map[netip.AddrPort]*trackedTCPConn)
	}
	return tr
}

// The hello is read by whatever reads the connection, in whatever pieces it arrives and is read in; the bytes pass through
// unchanged, the connection is never read ahead, and the classification is the same however the hello is cut up.
func TestTrackedConnClassifiesHelloInAnyPieces(t *testing.T) {
	pq := buildHello([]share{shGrease, shMLKEM, shX25519}, true)
	classic := buildHello([]share{shGrease, shX25519}, true)
	for _, c := range []struct {
		name         string
		hello        []byte
		write, read  int
		want         helloKex
		wantHelloOne bool
	}{
		{"pq in one write", pq, len(pq), 4096, kexPQ, true},
		{"pq a byte at a time, read 3 bytes at a time", pq, 1, 3, kexPQ, true},
		{"pq in 700-byte segments", pq, 700, 512, kexPQ, true},
		{"classic in 1200-byte segments", classic, 1200, 100, kexClassic, true},
		{"not TLS", []byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"), 5, 4096, kexUnknown, false},
		{"TLS but no ClientHello", []byte{0x16, 0x03, 0x03, 0, 4, 0x02, 0, 0, 0}, 9, 4096, kexUnknown, false},
	} {
		tr := newKexTracker(false)
		client, server := tcpPair(t, tr)
		go func() {
			for off := 0; off < len(c.hello); off += c.write {
				_, _ = client.Write(c.hello[off:min(off+c.write, len(c.hello))])
				time.Sleep(time.Millisecond)
			}
		}()
		got := make([]byte, 0, len(c.hello))
		buf := make([]byte, c.read)
		_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
		for len(got) < len(c.hello) {
			n, err := server.Read(buf)
			got = append(got, buf[:n]...)
			if err != nil {
				t.Fatalf("%s: read: %v", c.name, err)
			}
		}
		if !bytes.Equal(got, c.hello) {
			t.Fatalf("%s: the bytes read differ from the bytes written", c.name)
		}
		if k := helloKex(server.kex.Load()); k != c.want || !server.peeked.Load() || server.hello != nil {
			t.Errorf("%s: kex = %d, peeked %v, buffer held %v; want %d, done, released", c.name, k, server.peeked.Load(), server.hello != nil, c.want)
		}
		if hellos := tr.counters.hellos.Load(); (hellos == 1) != c.wantHelloOne {
			t.Errorf("%s: hellos = %d", c.name, hellos)
		}
	}
}

// A hello that is longer than the buffer is read as far as the buffer goes; a connection that dies in the middle of its hello
// leaves no count and nothing held for good.
func TestTrackedConnLongAndCutHellos(t *testing.T) {
	many := make([]share, 0, 900)
	for i := 0; i < 900; i++ {
		many = append(many, shGrease)
	}
	long := buildHello(append(many, shMLKEM), true) // the ML-KEM share lies past 4096 bytes: not readable
	tr := newKexTracker(false)
	client, server := tcpPair(t, tr)
	go func() { _, _ = client.Write(long) }()
	_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(server, make([]byte, len(long))); err != nil {
		t.Fatal(err)
	}
	if k := helloKex(server.kex.Load()); k != kexUnknown || !server.peeked.Load() {
		t.Errorf("a %d-byte hello: kex = %d, want unknown and done", len(long), k)
	}

	tr = newKexTracker(false)
	client, server = tcpPair(t, tr)
	_, _ = client.Write(buildHello([]share{shMLKEM, shX25519}, true)[:300])
	_ = client.Close()
	_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.Copy(io.Discard, server)
	if k := helloKex(server.kex.Load()); k != kexPending {
		t.Errorf("a hello cut by the connection closing: kex = %d, want pending (the dispatch reads it as unknown)", k)
	}
	if server.kexKind() != kexUnknown {
		t.Error("kexKind of an unfinished hello is not unknown")
	}
}

// RAW: the first request of the connection counts its hello for its key, once; a second key on the same connection (mux)
// does not count it again.
func TestMarkSessionCountsKexOnce(t *testing.T) {
	tr := newKexTracker(false)
	client, server := tcpPair(t, tr)
	go func() { _, _ = client.Write(buildHello([]share{shMLKEM, shX25519}, true)) }()
	_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(server, make([]byte, len(buildHello([]share{shMLKEM, shX25519}, true)))); err != nil {
		t.Fatal(err)
	}
	a, b := &credState{id: "alice"}, &credState{id: "bob"}
	if !server.markSession(a) || server.markSession(b) {
		t.Fatal("markSession did not claim the connection for its first key only")
	}
	if a.helloPQ.Load() != 1 || a.helloClassic.Load() != 0 || b.helloPQ.Load() != 0 {
		t.Errorf("counts: alice pq %d classic %d, bob pq %d; want 1, 0, 0", a.helloPQ.Load(), a.helloClassic.Load(), b.helloPQ.Load())
	}
	if st := a.takeLinkStats(); st.HelloPQ != 1 || st.HelloClassic != 0 || st.HelloUnknown != 0 || st.Zero() {
		t.Errorf("link stat = %+v", st)
	}
	if st := a.takeLinkStats(); !st.Zero() {
		t.Errorf("a second take is not empty: %+v", st)
	}
}

// XHTTP: the session is found by the TCP peer's address; the first session of a connection counts, the next ones on it do
// not; an address nobody holds (a forwarded one) counts unknown; the address leaves the table with the connection.
func TestXHTTPSessionKexByAddress(t *testing.T) {
	tr := newKexTracker(true)
	client, server := tcpPair(t, tr)
	hello := buildHello([]share{shGrease, shX25519}, true)
	go func() { _, _ = client.Write(hello) }()
	_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(server, make([]byte, len(hello))); err != nil {
		t.Fatal(err)
	}
	peer := client.LocalAddr().(*net.TCPAddr)
	src := xnet.TCPDestination(xnet.IPAddress(peer.IP), xnet.Port(peer.Port))
	a := &credState{id: "alice"}
	tr.sessionKex(a, src)
	tr.sessionKex(a, src) // a second session on the same connection
	if a.helloClassic.Load() != 1 || a.helloPQ.Load() != 0 || a.helloUnknown.Load() != 0 {
		t.Errorf("two sessions on one connection: classic %d pq %d unknown %d; want one classic", a.helloClassic.Load(), a.helloPQ.Load(), a.helloUnknown.Load())
	}
	tr.sessionKex(a, xnet.TCPDestination(xnet.IPAddress(peer.IP), 0)) // forwarded: no port
	tr.sessionKex(a, xnet.TCPDestination(xnet.DomainAddress("example.test"), 443))
	if a.helloUnknown.Load() != 2 {
		t.Errorf("unknown = %d, want 2 for an address nobody holds", a.helloUnknown.Load())
	}
	_ = server.Close()
	if len(tr.byAddr) != 0 {
		t.Errorf("the closed connection is still in the address table")
	}
}

// kexTotals adds up the hello counts by "inbound/cred" until ready holds.
func kexTotals(t *testing.T, e *eng, ready func(map[string]plugin.LinkStat) bool) map[string]plugin.LinkStat {
	t.Helper()
	got := map[string]plugin.LinkStat{}
	deadline := time.Now().Add(10 * time.Second)
	for {
		collected, err := e.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range collected.Links {
			k := s.InboundID + "/" + s.CredID
			sum := got[k]
			sum.HelloPQ += s.HelloPQ
			sum.HelloClassic += s.HelloClassic
			sum.HelloUnknown += s.HelloUnknown
			sum.Connects += s.Connects
			got[k] = sum
		}
		if ready(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("hello counters = %+v, still not there", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Real Xray clients through REALITY, over RAW and over XHTTP: the chrome fingerprint of this core offers the post-quantum share
// first, the ios one never does, and each count lands on the key of its client and nowhere else.
func TestRealityHelloKexPerKey(t *testing.T) {
	for _, transport := range []string{"tcp", "xhttp"} {
		t.Run(transport, func(t *testing.T) {
			f := newRuntimeFixture(t, false)
			id := "kex-" + transport
			spec := f.spec(id, transport)
			alice := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b71")
			bob := testCredential("bob", "66ad4540-b58c-4ad2-9926-ea63445a9b72")
			if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{alice, bob}); err != nil {
				t.Fatal(err)
			}
			echo := startEchoServer(t)
			flow := ""
			if transport == "tcp" {
				flow = "xtls-rprx-vision"
			}
			for _, c := range []struct {
				cred plugin.UserCred
				fp   string
			}{{alice, "chrome"}, {bob, "ios"}} {
				client := newXrayClientFingerprint(t, c.fp, "localhost", "6ba85179", spec.Listen.Port, transport, c.cred.CredID, stringID(c.cred), flow, false, "")
				conn := client.dial(t, echo.Addr().String())
				if _, err := exchange(conn, []byte("hello")); err != nil {
					t.Fatal(err)
				}
				_ = conn.Close()
			}
			got := kexTotals(t, f.engine, func(m map[string]plugin.LinkStat) bool {
				return m[id+"/alice"].HelloPQ+m[id+"/alice"].HelloClassic+m[id+"/bob"].HelloPQ+m[id+"/bob"].HelloClassic >= 2
			})
			if a := got[id+"/alice"]; a.HelloPQ != 1 || a.HelloClassic != 0 || a.HelloUnknown != 0 {
				t.Errorf("alice (chrome) = %+v; want one pq hello", a)
			}
			if b := got[id+"/bob"]; b.HelloClassic != 1 || b.HelloPQ != 0 || b.HelloUnknown != 0 {
				t.Errorf("bob (ios) = %+v; want one classic hello", b)
			}
			if in := got[id+"/"]; in.HelloPQ|in.HelloClassic|in.HelloUnknown != 0 {
				t.Errorf("the inbound row carries hello counts: %+v", in)
			}
		})
	}
}
