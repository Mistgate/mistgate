package agentlink

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"testing"
)

// The v2 digest covers the agent's nonce and lives in a domain of its own: a v2 signature is not a v1 one, and the
// other way round, so a v1 signature forwarded with another nonce proves nothing to a v2 reader.
func TestAgentDigestV2(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	panel, agent := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	v1 := AgentDigest("nod_a", "panel.example.com", panel)
	v2 := AgentDigestV2("nod_a", "panel.example.com", panel, agent)
	if bytes.Equal(v1, v2) {
		t.Fatal("v1 and v2 digests are equal")
	}
	if bytes.Equal(v2, AgentDigestV2("nod_a", "panel.example.com", panel, bytes.Repeat([]byte{3}, 32))) {
		t.Fatal("the v2 digest does not cover the agent nonce")
	}
	sig, err := Sign(key, v2)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(&key.PublicKey, v2, sig) || Verify(&key.PublicKey, v1, sig) {
		t.Fatal("a v2 signature must verify as v2 and not as v1")
	}
}
