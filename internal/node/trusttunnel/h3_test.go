package trusttunnel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apernet/quic-go"
	"github.com/apernet/quic-go/http3"
	"github.com/mistgate/mistgate/internal/node/egress"
	nodeengine "github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
	wire "github.com/mistgate/mistgate/internal/trusttunnel"
)

type testHTTP3Client struct {
	transport  *http3.Transport
	addr       string
	mu         sync.Mutex
	conns      []*quic.Conn
	clientConn *http3.ClientConn
}

func newH3Harness(t *testing.T, creds []plugin.UserCred, outbound *testEgress, ts timings) *testHarness {
	t.Helper()
	return newH3HarnessEnv(t, creds, outbound, ts, nil)
}

func newH3HarnessEnv(t *testing.T, creds []plugin.UserCred, outbound *testEgress, ts timings, mutate func(*nodeengine.Env)) *testHarness {
	t.Helper()
	h := newHarnessEnv(t, creds, outbound, ts, mutate)
	spec := h.spec
	spec.Settings = []byte(`{"h3":true}`)
	if _, err := h.e.Apply(context.Background(), spec, creds); err != nil {
		t.Fatalf("apply h3 spec: %v", err)
	}
	h.spec, h.in = spec, h.e.inbounds[spec.ID]
	return h
}

func newHTTP3Client(t *testing.T, addr string) *testHTTP3Client {
	t.Helper()
	c := &testHTTP3Client{addr: addr}
	c.transport = &http3.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: "example.com"},
		Dial: func(ctx context.Context, _ string, tlsConfig *tls.Config, quicConfig *quic.Config) (*quic.Conn, error) {
			conn, err := quic.DialAddr(ctx, addr, tlsConfig, quicConfig)
			if err == nil {
				c.mu.Lock()
				c.conns = append(c.conns, conn)
				c.mu.Unlock()
			}
			return conn, err
		},
	}
	t.Cleanup(func() {
		_ = c.transport.Close()
		c.mu.Lock()
		conns := append([]*quic.Conn(nil), c.conns...)
		c.mu.Unlock()
		for _, conn := range conns {
			_ = conn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeNoError), "test complete")
		}
	})
	return c
}

func (c *testHTTP3Client) useSingleConnection(t *testing.T) {
	t.Helper()
	conn, err := quic.DialAddr(context.Background(), c.addr, &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "example.com",
		NextProtos:         []string{"h3"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.conns = append(c.conns, conn)
	c.clientConn = c.transport.NewClientConn(conn)
	c.mu.Unlock()
}

func (c *testHTTP3Client) roundTrip(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	clientConn := c.clientConn
	c.mu.Unlock()
	if clientConn != nil {
		return clientConn.RoundTrip(req)
	}
	return c.transport.RoundTrip(req)
}

func (c *testHTTP3Client) connCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.conns)
}

func (c *testHTTP3Client) firstConn() *quic.Conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.conns) == 0 {
		return nil
	}
	return c.conns[0]
}

func (c *testHTTP3Client) connect(t *testing.T, authority, auth string, streaming bool) (*http.Response, *io.PipeWriter) {
	t.Helper()
	var body io.ReadCloser = http.NoBody
	var writer *io.PipeWriter
	length := int64(0)
	if streaming {
		reader, pipeWriter := io.Pipe()
		body, writer, length = reader, pipeWriter, -1
	}
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Scheme: "https", Host: authority},
		Host:   authority,
		Header: make(http.Header),
		Body:   body,
	}
	req.ContentLength = length
	if auth != "" {
		req.Header.Set("Proxy-Authorization", auth)
	}
	resp, err := c.roundTrip(req)
	if err != nil {
		if writer != nil {
			_ = writer.Close()
		}
		t.Fatalf("HTTP/3 CONNECT failed: %v", err)
	}
	return resp, writer
}

// kill drops every QUIC connection of the client at once, as a vanished phone does (the server learns of it from the
// closing packet or from the idle timeout).
func (c *testHTTP3Client) kill() {
	c.mu.Lock()
	conns := append([]*quic.Conn(nil), c.conns...)
	c.mu.Unlock()
	for _, conn := range conns {
		_ = conn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeNoError), "")
	}
}

func (c *testHTTP3Client) close() { _ = c.transport.Close() }

func (c *testHTTP3Client) get(t *testing.T) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	resp, err := c.roundTrip(req)
	if err != nil {
		t.Fatalf("HTTP/3 GET failed: %v", err)
	}
	return resp
}

func (c *testHTTP3Client) status(t *testing.T, authority, auth string) *http.Response {
	t.Helper()
	resp, _ := c.connect(t, authority, auth, false)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp
}

func openUDP3(t *testing.T, h *testHarness, cred credentialFixture) (*testHTTP3Client, io.ReadCloser, *io.PipeWriter) {
	t.Helper()
	client := newHTTP3Client(t, h.addr)
	response, writer := client.connect(t, "_udp2", authValue(t, cred), true)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTP/3 _udp2 status = %d", response.StatusCode)
	}
	return client, response.Body, writer
}

func TestHTTP3TCPConnectEchoAndCollect(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	backend := echoBackend(t)
	ts := defaultTimings
	ts.eofGrace = 20 * time.Millisecond
	h := newH3Harness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, ts)
	client := newHTTP3Client(t, h.addr)
	response, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HTTP/3 CONNECT status = %d", response.StatusCode)
	}
	payload := "http3 shares the TCP tunnel"
	if _, err := io.WriteString(writer, payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(response.Body)
	if err != nil || string(got) != payload {
		t.Fatalf("HTTP/3 echo = %q, %v", got, err)
	}
	_ = response.Body.Close()
	stats := collect(t, h.e)
	if up, down := trafficFor(stats, "alice"); up != uint64(len(payload)) || down != uint64(len(payload)) {
		t.Fatalf("HTTP/3 traffic up/down = %d/%d, want %d/%d", up, down, len(payload), len(payload))
	}
}

func TestHTTP3UDPConnectEchoAndCollect(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	var sockets socketLog
	h := newH3Harness(t, []plugin.UserCred{alice.cred}, sockets.egress(nil, nil), timings{})
	_, body, writer := openUDP3(t, h, alice)
	frame := udpFrame("192.0.2.1", 1111, "192.0.2.10", 443, "udp over h3")
	writeUDPFrame(t, writer, frame)
	reply, err := readUDPFrame(body)
	if err != nil || reply.Source != frame.Target || reply.Target != frame.Source || string(reply.Payload) != string(frame.Payload) {
		t.Fatalf("HTTP/3 UDP reply = %+v, %v, want the echoed frame", reply, err)
	}
	_ = writer.Close()
	waitFor(t, time.Second, func() bool {
		return h.in.idx.Load().byID["alice"].up.Load() == uint64(len(frame.Payload)) &&
			h.in.idx.Load().byID["alice"].down.Load() == uint64(len(frame.Payload))
	})
	stats := collect(t, h.e)
	if up, down := trafficFor(stats, "alice"); up != uint64(len(frame.Payload)) || down != uint64(len(frame.Payload)) {
		t.Fatalf("HTTP/3 UDP traffic up/down = %d/%d, want %d/%d", up, down, len(frame.Payload), len(frame.Payload))
	}
}

func TestHTTP3WrongAndUnknownCredentialsUseTheMasquerade(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newH3Harness(t, []plugin.UserCred{alice.cred}, nil, timings{})
	client := newHTTP3Client(t, h.addr)

	badPassword, err := wire.BuildProxyAuthorization(alice.username, "wrong-password")
	if err != nil {
		t.Fatal(err)
	}
	unknownUser, err := wire.BuildProxyAuthorization("bob", "password")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		auth string
	}{
		{name: "wrong password", auth: badPassword},
		{name: "unknown user", auth: unknownUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := client.connect(t, "example.com:80", tc.auth, false)
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil || resp.StatusCode != http.StatusNotFound || resp.Header.Get("X-Decoy") != "same" || string(body) != "decoy site" {
				t.Fatalf("masquerade = %d %v %q, %v", resp.StatusCode, resp.Header, body, readErr)
			}
			if got := resp.Header.Get("Alt-Svc"); got != "" {
				t.Fatalf("HTTP/3 masquerade advertised Alt-Svc %q", got)
			}
		})
	}
}

func TestHTTP3ErrorsKeepTheTunnelWarningHeaders(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	outbound := &testEgress{blocked: map[string]error{"example.com:80": egress.ErrBlocked}}
	h := newH3Harness(t, []plugin.UserCred{alice.cred}, outbound, timings{})
	client := newHTTP3Client(t, h.addr)
	resp, _ := client.connect(t, "example.com:80", authValue(t, alice), false)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || resp.Header.Get("X-Warning") != "310 - Destination address is blocked" ||
		resp.Header.Get("X-Adguard-Vpn-Error") != "example.com" || resp.Header.Get("Content-Length") != "0" {
		t.Fatalf("HTTP/3 blocked-destination response = %d %v, want the shared 310 warning", resp.StatusCode, resp.Header)
	}
	resp, _ = client.connect(t, "_icmp", authValue(t, alice), false)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || resp.Header.Get("X-Warning") != "300 - Connection failed for some reason" {
		t.Fatalf("HTTP/3 _icmp response = %d %v, want the shared 300 warning", resp.StatusCode, resp.Header)
	}
}

func TestHTTP3KickAndCredentialRemovalResetOnlyMatchingStreams(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	bob := makeCredential(t, "bob", "bob", "person-bob", 0, 0)
	backend := echoBackend(t)
	h := newH3Harness(t, []plugin.UserCred{alice.cred, bob.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, timings{})
	shared := newHTTP3Client(t, h.addr)
	shared.useSingleConnection(t)
	other := newHTTP3Client(t, h.addr)

	aliceResponse, aliceWriter := shared.connect(t, "example.com:80", authValue(t, alice), true)
	bobSharedResponse, bobSharedWriter := shared.connect(t, "example.com:80", authValue(t, bob), true)
	bobOtherResponse, bobOtherWriter := other.connect(t, "example.com:80", authValue(t, bob), true)
	for _, pair := range []struct {
		writer *io.PipeWriter
		body   io.Reader
		text   string
	}{
		{aliceWriter, aliceResponse.Body, "alice before kick"},
		{bobSharedWriter, bobSharedResponse.Body, "bob shared before kick"},
		{bobOtherWriter, bobOtherResponse.Body, "bob other before kick"},
	} {
		exchange(t, pair.writer, pair.body, pair.text)
	}
	sharedConn := shared.firstConn()
	if sharedConn == nil || shared.connCount() != 1 {
		t.Fatalf("shared HTTP/3 transport has %d QUIC connections, want 1", shared.connCount())
	}
	start := time.Now()
	if affected, err := h.e.Kick(context.Background(), []string{"alice"}); err != nil || affected != 1 {
		t.Fatalf("HTTP/3 Kick = %d, %v", affected, err)
	}
	streamErr, ended := streamErrorWithin(aliceResponse.Body, time.Second)
	if !ended || streamErr == nil {
		t.Fatalf("Alice's HTTP/3 stream was not reset within 1 s (elapsed %s)", time.Since(start))
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Alice's HTTP/3 stream reset after %s, want <=1 s", elapsed)
	}
	if sharedConn.Context().Err() != nil {
		t.Fatal("Kick closed the shared QUIC connection that Bob still uses")
	}
	exchange(t, bobSharedWriter, bobSharedResponse.Body, "bob shared after kick")
	exchange(t, bobOtherWriter, bobOtherResponse.Body, "bob other after kick")
	if resp := shared.status(t, "_check", authValue(t, alice)); resp.StatusCode != http.StatusNotFound || resp.Header.Get("X-Decoy") != "same" {
		t.Fatalf("Alice's new CONNECT on the same QUIC connection = %d %v, want masquerade", resp.StatusCode, resp.Header)
	}
	if shared.connCount() != 1 || sharedConn.Context().Err() != nil {
		t.Fatal("the denied CONNECT replaced or closed the shared QUIC connection")
	}

	// Removing Alice also resets her active stream while Bob remains usable on this connection and on another one.
	aliceResponse2, aliceWriter2 := shared.connect(t, "example.com:80", authValue(t, alice), true)
	if aliceResponse2.StatusCode != http.StatusNotFound {
		t.Fatalf("Alice reconnected on a kicked QUIC connection: %d", aliceResponse2.StatusCode)
	}
	_, _ = io.Copy(io.Discard, aliceResponse2.Body)
	_ = aliceResponse2.Body.Close()
	_ = aliceWriter2.Close()

	removalShared := newHTTP3Client(t, h.addr)
	removalShared.useSingleConnection(t)
	removedAliceResponse, removedAliceWriter := removalShared.connect(t, "example.com:80", authValue(t, alice), true)
	if removedAliceResponse.StatusCode != http.StatusOK {
		t.Fatalf("Alice on a fresh QUIC connection before removal = %d", removedAliceResponse.StatusCode)
	}
	bobRemovalResponse, bobRemovalWriter := removalShared.connect(t, "example.com:80", authValue(t, bob), true)
	if bobRemovalResponse.StatusCode != http.StatusOK {
		t.Fatalf("Bob on Alice's QUIC connection before removal = %d", bobRemovalResponse.StatusCode)
	}
	exchange(t, removedAliceWriter, removedAliceResponse.Body, "alice before removal")
	exchange(t, bobRemovalWriter, bobRemovalResponse.Body, "bob before Alice removal")
	removalConn := removalShared.firstConn()
	start = time.Now()
	if _, err := h.e.Apply(context.Background(), h.spec, []plugin.UserCred{bob.cred}); err != nil {
		t.Fatal(err)
	}
	streamErr, ended = streamErrorWithin(removedAliceResponse.Body, time.Second)
	if !ended || streamErr == nil {
		t.Fatalf("removed Alice's HTTP/3 stream was not reset within 1 s (elapsed %s)", time.Since(start))
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("removed Alice's HTTP/3 stream reset after %s, want <=1 s", elapsed)
	}
	if removalConn == nil || removalConn.Context().Err() != nil {
		t.Fatal("removing Alice closed the QUIC connection that Bob still uses")
	}
	if resp := removalShared.status(t, "_check", authValue(t, alice)); resp.StatusCode != http.StatusNotFound || resp.Header.Get("X-Decoy") != "same" {
		t.Fatalf("removed Alice's new CONNECT on the same QUIC connection = %d %v, want masquerade", resp.StatusCode, resp.Header)
	}
	exchange(t, bobRemovalWriter, bobRemovalResponse.Body, "bob after Alice removal")
	exchange(t, bobSharedWriter, bobSharedResponse.Body, "bob shared after Alice removal")
	exchange(t, bobOtherWriter, bobOtherResponse.Body, "bob other after Alice removal")
	_ = aliceWriter.Close()
	_ = removedAliceWriter.Close()
	_ = bobRemovalWriter.Close()
	_ = bobSharedWriter.Close()
	_ = bobOtherWriter.Close()
}

func TestHTTP3ConnectionLimitCountsTCPAndQUICTogether(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newH3Harness(t, []plugin.UserCred{alice.cred}, nil, timings{})
	h2 := make([]*testHTTP2Client, 8)
	h3 := make([]*testHTTP3Client, 8)
	for i := range h2 {
		h2[i] = newHTTP2Client(t, h.addr)
		if got := statusOf(t, h2[i], "_check", authValue(t, alice)); got.StatusCode != http.StatusOK {
			t.Fatalf("TCP connection %d status = %d", i+1, got.StatusCode)
		}
		h3[i] = newHTTP3Client(t, h.addr)
		if got := h3[i].status(t, "_check", authValue(t, alice)); got.StatusCode != http.StatusOK {
			t.Fatalf("QUIC connection %d status = %d", i+1, got.StatusCode)
		}
	}
	cs := h.in.idx.Load().byID["alice"]
	cs.mu.Lock()
	count := len(cs.connections)
	cs.mu.Unlock()
	if count != maxCredentialConnections {
		t.Fatalf("mixed TCP+QUIC session count = %d, want %d", count, maxCredentialConnections)
	}
	deniedH3 := newHTTP3Client(t, h.addr)
	if got := deniedH3.status(t, "_check", authValue(t, alice)); got.StatusCode != http.StatusNotFound {
		t.Fatalf("17th mixed connection over QUIC = %d, want masquerade", got.StatusCode)
	}
	deniedH2 := newHTTP2Client(t, h.addr)
	if got := statusOf(t, deniedH2, "_check", authValue(t, alice)); got.StatusCode != http.StatusNotFound {
		t.Fatalf("17th mixed connection over TCP = %d, want masquerade", got.StatusCode)
	}
}

func TestHTTP3AnonymousConnectionLimitEvictsOldestAndSparesAuthenticated(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newH3Harness(t, []plugin.UserCred{alice.cred}, nil, timings{})
	clients := make([]*testHTTP3Client, 10)
	for i := range clients {
		clients[i] = newHTTP3Client(t, h.addr)
		resp := clients[i].get(t)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	for i := range clients {
		conn := clients[i].firstConn()
		if conn == nil {
			t.Fatalf("anonymous client %d never opened QUIC", i)
		}
		if i < 2 {
			waitFor(t, time.Second, func() bool { return conn.Context().Err() != nil })
		} else if conn.Context().Err() != nil {
			t.Fatalf("newer anonymous QUIC connection %d was evicted", i)
		}
	}

	authenticated := newHTTP3Client(t, h.addr)
	if got := authenticated.status(t, "_check", authValue(t, alice)); got.StatusCode != http.StatusOK {
		t.Fatalf("authenticated _check = %d", got.StatusCode)
	}
	proven := authenticated.firstConn()
	if proven == nil {
		t.Fatal("authenticated client did not open QUIC")
	}
	flood := make([]*testHTTP3Client, 11)
	for i := range flood {
		flood[i] = newHTTP3Client(t, h.addr)
		resp := flood[i].get(t)
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if proven.Context().Err() != nil {
		t.Fatal("anonymous QUIC flood evicted a connection that authenticated")
	}
	for i, client := range flood {
		conn := client.firstConn()
		if conn == nil {
			t.Fatalf("anonymous flood client %d never opened QUIC", i)
		}
		if i < 3 {
			waitFor(t, time.Second, func() bool { return conn.Context().Err() != nil })
		} else if conn.Context().Err() != nil {
			t.Fatalf("newer anonymous connection %d was evicted ahead of an older one", i)
		}
	}
}

func TestHTTP3AltSvcIsOnlyOnTCPMasqueradeWhenEnabled(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred}, nil, timings{})
	tcp := newHTTP2Client(t, h.addr)
	if got := statusOf(t, tcp, "example.com:80", ""); got.Header.Get("Alt-Svc") != "" {
		t.Fatalf("HTTP/2 masquerade advertised Alt-Svc with h3 disabled: %q", got.Header.Get("Alt-Svc"))
	}
	spec := h.spec
	spec.Settings = []byte(`{"h3":true}`)
	if _, err := h.e.Apply(context.Background(), spec, []plugin.UserCred{alice.cred}); err != nil {
		t.Fatal(err)
	}
	h.spec, h.in = spec, h.e.inbounds[spec.ID]

	tcp = newHTTP2Client(t, h.addr)
	resp := statusOf(t, tcp, "example.com:80", "")
	want := `h3=":` + strconv.Itoa(int(spec.Listen.Port)) + `"; ma=2592000`
	if got := resp.Header.Get("Alt-Svc"); got != want {
		t.Fatalf("TCP masquerade Alt-Svc = %q, want %q", got, want)
	}
	quicClient := newHTTP3Client(t, h.addr)
	resp = quicClient.get(t)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if got := resp.Header.Get("Alt-Svc"); got != "" {
		t.Fatalf("HTTP/3 masquerade advertised Alt-Svc %q", got)
	}
}

func TestHTTP3LimitsAndSpecRestartReleaseUDPPort(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newH3Harness(t, []plugin.UserCred{alice.cred}, nil, timings{})
	old := h.in
	if old.h3Server == nil || old.h3Ln == nil {
		t.Fatal("h3:true did not start the HTTP/3 server and UDP listener")
	}
	if old.h3Ln.LocalAddr().String() != h.addr {
		t.Fatalf("HTTP/3 UDP listener = %s, want TCP listener port %s", old.h3Ln.LocalAddr(), h.addr)
	}
	config := old.h3Server.QUICConfig
	if config.MaxIncomingStreams != 250 || config.MaxIdleTimeout != max(old.e.timing.serverIdle, old.e.timing.streamIdle) ||
		config.HandshakeIdleTimeout != old.e.timing.readHeader || config.InitialStreamReceiveWindow != h3InitialStreamWindow ||
		config.MaxStreamReceiveWindow != h3MaxStreamWindow || config.InitialConnectionReceiveWindow != h3InitialConnectionWindow ||
		config.MaxConnectionReceiveWindow != h3MaxConnectionWindow || config.MaxIncomingUniStreams != maxH3IncomingUniStreams ||
		old.h3Server.IdleTimeout != old.e.timing.connectionIdle {
		t.Fatalf("HTTP/3 QUIC limits = %+v", config)
	}
	if got := old.h3Server.TLSConfig.NextProtos; len(got) != 1 || got[0] != "h3" || old.h3Server.TLSConfig.MinVersion != tls.VersionTLS13 {
		t.Fatalf("HTTP/3 TLS config = ALPN %v, minimum %v; want h3 and TLS 1.3", got, old.h3Server.TLSConfig.MinVersion)
	}

	spec := h.spec
	spec.Settings = []byte(`{"h3":false}`)
	if _, err := h.e.Apply(context.Background(), spec, []plugin.UserCred{alice.cred}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-old.h3ServeDone:
	case <-time.After(time.Second):
		t.Fatal("spec restart did not stop the old QUIC listener")
	}
	udp, err := net.ListenPacket("udp", h.addr)
	if err != nil {
		t.Fatalf("UDP port %s was not freed by the restart: %v", h.addr, err)
	}
	_ = udp.Close()
	if h.e.inbounds[spec.ID].h3Server != nil || h.e.inbounds[spec.ID].h3Ln != nil {
		t.Fatal("h3:false opened a UDP listener")
	}
}

func TestHTTP3TCPAndUDPUseTheSameCredentialCounters(t *testing.T) {
	// The individual TCP and UDP tests assert payload bytes; this test pins the common session and Connects accounting.
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	backend := echoBackend(t)
	var sockets socketLog
	outbound := sockets.egress(nil, nil)
	outbound.tcpAddr = backend.listener.Addr().String()
	h := newH3Harness(t, []plugin.UserCred{alice.cred}, outbound, timings{})
	client := newHTTP3Client(t, h.addr)
	client.useSingleConnection(t)
	resp, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("TCP stream status = %d", resp.StatusCode)
	}
	cp := "tcp count"
	if _, err := io.WriteString(writer, cp); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil || string(got) != cp {
		t.Fatalf("TCP stream echo = %q, %v", got, err)
	}
	udpResp, udpWriter := client.connect(t, "_udp2", authValue(t, alice), true)
	if udpResp.StatusCode != http.StatusOK {
		t.Fatalf("UDP stream status = %d", udpResp.StatusCode)
	}
	udpPayload := "udp count"
	frame := udpFrame("192.0.2.1", 1000, "192.0.2.10", 443, udpPayload)
	writeUDPFrame(t, udpWriter, frame)
	reply, err := readUDPFrame(udpResp.Body)
	if err != nil || string(reply.Payload) != udpPayload {
		t.Fatalf("UDP reply = %q, %v", reply.Payload, err)
	}
	_ = udpWriter.Close()
	wantBytes := uint64(len(cp) + len(udpPayload))
	waitFor(t, time.Second, func() bool {
		cs := h.in.idx.Load().byID["alice"]
		return cs.up.Load() == wantBytes && cs.down.Load() == wantBytes
	})
	stats := collect(t, h.e)
	if up, down := trafficFor(stats, "alice"); up != wantBytes || down != wantBytes {
		t.Fatalf("combined HTTP/3 traffic up/down = %d/%d, want %d/%d", up, down, wantBytes, wantBytes)
	}
	if sessionsOf(stats, "alice") != 1 {
		t.Fatalf("TCP and UDP streams on one QUIC connection produced %d sessions, want 1", sessionsOf(stats, "alice"))
	}
	var connects uint32
	for _, stat := range stats.Links {
		if stat.CredID == "alice" {
			connects += stat.Connects
		}
	}
	if connects != 1 {
		t.Fatalf("TCP and UDP on one QUIC connection counted %d connects, want 1", connects)
	}
}

func TestHTTP3QuietTunnelOutlivesSiteTimeouts(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	ts := shortTimings()
	backend := startTCPBackend(t, func(conn net.Conn) {
		time.Sleep(15 * ts.write)
		_, _ = io.WriteString(conn, "late")
		_, _ = io.Copy(conn, conn)
	})
	h := newH3Harness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, ts)
	client := newHTTP3Client(t, h.addr)
	resp, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	time.Sleep(25 * ts.write)
	late := make([]byte, 4)
	if _, err := io.ReadFull(resp.Body, late); err != nil || string(late) != "late" {
		t.Fatalf("HTTP/3 upstream's late data = %q, %v", late, err)
	}
	exchange(t, writer, resp.Body, "still here")
	_ = writer.Close()
	if !endsWithin(resp.Body, 2*time.Second) {
		t.Fatal("quiet HTTP/3 tunnel did not close after the client half-close")
	}
}

func measureHTTP3Rate(t *testing.T, upload bool) {
	t.Helper()
	const payload = 600_000
	alice := makeCredential(t, "alice", "alice", "person-alice", 2_000_000, 0)
	var backend *tcpBackend
	if upload {
		backend = sinkBackend(t)
	} else {
		backend = startTCPBackend(t, func(conn net.Conn) { _, _ = conn.Write(make([]byte, payload)) })
	}
	ts := defaultTimings
	ts.eofGrace = 20 * time.Millisecond
	h := newH3Harness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, ts)
	client := newHTTP3Client(t, h.addr)
	resp, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP/3 rate-limit CONNECT status = %d", resp.StatusCode)
	}
	start := time.Now()
	if upload {
		if _, err := writer.Write(make([]byte, payload)); err != nil {
			t.Fatal(err)
		}
		_ = writer.Close()
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start).Seconds()
	if !upload && len(got) != payload {
		t.Fatalf("HTTP/3 download = %d bytes, want %d", len(got), payload)
	}
	_ = writer.Close()
	const burstBytes = 250_000
	measured := float64(payload-burstBytes) * 8 / elapsed
	if measured < 1_800_000 || measured > 2_200_000 {
		t.Fatalf("HTTP/3 %s rate = %.0f bit/s over %.2fs, want 2,000,000 +/-10%%", map[bool]string{true: "upload", false: "download"}[upload], measured, elapsed)
	}
}

func TestHTTP3DeviceLimitAppliesToUploadAndDownload(t *testing.T) {
	for _, tc := range []struct {
		name   string
		upload bool
	}{{name: "upload", upload: true}, {name: "download"}} {
		t.Run(tc.name, func(t *testing.T) { measureHTTP3Rate(t, tc.upload) })
	}
}

// A tunnel that is ended before its response headers went out (a Kick while the upstream is still being dialled) is reset:
// the stream must never end with a bare FIN and no HEADERS, which a client takes for a protocol error of the whole
// connection. The connection and the other streams stay up.
func TestHTTP3KickBeforeHeadersResetsTheStreamAndKeepsTheConnection(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	bob := makeCredential(t, "bob", "bob", "person-bob", 0, 0)
	backend := echoBackend(t)
	release := make(chan struct{})
	outbound := &testEgress{tcpDial: func(target string) (net.Conn, error) {
		if target == "slow.example.com:80" {
			<-release
			return nil, errors.New("gave up")
		}
		return net.Dial("tcp", backend.listener.Addr().String())
	}}
	defer close(release)
	h := newH3Harness(t, []plugin.UserCred{alice.cred, bob.cred}, outbound, timings{})
	client := newHTTP3Client(t, h.addr)
	client.useSingleConnection(t)
	bobResponse, bobWriter := client.connect(t, "example.com:80", authValue(t, bob), true)
	exchange(t, bobWriter, bobResponse.Body, "bob before")

	type result struct {
		resp *http.Response
		err  error
	}
	answered := make(chan result, 1)
	go func() {
		req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Scheme: "https", Host: "slow.example.com:80"}, Host: "slow.example.com:80", Header: make(http.Header), Body: http.NoBody}
		req.Header.Set("Proxy-Authorization", authValue(t, alice))
		resp, err := client.roundTrip(req)
		answered <- result{resp, err}
	}()
	waitFor(t, 2*time.Second, func() bool {
		cs := h.in.idx.Load().byID["alice"]
		cs.mu.Lock()
		defer cs.mu.Unlock()
		return cs.active == 1
	})
	if affected, err := h.e.Kick(context.Background(), []string{"alice"}); err != nil || affected != 1 {
		t.Fatalf("Kick = %d, %v", affected, err)
	}
	select {
	case got := <-answered:
		if got.err == nil {
			_ = got.resp.Body.Close()
			t.Fatalf("a kicked CONNECT that never got headers was answered with %d, want a reset stream", got.resp.StatusCode)
		}
		var streamErr *http3.Error
		if !errors.As(got.err, &streamErr) || streamErr.ErrorCode == http3.ErrCodeFrameUnexpected {
			t.Fatalf("kicked CONNECT failed with %v, want a stream reset", got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("a kicked CONNECT that never got headers was not ended within 1 s")
	}
	if conn := client.firstConn(); conn.Context().Err() != nil {
		t.Fatalf("the QUIC connection was closed: %v", context.Cause(conn.Context()))
	}
	exchange(t, bobWriter, bobResponse.Body, "bob after")
	_ = bobWriter.Close()
}

// Over HTTP/3 a wrong password, an unknown user and a missing header get the answer a plain GET gets, header for header, and
// the first two cost the same work. The credential header never reaches the site or the log.
func TestHTTP3RefusedConnectsLookAndCostTheSameAndLeakNothing(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	wrong := makeCredential(t, "wrong", "alice", "person-alice", 0, 0)
	unknown := makeCredential(t, "unknown", "stranger", "person-x", 0, 0)
	var digests atomic.Int64
	real := passwordDigest
	passwordDigest = func(password string) [sha256.Size]byte {
		digests.Add(1)
		return real(password)
	}
	t.Cleanup(func() { passwordDigest = real })
	handler := &recordingHandler{}
	var logs bytes.Buffer
	h := newH3HarnessEnv(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{}, func(env *nodeengine.Env) {
		env.Masquerade = func(string) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handler.ServeHTTP(w, r)
				w.Header().Set("Content-Type", "text/plain")
				_, _ = io.WriteString(w, "decoy site")
			})
		}
		env.Log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	client := newHTTP3Client(t, h.addr)
	read := func(resp *http.Response) (int, http.Header, string) {
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, resp.Header.Clone(), string(body)
	}
	plain := client.get(t)
	// A GET and a CONNECT differ in the request, so the reference answer is the one a CONNECT without a header gets.
	missing, _ := client.connect(t, "example.com:80", "", false)
	wantStatus, wantHeader, wantBody := read(missing)
	_ = plain.Body.Close()
	digests.Store(0)
	for name, auth := range map[string]string{"wrong password": authValue(t, wrong), "unknown user": authValue(t, unknown), "broken header": "Basic broken"} {
		before := digests.Load()
		resp, _ := client.connect(t, "example.com:80", auth, false)
		status, header, body := read(resp)
		if status != wantStatus || body != wantBody || !sameResponseHeaders(header, wantHeader) {
			t.Fatalf("%s over HTTP/3: %d %v %q, want %d %v %q", name, status, header, body, wantStatus, wantHeader, wantBody)
		}
		if got := digests.Load() - before; (name == "broken header") != (got == 0) || got > 1 {
			t.Fatalf("%s over HTTP/3 cost %d password digests", name, got)
		}
	}
	resp, _ := client.connect(t, "_check", authValue(t, alice), false)
	_, _, _ = read(resp)

	handler.mu.Lock()
	seen := len(handler.headers)
	for _, header := range handler.headers {
		for key, values := range header {
			if strings.EqualFold(key, "Proxy-Authorization") {
				t.Fatalf("the site's handler saw %s over HTTP/3: %v", key, values)
			}
		}
	}
	handler.mu.Unlock()
	if seen < 5 {
		t.Fatalf("the site's handler saw %d HTTP/3 requests, want the refused ones", seen)
	}
	if _, err := h.e.Apply(context.Background(), h.spec, []plugin.UserCred{alice.cred}); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{alice.password, wrong.password, unknown.password, "Basic", "Proxy-Authorization"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("the log contains %q after HTTP/3 traffic: %s", secret, logs.String())
		}
	}
}

// A HEAD request to the HTTP/3 site gets its headers and an end of stream at once (the HEAD of an HTTP/3 reverse proxy of
// the official server hangs).
func TestHTTP3HeadRequestEndsAtOnce(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newH3Harness(t, []plugin.UserCred{alice.cred}, nil, timings{})
	client := newHTTP3Client(t, h.addr)
	req, _ := http.NewRequest(http.MethodHead, "https://example.com/", nil)
	done := make(chan error, 1)
	go func() {
		resp, err := client.roundTrip(req)
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("HEAD over HTTP/3: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HEAD over HTTP/3 did not end")
	}
}

// tunnelClient is a client of either transport; the tunnel behaviour is the same over both, so most tunnel tests run on
// both.
type tunnelClient interface {
	connect(t *testing.T, authority, auth string, streaming bool) (*http.Response, *io.PipeWriter)
	get(t *testing.T) *http.Response
	kill()
	close()
}

var (
	_ tunnelClient = (*testHTTP2Client)(nil)
	_ tunnelClient = (*testHTTP3Client)(nil)
)

type transportKind struct {
	name string
	h3   bool
}

func (v transportKind) harness(t *testing.T, creds []plugin.UserCred, outbound *testEgress, ts timings) *testHarness {
	t.Helper()
	return v.harnessEnv(t, creds, outbound, ts, nil)
}

func (v transportKind) harnessEnv(t *testing.T, creds []plugin.UserCred, outbound *testEgress, ts timings, mutate func(*nodeengine.Env)) *testHarness {
	t.Helper()
	if v.h3 {
		return newH3HarnessEnv(t, creds, outbound, ts, mutate)
	}
	return newHarnessEnv(t, creds, outbound, ts, mutate)
}

func (v transportKind) client(t *testing.T, addr string) tunnelClient {
	t.Helper()
	if v.h3 {
		return newHTTP3Client(t, addr)
	}
	return newHTTP2Client(t, addr)
}

func forEachTransport(t *testing.T, test func(t *testing.T, v transportKind)) {
	t.Helper()
	for _, v := range []transportKind{{name: "h2"}, {name: "h3", h3: true}} {
		t.Run(v.name, func(t *testing.T) { test(t, v) })
	}
}

// The same rule over QUIC: a connection carries one _udp2 stream per credential; a newer one replaces the older, which is
// reset (not left to a bare FIN) while the connection and the other streams stay.
func TestHTTP3SecondUDP2OnAConnectionReplacesTheFirst(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	backend := echoBackend(t)
	var sockets socketLog
	outbound := sockets.egress(nil, nil)
	outbound.tcpAddr = backend.listener.Addr().String()
	h := newH3Harness(t, []plugin.UserCred{alice.cred}, outbound, timings{})
	client := newHTTP3Client(t, h.addr)
	client.useSingleConnection(t)
	tcp, tcpWriter := client.connect(t, "example.com:80", authValue(t, alice), true)
	exchange(t, tcpWriter, tcp.Body, "tcp stays")

	first, firstWriter := client.connect(t, "_udp2", authValue(t, alice), true)
	writeUDPFrame(t, firstWriter, udpFrame("192.0.2.1", 1111, "192.0.2.10", 443, "first"))
	if reply, err := readUDPFrame(first.Body); err != nil || string(reply.Payload) != "first" {
		t.Fatalf("first stream echo = %+v, %v", reply, err)
	}
	second, secondWriter := client.connect(t, "_udp2", authValue(t, alice), true)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second _udp2 status = %d", second.StatusCode)
	}
	if err, ended := streamErrorWithin(first.Body, time.Second); !ended || err == nil {
		t.Fatalf("the replaced _udp2 stream ended = %v, err = %v; it must be reset within a second", ended, err)
	}
	waitFor(t, time.Second, func() bool { return sockets.get(0).isClosed.Load() })
	writeUDPFrame(t, secondWriter, udpFrame("192.0.2.1", 2222, "192.0.2.10", 443, "second"))
	if reply, err := readUDPFrame(second.Body); err != nil || string(reply.Payload) != "second" {
		t.Fatalf("second stream echo = %+v, %v", reply, err)
	}
	exchange(t, tcpWriter, tcp.Body, "tcp still")
	if client.firstConn().Context().Err() != nil {
		t.Fatal("replacing a _udp2 stream closed the QUIC connection")
	}
	_ = tcpWriter.Close()
	_ = secondWriter.Close()
}
