package vless

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

const (
	selfName   = "site.example.com"
	selfMarker = "the node's own site"
)

// ---- parseSpec ----

func selfSpec(id string, port uint16, name string) plugin.InboundSpec {
	return plugin.InboundSpec{
		ID: id, Protocol: Protocol, Enabled: true, Listen: plugin.Listen{Network: "tcp", Port: port},
		TLS: plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: name},
		Settings: json.RawMessage(fmt.Sprintf(`{"transport":"xhttp","security":"reality","flow":"","reality":{"target":"self","server_names":[%q],"short_ids":["6ba85179"],"private_key":%q},"xhttp":{"path":"/q8x2kd7w","host":"","mode":"auto","x_padding_bytes":"100-200"},"ws":{"path":"","host":""}}`,
			name, testPrivateKey)),
	}
}

func TestParseSpecSelfTarget(t *testing.T) {
	cfg, err := parseSpec(selfSpec("node-a", 28443, "Site.Example.com."))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.reality.self || cfg.reality.target != "self" || len(cfg.reality.serverNames) != 1 || cfg.reality.serverNames[0] != "site.example.com" {
		t.Fatalf("parsed = %+v", cfg.reality)
	}

	for _, tt := range []struct {
		name string
		edit func(*plugin.InboundSpec)
		want string
	}{
		{"no tls", func(s *plugin.InboundSpec) { s.TLS = plugin.TLS{} }, "acme_domain"},
		{"self-signed tls", func(s *plugin.InboundSpec) { s.TLS.Mode = plugin.TLSSelfSigned }, "acme_domain"},
		{"no server name", func(s *plugin.InboundSpec) { s.TLS.ServerName = "" }, "domain"},
		{"IP address", func(s *plugin.InboundSpec) { s.TLS.ServerName = "203.0.113.10" }, "domain"},
		{"no dot", func(s *plugin.InboundSpec) { s.TLS.ServerName = "localhost" }, "domain"},
		{"server_names differ", func(s *plugin.InboundSpec) { s.TLS.ServerName = "other.example.com" }, "exactly the tls server name"},
		{"two server_names", func(s *plugin.InboundSpec) {
			s.Settings = json.RawMessage(strings.Replace(string(s.Settings), `["site.example.com"]`, `["site.example.com","b.example.com"]`, 1))
		}, "exactly the tls server name"},
		{"no server_names", func(s *plugin.InboundSpec) {
			s.Settings = json.RawMessage(strings.Replace(string(s.Settings), `["site.example.com"]`, `[]`, 1))
		}, "exactly the tls server name"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			spec := selfSpec("node-a", 28443, selfName)
			tt.edit(&spec)
			if _, err := parseSpec(spec); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("parseSpec error = %v, want containing %q", err, tt.want)
			}
		})
	}

	// Another site as the target still takes no TLS fields.
	spec := settingsSpec("xhttp", "reality")
	spec.TLS = plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: selfName}
	if _, err := parseSpec(spec); err == nil || !strings.Contains(err.Error(), "TLS fields") {
		t.Fatalf("a site target with TLS fields: %v", err)
	}
}

// ---- a certificate source that tells what was asked of it ----

type fakeCerts struct {
	mu        sync.Mutex
	issued    bool
	acquired  []string
	released  []string
	hellos    []tls.ClientHelloInfo // the ACME entry's hellos, as the site passed them
	fallback  *tls.Certificate
	acmeChain *tls.Certificate
	challenge *tls.Certificate
	acquireOK func(plugin.TLS) error
}

func newFakeCerts(t *testing.T, name string) *fakeCerts {
	t.Helper()
	return &fakeCerts{
		fallback:  selfSignedCert(t, name),
		acmeChain: chainCert(t, name, false),
		challenge: selfSignedCert(t, name+" challenge"),
	}
}

func (f *fakeCerts) setIssued() {
	f.mu.Lock()
	f.issued = true
	f.mu.Unlock()
}

func (f *fakeCerts) Acquire(_ context.Context, id string, tl plugin.TLS) (engine.Cert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquired = append(f.acquired, id+":"+strconv.Itoa(int(tl.Mode)))
	if f.acquireOK != nil {
		if err := f.acquireOK(tl); err != nil {
			return engine.Cert{}, err
		}
	}
	switch tl.Mode {
	case plugin.TLSAcmeDomain:
		return engine.Cert{
			GetCertificate: func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
				f.mu.Lock()
				defer f.mu.Unlock()
				f.hellos = append(f.hellos, tls.ClientHelloInfo{ServerName: h.ServerName, SupportedProtos: slices.Clone(h.SupportedProtos)})
				switch {
				case len(h.SupportedProtos) == 1 && h.SupportedProtos[0] == "acme-tls/1":
					return f.challenge, nil
				case f.issued:
					return f.acmeChain, nil
				}
				return nil, errors.New("not issued yet")
			},
			Info: func() engine.CertInfo {
				f.mu.Lock()
				defer f.mu.Unlock()
				if !f.issued {
					return engine.CertInfo{}
				}
				return infoOfCert(f.acmeChain)
			},
		}, nil
	case plugin.TLSSelfSigned:
		return engine.Cert{
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return f.fallback, nil },
			Info:           func() engine.CertInfo { return infoOfCert(f.fallback) },
		}, nil
	}
	return engine.Cert{}, errors.New("unexpected tls mode")
}

func (f *fakeCerts) Release(id string) {
	f.mu.Lock()
	f.released = append(f.released, id)
	f.mu.Unlock()
}

func (f *fakeCerts) counts() (acquired, released int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.acquired), len(f.released)
}

func (f *fakeCerts) acmeHellos() []tls.ClientHelloInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.hellos)
}

func infoOfCert(c *tls.Certificate) engine.CertInfo {
	sum := sha256.Sum256(c.Certificate[0])
	return engine.CertInfo{PinSHA256: hex.EncodeToString(sum[:]), NotAfter: c.Leaf.NotAfter}
}

func newSerial(t *testing.T) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 100))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// selfSignedCert is a P-256 certificate that signed itself, as certs.Source makes for the fallback.
func selfSignedCert(t *testing.T, name string) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: newSerial(t), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// chainCert is a leaf under an intermediate under a root, the shape of a Let's Encrypt answer (two certificates sent).
func chainCert(t *testing.T, name string, useRSA bool) *tls.Certificate {
	t.Helper()
	mk := func(parent *x509.Certificate, parentKey any, cn string, ca bool, usage x509.KeyUsage) (*x509.Certificate, []byte, any) {
		var key any
		var pub any
		if useRSA && !ca {
			k, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatal(err)
			}
			key, pub = k, &k.PublicKey
		} else {
			k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			key, pub = k, &k.PublicKey
		}
		tpl := &x509.Certificate{
			SerialNumber: newSerial(t), Subject: pkix.Name{CommonName: cn, Organization: []string{"Example Test CA"}},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour),
			KeyUsage: usage, BasicConstraintsValid: true, IsCA: ca,
		}
		if ca {
			tpl.NotAfter = time.Now().Add(5 * 365 * 24 * time.Hour)
		} else {
			tpl.DNSNames = []string{name}
			tpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
		if parent == nil {
			parent, parentKey = tpl, key
		}
		der, err := x509.CreateCertificate(rand.Reader, tpl, parent, pub, parentKey)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return cert, der, key
	}
	root, _, rootKey := mk(nil, nil, "Example Root", true, x509.KeyUsageCertSign)
	mid, midDER, midKey := mk(root, rootKey, "Example Intermediate", true, x509.KeyUsageCertSign)
	leaf, leafDER, leafKey := mk(mid, midKey, name, false, x509.KeyUsageDigitalSignature)
	return &tls.Certificate{Certificate: [][]byte{leafDER, midDER}, PrivateKey: leafKey, Leaf: leaf}
}
