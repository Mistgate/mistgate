package fleet

import (
	"crypto/rand"
	"crypto/x509"
	"net/url"
	"testing"
)

// Nodes enrolled before the product-neutral SAN carry mistgate://node/<id>; the panel must keep accepting
// them until they renew, and must not accept any other foreign scheme.
func TestLegacyNodeURIAccepted(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("legacy")

	resign := func(u string) *x509.Certificate {
		t.Helper()
		parsed, err := url.Parse(u)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := *a.leaf
		tmpl.URIs = []*url.URL{parsed}
		der, err := x509.CreateCertificate(rand.Reader, &tmpl, e.f.ca.cert, &a.key.PublicKey, e.f.ca.key)
		if err != nil {
			t.Fatal(err)
		}
		c, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	id, _, err := e.f.nodeFromCert(e.ctx, resign("mistgate://node/"+a.nodeID))
	if err != nil || id != a.nodeID {
		t.Fatalf("legacy SAN: id=%q err=%v", id, err)
	}
	for _, bad := range []string{"other://node/" + a.nodeID, "mistgate://agent/" + a.nodeID, "spiffe://node/" + a.nodeID} {
		if _, _, err := e.f.nodeFromCert(e.ctx, resign(bad)); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
