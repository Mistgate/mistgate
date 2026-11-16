package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	nodebridge "github.com/mistgate/mistgate/internal/node/bridge"
	"github.com/mistgate/mistgate/internal/node/certs"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

func TestReview10_BridgeSiteCertificateIsPersistentAndNotFleetCA(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "certs")
	firstSource := certs.New(dir)
	first := &Agent{cfg: Config{Certs: firstSource}}
	getFirst, releaseFirst, pinFirst, err := first.bridgeSiteCertificate(context.Background(), newModel())
	if err != nil {
		t.Fatal(err)
	}
	certFirst, err := getFirst(&tls.ClientHelloInfo{ServerName: bridgeSiteCertName})
	if err != nil || certFirst == nil || len(certFirst.Certificate) == 0 {
		t.Fatalf("first bridge site certificate: %v", err)
	}
	leafFirst, err := x509.ParseCertificate(certFirst.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leafFirst.IsCA || leafFirst.Issuer.String() != leafFirst.Subject.String() ||
		leafFirst.CheckSignature(leafFirst.SignatureAlgorithm, leafFirst.RawTBSCertificate, leafFirst.Signature) != nil {
		t.Fatal("bridge fallback certificate is not a persistent self-signed leaf")
	}
	if text := strings.ToLower(leafFirst.Subject.String() + " " + strings.Join(leafFirst.DNSNames, " ")); strings.Contains(text, "mistgate") || strings.Contains(text, "bridge") {
		t.Fatalf("the certificate of the bridge site names this software: %q", text)
	}
	sum := sha256.Sum256(leafFirst.Raw)
	if pinFirst != hex.EncodeToString(sum[:]) {
		t.Fatalf("bridge certificate pin = %q, want SHA256(leaf DER) %x", pinFirst, sum)
	}
	if _, err := leafFirst.Verify(x509.VerifyOptions{}); err == nil {
		t.Fatal("bridge fallback certificate unexpectedly verifies through the system trust roots")
	}
	releaseFirst()

	second := &Agent{cfg: Config{Certs: certs.New(dir)}}
	getSecond, releaseSecond, pinSecond, err := second.bridgeSiteCertificate(context.Background(), newModel())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSecond()
	certSecond, err := getSecond(&tls.ClientHelloInfo{ServerName: bridgeSiteCertName})
	if err != nil || certSecond == nil || len(certSecond.Certificate) == 0 {
		t.Fatalf("restarted bridge site certificate: %v", err)
	}
	if pinSecond != pinFirst || string(certSecond.Certificate[0]) != string(certFirst.Certificate[0]) {
		t.Fatal("bridge site certificate changed after reopening the certificate source")
	}
}

type bridgeCertSource struct {
	active   map[string]plugin.TLS
	acquired []string
	released []string
}

func (s *bridgeCertSource) Acquire(_ context.Context, id string, spec plugin.TLS) (engine.Cert, error) {
	if s.active == nil {
		s.active = map[string]plugin.TLS{}
	}
	s.active[id] = spec
	s.acquired = append(s.acquired, id)
	return engine.Cert{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &tls.Certificate{}, nil },
		Info:           func() engine.CertInfo { return engine.CertInfo{PinSHA256: "a"} },
	}, nil
}

func (s *bridgeCertSource) Release(id string) {
	s.released = append(s.released, id)
	delete(s.active, id)
}

func TestBridgeSiteReplacementKeepsNewCertificateClaim(t *testing.T) {
	source := &bridgeCertSource{}
	a := &Agent{cfg: Config{Certs: source}}
	_, releaseFirst, _, err := a.bridgeSiteCertificate(context.Background(), newModel())
	if err != nil {
		t.Fatal(err)
	}
	_, releaseSecond, _, err := a.bridgeSiteCertificate(context.Background(), newModel())
	if err != nil {
		t.Fatal(err)
	}
	if len(source.acquired) != 2 || source.acquired[0] == source.acquired[1] {
		t.Fatalf("certificate source acquisition IDs = %v, want unique IDs", source.acquired)
	}
	releaseFirst()
	if _, ok := source.active[source.acquired[1]]; !ok {
		t.Fatal("releasing the old site claim also released the replacement claim")
	}
	releaseSecond()
	if len(source.active) != 0 {
		t.Fatalf("certificate source claims remained after both releases: %v", source.active)
	}
}

func TestRejectedBridgeServeDeltaStopsExistingBridge(t *testing.T) {
	l := newRelayLab(t)
	known := func(string) bool { return true }
	previous := newModel()
	previous.revision = 7
	previous.bridgeServe = &plugin.BridgeServe{Path: "/old-bridge", ExitNodeIDs: []string{"nod_de1"}}
	if err := l.a.bridgeEntry.SetServe(nodebridge.ServeConfig{EntryNodeID: "nod_msk1", Path: previous.bridgeServe.Path,
		ExitNodeIDs: previous.bridgeServe.ExitNodeIDs, Roots: x509.NewCertPool()}); err != nil {
		t.Fatal(err)
	}
	next, err := merge(previous, &pb.DesiredState{Revision: 8, BaseRevision: 7,
		BridgeServe: &pb.BridgeServe{Path: "/bad", ExitNodeIds: []string{"nod_de1", "nod_de1"}}}, known)
	if _, ok := err.(*partialStateError); !ok {
		t.Fatalf("invalid bridge serve did not produce a partial state: %v", err)
	}
	if next.bridgeServe != nil || len(next.issues) == 0 {
		t.Fatalf("rejected bridge serve was retained: serve=%+v issues=%+v", next.bridgeServe, next.issues)
	}
	l.a.syncBridgeServe(context.Background(), next)
	errs := l.a.bridgeEntry.SyncListeners([]nodebridge.RelayListener{{RelayID: "rly_test", ExitNodeID: "nod_de1", Port: 1, Address: "127.0.0.1:0"}})
	if err := errs["rly_test"]; err == nil || err.Error() != "exit_not_allowed" {
		t.Fatalf("bridge still allows the old exit after rejected delta: %v", err)
	}
}
