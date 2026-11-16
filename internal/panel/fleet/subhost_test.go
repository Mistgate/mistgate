//go:build !js

package fleet

import (
	"crypto/x509"
	"encoding/pem"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

func TestTunnelCertificateVerifiesAsTheNodeExpects(t *testing.T) {
	e := newCoreEnv(t)
	const sni = "t1.tunnel.example"
	c, err := e.f.TunnelCertificate(sni)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(e.f.CACertPEM()))
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: sni, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if c.PrivateKey == nil {
		t.Fatal("no private key")
	}
	again, _ := e.f.TunnelCertificate(sni)
	if string(again.Certificate[0]) != string(c.Certificate[0]) {
		t.Error("not cached")
	}
}

func TestVerifyNodeCertificate(t *testing.T) {
	e := newEnv(t)
	a, b := e.enroll("nodea"), e.enroll("nodeb")
	if err := e.f.VerifyNodeCertificate(a.nodeID, a.cert.Certificate); err != nil {
		t.Fatalf("own certificate: %v", err)
	}
	if err := e.f.VerifyNodeCertificate(a.nodeID, b.cert.Certificate); err == nil {
		t.Error("accepted another node's certificate")
	}
	tc, _ := e.f.TunnelCertificate("x.example")
	if err := e.f.VerifyNodeCertificate(a.nodeID, tc.Certificate); err == nil {
		t.Error("accepted a server certificate")
	}
	if _, err := (nodeService{e.f}).RetireNode(e.ctx, connect.NewRequest(&adminv1.RetireNodeRequest{NodeId: a.nodeID, ConfirmName: "nodea"})); err != nil {
		t.Fatal(err)
	}
	if err := e.f.VerifyNodeCertificate(a.nodeID, a.cert.Certificate); err == nil {
		t.Error("accepted a retired node")
	}
}

func TestEnrollInSession(t *testing.T) {
	e := newEnv(t)
	id, token, _ := e.createEnrollment("nodea", "nodea.example.com")
	_, csr := newCSR(t)
	if _, _, _, err := e.f.EnrollInSession(e.ctx, "wrong", csr); err != ErrEnrollRefused {
		t.Fatalf("bad token: %v", err)
	}
	nodeID, certPEM, caPEM, err := e.f.EnrollInSession(e.ctx, token, csr)
	if err != nil || nodeID != id || string(caPEM) != e.f.CACertPEM() {
		t.Fatalf("enrol: %q %v", nodeID, err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no certificate")
	}
	if err := e.f.VerifyNodeCertificate(id, [][]byte{block.Bytes}); err != nil {
		t.Errorf("issued certificate: %v", err)
	}
}

func TestNodeSettingsHostTunnelAndPanelLinkNeedCaps(t *testing.T) {
	e := newCoreEnv(t)
	e.f.SetPanelRouting(PanelRouting{LinkURL: "wss://b.example/k/", Tunnels: map[string]HostTunnel{"n1": {SNI: "t.example", Names: []string{"b.example"}}}})
	n := store.NodeRow{ID: "n1"}
	if s := e.f.nodeSettings(n, nil); s.HostTunnel != nil || s.PanelLinkUrl != "" {
		t.Errorf("without caps: %v", s)
	}
	s := e.f.nodeSettings(n, []string{plugin.CapHostTunnel, plugin.CapPanelLink})
	if s.HostTunnel.GetTunnelSni() != "t.example" || len(s.HostTunnel.GetForwardNames()) != 1 || s.PanelLinkUrl != "wss://b.example/k/" {
		t.Errorf("with caps: %v", s)
	}
	if s := e.f.nodeSettings(store.NodeRow{ID: "n2"}, []string{plugin.CapHostTunnel}); s.HostTunnel != nil {
		t.Errorf("not the host: %v", s)
	}
	bare := e.f.nodeSettings(n, nil)
	if settingsSig(s) == settingsSig(bare) {
		t.Error("host_tunnel and panel_link_url do not change the settings signature: agents would never get them")
	}
	if settingsSig(bare) != settingsSig(nodeSettingsFor(n, nil, e.f.osStatusAllowed())) {
		t.Error("an agent without the caps sees a new signature")
	}
}

func TestRetireRefusedForTheSubscriptionHost(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	e.f.SetPanelRouting(PanelRouting{IsHost: func(id string) bool { return id == a.nodeID }})
	_, err := (nodeService{e.f}).RetireNode(e.ctx, connect.NewRequest(&adminv1.RetireNodeRequest{NodeId: a.nodeID, ConfirmName: "nodea"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("retire host: %v", err)
	}
}
