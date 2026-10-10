package httpserver

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	testRPID     = "localhost"
	testOrigin   = "http://localhost:8081"
	testSecret   = "faketestsecret2345672345" // 24 chars, like `mistgate setup` makes
	testPrefix   = "/" + testSecret + "/"
	testAdminHst = "k7q2x9.example.test"
)

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// testEnv is a panel with all three admin reachability modes enabled at once.
type testEnv struct {
	st     *store.Store
	auth   *auth.Service
	srv    *Server
	public *httptest.Server // main listener: decoy + admin by host + admin by prefix
	admin  *httptest.Server // separate admin listener
}

func newTestEnv(t testing.TB, mutate ...func(*Config)) *testEnv {
	t.Helper()
	return newTestEnvAuth(t, nil, mutate...)
}

// newTestEnvAuth is newTestEnv with a say in the auth service configuration.
func newTestEnvAuth(t testing.TB, authMutate func(*auth.Config), mutate ...func(*Config)) *testEnv {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v, err := vault.New(bytes.Repeat([]byte{5}, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	acfg := auth.Config{RPID: testRPID, Origins: []string{testOrigin}, Vault: v}
	if authMutate != nil {
		authMutate(&acfg)
	}
	a, err := auth.New(st, acfg, quietLog)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		AdminHost:      testAdminHst,
		AdminPrefix:    testPrefix,
		TrustedOrigins: []string{testOrigin},
		Dist: fstest.MapFS{
			"index.html":           {Data: []byte(`<!doctype html><html><head><base href="/"><script type="module" src="./assets/app-abc123.js"></script></head><body>app</body></html>`)},
			"assets/app-abc123.js": {Data: []byte(`console.log(1)`)},
			".gitkeep":             {Data: nil},
		},
		Log: quietLog,
		// The suites hammer one address; the limits have their own tests (ratelimit_test.go).
		Limits: Limits{Public: Rate{PerSecond: -1}, Admin: Rate{PerSecond: -1}, Agent: Rate{PerSecond: -1}},
	}
	for _, m := range mutate {
		m(&cfg)
	}
	srv, err := New(cfg, a, st)
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{st: st, auth: a, srv: srv, public: httptest.NewServer(srv.Public()), admin: httptest.NewServer(srv.AdminListener())}
	t.Cleanup(e.public.Close)
	t.Cleanup(e.admin.Close)
	return e
}

// response is a fully read HTTP response.
type response struct {
	status int
	header http.Header
	body   string
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// do sends a request with the URL used verbatim (no client-side path cleaning).
func do(t *testing.T, method, base, rawPath string, hdr map[string]string) response {
	t.Helper()
	req, err := http.NewRequest(method, base+rawPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, rawPath, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	h := resp.Header.Clone()
	h.Del("Date")
	return response{resp.StatusCode, h, string(b)}
}

// cookieJar is a minimal RoundTripper-level cookie store. net/http/cookiejar refuses to
// send Secure cookies over plain HTTP, which is all httptest gives us.
type cookieJar struct {
	mu     sync.Mutex
	host   string        // overrides the Host header when set
	cookie string        // "name=value" sent on every request
	xff    func() string // if set, the X-Forwarded-For value of each request (tests behind a trusted proxy)
	last   []*http.Cookie
}

func (j *cookieJar) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	j.mu.Lock()
	if j.host != "" {
		r.Host = j.host
	}
	if j.cookie != "" {
		r.Header.Set("Cookie", j.cookie)
	}
	if j.xff != nil {
		r.Header.Set("X-Forwarded-For", j.xff())
	}
	j.mu.Unlock()
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range resp.Cookies() {
		j.last = append(j.last, c)
		if c.MaxAge < 0 || c.Value == "" {
			j.cookie = ""
		} else {
			j.cookie = c.Name + "=" + c.Value
		}
	}
	return resp, nil
}

// loopback is the trusted-proxy list of tests that send X-Forwarded-For from 127.0.0.1.
func loopback() []netip.Prefix {
	return []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
}

// tryConfig builds a server from a minimal Config changed by mutate and returns New's error.
func tryConfig(t *testing.T, mutate func(*Config)) error {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, err := auth.New(st, auth.Config{RPID: testRPID, Origins: []string{testOrigin}}, quietLog)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Dist: fstest.MapFS{}, Log: quietLog}
	mutate(&cfg)
	_, err = New(cfg, a, st)
	return err
}

// newBrowserOn is a client of the admin API at adminBase that uses the given cookie jar.
func newBrowserOn(t *testing.T, adminBase string, jar *cookieJar) *browser {
	t.Helper()
	b := newBrowser(t, adminBase, "")
	b.jar = jar
	b.api = adminv1connect.NewAuthServiceClient(&http.Client{Transport: jar}, adminBase+"api", connect.WithProtoJSON())
	return b
}
