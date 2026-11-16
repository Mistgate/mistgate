package trusttunnel

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apernet/quic-go"
	"github.com/apernet/quic-go/http3"
	nodeengine "github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

// countingPacketConn is a client's UDP socket that counts the bytes it sends and receives and can swallow what arrives.
type countingPacketConn struct {
	net.PacketConn
	sent, received atomic.Int64
	deaf           atomic.Bool
}

func (c *countingPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	c.sent.Add(int64(len(b)))
	return c.PacketConn.WriteTo(b, addr)
}

func (c *countingPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, addr, err := c.PacketConn.ReadFrom(b)
		if n > 0 {
			c.received.Add(int64(n))
		}
		if err != nil || !c.deaf.Load() {
			return n, addr, err
		}
	}
}

func listenClientUDP(t *testing.T) *countingPacketConn {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c := &countingPacketConn{PacketConn: conn}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// bigChainCerts serves the test certificate with filler behind it, so that the server's first flight is far larger than
// three times a client's first packet: the anti-amplification limit has something to cut.
type bigChainCerts struct{ testCertSource }

func (bigChainCerts) Acquire(ctx context.Context, id string, spec plugin.TLS) (nodeengine.Cert, error) {
	cert, err := testCertificate()
	if err != nil {
		return nodeengine.Cert{}, err
	}
	big := *cert
	big.Certificate = append([][]byte(nil), cert.Certificate...)
	for range 8 {
		filler := make([]byte, 1000)
		_, _ = rand.Read(filler)
		big.Certificate = append(big.Certificate, filler)
	}
	return nodeengine.Cert{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &big, nil },
		Info: func() nodeengine.CertInfo {
			return nodeengine.CertInfo{PinSHA256: "test-pin", NotAfter: cert.Leaf.NotAfter}
		},
	}, nil
}

func dialQUIC(t *testing.T, transport *quic.Transport, addr string, config *quic.Config) *quic.Conn {
	t.Helper()
	remote, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := transport.Dial(context.Background(), remote,
		&tls.Config{InsecureSkipVerify: true, ServerName: "example.com", NextProtos: []string{"h3"}}, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseWithError(0, "") })
	return conn
}

func clientOn(addr string, conn *quic.Conn) *testHTTP3Client {
	transport := &http3.Transport{}
	return &testHTTP3Client{addr: addr, transport: transport, conns: []*quic.Conn{conn}, clientConn: transport.NewClientConn(conn)}
}

// Before a client's address is validated, the server sends at most three times what it received: the UDP port is no
// amplifier for a spoofed source. The client here never answers, so the handshake never validates it.
func TestHTTP3NeverAnswersAnUnvalidatedAddressWithMoreThanThreeTimesItsBytes(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newH3HarnessEnv(t, []plugin.UserCred{alice.cred}, nil, timings{}, func(env *nodeengine.Env) { env.Certs = bigChainCerts{} })
	conn := listenClientUDP(t)
	conn.deaf.Store(true) // swallows the server's packets after counting them: no ACK, no handshake
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	remote, _ := net.ResolveUDPAddr("udp", h.addr)
	_, err := quic.Dial(ctx, conn, remote, &tls.Config{InsecureSkipVerify: true, ServerName: "example.com", NextProtos: []string{"h3"}}, nil)
	if err == nil {
		t.Fatal("a client that never read a packet completed the handshake")
	}
	sent, received := conn.sent.Load(), conn.received.Load()
	if received == 0 {
		t.Fatal("the server never answered the first packet: the test sent nothing it could judge")
	}
	if received > 3*sent {
		t.Fatalf("the server sent %d bytes to an address that sent %d: more than three times", received, sent)
	}
	if received < sent {
		t.Fatalf("the server's first flight was %d bytes against %d: too small to reach the limit the test is about", received, sent)
	}
}

// The path of a connection may change under it (a NAT rebinding, a phone moving from Wi-Fi to mobile): the session, its
// tunnels and its place in the accounting stay.
func TestHTTP3ConnectionSurvivesAClientAddressChange(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	backend := echoBackend(t)
	h := newH3Harness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, timings{})

	first, second := listenClientUDP(t), listenClientUDP(t)
	transport := &quic.Transport{Conn: first}
	t.Cleanup(func() { _ = transport.Close() })
	other := &quic.Transport{Conn: second}
	t.Cleanup(func() { _ = other.Close() })
	conn := dialQUIC(t, transport, h.addr, nil)
	client := clientOn(h.addr, conn)
	response, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	exchange(t, writer, response.Body, "before the change")

	path, err := conn.AddPath(other)
	if err != nil {
		t.Fatal(err)
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := path.Probe(probeCtx); err != nil {
		t.Fatalf("the server did not validate the client's new address: %v", err)
	}
	if err := path.Switch(); err != nil {
		t.Fatal(err)
	}
	sentBefore := second.sent.Load()
	exchange(t, writer, response.Body, "after the change")
	if second.sent.Load() == sentBefore {
		t.Fatal("the test never moved the connection to the new socket")
	}
	if conn.Context().Err() != nil {
		t.Fatalf("the connection ended with the address change: %v", context.Cause(conn.Context()))
	}
	cs := h.in.idx.Load().byID["alice"]
	if n := credentialConnectionCount(cs); n != 1 {
		t.Fatalf("the credential has %d connections after the change, want the same one", n)
	}
	if n := authenticatedConnectionCount(h.in); n != 1 {
		t.Fatalf("the inbound has %d authenticated connections after the change, want 1", n)
	}
	_ = writer.Close()
	_, _ = io.Copy(io.Discard, response.Body)
}

// A quiet tunnel outlives the client's own idle time: the server pings, and the client's answer keeps both ends' idle
// timers (and the NAT's) fresh.
func TestHTTP3ServerKeepsAQuietConnectionAlive(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	backend := echoBackend(t)
	h := newH3Harness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, timings{})
	udp := listenClientUDP(t)
	transport := &quic.Transport{Conn: udp}
	t.Cleanup(func() { _ = transport.Close() })
	conn := dialQUIC(t, transport, h.addr, &quic.Config{MaxIdleTimeout: 5 * time.Second}) // the shortest idle time a quic-go peer honours; the client sends nothing on its own
	client := clientOn(h.addr, conn)
	response, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	exchange(t, writer, response.Body, "hello")
	sentBefore := udp.sent.Load()
	time.Sleep(6 * time.Second) // longer than the client's idle time, in silence
	if conn.Context().Err() != nil {
		t.Fatalf("the quiet connection ended: %v", context.Cause(conn.Context()))
	}
	if udp.sent.Load() == sentBefore {
		t.Fatal("the client sent nothing in the quiet time: the server's pings never reached it")
	}
	exchange(t, writer, response.Body, "still here")
	_ = writer.Close()
	_, _ = io.Copy(io.Discard, response.Body)
}

// The engine's Close leaves nothing behind: the TCP and the UDP port are free again, and the goroutines that served them
// (the QUIC listener, connections, streams) are gone.
func TestHTTP3EngineCloseFreesBothPortsAndGoroutines(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	backend := echoBackend(t)
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	h := newH3Harness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, timings{})
	anonymous := newHTTP3Client(t, h.addr)
	resp := anonymous.get(t)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	client := newHTTP3Client(t, h.addr)
	tunnel, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	exchange(t, writer, tunnel.Body, "open when the engine stops")
	udpClient := newHTTP3Client(t, h.addr)
	udpResp, udpWriter := udpClient.connect(t, "_udp2", authValue(t, alice), true)
	writeUDPFrame(t, udpWriter, udpFrame("192.0.2.1", 1000, "192.0.2.10", 443, "x"))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := h.e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if udp, err := net.ListenPacket("udp", h.addr); err != nil {
		t.Fatalf("the UDP port is still held after Close: %v", err)
	} else {
		_ = udp.Close()
	}
	if tcp, err := net.Listen("tcp", h.addr); err != nil {
		t.Fatalf("the TCP port is still held after Close: %v", err)
	} else {
		_ = tcp.Close()
	}
	_ = writer.Close()
	_ = udpWriter.Close()
	for _, c := range []*testHTTP3Client{anonymous, client, udpClient} {
		c.kill()
		c.close()
	}
	_ = tunnel.Body.Close()
	_ = udpResp.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > baseline+2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > baseline+2 {
		t.Fatalf("goroutines after Close = %d, baseline %d", got, baseline)
	}
}

// Changing only the credentials leaves the QUIC listener and its connections alone; only a new spec_hash restarts it.
func TestHTTP3CredentialChangeKeepsTheListenerAndItsConnections(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	bob := makeCredential(t, "bob", "bob", "person-bob", 0, 0)
	backend := echoBackend(t)
	h := newH3Harness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, timings{})
	old := h.in
	server, listener := old.h3Server, old.h3Ln
	client := newHTTP3Client(t, h.addr)
	client.useSingleConnection(t)
	response, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	exchange(t, writer, response.Body, "before")

	report, err := h.e.Apply(context.Background(), h.spec, []plugin.UserCred{alice.cred, bob.cred})
	if err != nil || report.Restarted {
		t.Fatalf("credential-only Apply = %+v, %v", report, err)
	}
	if h.e.inbounds[h.spec.ID] != old || old.h3Server != server || old.h3Ln != listener {
		t.Fatal("a credential-only change replaced the inbound or its QUIC listener")
	}
	exchange(t, writer, response.Body, "after")
	if conn := client.firstConn(); conn.Context().Err() != nil {
		t.Fatalf("the credential change closed a connection: %v", context.Cause(conn.Context()))
	}
	other := newHTTP3Client(t, h.addr)
	if got := other.status(t, "_check", authValue(t, bob)); got.StatusCode != http.StatusOK {
		t.Fatalf("the new credential's _check over HTTP/3 = %d", got.StatusCode)
	}
	_ = writer.Close()
}

// A connection that an anonymous flood pushes out is closed the way a web server closes one: H3_NO_ERROR from the peer, no
// reason, nothing to tell it from a polite shutdown.
func TestHTTP3EvictedAnonymousConnectionIsClosedWithNoError(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newH3Harness(t, []plugin.UserCred{alice.cred}, nil, timings{})
	var clients []*testHTTP3Client
	for range 9 { // one more than a source may hold
		c := newHTTP3Client(t, h.addr)
		resp := c.get(t)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		clients = append(clients, c)
	}
	first := clients[0].firstConn()
	waitFor(t, time.Second, func() bool { return first.Context().Err() != nil })
	var closed *quic.ApplicationError
	if !errors.As(context.Cause(first.Context()), &closed) || closed.ErrorCode != quic.ApplicationErrorCode(http3.ErrCodeNoError) ||
		!closed.Remote || closed.ErrorMessage != "" {
		t.Fatalf("evicted connection ended with %v, want a remote H3_NO_ERROR without a reason", context.Cause(first.Context()))
	}
}

// While the anonymous set is crowded, a new handshake proves its address with a Retry first and still succeeds; with room
// to spare nobody pays the extra round trip.
func TestHTTP3RetryOnlyWhileTheAnonymousSetIsCrowded(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newH3Harness(t, []plugin.UserCred{alice.cred}, nil, timings{})
	client := newHTTP3Client(t, h.addr)
	resp := client.get(t)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if got := h.in.h3Retries.Load(); got != 0 {
		t.Fatalf("a handshake with room to spare was sent %d Retries", got)
	}
	for i := range 20 { // 20 connections of other sources: more than half of the set
		addr := &net.UDPAddr{IP: net.ParseIP("192.0.2." + strconv.Itoa(10+i%4)), Port: 40000 + i}
		h.in.h3Anonymous.Add(new(int), addr, func() {})
	}
	crowded := newHTTP3Client(t, h.addr)
	resp = crowded.get(t)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a handshake that went through a Retry got %d", resp.StatusCode)
	}
	if got := h.in.h3Retries.Load(); got == 0 {
		t.Fatal("a crowded anonymous set did not make the server ask for address validation")
	}
}
