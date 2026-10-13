package vless

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/xtls/xray-core/app/dispatcher"
	"github.com/xtls/xray-core/app/policy"
	"github.com/xtls/xray-core/app/proxyman"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	xraycore "github.com/xtls/xray-core/core"
	xraysocks "github.com/xtls/xray-core/proxy/socks"
	xrayvless "github.com/xtls/xray-core/proxy/vless"
	vlessout "github.com/xtls/xray-core/proxy/vless/outbound"
	"github.com/xtls/xray-core/transport/internet"
	"github.com/xtls/xray-core/transport/internet/splithttp"
	xraytls "github.com/xtls/xray-core/transport/internet/tls"
	websocketcfg "github.com/xtls/xray-core/transport/internet/websocket"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

const tlsTestServerName = "tls.example.com"

func newTLSRuntimeFixture(t *testing.T) (*runtimeFixture, *fakeCerts) {
	t.Helper()
	oldBindIP := BindIP
	BindIP = net.ParseIP("127.0.0.1")
	t.Cleanup(func() { BindIP = oldBindIP })
	certs := newFakeCerts(t, tlsTestServerName)
	logs := &lockedLogBuffer{}
	env := engine.Env{
		Log: slogForTest(logs), Certs: certs,
		Egress: func(name string) (engine.Egress, error) {
			if name != "" && name != "direct" {
				return nil, fmt.Errorf("egress %q is not available", name)
			}
			return localEgress{}, nil
		},
	}
	instance, err := New(env)
	if err != nil {
		t.Fatal(err)
	}
	eng := instance.(*eng)
	t.Cleanup(func() { _ = eng.Close(context.Background()) })
	return &runtimeFixture{engine: eng, logs: logs}, certs
}

func slogForTest(logs *lockedLogBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(logs, nil))
}

func (f *runtimeFixture) tlsSpec(id, transport, mode string) plugin.InboundSpec {
	path := "/q8x2kd7w"
	wsPath := ""
	if transport == "ws" {
		wsPath = path
	}
	settings := fmt.Sprintf(`{"transport":%q,"security":"tls","flow":"","reality":{"target":"","server_names":null,"short_ids":null,"private_key":""},"xhttp":{"path":%q,"host":"","mode":%q,"x_padding_bytes":"100-200"},"ws":{"path":%q,"host":""}}`, transport, path, mode, wsPath)
	return plugin.InboundSpec{
		ID: id, Protocol: Protocol, Enabled: true,
		Listen:   plugin.Listen{Network: "tcp", Port: freeTCPPort(f.engine, nil)},
		TLS:      plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: tlsTestServerName},
		Settings: json.RawMessage(settings),
	}
}

func TestTLSFrontALPNAndACMEChallenge(t *testing.T) {
	f, certs := newTLSRuntimeFixture(t)
	certs.setIssued()
	spec := f.tlsSpec("tls-front", "xhttp", "auto")
	if _, err := f.engine.Apply(context.Background(), spec, nil); err != nil {
		t.Fatal(err)
	}

	for _, protocol := range []string{"h2", "http/1.1", "acme-tls/1"} {
		t.Run(protocol, func(t *testing.T) {
			conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.Listen.Port))), &tls.Config{
				ServerName: tlsTestServerName, InsecureSkipVerify: true, NextProtos: []string{protocol},
			})
			if err != nil {
				t.Fatalf("TLS handshake: %v", err)
			}
			defer conn.Close()
			if got := conn.ConnectionState().NegotiatedProtocol; got != protocol {
				t.Fatalf("negotiated ALPN %q, want %q", got, protocol)
			}
			if protocol == "acme-tls/1" {
				if got := conn.ConnectionState().PeerCertificates[0].Subject.CommonName; !strings.Contains(got, "challenge") {
					t.Fatalf("ACME ALPN presented %q, want the challenge certificate", got)
				}
			}
		})
	}
	hellos := certs.acmeHellos()
	found := false
	for _, hello := range hellos {
		if len(hello.SupportedProtos) == 1 && hello.SupportedProtos[0] == "acme-tls/1" {
			found = true
		}
	}
	if !found {
		t.Fatal("certificate source did not receive the acme-tls/1 ClientHello")
	}
}

func TestXrayClientTLSFrontTransportsAndTraffic(t *testing.T) {
	for _, tc := range []struct {
		transport string
		mode      string
		alpn      string
	}{
		{transport: "xhttp", mode: "stream-up", alpn: "h2"},
		{transport: "xhttp", mode: "packet-up", alpn: "h2"},
		{transport: "ws", alpn: "http/1.1"},
	} {
		t.Run(tc.transport+"/"+tc.mode, func(t *testing.T) {
			f, certs := newTLSRuntimeFixture(t)
			certs.setIssued()
			spec := f.tlsSpec("tls-"+tc.transport+"-"+tc.mode, tc.transport, tc.mode)
			cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
			if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
				t.Fatal(err)
			}
			echo := startEchoServer(t)
			client := newXrayTLSClient(t, spec.Listen.Port, tc.transport, tc.mode, tc.alpn, cred.CredID, stringID(cred))
			conn := client.dial(t, echo.Addr().String())
			defer conn.Close()
			const size = 64 << 10
			if _, err := exchange(conn, payload(size)); err != nil {
				t.Fatalf("TLS %s exchange: %v; health=%+v logs=%s", tc.transport, err, f.engine.Health(), f.logs.String())
			}
			collected, err := f.engine.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			up, down := trafficOf(collected, cred.CredID)
			if !withinPercent(up, size, 1) || !withinPercent(down, size, 1) {
				t.Fatalf("traffic counters up=%d down=%d, want %d in both directions", up, down, size)
			}
		})
	}
}

func TestTLSCertificateRotationKeepsLiveSessionAndUpdatesHealth(t *testing.T) {
	f, certs := newTLSRuntimeFixture(t)
	certs.setIssued()
	spec := f.tlsSpec("tls-rotation", "xhttp", "stream-up")
	cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	echo := startEchoServer(t)
	client := newXrayTLSClient(t, spec.Listen.Port, "xhttp", "stream-up", "h2", cred.CredID, stringID(cred))
	conn := client.dial(t, echo.Addr().String())
	defer conn.Close()
	if _, err := exchange(conn, []byte("before certificate rotation")); err != nil {
		t.Fatal(err)
	}
	before := f.engine.Health()[0].CertPinSHA256
	rotated := chainCert(t, tlsTestServerName, false)
	certs.mu.Lock()
	certs.acmeChain = rotated
	certs.mu.Unlock()
	health := f.engine.Health()
	wantCert := infoOfCert(rotated)
	if len(health) != 1 || health[0].CertPinSHA256 == before || health[0].CertPinSHA256 != wantCert.PinSHA256 || !health[0].CertNotAfter.Equal(wantCert.NotAfter) {
		t.Fatalf("health did not report the rotated certificate: before=%q health=%+v", before, health)
	}
	rotatedConn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.Listen.Port))), &tls.Config{
		ServerName: tlsTestServerName, InsecureSkipVerify: true, NextProtos: []string{"h2"},
	})
	if err != nil {
		t.Fatalf("new TLS handshake after rotation: %v", err)
	}
	servedPin := sha256.Sum256(rotatedConn.ConnectionState().PeerCertificates[0].Raw)
	_ = rotatedConn.Close()
	if hex.EncodeToString(servedPin[:]) != wantCert.PinSHA256 {
		t.Fatalf("new handshake served pin %q, want %q", hex.EncodeToString(servedPin[:]), wantCert.PinSHA256)
	}
	if _, err := exchange(conn, []byte("existing TLS session after rotation")); err != nil {
		t.Fatalf("live session dropped during certificate rotation: %v", err)
	}
}

func newXrayTLSClient(t *testing.T, serverPort uint16, transport, mode, alpn, credID, userID string) *xrayClient {
	t.Helper()
	socksPort := reservePort(t)
	transportName := "splithttp"
	var transportSettings []*internet.TransportConfig
	if transport == "ws" {
		transportName = "websocket"
		transportSettings = []*internet.TransportConfig{{
			ProtocolName: "websocket",
			Settings:     serial.ToTypedMessage(&websocketcfg.Config{Path: "/q8x2kd7w"}),
		}}
	} else {
		transportSettings = []*internet.TransportConfig{{
			ProtocolName: "splithttp",
			Settings: serial.ToTypedMessage(&splithttp.Config{
				Path: "/q8x2kd7w", Mode: mode, XPaddingBytes: &splithttp.RangeConfig{From: 100, To: 200},
			}),
		}}
	}
	stream := &internet.StreamConfig{
		ProtocolName:      transportName,
		TransportSettings: transportSettings,
		SecurityType:      serial.GetMessageType(&xraytls.Config{}),
		SecuritySettings: []*serial.TypedMessage{serial.ToTypedMessage(&xraytls.Config{
			AllowInsecure: true, ServerName: tlsTestServerName, Fingerprint: "chrome", NextProtocol: []string{alpn},
		})},
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
				User: &protocol.User{Email: credID, Account: serial.ToTypedMessage(&xrayvless.Account{
					Id: userID,
				})},
			}}),
			SenderSettings: serial.ToTypedMessage(&proxyman.SenderConfig{StreamSettings: stream}),
		}},
	}
	instance, err := xraycore.New(config)
	if err != nil {
		t.Fatalf("create in-process TLS Xray client: %v", err)
	}
	if err := instance.Start(); err != nil {
		_ = instance.Close()
		t.Fatalf("start in-process TLS Xray client: %v", err)
	}
	client := &xrayClient{instance: instance, socks: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(socksPort)))}
	t.Cleanup(func() { _ = instance.Close() })
	return client
}

func BenchmarkTLSFrontCopy(b *testing.B) {
	const chunkSize = 64 << 10
	frontClient, left := io.Pipe()
	right, frontUpstream := io.Pipe()
	copyDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(frontUpstream, frontClient)
		_ = frontUpstream.Close()
		copyDone <- err
	}()
	buffer := make([]byte, chunkSize)
	readDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, right)
		readDone <- err
	}()
	b.SetBytes(chunkSize)
	b.ResetTimer()
	for range b.N {
		if _, err := left.Write(buffer); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	_ = left.Close()
	if err := <-copyDone; err != nil {
		b.Fatal(err)
	}
	if err := <-readDone; err != nil {
		b.Fatal(err)
	}
}
