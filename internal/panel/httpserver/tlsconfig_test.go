package httpserver

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
)

func hello(name string, protos ...string) *tls.ClientHelloInfo {
	return &tls.ClientHelloInfo{ServerName: name, SupportedProtos: protos}
}

func TestPublicTLSStaticCertificate(t *testing.T) {
	e := newTestEnv(t)
	dir := t.TempDir()
	cert := selfSigned(t, "example.test")
	certFile, keyFile := writePEM(t, dir, cert)

	if cfg, mgr, err := e.srv.publicTLS(ServeOptions{}); cfg != nil || mgr != nil || err != nil {
		t.Fatalf("no TLS requested: %v %v %v", cfg, mgr, err)
	}
	cfg, mgr, err := e.srv.publicTLS(ServeOptions{TLSCert: certFile, TLSKey: keyFile})
	if err != nil || mgr != nil {
		t.Fatal(err)
	}
	if cfg.MinVersion < tls.VersionTLS12 || strings.Join(cfg.NextProtos, ",") != "h2,http/1.1" {
		t.Errorf("config: min %x protos %v", cfg.MinVersion, cfg.NextProtos)
	}
	for _, name := range []string{"example.test", "anything.else", ""} {
		got, err := cfg.GetCertificate(hello(name))
		if err != nil || string(got.Certificate[0]) != string(cert.Certificate[0]) {
			t.Errorf("SNI %q: %v", name, err)
		}
	}
	if _, _, err := e.srv.publicTLS(ServeOptions{TLSCert: certFile + ".missing", TLSKey: keyFile}); err == nil {
		t.Error("missing certificate file accepted")
	}
	if _, _, err := e.srv.publicTLS(ServeOptions{TLSCert: keyFile, TLSKey: keyFile}); err == nil {
		t.Error("mismatched pair accepted")
	}
}

func TestKeyPairIsReloadedWhenTheFileChanges(t *testing.T) {
	dir := t.TempDir()
	first := selfSigned(t, "example.test")
	certFile, keyFile := writePEM(t, dir, first)
	k, err := newKeyPair(certFile, keyFile, quietLog)
	if err != nil {
		t.Fatal(err)
	}
	same := func(c *tls.Certificate, want tls.Certificate) bool {
		return string(c.Certificate[0]) == string(want.Certificate[0])
	}
	if !same(k.get(), first) {
		t.Fatal("first certificate")
	}
	second := selfSigned(t, "example.test")
	writePEM(t, dir, second)
	future := time.Now().Add(time.Minute)
	os.Chtimes(certFile, future, future)
	if !same(k.get(), first) { // within the recheck interval: no disk access
		t.Fatal("reloaded before the recheck interval")
	}
	k.mu.Lock()
	k.checked = time.Now().Add(-2 * recheck)
	k.mu.Unlock()
	if !same(k.get(), second) {
		t.Fatal("renewed certificate not picked up")
	}
	// A half-written renewal keeps the old certificate serving.
	os.WriteFile(certFile, []byte("garbage"), 0o600)
	later := future.Add(time.Minute)
	os.Chtimes(certFile, later, later)
	k.mu.Lock()
	k.checked = time.Now().Add(-2 * recheck)
	k.mu.Unlock()
	if !same(k.get(), second) {
		t.Fatal("broken renewal replaced a working certificate")
	}
}

func TestPublicTLSWithACME(t *testing.T) {
	e := newTestEnv(t)
	dir := t.TempDir()
	cacheDir := dir + "/acme"

	// ACME only: names outside the list get no certificate (and no network traffic).
	cfg, mgr, err := e.srv.publicTLS(ServeOptions{ACMEDomains: []string{"Example.COM.", "www.example.com"}, ACMEDir: cacheDir})
	if err != nil || mgr == nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.NextProtos, ",") != "h2,http/1.1,"+acme.ALPNProto {
		t.Errorf("protocols %v", cfg.NextProtos)
	}
	if mgr.HostPolicy(context.Background(), "example.com") != nil || mgr.HostPolicy(context.Background(), "www.example.com") != nil {
		t.Error("configured domain refused")
	}
	for _, bad := range []string{"evil.example", "example.com.evil", "sub.example.com", ""} {
		if mgr.HostPolicy(context.Background(), bad) == nil {
			t.Errorf("host policy accepts %q", bad)
		}
		if _, err := cfg.GetCertificate(hello(bad)); err == nil {
			t.Errorf("certificate issued for %q", bad)
		}
	}
	// TLS-ALPN-01 challenge handshakes are answered from the challenge store, never by
	// starting an order.
	if _, err := cfg.GetCertificate(hello("example.com", acme.ALPNProto)); err == nil || !strings.Contains(err.Error(), "no token cert") {
		t.Errorf("challenge handshake for a name without a pending challenge: %v", err)
	}
	if cfg.GetConfigForClient == nil {
		t.Error("agent hook not installed")
	}

	// Static certificate next to ACME: ACME names (and challenges) go to the manager, every other name to the static pair.
	cert := selfSigned(t, "other.test")
	certFile, keyFile := writePEM(t, dir, cert)
	cfg, _, err = e.srv.publicTLS(ServeOptions{TLSCert: certFile, TLSKey: keyFile, ACMEDomains: []string{"example.com"}, ACMEDir: cacheDir})
	if err != nil {
		t.Fatal(err)
	}
	got, err := cfg.GetCertificate(hello("other.test"))
	if err != nil || string(got.Certificate[0]) != string(cert.Certificate[0]) {
		t.Errorf("static certificate for a non-ACME name: %v", err)
	}
	if got, err := cfg.GetCertificate(hello("")); err != nil || string(got.Certificate[0]) != string(cert.Certificate[0]) {
		t.Errorf("static certificate without SNI: %v", err)
	}
	if _, err := cfg.GetCertificate(hello("example.com", acme.ALPNProto)); err == nil || !strings.Contains(err.Error(), "no token cert") {
		t.Errorf("challenge handshake with a static pair present: %v", err)
	}

	for _, bad := range []string{"", "localhost", "*.example.com", "a/b.example.com", "a b.example.com", "example.com:443"} {
		if _, _, err := e.srv.publicTLS(ServeOptions{ACMEDomains: []string{bad}, ACMEDir: cacheDir}); err == nil {
			t.Errorf("ACME domain %q accepted", bad)
		}
	}
}

func TestACMEHTTPListenerHandler(t *testing.T) {
	e := newTestEnv(t)
	_, mgr, err := e.srv.publicTLS(ServeOptions{ACMEDomains: []string{"example.com"}, ACMEDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	h := e.srv.acmeHTTP(mgr, []string{"example.com"}, "443")
	h8443 := e.srv.acmeHTTP(mgr, []string{"example.com"}, "8443")
	ask := func(h http.Handler, method, host, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		req.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	for _, tc := range []struct {
		h               http.Handler
		method, host, u string
		want            string
	}{
		{h, "GET", "example.com", "/", "https://example.com/"},
		{h, "GET", "EXAMPLE.com:80", "/a/b?x=1&y=2", "https://example.com/a/b?x=1&y=2"},
		{h, "POST", "example.com", "/form", "https://example.com/form"},
		{h, "HEAD", "example.com.", "/" + testSecret + "/", "https://example.com/" + testSecret + "/"},
		{h8443, "GET", "example.com", "/x", "https://example.com:8443/x"},
	} {
		w := ask(tc.h, tc.method, tc.host, tc.u)
		if w.Code != http.StatusPermanentRedirect || w.Header().Get("Location") != tc.want {
			t.Errorf("%s %s%s: %d %q, want %q", tc.method, tc.host, tc.u, w.Code, w.Header().Get("Location"), tc.want)
		}
	}
	// Unknown hosts and odd paths are the decoy's 404, never a redirect to somewhere else.
	want := ask(h, "GET", "example.com", "/a/../b")
	for _, tc := range [][2]string{{"evil.example", "/"}, {"example.com.evil", "/"}, {"", "/"}, {"example.com", "//x"}, {"example.com", "/a/../b"}, {"example.com", "/%2e%2e/x"}} {
		w := ask(h, "GET", tc[0], tc[1])
		if w.Code != 404 || w.Header().Get("Location") != "" || w.Body.String() != want.Body.String() || !strings.Contains(w.Body.String(), "Not Found") {
			t.Errorf("host %q %s: %d %q", tc[0], tc[1], w.Code, w.Header().Get("Location"))
		}
	}
	// HTTP-01: the challenge path is the manager's (404 for an unknown token, 403 for a host
	// that is not configured), and does not redirect.
	if w := ask(h, "GET", "example.com", "/.well-known/acme-challenge/tok"); w.Code != 404 || w.Header().Get("Location") != "" {
		t.Errorf("challenge, unknown token: %d", w.Code)
	}
	if w := ask(h, "GET", "evil.example", "/.well-known/acme-challenge/tok"); w.Code != 403 {
		t.Errorf("challenge, foreign host: %d", w.Code)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func waitFor(t *testing.T, addr string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("nothing listens on %s", addr)
}

// Serve end to end: plain listeners with the decoy in front of malformed requests, then
// TLS with a static certificate next to ACME domains and the port-80 redirect.
func TestServeListeners(t *testing.T) {
	e := newTestEnv(t)
	run := func(o ServeOptions) (stop func()) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- e.srv.Serve(ctx, o) }()
		return func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Serve: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("Serve did not stop")
			}
		}
	}

	pub, adm := freeAddr(t), freeAddr(t)
	stop := run(ServeOptions{Public: pub, Admin: adm})
	waitFor(t, pub)
	waitFor(t, adm)
	if r := do(t, http.MethodGet, "http://"+pub, "/", nil); r.status != 200 || !strings.Contains(r.body, "Coming soon") {
		t.Errorf("decoy: %d", r.status)
	}
	if r := postMe(t, "http://"+adm, "/", nil); !isAdminAPI(r) {
		t.Errorf("admin listener: %+v", r)
	}
	ref := do(t, http.MethodGet, "http://"+pub, "/nothing", nil)
	if st, _, body := parseRaw(t, rawExchange(t, pub, "GARBAGE\r\n\r\n")); !strings.Contains(st, "404") || body != ref.body {
		t.Errorf("malformed request on the public listener: %q %q", st, body)
	}
	if st, _, body := parseRaw(t, rawExchange(t, adm, "GARBAGE\r\n\r\n")); !strings.Contains(st, "404") || body != ref.body {
		t.Errorf("malformed request on the admin listener: %q %q", st, body)
	}
	stop()

	// TLS: a static pair serves every name the ACME list does not cover; port 80 redirects.
	cert := selfSigned(t, "other.test")
	certFile, keyFile := writePEM(t, t.TempDir(), cert)
	pub, httpAddr := freeAddr(t), freeAddr(t)
	stop = run(ServeOptions{Public: pub, TLSCert: certFile, TLSKey: keyFile, ACMEDomains: []string{"example.test"}, ACMEDir: t.TempDir(), ACMEHTTP: httpAddr})
	waitFor(t, pub)
	waitFor(t, httpAddr)
	code, body, proto, err := get(t, tlsClient(pub, nil), "https://other.test/", "")
	if err != nil || code != 200 || !strings.Contains(body, "Coming soon") || proto != "HTTP/2.0" {
		t.Errorf("HTTPS with the static pair: %d %s %v", code, proto, err)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", "http://"+httpAddr+"/x?y=1", nil)
	req.Host = "example.test"
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	_, port, _ := net.SplitHostPort(pub)
	if resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != "https://example.test:"+port+"/x?y=1" {
		t.Errorf("port 80 redirect: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	stop()

	// Options that cannot work are refused up front.
	if err := e.srv.Serve(context.Background(), ServeOptions{Public: freeAddr(t), AgentListen: freeAddr(t)}); err == nil {
		t.Error("agent listener without hooks accepted")
	}
	if err := e.srv.Serve(context.Background(), ServeOptions{Public: freeAddr(t), TLSCert: "nope", TLSKey: "nope"}); err == nil {
		t.Error("missing certificate accepted")
	}
}
