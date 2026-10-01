package certs

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/plugin"
)

func leafOf(t *testing.T, gc func(*tls.ClientHelloInfo) (*tls.Certificate, error), sni string) *x509.Certificate {
	t.Helper()
	c, err := gc(&tls.ClientHelloInfo{ServerName: sni})
	if err != nil {
		t.Fatal(err)
	}
	l, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestSelfSignedPinIsStableAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	tl := plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: "node.example.com"}

	c1, err := New(dir).Acquire(context.Background(), "inb1", tl)
	if err != nil {
		t.Fatal(err)
	}
	info := c1.Info()
	leaf := leafOf(t, c1.GetCertificate, "whatever")
	sum := sha256.Sum256(leaf.Raw)
	if info.PinSHA256 != hex.EncodeToString(sum[:]) || len(info.PinSHA256) != 64 {
		t.Fatalf("pin %q is not sha256(DER leaf)", info.PinSHA256)
	}
	if !info.NotAfter.After(time.Now().Add(365 * 24 * time.Hour)) {
		t.Fatalf("NotAfter too soon: %v", info.NotAfter)
	}
	if err := leaf.VerifyHostname("node.example.com"); err != nil {
		t.Fatal(err)
	}
	if leaf.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("want ECDSA, got %v", leaf.PublicKeyAlgorithm)
	}

	// A fresh Source on the same dir (agent restart) serves the same certificate.
	c2, err := New(dir).Acquire(context.Background(), "inb1", tl)
	if err != nil || c2.Info().PinSHA256 != info.PinSHA256 {
		t.Fatalf("pin changed across restart: %v %v", err, c2.Info().PinSHA256)
	}
	// Another inbound with the same name shares it.
	c3, _ := New(dir).Acquire(context.Background(), "inb2", tl)
	if c3.Info().PinSHA256 != info.PinSHA256 {
		t.Fatal("same server name must give the same certificate")
	}
	// A different name is a different certificate.
	c4, err := New(dir).Acquire(context.Background(), "inb3", plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: "203.0.113.10"})
	if err != nil || c4.Info().PinSHA256 == info.PinSHA256 {
		t.Fatalf("expected a separate certificate for an IP name: %v", err)
	}
	if leafOf(t, c4.GetCertificate, "").VerifyHostname("203.0.113.10") != nil {
		t.Fatal("IP name must land in the IP SANs")
	}
}

func TestSelfSignedRegeneratesWhenBroken(t *testing.T) {
	dir := t.TempDir()
	tl := plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: "node.example.com"}
	c1, _ := New(dir).Acquire(context.Background(), "a", tl)
	files, _ := filepath.Glob(filepath.Join(dir, "selfsigned-*.pem"))
	if len(files) != 1 {
		t.Fatalf("want one pem file, got %v", files)
	}
	if err := os.WriteFile(files[0], []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	c2, err := New(dir).Acquire(context.Background(), "a", tl)
	if err != nil || c2.Info().PinSHA256 == c1.Info().PinSHA256 {
		t.Fatalf("a corrupt file must be replaced by a new certificate: %v", err)
	}
	if _, err := New("").Acquire(context.Background(), "a", plugin.TLS{Mode: plugin.TLSSelfSigned}); err == nil {
		t.Fatal("empty server name must be rejected")
	}
}

func TestModes(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Acquire(context.Background(), "a", plugin.TLS{Mode: plugin.TLSAcmeIP, ServerName: "203.0.113.10"}); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("acme_ip must say it is unsupported, got %v", err)
	}
	if _, err := s.Acquire(context.Background(), "a", plugin.TLS{Mode: 0, ServerName: "x.example.com"}); err == nil {
		t.Fatal("unspecified mode must fail")
	}
	for _, bad := range []string{"", "localhost", "203.0.113.10", "::1"} {
		if _, err := s.Acquire(context.Background(), "a", plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: bad}); err == nil {
			t.Errorf("acme_domain must reject %q", bad)
		}
	}
}

func TestACMEHostPolicyAndHTTP01(t *testing.T) {
	s := New(t.TempDir())
	s.DirectoryURL = "http://127.0.0.1:1/directory" // nothing there: a test must never reach a real CA
	s.HTTPAddr = "127.0.0.1:0"
	tl := plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "Node.Example.com."}
	c, err := s.Acquire(context.Background(), "inb1", tl)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Release("inb1") })
	if c.Info().PinSHA256 != "" {
		t.Fatal("no certificate yet, Info must be empty")
	}

	// HostPolicy is exactly the inbound's name: anything else (or no SNI) is refused before any CA traffic.
	for _, sni := range []string{"other.example.com", "", "example.com"} {
		if _, err := c.GetCertificate(&tls.ClientHelloInfo{ServerName: sni}); err == nil {
			t.Errorf("SNI %q must be refused", sni)
		}
	}

	// HTTP-01 listener is up; everything that is not a challenge (known host or not, any method) gets the
	// decoy 404, byte for byte what TCP 443 answers, never Go's text/plain 404 or autocert's redirect.
	s.mu.Lock()
	h := s.http
	s.mu.Unlock()
	if h == nil {
		t.Fatal(":80 listener did not start")
	}
	want := httptest.NewRecorder()
	decoy.NotFound().ServeHTTP(want, httptest.NewRequest("GET", "/", nil))
	wantBody := want.Body.String()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 5 * time.Second}
	for _, tc := range []struct{ method, host string }{
		{"GET", "node.example.com"}, {"POST", "node.example.com"}, {"GET", "stranger.example.com"}, {"GET", ""},
	} {
		req, _ := http.NewRequest(tc.method, "http://"+h.addr.String()+"/", nil)
		req.Host = tc.host
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound || resp.Header.Get("Content-Type") != want.Header().Get("Content-Type") ||
			string(body) != wantBody || resp.Header.Get("Location") != "" {
			t.Fatalf("%s host=%q: want decoy 404, got %d %q %q", tc.method, tc.host, resp.StatusCode, resp.Header.Get("Content-Type"), body)
		}
	}

	// Re-acquiring under another server name moves the claim instead of failing.
	if _, err := s.Acquire(context.Background(), "inb1", plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "moved.example.com"}); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	_, oldLeft := s.acme["node.example.com"]
	_, newThere := s.acme["moved.example.com"]
	s.mu.Unlock()
	if oldLeft || !newThere {
		t.Fatalf("claim must move: old=%v new=%v", oldLeft, newThere)
	}

	// Release of the last claim stops :80 and forgets the name.
	s.Release("inb1")
	s.mu.Lock()
	left, hs := len(s.acme), s.http
	s.mu.Unlock()
	if left != 0 || hs != nil {
		t.Fatalf("release must clean up: %d entries, http=%v", left, hs)
	}
}

func TestACMEHTTPPortBusyIsNotFatal(t *testing.T) {
	first := New(t.TempDir())
	first.DirectoryURL = "http://127.0.0.1:1/directory"
	first.HTTPAddr = "127.0.0.1:0"
	if _, err := first.Acquire(context.Background(), "a", plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "a.example.com"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Release("a") })

	second := New(t.TempDir())
	second.DirectoryURL = "http://127.0.0.1:1/directory"
	second.HTTPAddr = first.http.addr.String() // taken
	c, err := second.Acquire(context.Background(), "b", plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "b.example.com"})
	if err != nil {
		t.Fatalf("busy :80 must not fail Acquire: %v", err)
	}
	t.Cleanup(func() { second.Release("b") })
	if c.GetCertificate == nil || second.http != nil {
		t.Fatal("expected a usable TLS-ALPN-only source")
	}
}
