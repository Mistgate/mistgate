package bridge

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/url"
	"testing"
	"time"
)

// issueURILeaf signs a leaf with the given URIs (none for an empty list), usage and validity, by root or by key's own root.
func issueURILeaf(t *testing.T, root *x509.Certificate, rootKey *ecdsa.PrivateKey, curve elliptic.Curve, uris []string, usage x509.ExtKeyUsage, notBefore, notAfter time.Time) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var parsed []*url.URL
	for _, raw := range uris {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, u)
	}
	template := &x509.Certificate{SerialNumber: serialNumber(t), Subject: pkix.Name{CommonName: "3f9a01c2b7de"}, NotBefore: notBefore, NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}, URIs: parsed}
	der, err := x509.CreateCertificate(rand.Reader, template, root, &key.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	return key, der
}

func TestPanelProofTrustDomainAndBinding(t *testing.T) {
	pki := newTestPKI(t)
	now := time.Now()
	exporter := bytes.Repeat([]byte{0x41}, 32)
	panelKey, panelDER := issueURILeaf(t, pki.root, pki.rootKey, elliptic.P256(), []string{"spiffe://panel/ca_1"}, x509.ExtKeyUsageClientAuth, now.Add(-time.Hour), now.Add(time.Hour))
	proof, err := MakePanelProof("nod_host", exporter, panelDER, panelKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPanelProof("nod_host", exporter, proof, pki.roots, now); err != nil {
		t.Fatalf("a valid panel proof: %v", err)
	}
	for name, check := range map[string]func() error{
		"another host": func() error { return VerifyPanelProof("nod_other", exporter, proof, pki.roots, now) },
		"another exporter": func() error {
			return VerifyPanelProof("nod_host", bytes.Repeat([]byte{0x42}, 32), proof, pki.roots, now)
		},
		"expired":       func() error { return VerifyPanelProof("nod_host", exporter, proof, pki.roots, now.Add(2*time.Hour)) },
		"not yet valid": func() error { return VerifyPanelProof("nod_host", exporter, proof, pki.roots, now.Add(-2*time.Hour)) },
		"no roots":      func() error { return VerifyPanelProof("nod_host", exporter, proof, nil, now) },
		"bridge domain": func() error {
			p, err := MakeProof("nod_host", exporter, panelDER, panelKey)
			if err != nil {
				return nil
			}
			return VerifyPanelProof("nod_host", exporter, p, pki.roots, now)
		},
		"agent certificate": func() error {
			p, err := MakePanelProof("nod_host", exporter, pki.exitDER, pki.exitKey)
			if err != nil {
				return nil
			}
			return VerifyPanelProof("nod_host", exporter, p, pki.roots, now)
		},
	} {
		if check() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The panel's certificate never passes as an agent, whatever the allow-list says.
	if _, err := VerifyProof("nod_host", exporter, proof, pki.roots, map[string]struct{}{"ca_1": {}, "panel": {}}, now); err == nil {
		t.Error("a panel proof passed as an agent's")
	}

	other := newTestPKI(t)
	for name, leaf := range map[string]struct {
		root  *x509.Certificate
		key   *ecdsa.PrivateKey
		curve elliptic.Curve
		uris  []string
		usage x509.ExtKeyUsage
	}{
		"foreign CA":       {other.root, other.rootKey, elliptic.P256(), []string{"spiffe://panel/ca_1"}, x509.ExtKeyUsageClientAuth},
		"server usage":     {pki.root, pki.rootKey, elliptic.P256(), []string{"spiffe://panel/ca_1"}, x509.ExtKeyUsageServerAuth},
		"P-384":            {pki.root, pki.rootKey, elliptic.P384(), []string{"spiffe://panel/ca_1"}, x509.ExtKeyUsageClientAuth},
		"agent URI":        {pki.root, pki.rootKey, elliptic.P256(), []string{"spiffe://agent/ca_1"}, x509.ExtKeyUsageClientAuth},
		"two URIs":         {pki.root, pki.rootKey, elliptic.P256(), []string{"spiffe://panel/ca_1", "spiffe://panel/ca_2"}, x509.ExtKeyUsageClientAuth},
		"nested path":      {pki.root, pki.rootKey, elliptic.P256(), []string{"spiffe://panel/ca_1/x"}, x509.ExtKeyUsageClientAuth},
		"empty id":         {pki.root, pki.rootKey, elliptic.P256(), []string{"spiffe://panel/"}, x509.ExtKeyUsageClientAuth},
		"other scheme":     {pki.root, pki.rootKey, elliptic.P256(), []string{"https://panel/ca_1"}, x509.ExtKeyUsageClientAuth},
		"no URI at all":    {pki.root, pki.rootKey, elliptic.P256(), nil, x509.ExtKeyUsageClientAuth},
		"query in the URI": {pki.root, pki.rootKey, elliptic.P256(), []string{"spiffe://panel/ca_1?x=1"}, x509.ExtKeyUsageClientAuth},
	} {
		key, der := issueURILeaf(t, leaf.root, leaf.key, leaf.curve, leaf.uris, leaf.usage, now.Add(-time.Hour), now.Add(time.Hour))
		p, err := MakePanelProof("nod_host", exporter, der, key)
		if err != nil {
			continue // a P-384 key cannot sign the fixed-width proof: refused already
		}
		if VerifyPanelProof("nod_host", exporter, p, pki.roots, now) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A subscription host's Entry takes the panel as its only exit, never with the control link, and checks its proof through
// the same cache as the bridge.
func TestPanelPeerServeConfig(t *testing.T) {
	pki := newTestPKI(t)
	e := NewEntry()
	t.Cleanup(e.Close)
	for name, ids := range map[string][]string{"none": nil, "an agent": {"nod_de1"}, "panel and agent": {PanelPeerID, "nod_de1"}} {
		if err := e.SetServe(ServeConfig{EntryNodeID: "nod_host", Path: "/p4n3l", ExitNodeIDs: ids, Roots: pki.roots, Peer: PeerPanel}); err == nil {
			t.Errorf("%s: a panel peer with exits %v accepted", name, ids)
		}
	}
	if err := e.SetServe(ServeConfig{EntryNodeID: "nod_host", Path: "/p4n3l", ExitNodeIDs: []string{PanelPeerID}, Roots: pki.roots, Peer: PeerPanel,
		PanelVia: true, PanelAddress: "panel.example.com:443"}); err != nil {
		t.Fatal(err)
	}
	if cfg := e.serveSnapshot(); cfg.PanelVia || cfg.PanelAddress != "" {
		t.Fatal("a subscription host must never dial the panel for its caller")
	}

	now := time.Now()
	exporter := bytes.Repeat([]byte{0x51}, 32)
	panelKey, panelDER := issueURILeaf(t, pki.root, pki.rootKey, elliptic.P256(), []string{"spiffe://panel/ca_1"}, x509.ExtKeyUsageClientAuth, now.Add(-time.Hour), now.Add(time.Hour))
	proof, err := MakePanelProof("nod_host", exporter, panelDER, panelKey)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // the second answer comes from the cache
		if id, err := e.verifyRequest(PeerPanel, "nod_host", exporter, proof, pki.roots, nil, now); err != nil || id != PanelPeerID {
			t.Fatalf("panel proof at a panel peer: %q, %v", id, err)
		}
	}
	agentProof, err := MakeProof("nod_host", bytes.Repeat([]byte{0x52}, 32), pki.exitDER, pki.exitKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.verifyRequest(PeerPanel, "nod_host", bytes.Repeat([]byte{0x52}, 32), agentProof, pki.roots, map[string]struct{}{"nod_de1": {}}, now); err == nil {
		t.Fatal("an agent's proof passed at a panel peer")
	}
	if _, err := e.verifyRequest(PeerAgents, "nod_host", bytes.Repeat([]byte{0x53}, 32), proof, pki.roots, map[string]struct{}{PanelPeerID: {}}, now); err == nil {
		t.Fatal("a panel proof passed at an agents' entry")
	}
}
