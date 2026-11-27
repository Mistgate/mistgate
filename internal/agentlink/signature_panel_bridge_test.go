package agentlink

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
)

// The panel's host proof is its own domain: the same exporter and node id give another digest than the bridge's, a signature
// over one never verifies over the other, and the digest binds both the exporter and the node id.
func TestPanelBridgeDigestIsItsOwnDomain(t *testing.T) {
	exporter := bytes.Repeat([]byte{7}, 32)
	panel, bridge := PanelBridgeDigest("nod_host", exporter), BridgeDigest("nod_host", exporter)
	if bytes.Equal(panel, bridge) || len(panel) != 32 {
		t.Fatal("the panel's digest must differ from the bridge's")
	}
	if bytes.Equal(panel, PanelBridgeDigest("nod_other", exporter)) || bytes.Equal(panel, PanelBridgeDigest("nod_host", bytes.Repeat([]byte{8}, 32))) {
		t.Fatal("the digest must bind the node id and the exporter")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := Sign(key, panel)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(&key.PublicKey, panel, sig) || Verify(&key.PublicKey, bridge, sig) {
		t.Fatal("a panel proof must verify as one, and never as a bridge proof")
	}
}
