package bridge

import (
	"crypto/ecdsa"
	"crypto/elliptic"
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
	// ClientHeader carries the address of the person a subscription host hands to the panel (ServeConfig.PassClient); it is
	// on the answers to a proven exit only.
	ClientHeader     = "X-Mistgate-Bridge-Client"
	ErrorHeader      = "X-Mistgate-Bridge-Error"
	ProtocolHeader   = "X-Mistgate-Bridge-Version"
	StreamHeader     = "X-Mistgate-Bridge-Stream"
	StreamKindHeader = "X-Mistgate-Bridge-Kind"
	TargetHeader     = "X-Mistgate-Bridge-Target"
	PairIDHeader     = "X-Mistgate-Bridge-Pair-ID"
	StreamKindPanel  = "panel"
	protocolVersion  = "2"

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
	leaf, pub, err := verifyLeaf(entryNodeID, exporter, proof, roots, now)
	if err != nil {
		return "", err
	}
	exitNodeID := idFromCertificate(leaf, "agent")
	if _, ok := allowed[exitNodeID]; !ok || !agentlink.Verify(pub, agentlink.BridgeDigest(entryNodeID, exporter), proof.Signature) {
		return "", errBadProof
	}
	return exitNodeID, nil
}

// VerifyPanelProof checks that the panel of this fleet calls subscription host hostNodeID over this connection: a certificate
// of the fleet CA with the one URI spiffe://panel/<id> and ClientAuth, and a signature over the connection's exporter in the
// panel's own domain (agentlink.PanelBridgeDigest). An agent's certificate or a bridge proof never passes.
func VerifyPanelProof(hostNodeID string, exporter []byte, proof Proof, roots *x509.CertPool, now time.Time) error {
	leaf, pub, err := verifyLeaf(hostNodeID, exporter, proof, roots, now)
	if err != nil {
		return err
	}
	if idFromCertificate(leaf, PanelURIHost) == "" || !agentlink.Verify(pub, agentlink.PanelBridgeDigest(hostNodeID, exporter), proof.Signature) {
		return errBadProof
	}
	return nil
}

// MakePanelProof is MakeProof in the panel's own domain (VerifyPanelProof).
func MakePanelProof(hostNodeID string, exporter []byte, certificateDER []byte, key *ecdsa.PrivateKey) (Proof, error) {
	if hostNodeID == "" || len(exporter) != 32 || len(certificateDER) == 0 || len(certificateDER) > maxCertificateDER || key == nil || key.Curve != elliptic.P256() {
		return Proof{}, errBadProof
	}
	sig, err := agentlink.Sign(key, agentlink.PanelBridgeDigest(hostNodeID, exporter))
	if err != nil {
		return Proof{}, err
	}
	return Proof{CertificateDER: append([]byte(nil), certificateDER...), Signature: sig}, nil
}

// verifyLeaf checks what every proof needs: a P-256 ClientAuth certificate of the fleet that is valid now.
func verifyLeaf(entryNodeID string, exporter []byte, proof Proof, roots *x509.CertPool, now time.Time) (*x509.Certificate, *ecdsa.PublicKey, error) {
	if entryNodeID == "" || len(exporter) != 32 || roots == nil || len(proof.CertificateDER) == 0 || len(proof.CertificateDER) > maxCertificateDER || len(proof.Signature) != 64 {
		return nil, nil, errBadProof
	}
	leaf, err := x509.ParseCertificate(proof.CertificateDER)
	if err != nil {
		return nil, nil, errBadProof
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, nil, errBadProof
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve.Params().BitSize != 256 {
		return nil, nil, errBadProof
	}
	return leaf, pub, nil
}

// PanelURIHost is the host of the panel's URI, spiffe://panel/<id> (internal/panel/fleet/ca.go issues it).
const PanelURIHost = "panel"

func nodeIDFromCertificate(leaf *x509.Certificate) string { return idFromCertificate(leaf, "agent") }

// idFromCertificate is the <id> of the certificate's one URI spiffe://<host>/<id>, "" when it has another shape.
func idFromCertificate(leaf *x509.Certificate, host string) string {
	if leaf == nil || len(leaf.URIs) != 1 {
		return ""
	}
	u := leaf.URIs[0]
	if u.Scheme != "spiffe" || u.Host != host || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
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
