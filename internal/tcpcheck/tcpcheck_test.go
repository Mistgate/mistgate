package tcpcheck

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProbeHTTPMarkOursAndOther(t *testing.T) {
	token := []byte("0123456789abcdef0123456789abcdef")
	answer := []byte("fedcba9876543210fedcba9876543210")
	path := MarkPath(token)
	good := string(MarkBody(token, answer))

	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "ours", body: good, want: VerdictOurs},
		{name: "the bare answer is not the key authorization", body: string(answer), want: VerdictOther},
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

func TestMarkFormatIsLikeLetsEncrypt(t *testing.T) {
	token, answer := make([]byte, MarkSize), make([]byte, MarkSize)
	token[0], answer[0] = 0xfb, 0xff // the base64url alphabet, not the standard one
	if got := MarkPath(token); len(got) != len("/.well-known/acme-challenge/")+43 || strings.ContainsAny(got, "+=") {
		t.Fatalf("MarkPath() = %q, want a 43 character unpadded base64url token", got)
	}
	body := string(MarkBody(token, answer))
	tok, ans, ok := strings.Cut(body, ".")
	if !ok || len(tok) != 43 || len(ans) != 43 || strings.ContainsAny(body, "+/=") {
		t.Fatalf("MarkBody() = %q, want <43 chars>.<43 chars> in base64url", body)
	}
	if err := Validate("127.0.0.1", []Target{{Port: 80, Kind: KindHTTPMark, Token: token[:16], Answer: answer[:16]}}); !errors.Is(err, ErrBadParams) {
		t.Fatalf("a 16 byte mark is valid: %v", err)
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
		sni   string // "" = example.com
		roots *x509.CertPool
		want  string
	}{
		{name: "trusted chain", roots: roots, want: VerdictOurs},
		{name: "reported pin", pin: pin, want: VerdictOurs},
		{name: "wrong certificate", pin: strings.Repeat("0", 64), want: VerdictOther},
		{name: "untrusted chain without a pin", want: VerdictOther},
		{name: "trusted root but wrong SNI", roots: roots, sni: "other.example.org", want: VerdictOther},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sni := tc.sni
			if sni == "" {
				sni = "example.com"
			}
			_, got, err := probe(context.Background(), "127.0.0.1", []Target{{
				Port: port, Kind: KindTLS, SNI: sni, PinSHA256: tc.pin,
			}}, probeOptions{rootCAs: tc.roots})
			if err != nil || got[port] != tc.want {
				t.Fatalf("probe() = %v, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestProbeBoundsTheNameLookup(t *testing.T) {
	hang := func(ctx context.Context, _ string) (netip.Addr, error) {
		<-ctx.Done()
		return netip.Addr{}, ctx.Err()
	}
	start := time.Now()
	_, _, err := probe(context.Background(), "slow.example.org", []Target{{Port: 443, Kind: KindTLS, SNI: "slow.example.org"}},
		probeOptions{resolve: hang, connectTimeout: 50 * time.Millisecond, requestTimeout: time.Minute})
	if !errors.Is(err, ErrNoRoute) || time.Since(start) > 5*time.Second {
		t.Fatalf("probe() = %v after %v, want no_route within the connect timeout", err, time.Since(start))
	}
}

func TestProbeCancelledRunIsAnErrorNotTimeouts(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { // accepts and says nothing: the handshake waits until the context ends
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	_, got, err := probe(ctx, "127.0.0.1", []Target{{Port: port, Kind: KindTLS, SNI: "example.com"}},
		probeOptions{connectTimeout: time.Minute, requestTimeout: time.Minute})
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("probe() = %v, %v; want context.Canceled and no verdicts", got, err)
	}
}

func TestProbeRefusesLinkLocalButNotLoopback(t *testing.T) {
	for host, refused := range map[string]bool{"169.254.169.254": true, "fe80::1": true, "224.0.0.1": true, "0.0.0.0": true,
		"127.0.0.1": false, "::1": false, "203.0.113.10": false} {
		if got := unsupportedDestination(netip.MustParseAddr(host)); got != refused {
			t.Errorf("unsupportedDestination(%s) = %v, want %v", host, got, refused)
		}
	}
	_, _, err := Probe(context.Background(), "169.254.169.254", []Target{{Port: 80, Kind: KindTLS, SNI: "example.com"}})
	if !errors.Is(err, ErrUnsupportedHost) {
		t.Fatalf("Probe(link-local) = %v, want ErrUnsupportedHost", err)
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
