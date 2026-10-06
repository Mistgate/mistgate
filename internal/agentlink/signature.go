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
	panelDomain   = "mistgate-panel-link/1\x00"
	scalarSize    = 32
	signatureSize = 2 * scalarSize
)

func AgentDigest(nodeID, audience string, panelNonce []byte) []byte {
	h := sha256.Sum256([]byte(agentDomain + nodeID + "\x00" + audience + "\x00" + string(panelNonce)))
	return h[:]
}

func PanelDigest(nodeID, audience string, panelNonce, agentNonce []byte) []byte {
	h := sha256.Sum256([]byte(panelDomain + nodeID + "\x00" + audience + "\x00" + string(panelNonce) + string(agentNonce)))
	return h[:]
}

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

func Verify(key *ecdsa.PublicKey, digest, signature []byte) bool {
	if key == nil || key.Curve == nil || len(signature) != signatureSize {
		return false
	}
	r := new(big.Int).SetBytes(signature[:scalarSize])
	s := new(big.Int).SetBytes(signature[scalarSize:])
	return ecdsa.Verify(key, digest, r, s)
}
