package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	nodebridge "github.com/mistgate/mistgate/internal/node/bridge"
	"github.com/mistgate/mistgate/internal/node/sitehttp"
	"github.com/mistgate/mistgate/internal/plugin"
)

func TestPanelSessionFallsBackThroughBridgeAndReturnsAfterTwoDirectProbes(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	entry := nodebridge.NewEntry()
	bridgeServerCert, pin := issueBridgeSiteCertificate(t, h.panel)
	if err := entry.SetServe(nodebridge.ServeConfig{EntryNodeID: "nod_msk1", Path: "/private-bridge-path", ExitNodeIDs: []string{h.a.meta.NodeID},
		PanelVia: true, PanelAddress: h.panel.addr(), Roots: h.a.id.Load().ca}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(entry.Handler(http.NotFoundHandler()))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{bridgeServerCert}, MinVersion: tls.VersionTLS13}
	server.Config.TLSConfig = server.TLS
	if err := sitehttp.ConfigureSiteHTTP2(server.Config); err != nil {
		t.Fatal(err)
	}
	server.StartTLS()
	t.Cleanup(func() {
		entry.Close()
		server.Close()
	})

	_, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	proofIdentity := issueBridgePeerIdentity(t, h.panel, h.a.meta.NodeID)
	dial := plugin.BridgeDial{EntryNodeID: "nod_msk1", Host: "127.0.0.1", Port: uint16(port), SNI: "msk1.example.com", PinSHA256: pin,
		Path: "/private-bridge-path", Pool: 2, PanelVia: true,
		Targets: []plugin.BridgeTarget{{RelayID: "rly_a", Network: "tcp", Port: 4443}}}
	dialer := nodebridge.NewDialer(func() nodebridge.DialIdentity { return proofIdentity })
	h.a.bridgeDialer.Close()
	h.a.bridgeDialer = dialer
	dialer.Sync(context.Background(), []nodebridge.DialSpec{{Dial: dial}})
	t.Cleanup(dialer.Close)
	waitPanelBridgeConns(t, dialer, dial.EntryNodeID)

	m := newModel()
	m.bridgeDials = []plugin.BridgeDial{dial}
	h.a.setModel(m)
	var failedDirect atomic.Int32
	var directReady atomic.Bool
	h.a.directDialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if !directReady.Load() {
			failedDirect.Add(1)
			return nil, errors.New("direct panel path unavailable")
		}
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, address)
	}
	h.a.bridgeFallbackAfter = time.Hour
	h.a.bridgeFallbackAttempts = 3
	h.a.bridgeProbeEvery = 10 * time.Minute
	h.a.backoffMin, h.a.backoffMax = 10*time.Millisecond, 20*time.Millisecond
	probeRequests := make(chan time.Duration, 3)
	probeAdvances := make(chan struct{}, 3)
	h.a.bridgeWait = func(ctx context.Context, delay time.Duration) bool {
		select {
		case probeRequests <- delay:
		case <-ctx.Done():
			return false
		}
		select {
		case <-probeAdvances:
			return true
		case <-ctx.Done():
			return false
		}
	}

	h.start()
	first := receivePanelHello(t, h.panel.hellos)
	if first.LinkViaNodeId != "nod_msk1" {
		t.Fatalf("bridge session Hello.link_via_node_id = %q, want nod_msk1", first.LinkViaNodeId)
	}
	if failedDirect.Load() != 3 {
		t.Fatalf("direct dials before fallback = %d, want three consecutive failures", failedDirect.Load())
	}
	if got := receiveProbeRequest(t, probeRequests); got < 10*time.Minute-time.Second || got > 10*time.Minute {
		t.Fatalf("first direct probe interval = %s, want 10m", got)
	}
	probeAdvances <- struct{}{} // this probe still fails, so the agent stays on the bridge
	if got := receiveProbeRequest(t, probeRequests); got < 10*time.Minute-time.Second || got > 10*time.Minute {
		t.Fatalf("second direct probe interval = %s, want 10m", got)
	}
	directReady.Store(true)
	probeAdvances <- struct{}{} // one success is not enough to leave the bridge
	if got := receiveProbeRequest(t, probeRequests); got < 10*time.Minute-time.Second || got > 10*time.Minute {
		t.Fatalf("third direct probe interval = %s, want 10m", got)
	}
	probeAdvances <- struct{}{} // the second consecutive success moves the next session back to direct
	second := receivePanelHello(t, h.panel.hellos)
	if second.LinkViaNodeId != "" {
		t.Fatalf("direct session Hello.link_via_node_id = %q, want empty", second.LinkViaNodeId)
	}
}

func TestPanelBridgeFallbackUsesThreeFailuresOrSixtySeconds(t *testing.T) {
	start := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	a := &Agent{bridgeFallbackAfter: time.Minute, bridgeFallbackAttempts: 3}
	for _, tc := range []struct {
		name     string
		failures int
		now      time.Time
		want     bool
	}{
		{name: "two failures before minute", failures: 2, now: start.Add(59 * time.Second)},
		{name: "three failures", failures: 3, now: start.Add(time.Second), want: true},
		{name: "minute elapsed", failures: 0, now: start.Add(time.Minute), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.bridgeFallbackDue(tc.failures, start, tc.now); got != tc.want {
				t.Fatalf("fallback due = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestPanelBridgeFallbackNeedsPanelViaDesiredAndAConnectedBridge(t *testing.T) {
	dialer := nodebridge.NewDialer(nil)
	defer dialer.Close()
	a := &Agent{bridgeDialer: dialer, model: &model{bridgeDials: []plugin.BridgeDial{{EntryNodeID: "nod_msk1"}}}}
	if a.hasPanelBridgeDesired() || a.panelBridgeEntry() != "" {
		t.Fatal("fallback was available without BridgeDial.panel_via")
	}
	a.model.bridgeDials[0].PanelVia = true
	if !a.hasPanelBridgeDesired() || a.panelBridgeEntry() != "" {
		t.Fatal("fallback was available before the panel-via bridge connected")
	}
}

func issueBridgeSiteCertificate(t *testing.T, panel *fakePanel) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(31), Subject: pkix.Name{CommonName: "msk1.example.com"},
		DNSNames: []string{"msk1.example.com"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}, panel.caCert, &key.PublicKey, panel.caKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(leaf.Raw)
	return tls.Certificate{Certificate: [][]byte{der, panel.caCert.Raw}, PrivateKey: key, Leaf: leaf}, hex.EncodeToString(sum[:])
}

func issueBridgePeerIdentity(t *testing.T, panel *fakePanel, nodeID string) nodebridge.DialIdentity {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse("spiffe://agent/" + nodeID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(32), Subject: pkix.Name{CommonName: nodeID}, URIs: []*url.URL{uri},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}, panel.caCert, &key.PublicKey, panel.caKey)
	if err != nil {
		t.Fatal(err)
	}
	return nodebridge.DialIdentity{NodeID: nodeID, CertificateDER: der, PrivateKey: key}
}

func waitPanelBridgeConns(t *testing.T, dialer *nodebridge.Dialer, entryID string) {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		for _, state := range dialer.States() {
			if state.PeerNodeID == entryID && state.Conns == 2 {
				return
			}
		}
		select {
		case <-timer.C:
			t.Fatalf("bridge pool did not connect: %+v", dialer.States())
		case <-ticker.C:
		}
	}
}

func receivePanelHello(t *testing.T, hellos <-chan *pb.Hello) *pb.Hello {
	t.Helper()
	select {
	case hello := <-hellos:
		return hello
	case <-time.After(10 * time.Second):
		t.Fatal("panel did not receive an agent Hello")
		return nil
	}
}

func receiveProbeRequest(t *testing.T, requests <-chan time.Duration) time.Duration {
	t.Helper()
	select {
	case delay := <-requests:
		return delay
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not schedule a direct-path probe")
		return 0
	}
}
