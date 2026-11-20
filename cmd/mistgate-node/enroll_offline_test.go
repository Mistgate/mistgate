package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/agent"
)

// The panel's side of an offline enrollment: the CSR the node printed, signed by the fleet CA.
func signOfflineCSR(t *testing.T, csrLine, nodeID string) (leafPEM, caPEM []byte, caPin string) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "5e1f0c2a9b7d"}, IsCA: true, BasicConstraintsValid: true,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	der, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(strings.TrimSpace(csrLine), "csr="))
	if err != nil {
		t.Fatalf("csr line %q: %v", csrLine, err)
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		t.Fatal(err)
	}
	uri, _ := url.Parse("spiffe://agent/" + nodeID)
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(2), URIs: []*url.URL{uri},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}, ca, csr.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(caDER)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), hex.EncodeToString(sum[:])
}

func TestOfflineEnrollment(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	const nodeID = "nod_abcdefghijklmnopqrstuvwxyz"
	const linkURL = "wss://sub.example.com/link0123456789abcdefgh/"
	const bridgePath = "/sub-host-bridge-path-0123"
	var csrOut bytes.Buffer
	if code := enrollOfflineCSR(&csrOut, dir, false); code != 0 {
		t.Fatalf("--offline-csr: %d", code)
	}
	leaf, ca, caPin := signOfflineCSR(t, csrOut.String(), nodeID)
	role, _ := json.Marshal(agent.OfflineRole{LinkURL: linkURL, SubHost: &agent.SubHostMeta{Names: []string{"sub.example.com"}, Path: bridgePath, SiteName: "de1.example.com"}})
	stdin := func(pemParts ...[]byte) *bytes.Reader {
		return bytes.NewReader(append(append(role, '\n'), bytes.Join(pemParts, nil)...))
	}
	cfg := agent.OfflineEnrollConfig{StateDir: dir, NodeID: nodeID, CASHA256: caPin}

	// Interrupted (the CA never came): nothing is enrolled, the pending key stays, and a second run finishes.
	var out bytes.Buffer
	if code := enrollOfflineFinish(stdin(leaf), &out, cfg); code != 1 || agent.IsEnrolled(dir) {
		t.Fatalf("a truncated input: %d, enrolled %v", code, agent.IsEnrolled(dir))
	}
	other := cfg
	other.CASHA256 = strings.Repeat("0", 64)
	if code := enrollOfflineFinish(stdin(leaf, ca), &out, other); code != 1 || agent.IsEnrolled(dir) {
		t.Fatalf("another CA: %d", code)
	}
	if code := enrollOfflineFinish(bytes.NewReader(append([]byte("not json\n"), leaf...)), &out, cfg); code != 2 {
		t.Fatalf("no role line: %d", code)
	}
	wrongNode := cfg
	wrongNode.NodeID = "nod_zzzzzzzzzzzzzzzzzzzzzzzzzz"
	if code := enrollOfflineFinish(stdin(leaf, ca), &out, wrongNode); code != 1 || agent.IsEnrolled(dir) {
		t.Fatalf("a certificate of another node: %d", code)
	}
	out.Reset()
	if code := enrollOfflineFinish(stdin(leaf, ca), &out, cfg); code != 0 || !agent.IsEnrolled(dir) {
		t.Fatalf("--offline-finish: %d", code)
	}
	pinLine := strings.TrimSpace(out.String())
	if !strings.HasPrefix(pinLine, "pin=") || len(pinLine) != len("pin=")+64 {
		t.Fatalf("stdout %q, want only the site pin", out.String())
	}
	if strings.Contains(out.String(), bridgePath) || strings.Contains(out.String(), "link0123456789") {
		t.Fatal("a secret reached stdout")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "agent.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta agent.Meta
	if err := json.Unmarshal(raw, &meta); err != nil || meta.Link != linkURL || meta.SubHost == nil || meta.SubHost.Path != bridgePath || meta.NodeID != nodeID {
		t.Fatalf("agent.json %+v, %v", meta, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "enroll-pending.pem")); !os.IsNotExist(err) {
		t.Fatal("the pending key must be gone once enrolled")
	}
	// The pin is the one the agent's site will present: the same self-signed file, by name.
	out.Reset()
	if code := enrollOfflineFinish(stdin(leaf, ca), &out, cfg); code != 1 {
		t.Fatal("a second enrollment without --force must be refused")
	}
}
