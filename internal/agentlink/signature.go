// Package agentlink contains the versioned signed-link handshake shared by the panel and node agent.
package agentlink

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"math/big"
)

const (
	agentDomain   = "mistgate-agent-link/1\x00"
	agentDomainV2 = "mistgate-agent-link/2\x00"
	panelDomain   = "mistgate-panel-link/1\x00"
	scalarSize    = 32
	signatureSize = 2 * scalarSize
)

// AgentDigest hashes the mistgate-agent-link/1 domain, NUL-separated node ID and audience, and panel nonce with SHA-256.
func AgentDigest(nodeID, audience string, panelNonce []byte) []byte {
	h := sha256.Sum256([]byte(agentDomain + nodeID + "\x00" + audience + "\x00" + string(panelNonce)))
	return h[:]
}

// AgentDigestV2 is AgentDigest in the mistgate-agent-link/2 domain with the agent's own nonce appended: the signature
// covers what the agent asks for (the seal marker in its nonce), so nobody can change that request on the way. A new agent
// signs only this; a v2 signature never verifies as a v1 one and the other way round.
func AgentDigestV2(nodeID, audience string, panelNonce, agentNonce []byte) []byte {
	h := sha256.Sum256([]byte(agentDomainV2 + nodeID + "\x00" + audience + "\x00" + string(panelNonce) + string(agentNonce)))
	return h[:]
}

// PanelDigest hashes the mistgate-panel-link/1 domain, NUL-separated node ID and audience, and concatenated nonces with SHA-256.
func PanelDigest(nodeID, audience string, panelNonce, agentNonce []byte) []byte {
	h := sha256.Sum256([]byte(panelDomain + nodeID + "\x00" + audience + "\x00" + string(panelNonce) + string(agentNonce)))
	return h[:]
}

// Sign signs digest and encodes the ECDSA signature as fixed-width r||s: two 32-byte big-endian scalars.
func Sign(key *ecdsa.PrivateKey, digest []byte) ([]byte, error) {
	r, s, err := ecdsa.Sign(rand.Reader, key, digest)
	if err != nil {
		return nil, err
	}
	out := make([]byte, signatureSize)
	r.FillBytes(out[:scalarSize])
	s.FillBytes(out[scalarSize:])
	return out, nil
}

// Verify reports whether digest matches the fixed-width 64-byte r||s ECDSA signature.
func Verify(key *ecdsa.PublicKey, digest, signature []byte) bool {
	if key == nil || key.Curve == nil || len(signature) != signatureSize {
		return false
	}
	r := new(big.Int).SetBytes(signature[:scalarSize])
	s := new(big.Int).SetBytes(signature[scalarSize:])
	return ecdsa.Verify(key, digest, r, s)
}
