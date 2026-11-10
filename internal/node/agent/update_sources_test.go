package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"log/slog"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/update"
	"github.com/mistgate/mistgate/internal/release"
)

func testRootPool(cert *x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return pool
}

func testManifest(f *updFix, content []byte, version string) (manifest, signature []byte) {
	f.t.Helper()
	sum := sha256.Sum256(content)
	m := &release.Manifest{
		Schema: 1, Version: version, Built: newBuilt, Expires: time.Now().Add(time.Hour).Unix(),
		Files: []release.File{{OS: "linux", Arch: "amd64", Name: binName, Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}},
	}
	b, err := m.Marshal()
	if err != nil {
		f.t.Fatal(err)
	}
	return b, release.Sign(f.priv, b)
}

func newVPSUpdateAgent(t *testing.T, mutate func(*update.Config)) (*Agent, *fakePanel, *updFix) {
	t.Helper()
	panel := newFakePanel(t)
	f := newUpdFix(t)
	f.panel = panel
	key, csr, err := newKeyAndCSR()
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := panel.issue(csr)
	if err != nil {
		t.Fatal(err)
	}
	identityData := identityPEM(key, certPEM)
	cert, err := tls.X509KeyPair(identityData, identityData)
	if err != nil {
		t.Fatal(err)
	}
	cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(panel.caCert)
	meta := Meta{Panel: panel.addr(), SNI: testSNI, NodeID: "nod_test"}
	id := &identity{meta: meta, cert: cert, ca: roots, caCert: panel.caCert}
	cfg := Config{StateDir: t.TempDir()}
	f.cfg(oldBuilt, "v0.1.0-old", mutate)(&cfg)
	a := &Agent{cfg: cfg, meta: meta, upd: cfg.Updater}
	a.id.Store(id)
	a.settings.Store(&pb.NodeSettings{})
	return a, panel, f
}

func applyVPSUpdate(a *Agent, f *updFix, content []byte, sources []string) (*pb.CommandResult, update.Finish) {
	manifest, signature := testManifest(f, content, "v1.2.3")
	req := &pb.UpdateAgent{RequestId: "vps-source-test", Manifest: manifest, Signature: signature, Sources: sources}
	return a.upd.Apply(context.Background(), req, &updSource{a: a, request: req})
}

func newR2TLSServer(t *testing.T, host string, handler http.Handler) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{host}, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, testRootPool(parsed)
}

func loopbackDial(srv *httptest.Server) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
}

func TestGitHubFetcherUsesRangedGETAndNoCredentials(t *testing.T) {
	content := []byte("0123456789abcdef")
	var request *http.Request
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request = r.Clone(r.Context())
		w.Header().Set("Content-Range", "bytes 5-15/16")
		w.Header().Set("Content-Length", "11")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[5:])
	}))
	t.Cleanup(srv.Close)
	oldBase := githubReleaseBase
	githubReleaseBase = srv.URL + "/releases/download/"
	t.Cleanup(func() { githubReleaseBase = oldBase })
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	a := &Agent{updateRootCAs: pool}
	s := updSource{a: a, request: &pb.UpdateAgent{Sources: []string{"github"}}}
	fetchers := s.Fetchers(&release.Manifest{Version: "v1.2.3"}, release.File{Name: "agent.bin", Size: int64(len(content))})
	if len(fetchers) != 1 || fetchers[0].Kind != "github" {
		t.Fatalf("fetchers = %+v", fetchers)
	}
	var got bytes.Buffer
	total, err := fetchers[0].Fetch(context.Background(), 5, &got)
	if err != nil {
		t.Fatal(err)
	}
	if total != uint64(len(content)) || !bytes.Equal(got.Bytes(), content[5:]) {
		t.Fatalf("fetch = %d, %q", total, got.Bytes())
	}
	if request == nil || request.URL.Path != "/releases/download/v1.2.3/agent.bin" || request.Header.Get("Range") != "bytes=5-" {
		t.Fatalf("request path/range = %v / %q", request.URL, request.Header.Get("Range"))
	}
	if request.Header.Get("Accept-Encoding") != "identity" || request.Header.Get("User-Agent") != updateHTTPUserAgent {
		t.Errorf("request headers omitted identity or generic user agent: %v", request.Header)
	}
	if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
		t.Errorf("credentials were sent to GitHub: %v", request.Header)
	}
}

func TestGitHubFetcherDiscardsIgnoredRangePrefixAndReportsActivity(t *testing.T) {
	content := []byte("0123456789abcdef")
	var gotRange string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}))
	t.Cleanup(srv.Close)
	oldBase := githubReleaseBase
	githubReleaseBase = srv.URL + "/releases/download/"
	t.Cleanup(func() { githubReleaseBase = oldBase })
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	s := &updSource{a: &Agent{updateRootCAs: pool}, request: &pb.UpdateAgent{Sources: []string{"github"}}}
	fetcher := s.Fetchers(&release.Manifest{Version: "v1.2.3"}, release.File{Name: "agent.bin", Size: int64(len(content))})[0]
	var activity atomic.Int32
	s.SetActivity(func() { activity.Add(1) })
	var got bytes.Buffer
	total, err := fetcher.Fetch(context.Background(), 5, &got)
	if err != nil {
		t.Fatal(err)
	}
	if total != uint64(len(content)) || got.String() != string(content[5:]) || gotRange != "bytes=5-" {
		t.Errorf("fetch total=%d body=%q range=%q", total, got.String(), gotRange)
	}
	if activity.Load() == 0 {
		t.Error("reading the ignored range prefix did not report network activity")
	}
}

func TestVPSPanelStallResumesFromGitHubAndInstalls(t *testing.T) {
	content := bytes.Repeat([]byte("s"), 2<<20)
	const prefix = 1 << 20
	rangeSeen := make(chan string, 1)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeSeen <- r.Header.Get("Range")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", prefix, len(content)-1, len(content)))
		w.Header().Set("Content-Length", fmt.Sprint(len(content)-prefix))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[prefix:])
	}))
	t.Cleanup(srv.Close)
	oldBase := githubReleaseBase
	githubReleaseBase = srv.URL + "/releases/download/"
	t.Cleanup(func() { githubReleaseBase = oldBase })

	a, panel, f := newVPSUpdateAgent(t, func(c *update.Config) {
		c.Timeout = 3 * time.Second
		c.SourceBackoff = []time.Duration{}
		c.SourceStallTimeout = 100 * time.Millisecond
		c.SourceSlowAfter = time.Hour
	})
	panel.bundle = map[string][]byte{binName: content}
	panel.fetchStallAfter = prefix
	a.updateRootCAs = x509.NewCertPool()
	a.updateRootCAs.AddCert(srv.Certificate())
	a.updateDialContext = loopbackDial(srv)

	res, finish := applyVPSUpdate(a, f, content, []string{"panel", "github"})
	if !res.Ok || finish == nil {
		t.Fatalf("Apply = %+v, finish %v", res, finish != nil)
	}
	select {
	case got := <-rangeSeen:
		if got != "bytes=1048576-" {
			t.Errorf("GitHub Range = %q, want bytes=1048576-", got)
		}
	case <-time.After(time.Second):
		t.Fatal("GitHub source was not requested")
	}
	if got := res.Params["sources_used"]; got != "panel,github" {
		t.Errorf("sources_used = %q", got)
	}
	installed, err := os.ReadFile(f.exe)
	if err != nil || !bytes.Equal(installed, content) {
		t.Error("installed binary differs from the signed content")
	}
	panel.mu.Lock()
	fetches := append([]uint64(nil), panel.fetches...)
	panel.mu.Unlock()
	if len(fetches) != 1 || fetches[0] != 0 {
		t.Errorf("panel offsets = %v, want [0]", fetches)
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
}

func TestVPSPanelResourceExhaustedSwitchesWithoutSpendingRetries(t *testing.T) {
	content := []byte("signed update bytes")
	a, panel, f := newVPSUpdateAgent(t, func(c *update.Config) {
		c.Timeout = 3 * time.Second
		c.SourceBackoff = []time.Duration{}
	})
	panel.bundle = map[string][]byte{binName: content}
	panel.fetchBusy = 3
	var githubCalls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		githubCalls.Add(1)
		http.Error(w, "temporary", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	oldBase := githubReleaseBase
	githubReleaseBase = srv.URL + "/releases/download/"
	t.Cleanup(func() { githubReleaseBase = oldBase })
	a.updateRootCAs = x509.NewCertPool()
	a.updateRootCAs.AddCert(srv.Certificate())
	a.updateDialContext = loopbackDial(srv)

	res, finish := applyVPSUpdate(a, f, content, []string{"panel", "github"})
	if !res.Ok || finish == nil {
		t.Fatalf("Apply = %+v, finish %v", res, finish != nil)
	}
	if got := githubCalls.Load(); got != 3 {
		t.Errorf("GitHub calls = %d, want 3 immediate switches after panel busy responses", got)
	}
	panel.mu.Lock()
	fetches := append([]uint64(nil), panel.fetches...)
	panel.mu.Unlock()
	if len(fetches) != 4 {
		t.Errorf("panel fetch calls = %d, want 4 after three uncharged busy responses", len(fetches))
	}
	if got := res.Params["sources_used"]; got != "panel" {
		t.Errorf("sources_used = %q", got)
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
}

func TestSourceFetcherIgnoresUnknownRepeatedPeerAndUnreleasedGitHub(t *testing.T) {
	a := &Agent{}
	s := updSource{a: a, request: &pb.UpdateAgent{Sources: []string{"peer", "unknown", "github", "github"}}}
	fetchers := s.Fetchers(&release.Manifest{Version: "0.2.0-custom"}, release.File{Name: "agent.bin", Size: 1})
	if len(fetchers) != 0 {
		t.Fatalf("fetchers = %+v, want unsupported requested kinds ignored", fetchers)
	}
}

func TestGitHubFetcherFollowsOnlyAllowedHTTPSRedirects(t *testing.T) {
	content := []byte("signed asset bytes")
	requests := 0
	var lastRange string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/releases/download/v1.2.3/agent.bin" {
			http.Redirect(w, r, "/asset", http.StatusFound)
			return
		}
		lastRange = r.Header.Get("Range")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 5-%d/%d", len(content)-1, len(content)))
		w.Header().Set("Content-Length", fmt.Sprint(len(content)-5))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[5:])
	}))
	t.Cleanup(srv.Close)
	oldBase := githubReleaseBase
	githubReleaseBase = srv.URL + "/releases/download/"
	t.Cleanup(func() { githubReleaseBase = oldBase })
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	s := updSource{a: &Agent{updateRootCAs: pool}, request: &pb.UpdateAgent{Sources: []string{"github"}}}
	fetcher := s.Fetchers(&release.Manifest{Version: "v1.2.3"}, release.File{Name: "agent.bin", Size: int64(len(content))})[0]
	var got bytes.Buffer
	if _, err := fetcher.Fetch(context.Background(), 5, &got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), content[5:]) || requests != 2 || lastRange != "bytes=5-" {
		t.Errorf("redirect fetch bytes=%q requests=%d range=%q", got.Bytes(), requests, lastRange)
	}
}

func TestGitHubRedirectAllowlistUsesInjectedLoopbackDial(t *testing.T) {
	t.Run("release asset host accepted", func(t *testing.T) {
		content := []byte("signed asset")
		var assetRequests atomic.Int32
		assetHost := "release-assets.githubusercontent.com"
		assetSrv, _ := newR2TLSServer(t, assetHost, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			assetRequests.Add(1)
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			_, _ = w.Write(content)
		}))
		githubSrv, _ := newR2TLSServer(t, "github.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://"+assetHost+"/asset", http.StatusFound)
		}))
		roots := x509.NewCertPool()
		roots.AddCert(githubSrv.Certificate())
		roots.AddCert(assetSrv.Certificate())
		a := &Agent{updateRootCAs: roots}
		a.updateDialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			switch host {
			case "github.com":
				return (&net.Dialer{}).DialContext(ctx, network, githubSrv.Listener.Addr().String())
			case assetHost:
				return (&net.Dialer{}).DialContext(ctx, network, assetSrv.Listener.Addr().String())
			default:
				return nil, errors.New("unexpected test dial")
			}
		}
		oldBase := githubReleaseBase
		githubReleaseBase = "https://github.com/Mistgate/mistgate/releases/download/"
		t.Cleanup(func() { githubReleaseBase = oldBase })
		fetcher := (&updSource{a: a, request: &pb.UpdateAgent{Sources: []string{"github"}}}).Fetchers(
			&release.Manifest{Version: "v1.2.3"}, release.File{Name: "agent.bin", Size: int64(len(content))},
		)[0]
		var got bytes.Buffer
		total, err := fetcher.Fetch(context.Background(), 0, &got)
		if err != nil || total != uint64(len(content)) || !bytes.Equal(got.Bytes(), content) || assetRequests.Load() != 1 {
			t.Fatalf("fetch total=%d bytes=%q asset requests=%d err=%v", total, got.Bytes(), assetRequests.Load(), err)
		}
	})

	for _, tc := range []struct {
		name       string
		redirect   string
		longChain  bool
		wantCalls  int32
		wantNoDial string
	}{
		{name: "other host refused", redirect: "https://other.invalid/asset", wantCalls: 1, wantNoDial: "other.invalid"},
		{name: "http refused", redirect: "http://release-assets.githubusercontent.com/asset", wantCalls: 1, wantNoDial: "release-assets.githubusercontent.com"},
		{name: "chain longer than three refused", longChain: true, wantCalls: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			srv, roots := newR2TLSServer(t, "github.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if tc.longChain {
					n := 0
					if r.URL.Path != "/Mistgate/mistgate/releases/download/v1.2.3/agent.bin" {
						_, _ = fmt.Sscanf(r.URL.Path, "/redirect/%d", &n)
					}
					http.Redirect(w, r, fmt.Sprintf("https://github.com/redirect/%d", n+1), http.StatusFound)
					return
				}
				http.Redirect(w, r, tc.redirect, http.StatusFound)
			}))
			var refusedDials atomic.Int32
			a := &Agent{updateRootCAs: roots}
			a.updateDialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				host, _, err := net.SplitHostPort(address)
				if err != nil {
					return nil, err
				}
				if tc.wantNoDial == host {
					refusedDials.Add(1)
					return nil, errors.New("refused redirect dial")
				}
				if host != "github.com" {
					return nil, errors.New("unexpected test dial")
				}
				return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
			}
			oldBase := githubReleaseBase
			githubReleaseBase = "https://github.com/Mistgate/mistgate/releases/download/"
			t.Cleanup(func() { githubReleaseBase = oldBase })
			fetcher := (&updSource{a: a, request: &pb.UpdateAgent{Sources: []string{"github"}}}).Fetchers(
				&release.Manifest{Version: "v1.2.3"}, release.File{Name: "agent.bin", Size: 1},
			)[0]
			_, err := fetcher.Fetch(context.Background(), 0, io.Discard)
			if !errors.Is(err, update.ErrPermanent) || requests.Load() != tc.wantCalls || refusedDials.Load() != 0 {
				t.Errorf("redirect error=%v requests=%d refused-host dials=%d", err, requests.Load(), refusedDials.Load())
			}
		})
	}
}

func TestGitHubFetcherRefusesUntrustedRedirectWithoutDial(t *testing.T) {
	requests := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Redirect(w, r, "https://other.invalid/asset", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	oldBase := githubReleaseBase
	githubReleaseBase = srv.URL + "/releases/download/"
	t.Cleanup(func() { githubReleaseBase = oldBase })
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	var refusedDials atomic.Int32
	a := &Agent{updateRootCAs: pool}
	a.updateDialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if host == "other.invalid" {
			refusedDials.Add(1)
			return nil, errors.New("refused redirect dial")
		}
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	s := updSource{a: a, request: &pb.UpdateAgent{Sources: []string{"github"}}}
	fetcher := s.Fetchers(&release.Manifest{Version: "v1.2.3"}, release.File{Name: "agent.bin", Size: 1})[0]
	_, err := fetcher.Fetch(context.Background(), 0, io.Discard)
	if !errors.Is(err, update.ErrPermanent) || requests != 1 || refusedDials.Load() != 0 {
		t.Errorf("redirect error=%v, requests=%d refused-host dials=%d", err, requests, refusedDials.Load())
	}
}

func TestGitHubFetcherRefusesMoreThanThreeRedirects(t *testing.T) {
	requests := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		n := 0
		if r.URL.Path != "/releases/download/v1.2.3/agent.bin" {
			_, _ = fmt.Sscanf(r.URL.Path, "/redirect/%d", &n)
		}
		http.Redirect(w, r, fmt.Sprintf("/redirect/%d", n+1), http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	oldBase := githubReleaseBase
	githubReleaseBase = srv.URL + "/releases/download/"
	t.Cleanup(func() { githubReleaseBase = oldBase })
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	s := updSource{a: &Agent{updateRootCAs: pool}, request: &pb.UpdateAgent{Sources: []string{"github"}}}
	fetcher := s.Fetchers(&release.Manifest{Version: "v1.2.3"}, release.File{Name: "agent.bin", Size: 1})[0]
	_, err := fetcher.Fetch(context.Background(), 0, io.Discard)
	if !errors.Is(err, update.ErrPermanent) || requests != 4 {
		t.Errorf("redirect error=%v, requests=%d, want 4 network requests", err, requests)
	}
}

func TestGitHubHTTPStatusClasses(t *testing.T) {
	for _, tc := range []struct {
		status    int
		permanent bool
		busy      bool
	}{
		{status: http.StatusRequestTimeout},
		{status: 421},
		{status: 425},
		{status: http.StatusTooManyRequests, busy: true},
		{status: http.StatusUnauthorized, permanent: true},
		{status: http.StatusForbidden, permanent: true},
		{status: http.StatusNotFound, permanent: true},
		{status: http.StatusGone, permanent: true},
	} {
		err := mapHTTPSourceStatus("source", tc.status)
		var failure *update.FetchFailure
		wantCode := fmt.Sprintf("http_%d", tc.status)
		if tc.busy {
			wantCode = "busy"
		}
		if !errors.As(err, &failure) || failure.Code != wantCode {
			t.Fatalf("status %d mapped to %v", tc.status, err)
		}
		if errors.Is(err, update.ErrPermanent) != tc.permanent || errors.Is(err, update.ErrRetryLater) != tc.busy {
			t.Errorf("status %d classes: permanent=%t busy=%t", tc.status, errors.Is(err, update.ErrPermanent), errors.Is(err, update.ErrRetryLater))
		}
	}
}

func TestLegacySourceErrorsKeepSafeClassText(t *testing.T) {
	panelErr := mapPanelSourceError(connect.NewError(connect.CodeUnavailable, errors.New("private-host-name")))
	if !strings.Contains(panelErr.Error(), "unavailable") || strings.Contains(panelErr.Error(), "private-host-name") {
		t.Errorf("panel error text = %q", panelErr)
	}
	linkErr := mapLinkSourceError(&httpStatusError{status: http.StatusRequestTimeout})
	if !strings.Contains(linkErr.Error(), "http_408") || errors.Is(linkErr, update.ErrPermanent) {
		t.Errorf("link error text/class = %q", linkErr)
	}
}

func TestRangedGETInvalidRequestIsPermanent(t *testing.T) {
	_, err := rangedGET(context.Background(), &http.Client{}, "http://%", 0, io.Discard, rangedGETPolicy{})
	if !errors.Is(err, update.ErrPermanent) {
		t.Fatalf("invalid request error = %v, want permanent", err)
	}
}

func TestGitHubSourceFailureFallsBackToPanel(t *testing.T) {
	content := []byte("signed update bytes")
	for _, tc := range []struct {
		name   string
		github func(http.ResponseWriter, *http.Request)
	}{
		{"not found", func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) }},
		{"wrong range start", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 1-%d/%d", len(content)-1, len(content)))
			w.WriteHeader(http.StatusPartialContent)
		}},
		{"wrong total", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(content)-1, len(content)+1))
			w.WriteHeader(http.StatusPartialContent)
		}},
		{"overflow", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(content)-1, len(content)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(append(bytes.Clone(content), '!'))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			panelOffset := -1
			var a *Agent
			var f *updFix
			var srv *httptest.Server
			a, f, srv = newLinkUpdateAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/releases/download/") {
					tc.github(w, r)
					return
				}
				panelOffset = 0
				if r.Header.Get("Range") != "" {
					_, _ = fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-", &panelOffset)
				}
				w.Header().Set("Content-Length", fmt.Sprint(len(content)))
				_, _ = w.Write(content)
			}), func(c *update.Config) { c.SourceBackoff = []time.Duration{} })
			oldBase := githubReleaseBase
			githubReleaseBase = srv.URL + "/releases/download/"
			t.Cleanup(func() { githubReleaseBase = oldBase })
			a.updateRootCAs = testRootPool(srv.Certificate())
			manifest, signature := testManifest(f, content, "v0.2.0")
			req := &pb.UpdateAgent{RequestId: "source-fallback", Manifest: manifest, Signature: signature,
				DownloadToken: linkDownloadToken, Sources: []string{"github", "panel"}}
			res, finish := a.upd.Apply(context.Background(), req, &updSource{a: a, token: linkDownloadToken, request: req})
			if !res.Ok || finish == nil || res.Params["sources_used"] != "panel" || panelOffset != 0 {
				t.Fatalf("Apply = %+v, finish %v, panel offset %d", res, finish != nil, panelOffset)
			}
			if err := finish(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCloudflareValidR2RangeAndNoRedirect(t *testing.T) {
	host := strings.Repeat("a", 32) + ".r2.cloudflarestorage.com"
	if !validCloudflareURL("https://" + host + ":443/bundle?sig=test") {
		t.Fatal("port 443 should be accepted for a valid R2 host")
	}
	content := []byte("0123456789abcdef")
	var requests atomic.Int32
	var rangeHeader, authHeader, cookieHeader string
	srv, roots := newR2TLSServer(t, host, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		rangeHeader, authHeader, cookieHeader = r.Header.Get("Range"), r.Header.Get("Authorization"), r.Header.Get("Cookie")
		w.Header().Set("Content-Range", "bytes 5-15/16")
		w.Header().Set("Content-Length", "11")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(content[5:])
	}))
	a := &Agent{updateRootCAs: roots, updateDialContext: loopbackDial(srv)}
	rawURL := "https://" + host + "/bundle?X-Amz-Credential=presign-test"
	s := updSource{a: a, request: &pb.UpdateAgent{Sources: []string{"cloudflare"}, CloudflareUrl: rawURL}}
	fetcher := s.Fetchers(&release.Manifest{Version: "v1.2.3"}, release.File{Name: "agent.bin", Size: int64(len(content))})[0]
	var got bytes.Buffer
	total, err := fetcher.Fetch(context.Background(), 5, &got)
	if err != nil {
		t.Fatal(err)
	}
	if total != uint64(len(content)) || !bytes.Equal(got.Bytes(), content[5:]) || requests.Load() != 1 {
		t.Errorf("cloudflare fetch total=%d bytes=%q requests=%d", total, got.Bytes(), requests.Load())
	}
	if rangeHeader != "bytes=5-" || authHeader != "" || cookieHeader != "" {
		t.Errorf("cloudflare request headers range=%q authorization=%q cookie=%q", rangeHeader, authHeader, cookieHeader)
	}
}

func TestCloudflareRejectsInvalidURLsBeforeDial(t *testing.T) {
	host := strings.Repeat("b", 32) + ".r2.cloudflarestorage.com"
	var dials atomic.Int32
	a := &Agent{updateDialContext: func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("unexpected dial")
	}}
	badURLs := []string{
		"https://other.invalid/bundle",
		"https://" + strings.Repeat("a", 31) + ".r2.cloudflarestorage.com/bundle",
		"http://" + host + "/bundle",
		"https://" + host + ":444/bundle",
		"https://alice@" + host + "/bundle",
		"https://127.0.0.1/bundle",
	}
	for _, rawURL := range badURLs {
		s := updSource{a: a, request: &pb.UpdateAgent{Sources: []string{"cloudflare"}, CloudflareUrl: rawURL}}
		fetcher := s.Fetchers(&release.Manifest{Version: "v1.2.3"}, release.File{Name: "agent.bin", Size: 1})[0]
		_, err := fetcher.Fetch(context.Background(), 0, io.Discard)
		if !errors.Is(err, update.ErrPermanent) || strings.Contains(err.Error(), rawURL) {
			t.Errorf("invalid URL was not refused safely: %v", err)
		}
	}
	if dials.Load() != 0 {
		t.Errorf("invalid URLs caused %d dials", dials.Load())
	}
}

func TestCloudflareRedirectIsNotFollowed(t *testing.T) {
	host := strings.Repeat("c", 32) + ".eu.r2.cloudflarestorage.com"
	var requests atomic.Int32
	srv, roots := newR2TLSServer(t, host, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Redirect(w, r, "/redirect-target", http.StatusFound)
	}))
	a := &Agent{updateRootCAs: roots, updateDialContext: loopbackDial(srv)}
	s := updSource{a: a, request: &pb.UpdateAgent{Sources: []string{"cloudflare"}, CloudflareUrl: "https://" + host + "/bundle?sig=presign-test"}}
	fetcher := s.Fetchers(&release.Manifest{Version: "v1.2.3"}, release.File{Name: "agent.bin", Size: 1})[0]
	_, err := fetcher.Fetch(context.Background(), 0, io.Discard)
	if !errors.Is(err, update.ErrPermanent) || requests.Load() != 1 || strings.Contains(err.Error(), host) {
		t.Errorf("redirect = %v, requests=%d", err, requests.Load())
	}
}

func TestCloudflareURLNeverReachesResultsOrLogs(t *testing.T) {
	host := strings.Repeat("d", 32) + ".fedramp.r2.cloudflarestorage.com"
	var logs lockedLogBuffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	srv, roots := newR2TLSServer(t, host, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	a, f, _ := newLinkUpdateAgent(t, http.NotFoundHandler(), func(c *update.Config) { c.Log = logger })
	a.updateRootCAs = roots
	a.updateDialContext = loopbackDial(srv)
	content := []byte("new signed binary")
	manifest, signature := f.manifest(content, newBuilt)
	secretURL := "https://" + host + "/bundle?X-Amz-Credential=presign-test-secret"
	req := &pb.UpdateAgent{RequestId: "cloudflare-redaction", Manifest: manifest, Signature: signature, Sources: []string{"cloudflare"}, CloudflareUrl: secretURL}
	res, finish := a.upd.Apply(context.Background(), req, &updSource{a: a, request: req})
	if finish != nil || res.Ok || res.Error != "download_failed" || res.Params["sources_failed"] != "cloudflare:http_404" {
		t.Fatalf("Apply = %+v, finish %v", res, finish != nil)
	}
	combined := res.Detail + " " + res.Params["sources_used"] + " " + res.Params["sources_failed"] + " " + logs.String()
	if strings.Contains(combined, secretURL) || strings.Contains(combined, "presign-test-secret") || strings.Contains(combined, host) {
		t.Errorf("Cloudflare URL data appeared in update output: %s", combined)
	}
}
