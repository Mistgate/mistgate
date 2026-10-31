package agentlink

import (
	"crypto/ed25519"
	"crypto/sha256"
	"strings"
)

// An enrollment token with EnrollTokenPrefix never leaves the node: the agent derives an Ed25519 key from it and sends
// the public key and a signature over its CSR instead, so whoever sits in the middle of the TLS connection (the link
// enrols over public-PKI TLS, before the CA pin is checked) learns nothing that enrols another key. The panel stores
// SHA-256 of that public key as the token's hash, so a token sent in clear by an older agent still finds its row.
// An older panel never issues such a token, and the agent sends a token without the prefix as it always did.

// EnrollTokenPrefix marks a token the agent proves instead of sending.
const EnrollTokenPrefix = "mg2_"

const enrollDomain = "mistgate-enroll/2\x00"

// ProvesEnrollToken reports whether token is proven rather than sent.
func ProvesEnrollToken(token string) bool { return strings.HasPrefix(token, EnrollTokenPrefix) }

// EnrollKey is the key a proven token stands for.
func EnrollKey(token string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte(enrollDomain + token))
	return ed25519.NewKeyFromSeed(seed[:])
}

// SignEnroll signs the CSR with the token's key; it returns the public key and the signature.
func SignEnroll(token string, csrDER []byte) (pub, signature []byte) {
	key := EnrollKey(token)
	return key.Public().(ed25519.PublicKey), ed25519.Sign(key, enrollMessage(csrDER))
}

// VerifyEnroll checks a signature of SignEnroll.
func VerifyEnroll(pub, csrDER, signature []byte) bool {
	return len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, enrollMessage(csrDER), signature)
}

func enrollMessage(csrDER []byte) []byte { return append([]byte(enrollDomain), csrDER...) }
