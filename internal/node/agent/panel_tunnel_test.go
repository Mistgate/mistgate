package agent

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/provision"
	"github.com/mistgate/mistgate/internal/panel/pubaddr"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/store/storetest"
	"github.com/mistgate/mistgate/internal/panel/subhost"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"github.com/mistgate/mistgate/internal/statehash"
)

// The first subscription host as the SSH install leaves it, against the real panel: enrolled inside the session with the
// fleet's certificate, the role turned on by the install, its own site on 443 (no engine serves one). The panel dials the
// tunnel and the agent's session runs inside it, over the real fleet link handler, up to the HelloAck.
func TestTunnelLinkSessionWithTheRealPanel(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, vault.KeySize)
	_, _ = rand.Read(key)
	v, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := protocols.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	fl, err := fleet.New(st, v, reg, fleet.Config{AgentSNI: "agents.invalid", Debounce: 20 * time.Millisecond,
		Desired: func(context.Context, string) ([]statehash.Inbound, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stopPanel := context.WithCancel(ctx)
	defer stopPanel()
	go fl.Run(runCtx)

	// The node's TCP 443: a loopback port the agent's own site listens on (again after every stop of a reconcile).
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	const linkPrefix = "/0123456789abcdef0123/"
	addr443 := probe.Addr().String()
	_ = probe.Close()

	const domain = "sub.test"
	svc := subhost.New(subhost.Config{Store: st, Fleet: fl, Addr: pubaddr.New("https://old.example"), StepUp: func(context.Context) error { return nil },
		LinkPrefix: linkPrefix, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr443)
		},
		CAA: func(context.Context, string) ([]string, bool, error) { return nil, false, nil }})

	// The install: the panel's checks (before the job creates the node), a pending node, the CSR from the node, the
	// certificate from the fleet, the identity on the node.
	sni, err := svc.PrepareInstall(ctx, "203.0.113.10", domain, true)
	if err != nil {
		t.Fatal(err)
	}
	nodeID := store.NewID("nod_")
	now := time.Now()
	token, caPin, err := fl.CreateProvisionEnrollment(ctx, provision.NodeSpec{ID: nodeID, Name: "host-a", Address: "203.0.113.10"}, "adm_test", now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "state")
	csr, err := OfflineCSR(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	gotID, certPEM, caPEM, err := fl.EnrollInSession(ctx, token, csr)
	if err != nil || gotID != nodeID {
		t.Fatalf("EnrollInSession = %q, %v", gotID, err)
	}
	if _, err := EnrollOffline(OfflineEnrollConfig{StateDir: dir, NodeID: nodeID, CASHA256: caPin, LinkURL: "wss://" + domain + linkPrefix,
		TunnelSNI: sni, TunnelName: domain, PEM: append(append([]byte{}, certPEM...), caPEM...)}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ActivateInstalled(ctx, nodeID, domain, sni); err != nil {
		t.Fatal(err)
	}
	if svc.State() != "on" {
		t.Fatalf("state %q after the install", svc.State())
	}
	go svc.Run(runCtx)

	a, err := New(Config{StateDir: dir, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), DoctorEnv: testDoctorEnv(t)},
		map[string]engine.Factory{}, &fakeHost{})
	if err != nil {
		t.Fatal(err)
	}
	a.standaloneListen = func(network, _ string) (net.Listener, error) { return net.Listen(network, addr443) }
	agentCtx, stopAgent := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- a.Run(agentCtx) }()
	t.Cleanup(func() { stopAgent(); <-done })

	eventually(t, func() bool { return fl.HasSession(nodeID) && a.inTunnel.Load() }, "a session inside the tunnel")
	if via := fl.SessionVia(nodeID); via != sni {
		t.Fatalf("the session came in via %q, want the tunnel SNI", via)
	}
	if !a.linkOnly() || a.linkURL() != "wss://"+domain+linkPrefix {
		t.Fatalf("link-only %v, link %q", a.linkOnly(), a.linkURL())
	}
}
