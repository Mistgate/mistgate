package fleet

import (
	"bytes"
	"crypto/elliptic"
	"crypto/x509"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/bridge"
)

// The panel's certificate is the fleet CA's, spiffe://panel/<ca id>, ClientAuth only, P-256; a host verifies a proof made
// with it, and the panel keeps it until it nears its end.
func TestPanelIdentity(t *testing.T) {
	e := newEnv(t)
	now := time.Now()
	der, key, err := e.f.PanelIdentity(now)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	if len(leaf.URIs) != 1 || leaf.URIs[0].String() != "spiffe://panel/"+e.f.ca.id || len(leaf.ExtKeyUsage) != 1 ||
		leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || key.Curve != elliptic.P256() || leaf.NotAfter.Sub(now) > panelCertTTL {
		t.Fatalf("certificate: URIs %v, usage %v, until %v", leaf.URIs, leaf.ExtKeyUsage, leaf.NotAfter)
	}
	exporter := bytes.Repeat([]byte{9}, 32)
	proof, err := bridge.MakePanelProof("nod_host", exporter, der, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.VerifyPanelProof("nod_host", exporter, proof, e.f.ca.pool, now); err != nil {
		t.Fatalf("a host does not accept the panel: %v", err)
	}

	again, _, _ := e.f.PanelIdentity(now.Add(time.Hour))
	if !bytes.Equal(again, der) {
		t.Fatal("the certificate must be kept while it has time left")
	}
	renewed, renewedKey, _ := e.f.PanelIdentity(now.Add(panelCertTTL - panelRenewAt + time.Minute))
	if bytes.Equal(renewed, der) || renewedKey.Equal(key) {
		t.Fatal("a certificate near its end must be replaced, with a new key")
	}
}
