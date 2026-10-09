package vless

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	xtlsreality "github.com/xtls/reality"
	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/app/policy"
	"github.com/xtls/xray-core/app/proxyman"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/session"
	xraycore "github.com/xtls/xray-core/core"
	xraysocks "github.com/xtls/xray-core/proxy/socks"
	xrayvless "github.com/xtls/xray-core/proxy/vless"
	vlessout "github.com/xtls/xray-core/proxy/vless/outbound"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/reality"
	"github.com/xtls/xray-core/transport/internet/splithttp"
	tcpcfg "github.com/xtls/xray-core/transport/internet/tcp"

	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

const testPublicKey = "E59WjnvZcQMu7tR7_BgyhycuEdBS-CtKxfImRCdAvFM"

type localEgress struct{}

func (localEgress) TCP(addr string) (net.Conn, error) { return net.Dial("tcp", addr) }
func (localEgress) CheckUDP(string) error             { return nil }
func (localEgress) UDP(addr string) (engine.EgressUDP, error) {
	conn, err := net.Dial("udp", addr)
	if err != nil {
		return nil, err
	}
	return connectedUDP{Conn: conn, target: addr}, nil
}

type connectedUDP struct {
	net.Conn
	target string
}

func (c connectedUDP) ReadFrom(p []byte) (int, string, error) {
	n, err := c.Conn.Read(p)
	return n, c.target, err
}
func (c connectedUDP) WriteTo(p []byte, _ string) (int, error) { return c.Conn.Write(p) }

type runtimeFixture struct {
	engine     *eng
	target     *httptest.Server
	targetAddr string
	logs       *bytes.Buffer
}

func newRuntimeFixture(t *testing.T, useRealEgress bool) *runtimeFixture {
	t.Helper()
	oldBindIP := BindIP
	BindIP = net.ParseIP("127.0.0.1")
	t.Cleanup(func() { BindIP = oldBindIP })

	target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	target.TLS = &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}}
	target.Config.ErrorLog = log.New(io.Discard, "", 0)
	target.StartTLS()
	address := strings.TrimPrefix(target.URL, "https://")
	t.Cleanup(target.Close)
	// REALITY measures the target's post-handshake records once per process, and a client that arrives before the
	// measurement is stored sleeps 5 s (xtls/reality tls.go). This target fails the measurement anyway (its certificate
	// is not trusted), so store that result up front: otherwise the first connection of a test waits 5 s at random.
	for alpn := range 3 {
		xtlsreality.GlobalPostHandshakeRecordsLens.Store(address+" localhost "+strconv.Itoa(alpn), []int{})
	}

	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	env := engine.Env{Log: logger}
	if useRealEgress {
		direct := egress.New(nil)
		env.Egress = func(name string) (engine.Egress, error) {
			if name != "" && name != "direct" {
				return nil, fmt.Errorf("egress %q is not available", name)
			}
			return direct, nil
		}
	} else {
		env.Egress = func(name string) (engine.Egress, error) {
			if name != "" && name != "direct" && name != "warp" {
				return nil, fmt.Errorf("egress %q is not available", name)
			}
			return localEgress{}, nil
		}
	}
	instance, err := New(env)
	if err != nil {
		t.Fatal(err)
	}
	eng := instance.(*eng)
	t.Cleanup(func() { _ = eng.Close(context.Background()) })
	return &runtimeFixture{engine: eng, target: target, targetAddr: address, logs: logs}
}

func (f *runtimeFixture) spec(id, transport string) plugin.InboundSpec {
	port := freeTCPPort(f.engine, nil)
	return plugin.InboundSpec{
		ID: id, Protocol: Protocol, Enabled: true, Listen: plugin.Listen{Network: "tcp", Port: port},
		Settings: json.RawMessage(fmt.Sprintf(`{"transport":%q,"security":"reality","flow":"ignored","reality":{"target":%q,"server_names":["localhost"],"short_ids":["6ba85179"],"private_key":%q},"xhttp":{"path":"/q8x2kd7w","host":"","mode":"auto","x_padding_bytes":"100-200"}}`, transport, f.targetAddr, testPrivateKey)),
	}
}

func freeTCPPort(e *eng, except map[uint16]bool) uint16 {
	for {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			panic(err)
		}
		port := uint16(listener.Addr().(*net.TCPAddr).Port)
		_ = listener.Close()
		if except != nil && except[port] {
			continue
		}
		used := false
		for _, in := range e.inbounds {
			if in.spec.Listen.Port == port {
				used = true
				break
			}
		}
		if !used {
			return port
		}
	}
}

func testCredential(id, uuid string) plugin.UserCred {
	return plugin.UserCred{CredID: id, UserID: "user-" + id, Data: json.RawMessage(fmt.Sprintf(`{"id":%q}`, uuid))}
}

func startEchoServer(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener
}

type xrayClient struct {
	instance *xraycore.Instance
	socks    string
}

func newXrayClient(t *testing.T, serverPort uint16, transport, credID, userID string) *xrayClient {
	flow := ""
	if transport == "tcp" {
		flow = "xtls-rprx-vision"
	}
	return newXrayClientWithFlow(t, serverPort, transport, credID, userID, flow)
}

func newXrayClientWithFlow(t *testing.T, serverPort uint16, transport, credID, userID, flow string) *xrayClient {
	return newXrayClientWithOptions(t, serverPort, transport, credID, userID, flow, false)
}

func newXrayMuxClient(t *testing.T, serverPort uint16, transport, credID, userID, flow string) *xrayClient {
	return newXrayClientWithOptions(t, serverPort, transport, credID, userID, flow, true)
}

func newXrayClientWithOptions(t *testing.T, serverPort uint16, transport, credID, userID, flow string, mux bool) *xrayClient {
	t.Helper()
	socksPort := reservePort(t)
	transportName := "tcp"
	var transportSettings []*internet.TransportConfig
	if transport == "tcp" {
		transportSettings = []*internet.TransportConfig{{ProtocolName: "tcp", Settings: serial.ToTypedMessage(&tcpcfg.Config{})}}
	} else {
		transportName = "splithttp"
		transportSettings = []*internet.TransportConfig{{ProtocolName: "splithttp", Settings: serial.ToTypedMessage(&splithttp.Config{
			Path: "/q8x2kd7w", Mode: "auto", XPaddingBytes: &splithttp.RangeConfig{From: 100, To: 200},
		})}}
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(testPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	shortID, err := hex.DecodeString("6ba85179")
	if err != nil {
		t.Fatal(err)
	}
	shortID = append(shortID, make([]byte, 8-len(shortID))...)
	stream := &internet.StreamConfig{
		ProtocolName:      transportName,
		TransportSettings: transportSettings,
		SecurityType:      serial.GetMessageType(&reality.Config{}),
		SecuritySettings: []*serial.TypedMessage{serial.ToTypedMessage(&reality.Config{
			Fingerprint: "chrome", ServerName: "localhost", PublicKey: publicKey, ShortId: shortID, SpiderX: "/",
		})},
	}
	sender := &proxyman.SenderConfig{StreamSettings: stream}
	if mux {
		sender.MultiplexSettings = &proxyman.MultiplexingConfig{Enabled: true, Concurrency: 8}
	}
	config := &xraycore.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&dispatcher.Config{}),
			serial.ToTypedMessage(&proxyman.InboundConfig{}),
			serial.ToTypedMessage(&proxyman.OutboundConfig{}),
			serial.ToTypedMessage(&policy.Config{Level: map[uint32]*policy.Policy{0: {Buffer: &policy.Policy_Buffer{Connection: 64 << 10}}}}),
		},
		Inbound: []*xraycore.InboundHandlerConfig{{
			Tag: "socks-in",
			ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
				PortList:       &xnet.PortList{Range: []*xnet.PortRange{xnet.SinglePortRange(xnet.Port(socksPort))}},
				Listen:         xnet.NewIPOrDomain(xnet.IPAddress(net.ParseIP("127.0.0.1"))),
				StreamSettings: &internet.StreamConfig{ProtocolName: "tcp"},
			}),
			ProxySettings: serial.ToTypedMessage(&xraysocks.ServerConfig{AuthType: xraysocks.AuthType_NO_AUTH}),
		}},
		Outbound: []*xraycore.OutboundHandlerConfig{{
			Tag: "vless-out",
			ProxySettings: serial.ToTypedMessage(&vlessout.Config{Vnext: &protocol.ServerEndpoint{
				Address: xnet.NewIPOrDomain(xnet.IPAddress(net.ParseIP("127.0.0.1"))),
				Port:    uint32(serverPort),
				User:    &protocol.User{Email: credID, Account: serial.ToTypedMessage(&xrayvless.Account{Id: userID, Flow: flow})},
			}}),
			SenderSettings: serial.ToTypedMessage(sender),
		}},
	}
	instance, err := xraycore.New(config)
	if err != nil {
		t.Fatalf("create in-process Xray client: %v", err)
	}
	if err := instance.Start(); err != nil {
		_ = instance.Close()
		t.Fatalf("start in-process Xray client: %v", err)
	}
	client := &xrayClient{instance: instance, socks: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(socksPort)))}
	t.Cleanup(func() { _ = instance.Close() })
	return client
}

func reservePort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return uint16(listener.Addr().(*net.TCPAddr).Port)
}

func (c *xrayClient) dial(t *testing.T, target string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", c.socks, 5*time.Second)
	if err != nil {
		t.Fatalf("connect to Xray SOCKS inbound: %v", err)
	}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	var method [2]byte
	if _, err := io.ReadFull(conn, method[:]); err != nil || method != [2]byte{5, 0} {
		_ = conn.Close()
		t.Fatalf("SOCKS method negotiation: %v %v", method, err)
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		_ = conn.Close()
		t.Fatalf("test destination is not IPv4: %q", target)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	request := []byte{5, 1, 0, 1}
	request = append(request, ip...)
	request = append(request, byte(port>>8), byte(port))
	if _, err := conn.Write(request); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil || reply[1] != 0 {
		_ = conn.Close()
		t.Fatalf("SOCKS CONNECT response=%v err=%v", reply, err)
	}
	addressLen := 4
	switch reply[3] {
	case 1:
		addressLen = 4
	case 4:
		addressLen = 16
	case 3:
		var n [1]byte
		if _, err := io.ReadFull(conn, n[:]); err != nil {
			_ = conn.Close()
			t.Fatal(err)
		}
		addressLen = int(n[0])
	default:
		_ = conn.Close()
		t.Fatalf("unknown SOCKS address type %d", reply[3])
	}
	if _, err := io.CopyN(io.Discard, conn, int64(addressLen+2)); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn
}

func exchange(conn net.Conn, payload []byte) (time.Duration, error) {
	start := time.Now()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	writeResult := make(chan error, 1)
	go func() {
		for written := 0; written < len(payload); {
			n, err := conn.Write(payload[written:])
			written += n
			if err != nil {
				writeResult <- err
				return
			}
		}
		writeResult <- nil
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		return time.Since(start), err
	}
	if err := <-writeResult; err != nil {
		return time.Since(start), err
	}
	if !bytes.Equal(got, payload) {
		return time.Since(start), errors.New("echo mismatch")
	}
	_ = conn.SetDeadline(time.Time{})
	return time.Since(start), nil
}

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((i % 251) + 1)
	}
	return b
}

func TestXrayClientTrafficCountersAndObserved(t *testing.T) {
	for _, transport := range []string{"tcp", "xhttp"} {
		t.Run(transport, func(t *testing.T) {
			f := newRuntimeFixture(t, false)
			spec := f.spec("node-a", transport)
			cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
			report, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred})
			if err != nil {
				t.Fatal(err)
			}
			if report.SpecHash != statehash.Spec(spec) {
				t.Fatalf("reported hash %q, want %q", report.SpecHash, statehash.Spec(spec))
			}
			if got := statehash.State(f.engine.Observed()); got != statehash.State([]statehash.Inbound{{Spec: spec, Creds: []plugin.UserCred{cred}}}) {
				t.Fatalf("Observed state hash %q differs from applied state", got)
			}

			echo := startEchoServer(t)
			client := newXrayClient(t, spec.Listen.Port, transport, cred.CredID, "66ad4540-b58c-4ad2-9926-ea63445a9b57")
			conn := client.dial(t, echo.Addr().String())
			defer conn.Close()
			const n = 2 << 20
			if _, err := exchange(conn, payload(n)); err != nil {
				failed, collectErr := f.engine.Collect(context.Background())
				t.Fatalf("exchange failed: %v; traffic: %+v collectErr=%v health=%+v slog: %s", err, failed.Traffic, collectErr, f.engine.Health(), f.logs.String())
			}
			collected, err := f.engine.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			up, down := trafficOf(collected, cred.CredID)
			if !withinPercent(up, n, 1) || !withinPercent(down, n, 1) {
				t.Fatalf("traffic counters swapped or out of tolerance: up=%d down=%d payload=%d", up, down, n)
			}
			if sessionsOf(collected, cred.CredID) != 1 {
				t.Fatalf("open link was not collected as a session: %+v", collected.Sessions)
			}
			next, err := f.engine.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(next.Traffic) != 0 {
				t.Fatalf("Collect replayed deltas: %+v", next.Traffic)
			}
		})
	}
}

func TestDestinationCloseEndsVLESSSession(t *testing.T) {
	for _, transport := range []string{"tcp", "xhttp"} {
		t.Run(transport, func(t *testing.T) {
			f := newRuntimeFixture(t, false)
			spec := f.spec("node-a", transport)
			cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
			if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
				t.Fatal(err)
			}
			destination := startCloseAfterResponseServer(t, []byte("response"))
			client := newXrayClient(t, spec.Listen.Port, transport, cred.CredID, stringID(cred))
			conn := client.dial(t, destination.Addr().String())
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			response, err := io.ReadAll(conn)
			if err != nil {
				t.Fatalf("read destination response to EOF: %v", err)
			}
			if string(response) != "response" {
				t.Fatalf("response = %q, want %q", response, "response")
			}
			if !waitForSessionEnd(f.engine, cred.CredID, 3*time.Second) {
				t.Fatal("Collect still listed the session after the destination closed")
			}
		})
	}
}

func startCloseAfterResponseServer(t *testing.T, response []byte) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_, _ = conn.Write(response)
		_ = conn.Close()
	}()
	return listener
}

func waitForSessionEnd(e *eng, credID string, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		collected, err := e.Collect(context.Background())
		if err == nil && sessionsOf(collected, credID) == 0 {
			return true
		}
		select {
		case <-deadline.C:
			return false
		case <-tick.C:
		}
	}
}

func TestApplyUsersOnlyKeepsAndRemovesSessions(t *testing.T) {
	f := newRuntimeFixture(t, false)
	spec := f.spec("node-a", "xhttp")
	alice := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	removed := testCredential("removed", "66ad4540-b58c-4ad2-9926-ea63445a9b58")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{alice, removed}); err != nil {
		t.Fatal(err)
	}
	server := f.engine.inbounds[spec.ID]
	echo := startEchoServer(t)
	aliceClient := newXrayClient(t, spec.Listen.Port, "xhttp", alice.CredID, stringID(alice))
	removedClient := newXrayClient(t, spec.Listen.Port, "xhttp", removed.CredID, stringID(removed))
	aliceConn := aliceClient.dial(t, echo.Addr().String())
	defer aliceConn.Close()
	removedConn := removedClient.dial(t, echo.Addr().String())
	defer removedConn.Close()
	if _, err := exchange(aliceConn, []byte("kept-before")); err != nil {
		t.Fatal(err)
	}
	if _, err := exchange(removedConn, []byte("removed-before")); err != nil {
		t.Fatal(err)
	}

	bob := testCredential("bob", "66ad4540-b58c-4ad2-9926-ea63445a9b59")
	report, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{alice, bob})
	if err != nil {
		t.Fatal(err)
	}
	if report.Restarted || f.engine.inbounds[spec.ID] != server {
		t.Fatalf("users-only Apply restarted inbound: report=%+v", report)
	}
	if _, err := exchange(aliceConn, []byte("kept-after")); err != nil {
		t.Fatalf("kept credential session stopped: %v", err)
	}
	assertConnectionEnds(t, removedConn)
	bobClient := newXrayClient(t, spec.Listen.Port, "xhttp", bob.CredID, stringID(bob))
	bobConn := bobClient.dial(t, echo.Addr().String())
	defer bobConn.Close()
	if _, err := exchange(bobConn, []byte("added")); err != nil {
		t.Fatalf("added credential cannot connect: %v", err)
	}
	if conn, err := removedClient.tryDial(echo.Addr().String()); err == nil {
		defer conn.Close()
		assertConnectionEnds(t, conn)
	}
}

func (c *xrayClient) tryDial(target string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", c.socks, time.Second)
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	var method [2]byte
	if _, err := io.ReadFull(conn, method[:]); err != nil || method[1] != 0 {
		_ = conn.Close()
		return nil, fmt.Errorf("SOCKS method: %v", err)
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	ip := net.ParseIP(host).To4()
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || ip == nil {
		_ = conn.Close()
		return nil, errors.New("invalid test destination")
	}
	request := append([]byte{5, 1, 0, 1}, ip...)
	request = append(request, byte(port>>8), byte(port))
	if _, err := conn.Write(request); err != nil {
		_ = conn.Close()
		return nil, err
	}
	var reply [4]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if reply[1] != 0 {
		_ = conn.Close()
		return nil, fmt.Errorf("SOCKS CONNECT status %d", reply[1])
	}
	addressLen := 4
	if reply[3] == 4 {
		addressLen = 16
	} else if reply[3] == 3 {
		var n [1]byte
		if _, err := io.ReadFull(conn, n[:]); err != nil {
			_ = conn.Close()
			return nil, err
		}
		addressLen = int(n[0])
	}
	if _, err := io.CopyN(io.Discard, conn, int64(addressLen+2)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func stringID(c plugin.UserCred) string {
	var data struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(c.Data, &data)
	return data.ID
}

func assertConnectionEnds(t *testing.T, conn net.Conn) {
	assertConnectionEndsWithin(t, conn, 5*time.Second)
}

func assertConnectionEndsWithin(t *testing.T, conn net.Conn, timeout time.Duration) {
	t.Helper()
	started := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	var b [1]byte
	_, err := conn.Read(b[:])
	if err == nil {
		t.Fatal("session remained open after kick or removal")
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatalf("session did not end before the 5 second deadline: %v", err)
	}
	t.Logf("session ended after %s", time.Since(started).Round(time.Millisecond))
	_ = conn.Close()
}

func assertReadDrainEndsWithin(t *testing.T, conn net.Conn, timeout time.Duration) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	_, err := io.Copy(io.Discard, conn)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			t.Fatalf("session did not end before the %s deadline: %v", timeout, err)
		}
	}
	_ = conn.Close()
}

func TestKickEndsIdleSessionsForBothTransports(t *testing.T) {
	for _, transport := range []string{"tcp", "xhttp"} {
		t.Run(transport, func(t *testing.T) {
			f := newRuntimeFixture(t, false)
			spec := f.spec("node-a", transport)
			cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
			if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
				t.Fatal(err)
			}
			echo := startEchoServer(t)
			client := newXrayClient(t, spec.Listen.Port, transport, cred.CredID, stringID(cred))
			conn := client.dial(t, echo.Addr().String())
			if _, err := exchange(conn, []byte("establish idle egress")); err != nil {
				t.Fatal(err)
			}
			if n, err := f.engine.Kick(context.Background(), []string{cred.CredID}); err != nil || n != 1 {
				t.Fatalf("Kick=(%d,%v), want one open session", n, err)
			}
			assertConnectionEnds(t, conn)
		})
	}
}

type manualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*manualTimer
}

type manualTimer struct {
	mu      sync.Mutex
	stopped bool
	f       func()
}

func (c *manualClock) After(d time.Duration, f func()) stoppable {
	t := &manualTimer{f: f}
	c.mu.Lock()
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	return t
}

func (t *manualTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := !t.stopped
	t.stopped = true
	return wasActive
}

func (c *manualClock) FireAll() {
	c.mu.Lock()
	timers := append([]*manualTimer(nil), c.timers...)
	c.mu.Unlock()
	for _, timer := range timers {
		timer.mu.Lock()
		if timer.stopped {
			timer.mu.Unlock()
			continue
		}
		timer.stopped = true
		f := timer.f
		timer.mu.Unlock()
		f()
	}
}

func TestExpiryRefusesPastAndEndsLiveSession(t *testing.T) {
	f := newRuntimeFixture(t, false)
	clock := &manualClock{now: time.Now().Truncate(time.Second)}
	f.engine.now = func() time.Time { clock.mu.Lock(); defer clock.mu.Unlock(); return clock.now }
	f.engine.after = clock.After
	spec := f.spec("node-a", "tcp")
	alice := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	alice.ValidUntil = clock.now.Add(time.Hour)
	passed := testCredential("passed", "66ad4540-b58c-4ad2-9926-ea63445a9b58")
	passed.ValidUntil = clock.now.Add(-time.Second)
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{alice, passed}); err != nil {
		t.Fatal(err)
	}
	echo := startEchoServer(t)
	aliceClient := newXrayClient(t, spec.Listen.Port, "tcp", alice.CredID, stringID(alice))
	conn := aliceClient.dial(t, echo.Addr().String())
	if _, err := exchange(conn, []byte("session before expiry")); err != nil {
		t.Fatal(err)
	}
	passedClient := newXrayClient(t, spec.Listen.Port, "tcp", passed.CredID, stringID(passed))
	if passedConn, err := passedClient.tryDial(echo.Addr().String()); err == nil {
		defer passedConn.Close()
		_ = passedConn.SetReadDeadline(time.Now().Add(time.Second))
		var b [1]byte
		if _, readErr := passedConn.Read(b[:]); readErr == nil {
			t.Fatal("expired credential reached the destination")
		}
	}
	clock.mu.Lock()
	clock.now = alice.ValidUntil
	clock.mu.Unlock()
	clock.FireAll()
	assertConnectionEnds(t, conn)
}

func TestRateLimitAppliesToVLESSPayload(t *testing.T) {
	f := newRuntimeFixture(t, false)
	spec := f.spec("node-a", "tcp")
	cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	cred.RateLimitBps = 8_000_000
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	echo := startEchoServer(t)
	client := newXrayClient(t, spec.Listen.Port, "tcp", cred.CredID, stringID(cred))
	conn := client.dial(t, echo.Addr().String())
	defer conn.Close()
	duration, err := exchange(conn, payload(4<<20))
	if err != nil {
		t.Fatal(err)
	}
	if duration < 2500*time.Millisecond || duration > 10*time.Second {
		t.Fatalf("4 MiB at 1 MiB/s completed in %v, outside the expected tolerance", duration)
	}
}

func TestPrivateDestinationBlockedThroughVLESSDispatch(t *testing.T) {
	f := newRuntimeFixture(t, true)
	spec := f.spec("node-a", "tcp")
	cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	private := startEchoServer(t)
	destination, err := xnet.ParseDestination("tcp:" + private.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	user := &protocol.MemoryUser{Email: cred.CredID}
	ctx := session.ContextWithInbound(context.Background(), &session.Inbound{User: user})
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: destination}})
	in := f.engine.inbounds[spec.ID]
	err = in.dispatch(ctx, &transport.Link{})
	if !errors.Is(err, egress.ErrBlocked) {
		t.Fatalf("VLESS dispatch error=%v, want blocked private destination", err)
	}
}

func TestWarpEgressFailureFailsClosedInHealth(t *testing.T) {
	f := newRuntimeFixture(t, false)
	f.engine.out = func(name string) (engine.Egress, error) {
		if name == "warp" {
			return nil, errors.New("egress warp: not configured on this node")
		}
		return localEgress{}, nil
	}
	spec := f.spec("node-a", "xhttp")
	spec.Egress = "warp"
	if _, err := f.engine.Apply(context.Background(), spec, nil); err == nil {
		t.Fatal("Apply succeeded despite failed WARP egress lookup")
	}
	health := f.engine.Health()
	if len(health) != 1 || health[0].State != plugin.RunFailed || !strings.Contains(health[0].Detail, "not configured") {
		t.Fatalf("WARP failure missing from Health: %+v", health)
	}
}

func TestSpecChangeRestartsOnlyOneInbound(t *testing.T) {
	f := newRuntimeFixture(t, false)
	a := f.spec("node-a", "xhttp")
	b := f.spec("node-b", "tcp")
	cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if _, err := f.engine.Apply(context.Background(), a, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.engine.Apply(context.Background(), b, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	oldA, oldB := f.engine.inbounds[a.ID], f.engine.inbounds[b.ID]
	echo := startEchoServer(t)
	clientB := newXrayClient(t, b.Listen.Port, "tcp", cred.CredID, stringID(cred))
	connB := clientB.dial(t, echo.Addr().String())
	defer connB.Close()
	if _, err := exchange(connB, []byte("survive other inbound restart")); err != nil {
		t.Fatal(err)
	}
	a.Settings = json.RawMessage(strings.Replace(string(a.Settings), `"/q8x2kd7w"`, `"/changed"`, 1))
	report, err := f.engine.Apply(context.Background(), a, []plugin.UserCred{cred})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Restarted || f.engine.inbounds[a.ID] == oldA || f.engine.inbounds[b.ID] != oldB {
		t.Fatalf("spec change restart scope wrong: report=%+v A-replaced=%v B-preserved=%v", report, f.engine.inbounds[a.ID] != oldA, f.engine.inbounds[b.ID] == oldB)
	}
	if _, err := exchange(connB, []byte("other inbound still running")); err != nil {
		t.Fatalf("unmodified inbound session stopped: %v", err)
	}
}

func TestXrayStartupAndTrafficDoNotWriteStdout(t *testing.T) {
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	defer func() { os.Stdout = oldStdout; _ = writer.Close(); _ = reader.Close() }()

	f := newRuntimeFixture(t, false)
	spec := f.spec("node-a", "tcp")
	cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	echo := startEchoServer(t)
	client := newXrayClient(t, spec.Listen.Port, "tcp", cred.CredID, stringID(cred))
	conn := client.dial(t, echo.Addr().String())
	if _, err := exchange(conn, []byte("quiet startup")); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	_ = client.instance.Close()
	_ = f.engine.Close(context.Background())
	_ = writer.Close()
	os.Stdout = oldStdout
	output, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 0 {
		t.Fatalf("Xray wrote to stdout during build/start/traffic: %q", output)
	}
}

func withinPercent(got uint64, want int, percent uint64) bool {
	delta := uint64(want) * percent / 100
	target := uint64(want)
	return got+delta >= target && got <= target+delta
}

func trafficOf(collected engine.Collected, credID string) (up, down uint64) {
	for _, traffic := range collected.Traffic {
		if traffic.CredID == credID {
			up += traffic.Up
			down += traffic.Down
		}
	}
	return
}

func sessionsOf(collected engine.Collected, credID string) int {
	n := 0
	for _, session := range collected.Sessions {
		if session.CredID == credID {
			n++
		}
	}
	return n
}
