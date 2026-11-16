package trusttunnel

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"reflect"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/egress"
	nodeengine "github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
	wire "github.com/mistgate/mistgate/internal/trusttunnel"
	"golang.org/x/net/http2"
)

type credentialFixture struct {
	cred     plugin.UserCred
	username string
	password string
}

func makeCredential(t *testing.T, id, username, userID string, deviceLimit, personLimit uint64) credentialFixture {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	password := base64.RawURLEncoding.EncodeToString(raw)
	passHash := sha256.Sum256([]byte(password))
	data := []byte(fmt.Sprintf("{\"user\":%q,\"pass_sha256\":%q}", username, hex.EncodeToString(passHash[:])))
	return credentialFixture{
		cred: plugin.UserCred{
			CredID: id, UserID: userID, DeviceID: id, Data: data,
			RateLimitBps: deviceLimit, UserRateLimitBps: personLimit,
		},
		username: username,
		password: password,
	}
}

type testCertSource struct{}

func (testCertSource) Acquire(context.Context, string, plugin.TLS) (nodeengine.Cert, error) {
	certificate, err := testCertificate()
	if err != nil {
		return nodeengine.Cert{}, err
	}
	return nodeengine.Cert{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return certificate, nil },
		Info: func() nodeengine.CertInfo {
			return nodeengine.CertInfo{PinSHA256: "test-pin", NotAfter: certificate.Leaf.NotAfter}
		},
	}, nil
}

func (testCertSource) Release(string) {}

var (
	testCertOnce sync.Once
	testCert     *tls.Certificate
	testCertErr  error
)

func testCertificate() (*tls.Certificate, error) {
	testCertOnce.Do(func() {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			testCertErr = err
			return
		}
		now := time.Now()
		template := &x509.Certificate{
			SerialNumber: big.NewInt(1),
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.Add(24 * time.Hour),
			DNSNames:     []string{"example.com"},
			KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			testCertErr = err
			return
		}
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			testCertErr = err
			return
		}
		pair, err := tls.X509KeyPair(
			pemEncode("CERTIFICATE", der),
			pemEncode("EC PRIVATE KEY", keyDER),
		)
		if err != nil {
			testCertErr = err
			return
		}
		pair.Leaf, _ = x509.ParseCertificate(der)
		testCert = &pair
	})
	return testCert, testCertErr
}

func pemEncode(kind string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der})
}

type testEgress struct {
	mu         sync.Mutex
	tcpAddr    string
	tcpDial    func(string) (net.Conn, error)
	blocked    map[string]error
	udpEcho    bool
	udpFactory func(string) (nodeengine.EgressUDP, error)
	udpConfig  func(*testUDP) // set on every new socket before it is used
	tcpTargets []string
	udpTargets []string
	udpSockets []*testUDP
}

func (e *testEgress) TCP(target string) (net.Conn, error) {
	e.mu.Lock()
	e.tcpTargets = append(e.tcpTargets, target)
	if err := e.blocked[target]; err != nil {
		e.mu.Unlock()
		return nil, err
	}
	dial, addr := e.tcpDial, e.tcpAddr
	e.mu.Unlock()
	if dial != nil {
		return dial(target)
	}
	if addr == "" {
		return nil, errors.New("fake egress has no TCP backend")
	}
	return net.Dial("tcp", addr)
}

func (e *testEgress) UDP(target string) (nodeengine.EgressUDP, error) {
	e.mu.Lock()
	e.udpTargets = append(e.udpTargets, target)
	factory, echo := e.udpFactory, e.udpEcho
	e.mu.Unlock()
	if factory != nil {
		return factory(target)
	}
	conn := newTestUDP(echo)
	if e.udpConfig != nil {
		e.udpConfig(conn)
	}
	e.mu.Lock()
	e.udpSockets = append(e.udpSockets, conn)
	e.mu.Unlock()
	return conn, nil
}

func (*testEgress) CheckUDP(string) error { return nil }

type udpReply struct {
	payload []byte
	address string
}

type testUDP struct {
	echo      bool
	writeErr  func(address string) error  // a datagram to this address fails, as the egress does for a forbidden one
	replyFrom func(address string) string // the address a reply to a datagram sent to address comes from
	writes    atomic.Int64
	replies   chan udpReply
	closed    chan struct{}
	closeOnce sync.Once
	isClosed  atomic.Bool
}

func newTestUDP(echo bool) *testUDP {
	return &testUDP{echo: echo, replies: make(chan udpReply, 1024), closed: make(chan struct{})}
}

func (c *testUDP) ReadFrom(dst []byte) (int, string, error) {
	select {
	case reply := <-c.replies:
		return copy(dst, reply.payload), reply.address, nil
	case <-c.closed:
		return 0, "", net.ErrClosed
	}
}

func (c *testUDP) WriteTo(payload []byte, address string) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	if c.writeErr != nil {
		if err := c.writeErr(address); err != nil {
			return 0, err
		}
	}
	c.writes.Add(1)
	if c.echo {
		from := address
		if c.replyFrom != nil {
			from = c.replyFrom(address)
		}
		reply := udpReply{payload: append([]byte(nil), payload...), address: from}
		select {
		case c.replies <- reply:
		case <-c.closed:
			return 0, net.ErrClosed
		}
	}
	return len(payload), nil
}

func (c *testUDP) Close() error {
	c.closeOnce.Do(func() {
		c.isClosed.Store(true)
		close(c.closed)
	})
	return nil
}

type tcpBackend struct {
	listener net.Listener
	accept   chan struct{}
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	wg       sync.WaitGroup
	once     sync.Once
	handler  func(net.Conn)
}

func startTCPBackend(t *testing.T, handler func(net.Conn)) *tcpBackend {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	backend := &tcpBackend{
		listener: listener, accept: make(chan struct{}), conns: make(map[net.Conn]struct{}), handler: handler,
	}
	go func() {
		defer close(backend.accept)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			backend.mu.Lock()
			backend.conns[conn] = struct{}{}
			backend.mu.Unlock()
			backend.wg.Add(1)
			go func() {
				defer backend.wg.Done()
				defer conn.Close()
				defer func() {
					backend.mu.Lock()
					delete(backend.conns, conn)
					backend.mu.Unlock()
				}()
				backend.handler(conn)
			}()
		}
	}()
	t.Cleanup(backend.close)
	return backend
}

func (backend *tcpBackend) close() {
	backend.once.Do(func() {
		_ = backend.listener.Close()
		backend.mu.Lock()
		for conn := range backend.conns {
			_ = conn.Close()
		}
		backend.mu.Unlock()
		<-backend.accept
		backend.wg.Wait()
	})
}

func echoBackend(t *testing.T) *tcpBackend {
	return startTCPBackend(t, func(conn net.Conn) {
		_, _ = io.Copy(conn, conn)
	})
}

func sinkBackend(t *testing.T) *tcpBackend {
	return startTCPBackend(t, func(conn net.Conn) {
		_, _ = io.Copy(io.Discard, conn)
	})
}

var bindOnce sync.Once

type testHarness struct {
	e      *eng
	in     *inbound
	spec   plugin.InboundSpec
	addr   string
	egress *testEgress
}

func newHarness(t *testing.T, creds []plugin.UserCred, outbound *testEgress, ts timings) *testHarness {
	t.Helper()
	return newHarnessEnv(t, creds, outbound, ts, nil)
}

// newHarnessEnv is newHarness with a say in the Env the engine is built with.
func newHarnessEnv(t *testing.T, creds []plugin.UserCred, outbound *testEgress, ts timings, mutate func(*nodeengine.Env)) *testHarness {
	t.Helper()
	bindOnce.Do(func() { BindIP = net.ParseIP("127.0.0.1") }) // once: harnesses of parallel tests read it while starting
	if outbound == nil {
		outbound = &testEgress{}
	}
	if ts == (timings{}) {
		ts = defaultTimings
	}
	env := nodeengine.Env{
		Certs: testCertSource{},
		Egress: func(name string) (nodeengine.Egress, error) {
			if name != "" && name != "direct" && name != "warp" {
				return nil, errors.New("unknown fake egress")
			}
			return outbound, nil
		},
		Masquerade: func(string) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("X-Decoy", "same")
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, "decoy site")
			})
		},
	}
	if mutate != nil {
		mutate(&env)
	}
	e, err := newWithTimings(env, ts)
	if err != nil {
		t.Fatal(err)
	}
	port := reservePort(t)
	spec := testSpec(port)
	if _, err := e.Apply(context.Background(), spec, creds); err != nil {
		t.Fatal(err)
	}
	in := e.inbounds[spec.ID]
	h := &testHarness{
		e: e, in: in, spec: spec, addr: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), egress: outbound,
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = e.Close(ctx)
	})
	return h
}

func testSpec(port uint16) plugin.InboundSpec {
	return plugin.InboundSpec{
		ID: "de1", Protocol: Protocol, ProfileID: "profile-de1", Version: 1, Enabled: true,
		Listen: plugin.Listen{Network: "tcp", Port: port},
		TLS:    plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "example.com"},
		Egress: "direct", Settings: []byte("{}"),
	}
}

func reservePort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()
	return port
}

type testHTTP2Client struct {
	transport *http2.Transport
	mu        sync.Mutex
	raw       []net.Conn
}

// kill drops every connection of the client without a word, as a vanished phone does.
func (c *testHTTP2Client) kill() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, conn := range c.raw {
		_ = conn.Close()
	}
}

func newHTTP2Client(t *testing.T, addr string) *testHTTP2Client {
	t.Helper()
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true, // the engine's tests use a generated test certificate.
		ServerName:         "example.com",
		NextProtos:         []string{"h2"},
	}
	client := &testHTTP2Client{}
	transport := &http2.Transport{
		TLSClientConfig: tlsConfig,
		DialTLSContext: func(ctx context.Context, network, _ string, config *tls.Config) (net.Conn, error) {
			raw, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			conn := tls.Client(raw, config.Clone())
			if err := conn.HandshakeContext(ctx); err != nil {
				_ = raw.Close()
				return nil, err
			}
			client.mu.Lock()
			client.raw = append(client.raw, raw)
			client.mu.Unlock()
			return conn, nil
		},
	}
	client.transport = transport
	// A test that waits for something a broken engine never sends fails after this, instead of hanging until the test
	// binary's own timeout takes every other test with it.
	watchdog := time.AfterFunc(30*time.Second, client.kill)
	t.Cleanup(func() { watchdog.Stop(); client.close() })
	return client
}

func (c *testHTTP2Client) close() { c.transport.CloseIdleConnections() }

func authValue(t *testing.T, credential credentialFixture) string {
	t.Helper()
	value, err := wire.BuildProxyAuthorization(credential.username, credential.password)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func (c *testHTTP2Client) connect(t *testing.T, authority, auth string, streaming bool) (*http.Response, *io.PipeWriter) {
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
	resp, err := c.transport.RoundTrip(req)
	if err != nil {
		if writer != nil {
			_ = writer.Close()
		}
		t.Fatalf("CONNECT failed: %v", err)
	}
	return resp, writer
}

func (c *testHTTP2Client) get(t *testing.T) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	resp, err := c.transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func collect(t *testing.T, e *eng) nodeengine.Collected {
	t.Helper()
	got, err := e.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func trafficFor(got nodeengine.Collected, credID string) (uint64, uint64) {
	for _, stat := range got.Traffic {
		if stat.CredID == credID {
			return stat.Up, stat.Down
		}
	}
	return 0, 0
}

func TestTCPConnectEchoHalfCloseAndCollect(t *testing.T) {
	credential := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	backend := echoBackend(t)
	outbound := &testEgress{tcpAddr: backend.listener.Addr().String()}
	h := newHarness(t, []plugin.UserCred{credential.cred}, outbound, timings{})
	client := newHTTP2Client(t, h.addr)

	response, writer := client.connect(t, "example.com:80", authValue(t, credential), true)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d", response.StatusCode)
	}
	payload := []byte("half-close travels in both directions")
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if string(got) != string(payload) {
		t.Fatalf("echo = %q, want exact payload", got)
	}

	first := collect(t, h.e)
	up, down := trafficFor(first, "alice")
	if up != uint64(len(payload)) || down != uint64(len(payload)) {
		t.Fatalf("traffic up/down = %d/%d, want %d/%d", up, down, len(payload), len(payload))
	}
	if next := collect(t, h.e); len(next.Traffic) != 0 {
		t.Fatalf("second Collect replayed traffic: %+v", next.Traffic)
	}
}

func TestUpstreamHalfCloseLeavesClientUploadOpen(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	const response = "remote half-close"
	received := make(chan []byte, 1)
	backend := startTCPBackend(t, func(conn net.Conn) {
		_, _ = io.WriteString(conn, response)
		if half, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		}
		payload, _ := io.ReadAll(conn)
		received <- payload
	})
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, timings{})
	client := newHTTP2Client(t, h.addr)
	resp, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d", resp.StatusCode)
	}
	first := []byte("client data before remote EOF")
	if _, err := writer.Write(first); err != nil {
		t.Fatal(err)
	}
	gotResponse := make([]byte, len(response))
	if _, err := io.ReadFull(resp.Body, gotResponse); err != nil {
		t.Fatal(err)
	}
	if string(gotResponse) != response {
		t.Fatalf("upstream response = %q, want %q", gotResponse, response)
	}
	last := []byte("client data after remote EOF")
	if _, err := writer.Write(last); err != nil {
		t.Fatalf("client upload stopped after upstream half-close: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	select {
	case got := <-received:
		if string(got) != string(append(first, last...)) {
			t.Fatalf("upstream received %q, want both client chunks", got)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream did not observe the client half-close")
	}
}

func TestBadAndUnknownCredentialsUseTheSameMasqueradeResponse(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
	client := newHTTP2Client(t, h.addr)

	direct := client.get(t)
	directBody, err := io.ReadAll(direct.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = direct.Body.Close()
	wrong := makeCredential(t, "wrong", "alice", "person-alice", 0, 0)
	unknown := makeCredential(t, "unknown", "unknown", "person-unknown", 0, 0)
	wrongResp, _ := client.connect(t, "_check", authValue(t, wrong), false)
	unknownResp, _ := client.connect(t, "_check", authValue(t, unknown), false)
	missingResp, _ := client.connect(t, "_check", "", false)
	wrongBody, err := io.ReadAll(wrongResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	unknownBody, err := io.ReadAll(unknownResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	missingBody, err := io.ReadAll(missingResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if wrongResp.StatusCode != direct.StatusCode || unknownResp.StatusCode != direct.StatusCode || missingResp.StatusCode != direct.StatusCode ||
		string(wrongBody) != string(directBody) || string(unknownBody) != string(directBody) ||
		string(missingBody) != string(directBody) ||
		!sameResponseHeaders(wrongResp.Header, direct.Header) ||
		!sameResponseHeaders(unknownResp.Header, direct.Header) ||
		!sameResponseHeaders(missingResp.Header, direct.Header) {
		t.Fatalf("masquerade mismatch: GET=%d/%q wrong=%d/%q unknown=%d/%q",
			direct.StatusCode, directBody, wrongResp.StatusCode, wrongBody, unknownResp.StatusCode, unknownBody)
	}
	_ = wrongResp.Body.Close()
	_ = unknownResp.Body.Close()
	_ = missingResp.Body.Close()

	stats := collect(t, h.e)
	var unknownCount, failedCount uint32
	for _, stat := range stats.Links {
		if stat.InboundID == "de1" {
			unknownCount += stat.UnknownKeys
		}
		if stat.CredID == "alice" {
			failedCount += stat.AuthFailed
		}
	}
	if unknownCount != 2 || failedCount != 1 {
		t.Fatalf("auth counters unknown=%d failed=%d, want 2 and 1", unknownCount, failedCount)
	}
}

func sameResponseHeaders(left, right http.Header) bool {
	left, right = left.Clone(), right.Clone()
	left.Del("Date")
	right.Del("Date")
	return reflect.DeepEqual(left, right)
}

func TestHTTP1ConnectUsesMasquerade(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
	raw, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	tlsConn := tls.Client(raw, &tls.Config{
		InsecureSkipVerify: true, ServerName: "example.com", NextProtos: []string{"http/1.1"},
	})
	if err := tlsConn.Handshake(); err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintf(tlsConn,
		"CONNECT example.com:80 HTTP/1.1\r\nHost: example.com:80\r\nProxy-Authorization: %s\r\nConnection: close\r\n\r\n",
		authValue(t, alice))
	response, err := http.ReadResponse(bufio.NewReader(tlsConn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNotFound || string(body) != "decoy site" {
		t.Fatalf("HTTP/1.1 CONNECT = %d %q, want the masquerade response", response.StatusCode, body)
	}
}

func TestCheckAndICMPResponses(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
	client := newHTTP2Client(t, h.addr)

	check, _ := client.connect(t, "_check", authValue(t, alice), false)
	body, err := io.ReadAll(check.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = check.Body.Close()
	if check.StatusCode != http.StatusOK || len(body) != 0 {
		t.Fatalf("_check = %d %q, want 200 with END_STREAM", check.StatusCode, body)
	}
	if got := collect(t, h.e); len(got.Traffic) != 0 {
		t.Fatalf("_check counted as traffic: %+v", got.Traffic)
	}

	icmp, _ := client.connect(t, "_icmp", authValue(t, alice), false)
	_ = icmp.Body.Close()
	if icmp.StatusCode != http.StatusBadGateway || icmp.Header.Get("X-Warning") != "300 - Connection failed for some reason" {
		t.Fatalf("_icmp = %d, X-Warning %q", icmp.StatusCode, icmp.Header.Get("X-Warning"))
	}
}

func TestUDP2EchoKeepsTwoFlowsAndCollectsPayload(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	outbound := &testEgress{udpEcho: true}
	h := newHarness(t, []plugin.UserCred{alice.cred}, outbound, timings{})
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "_udp2", authValue(t, alice), true)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("_udp2 status = %d", response.StatusCode)
	}

	first := wire.UDPFrame{
		Source: mustAddrPort("192.0.2.1:12345"), Target: mustAddrPort("192.0.2.10:443"),
		Payload: []byte("first-flow"),
	}
	second := wire.UDPFrame{
		Source: mustAddrPort("192.0.2.2:12346"), Target: mustAddrPort("192.0.2.11:5353"),
		Payload: []byte("second-flow"),
	}
	writeUDPFrame(t, writer, first)
	writeUDPFrame(t, writer, second)

	replies := map[netip.AddrPort]wire.UDPFrame{}
	for range 2 {
		got, err := readUDPFrame(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		replies[got.Target] = got
	}
	for _, sent := range []wire.UDPFrame{first, second} {
		got, ok := replies[sent.Source]
		if !ok || got.Source != sent.Target || string(got.Payload) != string(sent.Payload) {
			t.Fatalf("reply for client source %s = %+v (present %v), want source %s and payload %q",
				sent.Source, got, ok, sent.Target, sent.Payload)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	stats := collect(t, h.e)
	up, down := trafficFor(stats, "alice")
	want := uint64(len(first.Payload) + len(second.Payload))
	if up != want || down != want {
		t.Fatalf("UDP payload traffic up/down = %d/%d, want %d/%d", up, down, want, want)
	}
}

func TestUDPFlowExpiryClosesSocket(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	outbound := &testEgress{}
	ts := defaultTimings
	ts.udpFlowIdle = 30 * time.Millisecond
	h := newHarness(t, []plugin.UserCred{alice.cred}, outbound, ts)
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "_udp2", authValue(t, alice), true)
	writeUDPFrame(t, writer, wire.UDPFrame{
		Source: mustAddrPort("192.0.2.8:23456"), Target: mustAddrPort("192.0.2.12:53"),
		Payload: []byte("expires"),
	})
	waitFor(t, time.Second, func() bool {
		outbound.mu.Lock()
		defer outbound.mu.Unlock()
		return len(outbound.udpSockets) == 1 && outbound.udpSockets[0].isClosed.Load()
	})
	_ = writer.Close()
	_ = response.Body.Close()
}

func TestUDPFlowLimitEvictsAndClosesOldestSocket(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	outbound := &testEgress{}
	h := newHarness(t, []plugin.UserCred{alice.cred}, outbound, timings{})
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "_udp2", authValue(t, alice), true)
	for i := 0; i < maxUDPFlows+1; i++ {
		writeUDPFrame(t, writer, wire.UDPFrame{
			Source: netip.AddrPortFrom(netip.MustParseAddr("192.0.2.9"), uint16(1000+i)),
			Target: mustAddrPort("192.0.2.13:53"), Payload: []byte{byte(i)},
		})
	}
	waitFor(t, 2*time.Second, func() bool {
		outbound.mu.Lock()
		defer outbound.mu.Unlock()
		return len(outbound.udpSockets) == maxUDPFlows+1
	})
	outbound.mu.Lock()
	if len(outbound.udpSockets) != maxUDPFlows+1 {
		outbound.mu.Unlock()
		t.Fatalf("created UDP sockets = %d, want %d", len(outbound.udpSockets), maxUDPFlows+1)
	}
	oldestClosed := outbound.udpSockets[0].isClosed.Load()
	outbound.mu.Unlock()
	if !oldestClosed {
		t.Fatal("oldest UDP flow socket was not closed on eviction")
	}
	_ = writer.Close()
	_ = response.Body.Close()
}

func TestBlockedLoopbackAndNodeTargetReturn310(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	outbound := &testEgress{blocked: map[string]error{"127.0.0.1:80": egress.ErrBlocked}}
	h := newHarness(t, []plugin.UserCred{alice.cred}, outbound, timings{})
	outbound.mu.Lock()
	outbound.blocked[h.addr] = egress.ErrBlocked
	outbound.mu.Unlock()
	client := newHTTP2Client(t, h.addr)
	for _, tc := range []struct {
		authority string
		host      string
	}{
		{"127.0.0.1:80", "127.0.0.1"},
		{h.addr, "127.0.0.1"},
	} {
		response, _ := client.connect(t, tc.authority, authValue(t, alice), false)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadGateway ||
			response.Header.Get("X-Warning") != "310 - Destination address is blocked" ||
			response.Header.Get("X-Adguard-Vpn-Error") != tc.host {
			t.Fatalf("blocked %s = status %d, warning %q, error %q", tc.authority, response.StatusCode,
				response.Header.Get("X-Warning"), response.Header.Get("X-Adguard-Vpn-Error"))
		}
	}
}

func TestIPv6AuthorityIsPassedToEgress(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	backend := echoBackend(t)
	outbound := &testEgress{tcpAddr: backend.listener.Addr().String()}
	h := newHarness(t, []plugin.UserCred{alice.cred}, outbound, timings{})
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "[2001:db8::1]:443", authValue(t, alice), true)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("IPv6 CONNECT status = %d", response.StatusCode)
	}
	payload := []byte("v6 authority")
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	got, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if string(got) != string(payload) {
		t.Fatalf("IPv6 echo = %q", got)
	}
	outbound.mu.Lock()
	defer outbound.mu.Unlock()
	if len(outbound.tcpTargets) != 1 || outbound.tcpTargets[0] != "[2001:db8::1]:443" {
		t.Fatalf("IPv6 egress targets = %v", outbound.tcpTargets)
	}
}

func TestConnectTimeoutReturns302AndClosesLateDial(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	release := make(chan struct{})
	peerReady := make(chan net.Conn, 1)
	outbound := &testEgress{tcpDial: func(string) (net.Conn, error) {
		<-release
		peer, conn := net.Pipe()
		peerReady <- peer
		return conn, nil
	}}
	ts := defaultTimings
	ts.connect = 25 * time.Millisecond
	h := newHarness(t, []plugin.UserCred{alice.cred}, outbound, ts)
	client := newHTTP2Client(t, h.addr)

	response, _ := client.connect(t, "example.com:80", authValue(t, alice), false)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || response.Header.Get("X-Warning") != "302 - Connection timed out" {
		t.Fatalf("connect timeout = %d %q", response.StatusCode, response.Header.Get("X-Warning"))
	}
	close(release)
	peer := <-peerReady
	defer peer.Close()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) { // a deadline error means nobody closed it
		t.Fatalf("late egress connection stayed open after timeout: %v", err)
	}
}

func TestCredentialSwapKeepsBobsTransferOnTheSameConnection(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	bob := makeCredential(t, "bob", "bob", "person-bob", 0, 0)
	backend := echoBackend(t)
	h := newHarness(t, []plugin.UserCred{alice.cred, bob.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, timings{})

	client := newHTTP2Client(t, h.addr)
	aliceResponse, aliceWriter := client.connect(t, "example.com:80", authValue(t, alice), true)
	alicePayload := []byte("Alice's original credential")
	if _, err := aliceWriter.Write(alicePayload); err != nil {
		t.Fatal(err)
	}
	_ = aliceWriter.Close()
	aliceEcho, err := io.ReadAll(aliceResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = aliceResponse.Body.Close()
	if string(aliceEcho) != string(alicePayload) {
		t.Fatalf("Alice setup echo = %q", aliceEcho)
	}
	oldAlice := h.in.idx.Load().byID["alice"]
	if count := authenticatedConnectionCount(h.in); count != 1 {
		t.Fatalf("Alice setup used %d connections, want one", count)
	}

	response, writer := client.connect(t, "example.com:80", authValue(t, bob), true)
	if count := authenticatedConnectionCount(h.in); count != 1 {
		t.Fatalf("Alice and Bob did not share one HTTP/2 connection: %d", count)
	}
	prefix, suffix := []byte("before swap/"), []byte("after swap")
	if _, err := writer.Write(prefix); err != nil {
		t.Fatal(err)
	}

	newAlice := makeCredential(t, "alice-v2", "alice", "person-alice", 0, 0)
	report, err := h.e.Apply(context.Background(), h.spec, []plugin.UserCred{newAlice.cred, bob.cred})
	if err != nil {
		t.Fatal(err)
	}
	if report.Restarted {
		t.Fatal("credential-only Apply restarted the inbound")
	}
	waitFor(t, time.Second, func() bool { return credentialConnectionCount(oldAlice) == 0 })
	if _, err := writer.Write(suffix); err != nil {
		t.Fatalf("bob's request stream stopped on the credential swap: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if string(got) != string(append(prefix, suffix...)) {
		t.Fatalf("bob transfer = %q, want byte-exact continuation", got)
	}
	if count := authenticatedConnectionCount(h.in); count != 1 {
		t.Fatalf("Bob's HTTP/2 connection was not kept alive across the swap: %d", count)
	}
	stats := collect(t, h.e)
	up, down := trafficFor(stats, "bob")
	want := uint64(len(prefix) + len(suffix))
	if up != want || down != want {
		t.Fatalf("post-swap Bob traffic = %d/%d, want %d/%d", up, down, want, want)
	}
	aliceUp, aliceDown := trafficFor(stats, "alice")
	aliceWant := uint64(len(alicePayload))
	if aliceUp != aliceWant || aliceDown != aliceWant {
		t.Fatalf("retired Alice traffic = %d/%d, want %d/%d", aliceUp, aliceDown, aliceWant, aliceWant)
	}
	var oldAliceConnects uint32
	for _, link := range stats.Links {
		if link.CredID == "alice" {
			oldAliceConnects += link.Connects
		}
	}
	if oldAliceConnects != 1 {
		t.Fatalf("retired Alice Connects = %d, want 1 on the next Collect", oldAliceConnects)
	}
	if next := collect(t, h.e); len(next.Traffic) != 0 || len(next.Links) != 0 {
		t.Fatalf("retired counters repeated after Collect: %+v", next)
	}
}

func TestSpecChangeRestartsOnlyItsInbound(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	bob := makeCredential(t, "bob", "bob", "person-bob", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
	secondSpec := testSpec(reservePort(t))
	secondSpec.ID, secondSpec.ProfileID = "de2", "profile-de2"
	if _, err := h.e.Apply(context.Background(), secondSpec, []plugin.UserCred{bob.cred}); err != nil {
		t.Fatal(err)
	}
	second := h.e.inbounds[secondSpec.ID]
	aliceClient := newHTTP2Client(t, h.addr)
	aliceResponse, _ := aliceClient.connect(t, "_check", authValue(t, alice), false)
	_ = aliceResponse.Body.Close()
	bobClient := newHTTP2Client(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(int(secondSpec.Listen.Port))))
	bobResponse, _ := bobClient.connect(t, "_check", authValue(t, bob), false)
	_ = bobResponse.Body.Close()
	oldAlice := h.in.idx.Load().byID["alice"]

	changed := h.spec
	changed.ProfileID = "profile-de1-v2"
	report, err := h.e.Apply(context.Background(), changed, []plugin.UserCred{alice.cred})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Restarted {
		t.Fatal("spec hash change did not restart the inbound")
	}
	waitFor(t, time.Second, func() bool { return credentialConnectionCount(oldAlice) == 0 })
	if h.e.inbounds[secondSpec.ID] != second || authenticatedConnectionCount(second) != 1 {
		t.Fatal("changing de1 disturbed the de2 inbound or its authenticated connection")
	}
	newClient := newHTTP2Client(t, h.addr)
	check, _ := newClient.connect(t, "_check", authValue(t, alice), false)
	_ = check.Body.Close()
	if check.StatusCode != http.StatusOK {
		t.Fatalf("restarted inbound check = %d, want 200", check.StatusCode)
	}
}

func TestKickClosesCredentialConnection(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
	client := newHTTP2Client(t, h.addr)
	response, _ := client.connect(t, "_check", authValue(t, alice), false)
	_ = response.Body.Close()
	if count := authenticatedConnectionCount(h.in); count != 1 {
		t.Fatalf("open authenticated connections = %d, want 1", count)
	}
	start := time.Now()
	affected, err := h.e.Kick(context.Background(), []string{"alice"})
	if err != nil || affected != 1 {
		t.Fatalf("Kick = %d, %v", affected, err)
	}
	if count := authenticatedConnectionCount(h.in); time.Since(start) > time.Second || count != 0 {
		t.Fatalf("Kick did not close the connection immediately: elapsed %s, open %d", time.Since(start), count)
	}
}

func TestQuietTunnelOutlivesSiteWriteTimeout(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	backend := echoBackend(t)
	ts := defaultTimings
	ts.read, ts.write, ts.readHeader = 25*time.Millisecond, 25*time.Millisecond, 25*time.Millisecond
	ts.streamIdle, ts.connectionIdle = time.Second, time.Second
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, ts)
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	time.Sleep(4 * ts.write)
	payload := []byte("quiet but alive")
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	got, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if string(got) != string(payload) {
		t.Fatalf("quiet tunnel echo = %q", got)
	}
}

func TestAuthenticatedIdleConnectionClosesAfterInjectedTimeout(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	ts := defaultTimings
	ts.connectionIdle = 25 * time.Millisecond
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, ts)
	client := newHTTP2Client(t, h.addr)
	response, _ := client.connect(t, "_check", authValue(t, alice), false)
	_ = response.Body.Close()
	waitFor(t, time.Second, func() bool { return authenticatedConnectionCount(h.in) == 0 })
}

func TestTCPStreamIdleTimeoutClosesTheTunnel(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	peerReady := make(chan net.Conn, 1)
	outbound := &testEgress{tcpDial: func(string) (net.Conn, error) {
		server, client := net.Pipe()
		peerReady <- client
		return server, nil
	}}
	ts := defaultTimings
	ts.streamIdle, ts.connectionIdle = 30*time.Millisecond, time.Second
	h := newHarness(t, []plugin.UserCred{alice.cred}, outbound, ts)
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	_ = writer
	peer := <-peerReady
	defer peer.Close()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatal("upstream socket stayed open after the stream idle timeout")
	}
	_ = response.Body.Close()
}

func TestSixteenConnectionCredentialLimit(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
	clients := make([]*testHTTP2Client, 17)
	responses := make([]*http.Response, 17)
	for i := range clients {
		clients[i] = newHTTP2Client(t, h.addr)
		responses[i], _ = clients[i].connect(t, "_check", authValue(t, alice), false)
		_ = responses[i].Body.Close()
	}
	for i := 0; i < 16; i++ {
		if responses[i].StatusCode != http.StatusOK {
			t.Fatalf("connection %d status = %d, want 200", i+1, responses[i].StatusCode)
		}
	}
	if responses[16].StatusCode != http.StatusNotFound {
		t.Fatalf("seventeenth connection status = %d, want masquerade 404", responses[16].StatusCode)
	}
	cs := h.in.idx.Load().byID["alice"]
	cs.mu.Lock()
	open := len(cs.connections)
	cs.mu.Unlock()
	if open != 16 {
		t.Fatalf("credential connection count = %d, want 16", open)
	}
}

func TestRateLimitPersonBucketWithinTenPercent(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 2_000_000)
	backend := sinkBackend(t)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, timings{})
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	payload := make([]byte, 600_000)
	start := time.Now()
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start).Seconds()
	_ = response.Body.Close()
	const burstBytes = 250_000
	measured := float64(len(payload)-burstBytes) * 8 / elapsed
	if measured < 1_800_000 || measured > 2_200_000 {
		t.Fatalf("person rate = %.0f bit/s over %s, want 2,000,000 +/-10%%", measured, time.Duration(elapsed*float64(time.Second)))
	}
}

func TestResetStreamLeavesNoCopyGoroutine(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	backend := echoBackend(t)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()}, timings{})
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	baseline := runtime.NumGoroutine()
	if _, err := writer.Write(make([]byte, 32<<10)); err != nil {
		t.Fatal(err)
	}
	_ = writer.CloseWithError(errors.New("test stream reset"))
	_ = response.Body.Close()
	client.close()
	waitFor(t, time.Second, func() bool { return runtime.NumGoroutine() <= baseline+1 })
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !condition() {
		t.Fatalf("condition not met within %s", timeout)
	}
}

func authenticatedConnectionCount(in *inbound) int {
	in.connMu.Lock()
	defer in.connMu.Unlock()
	return len(in.conns)
}

func credentialConnectionCount(credential *credState) int {
	credential.mu.Lock()
	defer credential.mu.Unlock()
	return len(credential.connections)
}

func readUDPFrame(reader io.Reader) (wire.UDPFrame, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return wire.UDPFrame{}, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length > 1<<16 {
		return wire.UDPFrame{}, errors.New("server UDP frame too large for test")
	}
	packet := make([]byte, 4+int(length))
	copy(packet, header[:])
	if _, err := io.ReadFull(reader, packet[4:]); err != nil {
		return wire.UDPFrame{}, err
	}
	frames, err := wire.NewUDPDecoder(wire.UDPServerToClient).Feed(packet)
	if err != nil {
		return wire.UDPFrame{}, err
	}
	if len(frames) != 1 {
		return wire.UDPFrame{}, fmt.Errorf("decoded %d UDP frames", len(frames))
	}
	return frames[0], nil
}

func writeUDPFrame(t *testing.T, writer *io.PipeWriter, frame wire.UDPFrame) {
	t.Helper()
	packet, err := wire.EncodeUDPClientFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(packet); err != nil {
		t.Fatal(err)
	}
}

func mustAddrPort(value string) netip.AddrPort {
	return netip.MustParseAddrPort(value)
}

// A credential that Apply keeps is shared with the handlers that are authenticating against it: Apply writes nothing but
// its atomics (run under -race: a rewrite of the user name or the hash is a data race).
func TestApplyOfTheSameCredentialsWhileConnectsAreInFlight(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	h := newHarness(t, []plugin.UserCred{alice.cred}, &testEgress{}, timings{})
	client := newHTTP2Client(t, h.addr)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if got := statusOf(t, client, "_check", authValue(t, alice)); got.StatusCode != http.StatusOK {
				t.Errorf("_check during Apply = %d", got.StatusCode)
				return
			}
		}
	}()
	for range 40 {
		if _, err := h.e.Apply(context.Background(), h.spec, []plugin.UserCred{alice.cred}); err != nil {
			t.Error(err)
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(stop)
	<-done
}
