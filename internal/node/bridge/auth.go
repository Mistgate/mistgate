package bridge

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/agentlink"
)

const (
	CertificateHeader = "X-Mistgate-Bridge-Certificate"
	SignatureHeader   = "X-Mistgate-Bridge-Signature"
	RelayIDHeader     = "X-Mistgate-Relay-ID"
	ErrorHeader       = "X-Mistgate-Bridge-Error"
	ProtocolHeader    = "X-Mistgate-Bridge-Version"
	StreamHeader      = "X-Mistgate-Bridge-Stream"
	PairIDHeader      = "X-Mistgate-Bridge-Pair-ID"
	protocolVersion   = "2"

	maxCertificateDER = 12 << 10
)

var maxCertificateB64 = base64.StdEncoding.EncodedLen(maxCertificateDER)

var errBadProof = errors.New("invalid bridge proof")

// Proof carries a node's certificate and a proof tied to one TLS connection.
type Proof struct {
	CertificateDER []byte
	Signature      []byte
}

func MakeProof(entryNodeID string, exporter []byte, certificateDER []byte, key *ecdsa.PrivateKey) (Proof, error) {
	if entryNodeID == "" || len(exporter) != 32 || len(certificateDER) == 0 || len(certificateDER) > maxCertificateDER || key == nil {
		return Proof{}, errBadProof
	}
	sig, err := agentlink.Sign(key, agentlink.BridgeDigest(entryNodeID, exporter))
	if err != nil {
		return Proof{}, err
	}
	return Proof{CertificateDER: append([]byte(nil), certificateDER...), Signature: sig}, nil
}

func (p Proof) setHeaders(h mapHeader) {
	h.Set(CertificateHeader, base64.StdEncoding.EncodeToString(p.CertificateDER))
	h.Set(SignatureHeader, base64.StdEncoding.EncodeToString(p.Signature))
}

type mapHeader interface {
	Set(string, string)
}

func proofFromHeaders(certText, sigText string) (Proof, error) {
	if len(certText) == 0 || len(certText) > maxCertificateB64 || len(sigText) == 0 || len(sigText) > 128 {
		return Proof{}, errBadProof
	}
	cert, err := base64.StdEncoding.DecodeString(certText)
	if err != nil || len(cert) == 0 || len(cert) > maxCertificateDER {
		return Proof{}, errBadProof
	}
	sig, err := base64.StdEncoding.DecodeString(sigText)
	if err != nil || len(sig) != 64 {
		return Proof{}, errBadProof
	}
	return Proof{CertificateDER: cert, Signature: sig}, nil
}

// VerifyProof checks fleet trust, the node id in the certificate, and the connection-bound signature.
func VerifyProof(entryNodeID string, exporter []byte, proof Proof, roots *x509.CertPool, allowed map[string]struct{}, now time.Time) (string, error) {
	if entryNodeID == "" || len(exporter) != 32 || roots == nil || len(proof.CertificateDER) == 0 || len(proof.CertificateDER) > maxCertificateDER || len(proof.Signature) != 64 {
		return "", errBadProof
	}
	leaf, err := x509.ParseCertificate(proof.CertificateDER)
	if err != nil {
		return "", errBadProof
	}
	exitNodeID := nodeIDFromCertificate(leaf)
	if !nodeIDPattern.MatchString(exitNodeID) {
		return "", errBadProof
	}
	if _, ok := allowed[exitNodeID]; !ok {
		return "", errBadProof
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return "", errBadProof
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve.Params().BitSize != 256 || !agentlink.Verify(pub, agentlink.BridgeDigest(entryNodeID, exporter), proof.Signature) {
		return "", errBadProof
	}
	return exitNodeID, nil
}

func nodeIDFromCertificate(leaf *x509.Certificate) string {
	if leaf == nil || len(leaf.URIs) != 1 {
		return ""
	}
	u := leaf.URIs[0]
	if u.Scheme != "spiffe" || u.Host != "agent" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		!strings.HasPrefix(u.Path, "/") || strings.Contains(strings.TrimPrefix(u.Path, "/"), "/") {
		return ""
	}
	id := strings.TrimPrefix(u.Path, "/")
	if !nodeIDPattern.MatchString(id) {
		return ""
	}
	return id
}

func exporterFor(state *tls.ConnectionState) ([]byte, error) {
	if state == nil || (state.Version != tls.VersionTLS12 && state.Version != tls.VersionTLS13) || !state.HandshakeComplete {
		return nil, errBadProof
	}
	return state.ExportKeyingMaterial("mistgate-bridge/1", nil, 32)
}

func sameSecretPath(got, want string) bool {
	gotSum, wantSum := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) == 1
}
