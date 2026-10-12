package tcpcheck

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProbeHTTPMarkOursAndOther(t *testing.T) {
	token := []byte("0123456789abcdef")
	answer := []byte("fedcba9876543210")
	path := "/.well-known/acme-challenge/" + hex.EncodeToString(token)

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "ours", body: string(answer), want: VerdictOurs},
		{name: "other", body: "some other response", want: VerdictOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != path || r.Host != "127.0.0.1" {
					t.Errorf("request = %s %s Host=%q", r.Method, r.URL.Path, r.Host)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			port := testPort(t, server.URL)
			family, got, err := Probe(context.Background(), "127.0.0.1", []Target{{
				Port: port, Kind: KindHTTPMark, Token: token, Answer: answer,
			}})
			if err != nil || family != "4" || got[port] != tc.want {
				t.Fatalf("Probe() = family %q, verdicts %v, err %v; want %q", family, got, err, tc.want)
			}
		})
	}
}

func TestProbeReportsRefusedConnection(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	family, got, err := Probe(context.Background(), "127.0.0.1", []Target{{Port: port, Kind: KindTLS, SNI: "example.com"}})
	if err != nil || family != "4" || got[port] != VerdictRefused {
		t.Fatalf("Probe() = family %q, verdicts %v, err %v; want refused", family, got, err)
	}
}

func TestProbeReportsTimeout(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	_, got, err := probe(context.Background(), "127.0.0.1", []Target{{Port: port, Kind: KindTLS, SNI: "example.com"}}, probeOptions{
		connectTimeout: 100 * time.Millisecond,
		requestTimeout: 50 * time.Millisecond,
	})
	if err != nil || got[port] != VerdictTimeout {
		t.Fatalf("probe() = %v, %v; want timeout", got, err)
	}
	select {
	case conn := <-accepted:
		_ = conn.Close()
	default:
	}
}

func TestProbeTLSChainPinAndWrongCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	port := testPort(t, server.URL)
	leaf := server.TLS.Certificates[0].Leaf
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "example.com", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Fatalf("test certificate is not a trusted example.com certificate: %v; DNS names %v, IPs %v", err, leaf.DNSNames, leaf.IPAddresses)
	}
	sum := sha256.Sum256(leaf.Raw)
	pin := hex.EncodeToString(sum[:])

	tests := []struct {
		name  string
		pin   string
		roots *x509.CertPool
		want  string
	}{
		{name: "trusted chain", roots: roots, want: VerdictOurs},
		{name: "reported pin", pin: pin, want: VerdictOurs},
		{name: "wrong certificate", pin: strings.Repeat("0", 64), want: VerdictOther},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, got, err := probe(context.Background(), "127.0.0.1", []Target{{
				Port: port, Kind: KindTLS, SNI: "example.com", PinSHA256: tc.pin,
			}}, probeOptions{rootCAs: tc.roots})
			if err != nil || got[port] != tc.want {
				t.Fatalf("probe() = %v, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func testPort(t *testing.T, rawURL string) uint16 {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseUint(u.Port(), 10, 16)
	if err != nil || n == 0 {
		t.Fatalf("port in %q: %v", rawURL, err)
	}
	return uint16(n)
}
