//go:build !js

package health

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/node/engine"
	panelvless "github.com/mistgate/mistgate/internal/panel/protocols/vless"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

type loopbackVLESSCerts struct {
	certificate *tls.Certificate
	info        engine.CertInfo
}

func (c *loopbackVLESSCerts) Acquire(context.Context, string, plugin.TLS) (engine.Cert, error) {
	return engine.Cert{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return c.certificate, nil },
		Info:           func() engine.CertInfo { return c.info },
	}, nil
}

func (*loopbackVLESSCerts) Release(string) {}

type vlessProbeRig struct {
	e      *env
	probe  *probeServer
	target *target
	creds  []plugin.UserCred
	pin    []byte
}

func newVLESSProbeRig(t *testing.T, security, transport string) *vlessProbeRig {
	t.Helper()
	probe := newProbeServer(t)
	_, port, err := net.SplitHostPort(probe.srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	probe.url204 = "http://127.0.0.1:" + port + "/generate_204"
	probe.urlTr = "http://127.0.0.1:" + port + "/cdn-cgi/trace"
	e := newEnv(t, func(c *Config) {
		c.Probe204URL, c.ProbeTraceURL = probe.url204, probe.urlTr
		c.HandshakeTimeout = 8 * time.Second
		c.ProbeTimeout = 10 * time.Second
	})
	e.node("de1", "provider", true)
	e.fl.set("de1", liveState{up: true, caps: []string{capDoctor, "vless/1", "vless-self/1", "vless-tls/1"}, lastSeen: e.clock.Now()})

	settingsRaw, err := panelvless.New().DefaultSettings()
	if err != nil {
		t.Fatal(err)
	}
	var settings panelvless.Settings
	if err := json.Unmarshal(settingsRaw, &settings); err != nil {
		t.Fatal(err)
	}
	settings.Security, settings.Transport = security, transport
	certName := "de1.example.com"
	if security == "tls" {
		certName = "origin.example.com"
		settings.TLSMode, settings.SNI = "acme_domain", certName
		settings.XHTTP.Host = ""
		if transport == "ws" {
			settings.WS = &panelvless.WSSettings{Path: "/socket", Host: ""}
		}
	}
	settingsRaw, err = json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := e.acc.CreateProfile(e.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{
		Protocol: "vless", Name: "vless-health-" + security + "-" + transport, SettingsJson: string(settingsRaw),
	}))
	if err != nil {
		t.Fatal(err)
	}
	portNumber := freeVLESSTCPPort(t)
	inbound, err := e.acc.CreateInbound(e.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{
		ProfileId: profile.Msg.Profile.Id, NodeId: "de1", PortOverride: uint32(portNumber),
	}))
	if err != nil {
		t.Fatal(err)
	}
	inboundID := inbound.Msg.Inbound.Id
	e.exec(`UPDATE inbound SET state = 'active' WHERE id = ?`, inboundID)
	e.s.invalidateSnapshot()
	sn, err := e.s.snapshot(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	target := sn.targets[inboundID]
	if target == nil || target.err != nil {
		t.Fatalf("panel could not build the VLESS target: %+v", target)
	}

	desired, err := e.acc.Desired(e.ctx, "de1")
	if err != nil {
		t.Fatal(err)
	}
	state := make([]statehash.Inbound, len(desired))
	for i := range desired {
		state[i] = statehash.Inbound(desired[i])
	}
	withProbe := e.s.WithProbeCreds(e.ctx, state)
	var creds []plugin.UserCred
	for _, item := range withProbe {
		if item.Spec.ID == inboundID {
			creds = slices.Clone(item.Creds)
		}
	}
	if len(creds) != 1 {
		t.Fatalf("VLESS inbound received %d probe credentials", len(creds))
	}

	certificate := newLoopbackVLESSCertificate(t, certName)
	sum := sha256.Sum256(certificate.Certificate[0])
	// The node engine is Xray too: it runs in a child process, so the panel's test process holds no Xray code at all.
	report := startVLESSTestServer(t, target.spec, creds, certificate)
	rig := &vlessProbeRig{e: e, probe: probe, target: target, creds: creds, pin: sum[:]}
	e.exec(`UPDATE inbound SET cert_pin_sha256 = ?, cert_not_after = ? WHERE id = ?`, report.PinSHA256, report.NotAfter, inboundID)
	// The profile and its REALITY site were built from the public node name. The listening endpoint itself is loopback here.
	target.node.Address = "127.0.0.1"
	target.in.CertPin = report.PinSHA256
	return rig
}

func newLoopbackVLESSCertificate(t *testing.T, name string) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		DNSNames: []string{name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: parsed}
}

func freeVLESSTCPPort(t *testing.T) uint16 {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(listener.Addr().(*net.TCPAddr).Port)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func (r *vlessProbeRig) openClient(t *testing.T, testPin []byte) Tunnel {
	return r.openClientWithSecret(t, "", testPin)
}

func (r *vlessProbeRig) openClientWithSecret(t *testing.T, secret string, testPin []byte) Tunnel {
	t.Helper()
	row, actualSecret, err := r.e.s.probeCredSecret(r.e.ctx, r.target)
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" {
		secret = actualSecret
	}
	client, err := dialVLESS(r.e.ctx, Target{
		Node: r.target.node, Inbound: r.target.in, Spec: r.target.spec, Settings: r.target.settings,
		CredID: row.CredID, Secret: secret,
	}, DialOptions{HandshakeTimeout: r.e.s.cfg.HandshakeTimeout, testTLSPin: testPin})
	if err != nil {
		t.Fatalf("dialVLESS: %v", err)
	}
	return client
}

func (r *vlessProbeRig) runProbe(t *testing.T, testPin []byte) Result {
	t.Helper()
	client := r.openClient(t, testPin)
	defer client.Close()
	return r.e.s.probe(r.e.ctx, client, r.target.node, time.Now())
}

func (r *vlessProbeRig) runProbeWithSecret(t *testing.T, secret string, testPin []byte) (Result, *vlessTunnel) {
	t.Helper()
	client, ok := r.openClientWithSecret(t, secret, testPin).(*vlessTunnel)
	if !ok {
		t.Fatal("dialVLESS returned an unexpected tunnel type")
	}
	result := r.e.s.probe(r.e.ctx, client, r.target.node, time.Now())
	if err := client.Close(); err != nil {
		t.Fatalf("close VLESS helper: %v", err)
	}
	return result, client
}

func (r *vlessProbeRig) runProbeWithTarget(t *testing.T, target Target, testPin []byte) Result {
	t.Helper()
	client, err := dialVLESS(r.e.ctx, target, DialOptions{HandshakeTimeout: r.e.s.cfg.HandshakeTimeout, testTLSPin: testPin})
	if err != nil {
		t.Fatalf("dialVLESS: %v", err)
	}
	defer client.Close()
	return r.e.s.probe(r.e.ctx, client, r.target.node, time.Now())
}

func (r *vlessProbeRig) warmTunnel(t *testing.T, testPin []byte) {
	t.Helper()
	client := r.openClient(t, testPin)
	conn, err := client.DialContext(r.e.ctx, "tcp", r.probe.srv.Listener.Addr().String())
	if err != nil {
		_ = client.Close()
		t.Fatalf("VLESS connection: %v", err)
	}
	if _, err := fmt.Fprintf(conn, "GET /generate_204 HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n"); err != nil {
		_ = conn.Close()
		_ = client.Close()
		t.Fatalf("write through VLESS tunnel: %v", err)
	}
	response, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.HasPrefix(response, "HTTP/1.1 204") {
		_ = conn.Close()
		_ = client.Close()
		t.Fatalf("read through VLESS tunnel: %q, %v", response, err)
	}
	_ = conn.Close()
	if err := client.Close(); err != nil {
		t.Fatalf("close VLESS tunnel: %v", err)
	}
}

func TestVLESSProbeAgainstREALITYInbound(t *testing.T) {
	rig := newVLESSProbeRig(t, "reality", "tcp")
	rig.warmTunnel(t, nil)
	result := rig.runProbe(t, nil)
	if result.Status != cOK || result.ExitIP != "203.0.113.9" || result.ExitCountry != "DE" || result.ErrorCode != "" {
		t.Fatalf("REALITY probe result: %+v", result)
	}
}

func TestVLESSProbeAgainstREALITYXHTTPInbound(t *testing.T) {
	rig := newVLESSProbeRig(t, "reality", "xhttp")
	rig.warmTunnel(t, nil)
	result := rig.runProbe(t, nil)
	if result.Status != cOK || result.ExitIP != "203.0.113.9" || result.ExitCountry != "DE" || result.ErrorCode != "" {
		t.Fatalf("REALITY XHTTP probe result: %+v", result)
	}
}

func TestVLESSProbeAgainstTLSXHTTPInbound(t *testing.T) {
	rig := newVLESSProbeRig(t, "tls", "xhttp")
	rig.warmTunnel(t, rig.pin)
	result := rig.runProbe(t, rig.pin)
	if result.Status != cOK || result.ExitIP != "203.0.113.9" || result.ExitCountry != "DE" || result.ErrorCode != "" {
		t.Fatalf("TLS probe result: %+v", result)
	}
}

func TestVLESSXHTTPHelperProcessesDoNotLeak(t *testing.T) {
	rig := newVLESSProbeRig(t, "tls", "xhttp")
	rig.warmTunnel(t, rig.pin)
	// Each round owns a separate client process. Retaining the handles here makes the exit check explicit.
	var helpers []*vlessTunnel
	original := rig.e.s.cfg.Dialers["vless"]
	rig.e.s.cfg.Dialers["vless"] = func(ctx context.Context, target Target, options DialOptions) (Tunnel, error) {
		options.testTLSPin = rig.pin
		tunnel, err := original(ctx, target, options)
		if err == nil {
			helper, ok := tunnel.(*vlessTunnel)
			if !ok {
				return nil, errors.New("unexpected VLESS tunnel type")
			}
			helpers = append(helpers, helper)
		}
		return tunnel, err
	}
	before := stableVLESSGoroutines()
	for range 30 {
		result, skipped := rig.e.s.attempt(rig.e.ctx, rig.target)
		if skipped || result.Status != cOK {
			t.Fatalf("TLS XHTTP round: skipped=%v result=%+v", skipped, result)
		}
	}
	after := stableVLESSGoroutines()
	for i, helper := range helpers {
		select {
		case <-helper.done:
		default:
			t.Fatalf("helper %d remained running after its round", i)
		}
		// Under -race the helper is race-instrumented and Xray's XHTTP client has known races: the helper reports them on its own
		// stderr and exits 66, which is the point of keeping that code out of the panel process. A kill is never acceptable.
		state := helper.cmd.ProcessState
		if state == nil || !(state.Success() || (raceBuild && state.ExitCode() == 66)) {
			t.Fatalf("helper %d was not reaped successfully: %+v", i, helper.cmd.ProcessState)
		}
	}
	if len(helpers) != 30 {
		t.Fatalf("started %d helpers, want 30", len(helpers))
	}
	t.Logf("30 TLS+XHTTP rounds: helpers exited=%d, panel goroutines %d -> %d", len(helpers), before, after)
	if after > before+2 {
		t.Fatalf("30 VLESS helper rounds left panel goroutines behind: before=%d after=%d", before, after)
	}
}

func TestVLESSProbeAgainstTLSWebSocketInbound(t *testing.T) {
	rig := newVLESSProbeRig(t, "tls", "ws")
	rig.warmTunnel(t, rig.pin)
	result := rig.runProbe(t, rig.pin)
	if result.Status != cOK || result.ExitIP != "203.0.113.9" || result.ExitCountry != "DE" || result.ErrorCode != "" {
		t.Fatalf("TLS WebSocket probe result: %+v", result)
	}
}

func TestVLESSWrongUUIDIsClassifiedAsAuth(t *testing.T) {
	rig := newVLESSProbeRig(t, "tls", "xhttp")
	wrongUUID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	result, helper := rig.runProbeWithSecret(t, wrongUUID, rig.pin)
	if result.Status != cFail || result.ErrorCode != "auth" {
		t.Fatalf("wrong UUID result: %+v", result)
	}
	if strings.Contains(helper.stderr.String(), wrongUUID) {
		t.Fatal("helper stderr exposed the VLESS UUID")
	}
}

func TestVLESSTLSRejectsUnpinnedCertificate(t *testing.T) {
	rig := newVLESSProbeRig(t, "tls", "xhttp")
	result := rig.runProbe(t, nil)
	if result.Status != cFail || result.ErrorCode != "tls" {
		t.Fatalf("unpinned TLS certificate result: %+v", result)
	}
}

func TestVLESSClosedPortIsRefused(t *testing.T) {
	rig := newVLESSProbeRig(t, "tls", "xhttp")
	row, secret, err := rig.e.s.probeCredSecret(rig.e.ctx, rig.target)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{
		Node: rig.target.node, Inbound: rig.target.in, Spec: rig.target.spec, Settings: rig.target.settings,
		CredID: row.CredID, Secret: secret,
	}
	target.Spec.Listen.Port = freeVLESSTCPPort(t)
	result := rig.runProbeWithTarget(t, target, rig.pin)
	if result.Status != cFail || result.ErrorCode != "refused" {
		t.Fatalf("closed port result: %+v", result)
	}
	// The target port is not listening, so Xray's outbound dial is refused before it can reach TLS or VLESS auth.
}

func TestVLESSHelperCrashUsesSanitizedFailure(t *testing.T) {
	rig := newVLESSProbeRig(t, "tls", "xhttp")
	t.Setenv(vlessHelperTestModeEnv, "crash")
	row, secret, err := rig.e.s.probeCredSecret(rig.e.ctx, rig.target)
	if err != nil {
		t.Fatal(err)
	}
	_, err = dialVLESS(rig.e.ctx, Target{
		Node: rig.target.node, Inbound: rig.target.in, Spec: rig.target.spec, Settings: rig.target.settings,
		CredID: row.CredID, Secret: secret,
	}, DialOptions{HandshakeTimeout: rig.e.s.cfg.HandshakeTimeout, testTLSPin: rig.pin})
	var probeErr *ProbeError
	if !errors.As(err, &probeErr) || probeErr.Code != "tls" || probeErr.Detail != "VLESS TLS verification failed" {
		t.Fatalf("crashed helper error = %v", err)
	}
}

func TestVLESSHelperCloseKillsProcessThatIgnoresStdin(t *testing.T) {
	rig := newVLESSProbeRig(t, "tls", "xhttp")
	t.Setenv(vlessHelperTestModeEnv, "hang")
	row, secret, err := rig.e.s.probeCredSecret(rig.e.ctx, rig.target)
	if err != nil {
		t.Fatal(err)
	}
	client, err := dialVLESS(rig.e.ctx, Target{
		Node: rig.target.node, Inbound: rig.target.in, Spec: rig.target.spec, Settings: rig.target.settings,
		CredID: row.CredID, Secret: secret,
	}, DialOptions{HandshakeTimeout: rig.e.s.cfg.HandshakeTimeout, testTLSPin: rig.pin})
	if err != nil {
		t.Fatalf("start fake hanging helper: %v", err)
	}
	helper := client.(*vlessTunnel)
	if err := client.Close(); err != nil {
		t.Fatalf("close hanging helper: %v", err)
	}
	select {
	case <-helper.done:
	default:
		t.Fatal("hanging helper process was not reaped")
	}
	if helper.cmd.ProcessState == nil {
		t.Fatal("hanging helper has no process exit state")
	}
}

// stableVLESSGoroutines counts the goroutines of the panel's test process. The loopback VLESS server and the client
// helpers all run in child processes, so nothing of Xray is in here.
func stableVLESSGoroutines() int {
	for i := 0; i < 12; i++ {
		runtime.GC()
		runtime.Gosched()
	}
	return runtime.NumGoroutine()
}

func TestClassifyVLESSFailure(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	cases := []struct {
		name   string
		err    error
		ctx    context.Context
		stderr string
		code   string
		known  bool
	}{
		{"x509 unknown authority", io.EOF, context.Background(), `[Info] transport/internet/splithttp: failed to GET > tls: failed to verify certificate: x509: certificate signed by unknown authority`, "tls", true},
		{"tls alert", io.EOF, context.Background(), `[Error] REALITY: remote error: tls: handshake failure`, "tls", true},
		{"server rejected the user", io.EOF, context.Background(), `[Warning] proxy/vless/inbound: invalid request user id`, "auth", true},
		{"server hung up after the request", io.EOF, context.Background(), `[Info] [1] proxy/vless/outbound: tunneling request to tcp:a:1 via b:2`, "auth", true},
		{"unmarked eof stays generic", io.EOF, context.Background(), `[Warning] core: Xray started`, "refused", false},
		{"linux refused", io.EOF, context.Background(), `[Info] failed > dial tcp 192.0.2.1:443: connect: connection refused`, "refused", true},
		{"windows refused", io.EOF, context.Background(), `[Error] failed to dial > connectex: No connection could be made because the target machine actively refused it.`, "refused", true},
		{"unreachable", errors.New("x"), context.Background(), `[Error] dial tcp: connect: network is unreachable`, "refused", true},
		{"dial timeout", errors.New("x"), context.Background(), `[Error] dial tcp 192.0.2.1:443: i/o timeout`, "timeout", true},
		{"deadline error", context.DeadlineExceeded, context.Background(), ``, "timeout", true},
		{"round context expired", io.EOF, expired, ``, "timeout", true},
		{"unknown failure", errors.New("boom"), context.Background(), ``, "refused", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pe, known := classifyVLESSFailure(tc.err, tc.ctx, tc.stderr)
			if pe.Code != tc.code || known != tc.known {
				t.Fatalf("classify = %s (known %v), want %s (known %v)", pe.Code, known, tc.code, tc.known)
			}
			if strings.Contains(pe.Detail, "tcp:") || strings.Contains(pe.Detail, "x509") {
				t.Fatalf("detail leaks Xray text: %q", pe.Detail)
			}
		})
	}
}

// TestVLESSSilentServerIsATimeout: the server accepts the connection and never answers (a firewall that swallows packets
// after the handshake looks the same). Nothing refuses and nothing fails, so the only honest label is a timeout.
func TestVLESSSilentServerIsATimeout(t *testing.T) {
	rig := newVLESSProbeRig(t, "tls", "xhttp")
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var held []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = silent.Close()
		mu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		mu.Unlock()
	})
	row, secret, err := rig.e.s.probeCredSecret(rig.e.ctx, rig.target)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Node: rig.target.node, Inbound: rig.target.in, Spec: rig.target.spec, Settings: rig.target.settings, CredID: row.CredID, Secret: secret}
	target.Spec.Listen.Port = uint16(silent.Addr().(*net.TCPAddr).Port)
	ctx, cancel := context.WithTimeout(rig.e.ctx, 4*time.Second)
	defer cancel()
	client, err := dialVLESS(ctx, target, DialOptions{HandshakeTimeout: 2 * time.Second, testTLSPin: rig.pin})
	if err != nil {
		t.Fatalf("dialVLESS: %v", err)
	}
	defer client.Close()
	result := rig.e.s.probe(ctx, client, rig.target.node, time.Now())
	if result.Status != cFail || result.ErrorCode != "timeout" {
		t.Fatalf("silent server result: %+v", result)
	}
}

func TestVLESSHelperThatNeverReportsAListenerIsKilledWithTheRound(t *testing.T) {
	rig := newVLESSProbeRig(t, "tls", "xhttp")
	t.Setenv(vlessHelperTestModeEnv, "silent")
	row, secret, err := rig.e.s.probeCredSecret(rig.e.ctx, rig.target)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(rig.e.ctx, 700*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = dialVLESS(ctx, Target{
		Node: rig.target.node, Inbound: rig.target.in, Spec: rig.target.spec, Settings: rig.target.settings,
		CredID: row.CredID, Secret: secret,
	}, DialOptions{HandshakeTimeout: rig.e.s.cfg.HandshakeTimeout, testTLSPin: rig.pin})
	var probeErr *ProbeError
	if !errors.As(err, &probeErr) || probeErr.Code != "timeout" || time.Since(started) > 3*time.Second {
		t.Fatalf("silent helper: err=%v after %v", err, time.Since(started))
	}
}
