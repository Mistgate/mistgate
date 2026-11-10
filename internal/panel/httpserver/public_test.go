package httpserver

import (
	"context"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/subs"
)

type oneToken struct{ token string }

func (o oneToken) Subscription(_ context.Context, token string) (access.SubView, error) {
	if token != o.token {
		return access.SubView{}, access.ErrUnknownToken
	}
	return access.SubView{Status: access.StatusActive, Lines: []string{"hysteria2://u@de1.example.com:443/"}}, nil
}

// The real subscription handler behind the public listener: an unknown token, a token of the wrong
// shape and a wrong method under the secret prefix answer exactly like an unknown path outside it,
// and a client that guessed too often gets the decoy for a valid token as well.
func TestSubscriptionMountIsIndistinguishableFromTheDecoy(t *testing.T) {
	valid := strings.Repeat("v", 43)
	var e *testEnv
	var once sync.Once
	mux := http.NewServeMux()
	mount := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { // e exists by the first request
			mux.Handle("GET /brand/logo.svg", NewLogoHandler(e.st, quietLog))
			mux.Handle("/", subs.Handler(oneToken{valid}, NewDecoy(""), subs.Config{Title: "t", MissLimit: 6}))
		})
		mux.ServeHTTP(w, r)
	})
	e = newTestEnvAuth(t, func(c *auth.Config) { c.TrustedProxies = loopback() }, func(c *Config) { c.PublicMounts = map[string]http.Handler{subPrefix: mount} })
	ip := xff("203.0.113.44")
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		want := do(t, method, e.public.URL, "/"+strings.Repeat("u", 43), ip) // a token-shaped path outside the prefix
		for name, path := range map[string]string{
			"unknown token": subPrefix + strings.Repeat("u", 43),
			"short token":   subPrefix + "abc",
			"bare prefix":   subPrefix,
		} {
			sameResponse(t, method+" "+name, want, do(t, method, e.public.URL, path, ip))
		}
	}
	if r := do(t, http.MethodGet, e.public.URL, subPrefix+valid, xff("203.0.113.45")); r.status != 200 || r.header.Get("Profile-Title") == "" {
		t.Fatalf("valid token: %d %v", r.status, r.header)
	}
	// The cached valid token remains available behind the blocked client address.
	if r := do(t, http.MethodGet, e.public.URL, subPrefix+valid, ip); r.status != 200 || r.header.Get("Profile-Title") == "" {
		t.Fatalf("blocked client with a cached valid token: %d %v", r.status, r.header)
	}
	want := do(t, http.MethodGet, e.public.URL, "/nothing", ip)
	sameResponse(t, "blocked client, unknown token", want, do(t, http.MethodGet, e.public.URL, subPrefix+strings.Repeat("u", 43), ip))
}

func TestServiceWorkerRequestsReceiveTheDecoy404(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"index.html": "<h1>Example Studio</h1>",
		"sw.js":      "self.addEventListener('fetch', () => {})",
		"sw.ecma":    "self.addEventListener('fetch', () => {})",
		"plain.txt":  "a public text file",
		"404.html":   "<h1>Lost at sea</h1>",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := mime.AddExtensionType(".ecma", "application/ecmascript"); err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, func(c *Config) { c.DecoyDir = dir })
	header := map[string]string{"Service-Worker": "script"}
	plainDecoy := do(t, http.MethodGet, e.public.URL, "/sw.js", nil)
	if plainDecoy.status != http.StatusOK || !strings.HasPrefix(plainDecoy.header.Get("Content-Type"), "text/javascript") {
		t.Fatalf("ordinary decoy script: %d %q %q", plainDecoy.status, plainDecoy.header.Get("Content-Type"), plainDecoy.body)
	}
	markedDecoy := do(t, http.MethodGet, e.public.URL, "/sw.js", header)
	if markedDecoy.status != http.StatusNotFound {
		t.Fatalf("service-worker decoy script: %d %q", markedDecoy.status, markedDecoy.body)
	}
	plainECMAScript := do(t, http.MethodGet, e.public.URL, "/sw.ecma", nil)
	markedECMAScript := do(t, http.MethodGet, e.public.URL, "/sw.ecma", header)
	if plainECMAScript.status != http.StatusOK || plainECMAScript.header.Get("Content-Type") != "application/ecmascript" {
		t.Fatalf("registered ECMAScript asset: %d %q", plainECMAScript.status, plainECMAScript.header.Get("Content-Type"))
	}
	if markedECMAScript.status != http.StatusNotFound || !strings.Contains(markedECMAScript.body, "Lost at sea") {
		t.Fatalf("service-worker ECMAScript asset: %d %q", markedECMAScript.status, markedECMAScript.body)
	}
	plainRoot := do(t, http.MethodGet, e.public.URL, "/", nil)
	markedRoot := do(t, http.MethodGet, e.public.URL, "/", header)
	if plainRoot.status != http.StatusOK || markedRoot.status != plainRoot.status || markedRoot.body != plainRoot.body ||
		markedRoot.header.Get("Content-Type") != plainRoot.header.Get("Content-Type") {
		t.Fatalf("Service-Worker header changed the HTML root: plain=%d %q %q marked=%d %q %q", plainRoot.status,
			plainRoot.header.Get("Content-Type"), plainRoot.body, markedRoot.status, markedRoot.header.Get("Content-Type"), markedRoot.body)
	}
	for _, tc := range []struct {
		name string
		url  string
		path string
		head map[string]string
	}{
		{name: "prefix admin", url: e.public.URL, path: testPrefix + "assets/app-abc123.js/"},
		{name: "host admin", url: e.public.URL, path: "/assets/app-abc123.js/", head: map[string]string{"Host": testAdminHst}},
		{name: "admin listener", url: e.admin.URL, path: "/assets/app-abc123.js/"},
	} {
		marked := do(t, http.MethodGet, tc.url, tc.path, headerWithHost(header, tc.head))
		if marked.status != http.StatusNotFound || !strings.Contains(marked.body, "Lost at sea") {
			t.Errorf("%s trailing-slash script: %d %q", tc.name, marked.status, marked.body)
		}
		if marked.header.Get("Service-Worker-Allowed") != "" {
			t.Errorf("%s allows a service-worker scope: %q", tc.name, marked.header.Get("Service-Worker-Allowed"))
		}
	}
	if r := do(t, http.MethodGet, e.public.URL, testPrefix+"assets/app-abc123.js", nil); r.status != http.StatusOK || r.body != "console.log(1)" {
		t.Fatalf("regular admin asset: %d %q", r.status, r.body)
	}
	adminPlain := do(t, http.MethodGet, e.admin.URL, "/assets/app-abc123.js", nil)
	adminMarked := do(t, http.MethodGet, e.admin.URL, "/assets/app-abc123.js", header)
	if adminPlain.status != http.StatusOK || adminMarked.status != http.StatusNotFound {
		t.Fatalf("separate admin listener script: plain=%d marked=%d", adminPlain.status, adminMarked.status)
	}
	adminRoot := do(t, http.MethodGet, e.admin.URL, "/", nil)
	adminRootMarked := do(t, http.MethodGet, e.admin.URL, "/", header)
	if adminRoot.status != http.StatusOK || adminRootMarked.status != adminRoot.status || adminRootMarked.body != adminRoot.body {
		t.Fatalf("Service-Worker header changed the admin HTML root: plain=%d marked=%d", adminRoot.status, adminRootMarked.status)
	}

	for _, tc := range []struct {
		name string
		url  string
		path string
		head map[string]string
	}{
		{name: "public root", url: e.public.URL, path: "/"},
		{name: "prefix admin root", url: e.public.URL, path: testPrefix},
		{name: "host admin root", url: e.public.URL, path: "/", head: map[string]string{"Host": testAdminHst}},
		{name: "admin listener root", url: e.admin.URL, path: "/"},
		{name: "public text asset", url: e.public.URL, path: "/plain.txt"},
		{name: "prefix admin text asset", url: e.public.URL, path: testPrefix + "assets/readme.txt"},
		{name: "host admin text asset", url: e.public.URL, path: "/assets/readme.txt", head: map[string]string{"Host": testAdminHst}},
		{name: "admin listener text asset", url: e.admin.URL, path: "/assets/readme.txt"},
	} {
		plain := do(t, http.MethodGet, tc.url, tc.path, tc.head)
		marked := do(t, http.MethodGet, tc.url, tc.path, headerWithHost(header, tc.head))
		if marked.status != plain.status || marked.body != plain.body || marked.header.Get("Content-Type") != plain.header.Get("Content-Type") {
			t.Errorf("%s changed with Service-Worker header: plain=%d %q %q marked=%d %q %q", tc.name, plain.status,
				plain.header.Get("Content-Type"), plain.body, marked.status, marked.header.Get("Content-Type"), marked.body)
		}
	}
}

func TestJavaScriptMIMETypeSetMatchesBrowserSniffingList(t *testing.T) {
	for _, contentType := range []string{
		"application/ecmascript", "application/javascript", "application/x-ecmascript", "application/x-javascript",
		"text/ecmascript", "text/javascript", "text/javascript1.0", "text/javascript1.1", "text/javascript1.2",
		"text/javascript1.3", "text/javascript1.4", "text/javascript1.5", "text/jscript", "text/livescript",
		"text/x-ecmascript", "text/x-javascript",
	} {
		if !isJavaScriptMIMEType(contentType) {
			t.Errorf("%q was not recognized as a JavaScript MIME type", contentType)
		}
	}
	if !isJavaScriptMIMEType("Text/JavaScript; charset=utf-8") {
		t.Error("case and parameters should not change JavaScript MIME type recognition")
	}
	for _, contentType := range []string{"text/plain", "application/json", "text/html"} {
		if isJavaScriptMIMEType(contentType) {
			t.Errorf("%q was recognized as a JavaScript MIME type", contentType)
		}
	}
}

func headerWithHost(header, host map[string]string) map[string]string {
	merged := make(map[string]string, len(header)+len(host))
	for k, v := range header {
		merged[k] = v
	}
	for k, v := range host {
		merged[k] = v
	}
	return merged
}

func TestServiceWorkerRequestsUseThePublicRateLimit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>Example Studio</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sw.js"), []byte("self.addEventListener('fetch', () => {})"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := limited(t, Limits{Public: Rate{PerSecond: 0.1, Burst: 3}}, func(c *Config) { c.DecoyDir = dir })
	client := xff("203.0.113.77")
	header := map[string]string{"Service-Worker": "script", "X-Forwarded-For": "203.0.113.77"}
	plain := do(t, http.MethodGet, e.public.URL, "/", client)
	marked := do(t, http.MethodGet, e.public.URL, "/", header)
	if plain.status != http.StatusOK || marked.status != plain.status || marked.body != plain.body {
		t.Fatalf("root responses differ: plain=%d %q marked=%d %q", plain.status, plain.body, marked.status, marked.body)
	}
	if r := do(t, http.MethodGet, e.public.URL, "/sw.js", header); r.status != http.StatusNotFound {
		t.Fatalf("marked script response: %d %q", r.status, r.body)
	}
	if r := do(t, http.MethodGet, e.public.URL, "/", client); r.status != http.StatusTooManyRequests {
		t.Fatalf("requests with Service-Worker header skipped the public budget: %d %q", r.status, r.body)
	}
}

func TestServiceWorkerAdminRequestsUseTheAdminRateLimit(t *testing.T) {
	cases := []struct {
		name string
		call func(*testing.T, *testEnv, map[string]string) response
	}{
		{
			name: "host admin",
			call: func(t *testing.T, e *testEnv, header map[string]string) response {
				return do(t, http.MethodGet, e.public.URL, "/assets/app-abc123.js", withHeader(header, "Host", testAdminHst))
			},
		},
		{
			name: "prefix admin",
			call: func(t *testing.T, e *testEnv, header map[string]string) response {
				return do(t, http.MethodGet, e.public.URL, testPrefix+"assets/app-abc123.js", header)
			},
		},
		{
			name: "separate admin listener",
			call: func(t *testing.T, e *testEnv, header map[string]string) response {
				return do(t, http.MethodGet, e.admin.URL, "/assets/app-abc123.js", header)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := limited(t, Limits{Public: Rate{PerSecond: -1}, Admin: Rate{PerSecond: 0.1, Burst: 1}})
			header := map[string]string{"Service-Worker": "script"}
			if r := tc.call(t, e, header); r.status != http.StatusNotFound {
				t.Fatalf("script response: %d %q", r.status, r.body)
			}
			if r := tc.call(t, e, nil); r.status != http.StatusTooManyRequests {
				t.Fatalf("service-worker request skipped the admin budget: %d %q", r.status, r.body)
			}
		})
	}
}

func withHeader(in map[string]string, key, value string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	out[key] = value
	return out
}

func TestAgentLinkUsesOnlyItsSecretPath(t *testing.T) {
	prefix := "/" + strings.Repeat("l", 24) + "/"
	var hits atomic.Int32
	var gotPath string
	e := newTestEnv(t, func(c *Config) {
		c.AgentLinkPrefix = prefix
		c.AgentLinkHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			gotPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		})
	})
	if r := do(t, http.MethodGet, e.public.URL, prefix+"link/node_de", nil); r.status != http.StatusNoContent || gotPath != "/link/node_de" || hits.Load() != 1 {
		t.Fatalf("link route: status=%d path=%q hits=%d", r.status, gotPath, hits.Load())
	}
	for _, path := range []string{prefix + "other", "/link/node_de"} {
		before := hits.Load()
		do(t, http.MethodGet, e.public.URL, path, nil)
		if hits.Load() != before {
			t.Errorf("unrecognized path %q reached the link handler", path)
		}
	}
}

// Link-only agents enrol with one POST under the secret prefix; every other path there (Renew, the other service, a
// near-miss spelling) gets the decoy without reaching the handler, and so does the enrol path with another method.
func TestAgentLinkAdmitsOnlyTheEnrolPathBesidesLinks(t *testing.T) {
	prefix := "/" + strings.Repeat("l", 24) + "/"
	enrol := "mistgate.agent.v1.EnrollmentService/Enroll"
	var hits atomic.Int32
	var gotPath string
	e := newTestEnv(t, func(c *Config) {
		c.AgentLinkPrefix = prefix
		c.AgentLinkHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			gotPath = r.URL.Path
			if r.Method != http.MethodPost || r.URL.Path != "/"+enrol {
				http.NotFound(w, r) // what the real handler does
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	})
	if r := do(t, http.MethodPost, e.public.URL, prefix+enrol, nil); r.status != http.StatusNoContent || gotPath != "/"+enrol || hits.Load() != 1 {
		t.Fatalf("enrol route: status=%d path=%q hits=%d", r.status, gotPath, hits.Load())
	}
	decoy := do(t, http.MethodGet, e.public.URL, "/nothing-here", nil)
	for _, path := range []string{
		prefix + "mistgate.agent.v1.EnrollmentService/Renew",
		prefix + "mistgate.agent.v1.AgentService/Connect",
		prefix + enrol + "/",
		prefix + "mistgate.agent.v1.EnrollmentService/enroll",
		prefix + "x/" + enrol,
		"/" + enrol,
	} {
		before := hits.Load()
		sameResponse(t, "POST "+path, decoy, do(t, http.MethodPost, e.public.URL, path, nil))
		if hits.Load() != before {
			t.Errorf("%q reached the link handler", path)
		}
	}
	// The enrol path with a method the handler refuses is answered exactly like the decoy.
	sameResponse(t, "GET enrol path", decoy, do(t, http.MethodGet, e.public.URL, prefix+enrol, nil))
}

func TestAgentLinkRoutesOnlyWellFormedUpdateAssetGets(t *testing.T) {
	prefix := "/" + strings.Repeat("u", 24) + "/"
	var hits atomic.Int32
	e := newTestEnv(t, func(c *Config) {
		c.AgentLinkPrefix = prefix
		c.AgentLinkHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			if r.Method != http.MethodGet || r.URL.Path != "/update/nod_test/mistgate-node-linux-amd64" {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	})
	valid := do(t, http.MethodGet, e.public.URL, prefix+"update/nod_test/mistgate-node-linux-amd64", nil)
	if valid.status != http.StatusNoContent || hits.Load() != 1 {
		t.Fatalf("valid asset route: status=%d hits=%d", valid.status, hits.Load())
	}
	decoy := do(t, http.MethodGet, e.public.URL, "/nothing", nil)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, prefix + "update/nod_test/mistgate-node-linux-amd64"},
		{http.MethodGet, prefix + "update/nod_test"},
		{http.MethodGet, prefix + "update/nod_test/file/extra"},
		{http.MethodGet, prefix + "update//file"},
	} {
		before := hits.Load()
		got := do(t, tc.method, e.public.URL, tc.path, nil)
		sameResponse(t, tc.method+" "+tc.path, decoy, got)
		if hits.Load() != before {
			t.Errorf("malformed asset route %s reached the handler", tc.path)
		}
	}

	// The VPS LinkHandler answers this admitted route with 404; the public listener must turn it into its ordinary decoy.
	vps := newTestEnv(t, func(c *Config) {
		c.AgentLinkPrefix = prefix
		c.AgentLinkHandler = http.NotFoundHandler()
	})
	vpsDecoy := do(t, http.MethodGet, vps.public.URL, "/nothing", nil)
	update := do(t, http.MethodGet, vps.public.URL, prefix+"update/nod_test/mistgate-node-linux-amd64", nil)
	sameResponse(t, "VPS update asset route", vpsDecoy, update)
}

func TestAgentLinkPrefixMustBeSeparateAndSecret(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"short":         func(c *Config) { c.AgentLinkPrefix = "/short/" },
		"same as admin": func(c *Config) { c.AdminPrefix, c.AgentLinkPrefix = testPrefix, testPrefix },
		"overlaps public mount": func(c *Config) {
			c.AgentLinkPrefix = subPrefix
			c.PublicMounts = map[string]http.Handler{subPrefix: http.NotFoundHandler()}
		},
	} {
		mutate := mutate
		t.Run(name, func(t *testing.T) {
			if err := tryConfig(t, func(c *Config) {
				mutate(c)
				c.AgentLinkHandler = http.NotFoundHandler()
			}); err == nil {
				t.Fatal("invalid agent link prefix accepted")
			}
		})
	}
}

// limited returns an env whose clients are told apart by X-Forwarded-For (127.0.0.1 is a trusted proxy)
// and whose three limits are the given buckets.
func limited(t *testing.T, l Limits, mutate ...func(*Config)) *testEnv {
	t.Helper()
	return newTestEnvAuth(t, func(c *auth.Config) { c.TrustedProxies = loopback() },
		append([]func(*Config){func(c *Config) { c.Limits = l }}, mutate...)...)
}

func xff(ip string) map[string]string { return map[string]string{"X-Forwarded-For": ip} }

// A client over its limit gets a 429 that looks like the decoy's 404 (same header set, styled page),
// with Retry-After; other clients are not affected; IPv6 clients share their /64.
func TestPublicListenerRateLimit(t *testing.T) {
	slow := Rate{PerSecond: 0.1, Burst: 3} // no refill worth mentioning inside a test
	e := limited(t, Limits{Public: slow, Admin: Rate{PerSecond: -1}, Agent: Rate{PerSecond: -1}})
	ref := do(t, http.MethodGet, e.public.URL, "/nothing-here", xff("198.51.100.1")) // a 404 of another client
	for i := 0; i < 2; i++ {
		if r := do(t, http.MethodGet, e.public.URL, "/", xff("203.0.113.9")); r.status != 200 {
			t.Fatalf("request %d inside the burst: %d", i, r.status)
		}
	}
	if r := do(t, http.MethodGet, e.public.URL, "/", xff("203.0.113.9")); r.status != 200 {
		t.Fatalf("third request: %d", r.status)
	}
	r := do(t, http.MethodGet, e.public.URL, "/", xff("203.0.113.9"))
	if r.status != http.StatusTooManyRequests {
		t.Fatalf("over the limit: %d %q", r.status, r.body)
	}
	if secs, err := strconv.Atoi(r.header.Get("Retry-After")); err != nil || secs < 1 || secs > 20 {
		t.Errorf("Retry-After = %q", r.header.Get("Retry-After"))
	}
	if ct := r.header.Get("Content-Type"); ct != "text/html; charset=utf-8" || !strings.Contains(r.body, "429 Too Many Requests") || strings.Contains(strings.ToLower(r.body), "mistgate") {
		t.Errorf("429 page: %q %q", ct, r.body)
	}
	want := ref.header.Clone()
	want.Set("Retry-After", r.header.Get("Retry-After"))
	got := r.header.Clone()
	got.Del("Content-Length")
	want.Del("Content-Length")
	if len(got) != len(want) {
		t.Errorf("429 header set %v differs from a decoy reply's %v plus Retry-After", got, want)
	}
	// The other limits and other clients are not touched; the limit covers the mounts and unknown paths too.
	if r := do(t, http.MethodGet, e.public.URL, "/", xff("203.0.113.10")); r.status != 200 {
		t.Errorf("another client: %d", r.status)
	}
	if r := do(t, http.MethodGet, e.public.URL, "/some/path", xff("203.0.113.9")); r.status != 429 {
		t.Errorf("unknown path while limited: %d", r.status)
	}
	if r := do(t, http.MethodGet, e.public.URL, subPrefix+"tok", xff("203.0.113.9")); r.status != 429 {
		t.Errorf("subscription mount while limited: %d", r.status)
	}
	// IPv6: one /64 is one client, however many addresses the client has.
	for i := 0; i < 3; i++ {
		do(t, http.MethodGet, e.public.URL, "/", xff("2001:db8:1:2::"+strconv.Itoa(i+1)))
	}
	if r := do(t, http.MethodGet, e.public.URL, "/", xff("2001:db8:1:2:ffff::9")); r.status != 429 {
		t.Errorf("same /64, fresh address: %d", r.status)
	}
	if r := do(t, http.MethodGet, e.public.URL, "/", xff("2001:db8:1:3::1")); r.status != 200 {
		t.Errorf("neighbouring /64: %d", r.status)
	}
}

// The 429 page follows the decoy dir's own 429.html, like the 404 follows 404.html.
func TestRateLimitPageFromDecoyDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "429.html"), []byte("<p>slow down</p>"), 0o600)
	e := limited(t, Limits{Public: Rate{PerSecond: 0.1, Burst: 1}}, func(c *Config) { c.DecoyDir = dir })
	do(t, http.MethodGet, e.public.URL, "/", nil)
	if r := do(t, http.MethodGet, e.public.URL, "/", nil); r.status != 429 || r.body != "<p>slow down</p>" {
		t.Errorf("%d %q", r.status, r.body)
	}
}

// The admin surface has its own bucket, separate from the public one (the separate admin listener draws on
// the admin bucket); exhausting one does not close the other.
func TestAdminAndPublicLimitsAreSeparate(t *testing.T) {
	e := limited(t, Limits{Public: Rate{PerSecond: 0.1, Burst: 2}, Admin: Rate{PerSecond: 0.1, Burst: 4}, Agent: Rate{PerSecond: -1}})
	me := func(base, prefix string) response {
		return postMe(t, base, prefix, xff("203.0.113.50"))
	}
	for i := 0; i < 4; i++ {
		if r := me(e.public.URL, testPrefix); !isAdminAPI(r) {
			t.Fatalf("admin request %d: %d %q", i, r.status, r.body)
		}
	}
	if r := me(e.public.URL, testPrefix); r.status != 429 {
		t.Fatalf("admin over its limit: %d", r.status)
	}
	// Same client, same bucket through the secret host...
	if r := postMe(t, e.public.URL, "/", map[string]string{"Host": testAdminHst, "X-Forwarded-For": "203.0.113.50"}); r.status != 429 {
		t.Errorf("admin by host shares the admin bucket: %d", r.status)
	}
	// ...but the decoy is still open to that client, and the admin to another client.
	if r := do(t, http.MethodGet, e.public.URL, "/", xff("203.0.113.50")); r.status != 200 {
		t.Errorf("public bucket was drained by the admin: %d", r.status)
	}
	if r := me(e.public.URL, testPrefix); r.status != 429 {
		t.Errorf("still limited: %d", r.status)
	}
	if r := postMe(t, e.public.URL, testPrefix, xff("203.0.113.51")); !isAdminAPI(r) {
		t.Errorf("another client on the admin: %d", r.status)
	}
	// The separate admin listener draws on the same admin bucket (one admin, one budget per client).
	if r := postMe(t, e.admin.URL, "/", xff("203.0.113.50")); r.status != 429 {
		t.Errorf("admin listener, same client: %d", r.status)
	}
	if r := postMe(t, e.admin.URL, "/", xff("203.0.113.52")); !isAdminAPI(r) {
		t.Errorf("admin listener, another client: %d", r.status)
	}
}

// The agent endpoint is limited per client as well, with a bucket of its own.
func TestAgentEndpointRateLimit(t *testing.T) {
	ae := newAgentEnv(t, nil, func(c *Config) {
		c.Limits = Limits{Public: Rate{PerSecond: -1}, Admin: Rate{PerSecond: -1}, Agent: Rate{PerSecond: 0.1, Burst: 2}}
	})
	clientCert := selfSigned(t, "node")
	c := tlsClient(ae.ts.Listener.Addr().String(), &clientCert)
	for i := 0; i < 2; i++ {
		if code, _, _, err := get(t, c, "https://"+testAgentSNI+"/mistgate.agent.v1.AgentService/Connect", ""); err != nil || code != 200 {
			t.Fatalf("agent call %d: %d %v", i, code, err)
		}
	}
	code, body, _, err := get(t, c, "https://"+testAgentSNI+"/mistgate.agent.v1.AgentService/Connect", "")
	if err != nil || code != 429 || !strings.Contains(body, "429 Too Many Requests") {
		t.Fatalf("agent over its limit: %d %q %v", code, body, err)
	}
	if ae.agentHits.Load() != 2 {
		t.Errorf("the handler saw %d calls, want 2", ae.agentHits.Load())
	}
}

func TestRobotsTxt(t *testing.T) {
	aiBots := []string{"GPTBot", "ChatGPT-User", "OAI-SearchBot", "ClaudeBot", "Claude-Web", "anthropic-ai", "CCBot", "Google-Extended",
		"Bytespider", "PerplexityBot", "Amazonbot", "Applebot-Extended", "meta-externalagent", "cohere-ai", "Diffbot"}
	e := newTestEnv(t)
	r := do(t, http.MethodGet, e.public.URL, "/robots.txt", nil)
	if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "text/plain") {
		t.Fatalf("built-in robots.txt: %d %v", r.status, r.header)
	}
	for _, ua := range aiBots {
		if !strings.Contains(r.body, "User-agent: "+ua+"\n") {
			t.Errorf("%s is not listed", ua)
		}
	}
	// The AI group disallows everything; everyone else is allowed; nothing names the panel.
	groups := strings.Split(strings.TrimSpace(r.body), "\n\n")
	if len(groups) != 2 || !strings.HasSuffix(groups[0], "Disallow: /") || groups[1] != "User-agent: *\nAllow: /" {
		t.Errorf("groups: %q", groups)
	}
	for _, secret := range []string{testSecret, subSecret, testAdminHst, "admin", "api", "sub", "mistgate"} {
		if strings.Contains(strings.ToLower(r.body), strings.ToLower(secret)) {
			t.Errorf("robots.txt mentions %q", secret)
		}
	}
	if h := do(t, http.MethodHead, e.public.URL, "/robots.txt", nil); h.status != 200 || h.body != "" {
		t.Errorf("HEAD: %d %q", h.status, h.body)
	}
	if p := do(t, http.MethodPost, e.public.URL, "/robots.txt", nil); p.status != 404 {
		t.Errorf("POST: %d", p.status)
	}

	// A decoy dir with its own robots.txt wins; one without gets the built-in file.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<p>studio</p>"), 0o600)
	e = newTestEnv(t, func(c *Config) { c.DecoyDir = dir })
	if r := do(t, http.MethodGet, e.public.URL, "/robots.txt", nil); r.status != 200 || !strings.Contains(r.body, "GPTBot") {
		t.Errorf("decoy dir without robots.txt: %d %q", r.status, r.body)
	}
	os.WriteFile(filepath.Join(dir, "robots.txt"), []byte("User-agent: *\nDisallow: /private\n"), 0o600)
	if r := do(t, http.MethodGet, e.public.URL, "/robots.txt", nil); r.status != 200 || r.body != "User-agent: *\nDisallow: /private\n" {
		t.Errorf("own robots.txt: %d %q", r.status, r.body)
	}
	// Only at the root: under a secret prefix it is an unknown path like any other.
	if r := do(t, http.MethodGet, e.public.URL, subPrefix+"robots.txt", nil); r.status != 404 {
		t.Errorf("robots.txt under the mount: %d", r.status)
	}
}

// panelMount is what cmd/mistgate mounts under the subscription prefix: the brand logo, and the rest
// to the subscription handler (here: NewDecoy, what it answers for anything that is not a token).
func panelMount(e *testEnv) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /brand/logo.svg", NewLogoHandler(e.st, quietLog))
	mux.Handle("/", NewDecoy(""))
	return mux
}

// F6: nothing under the secret prefix that is not a real resource differs from what the public listener
// says for any unknown path: status, headers, body. That includes the bare prefix, the ServeMux's own 405,
// and the logo handler's own 404 (no custom logo) and error.
func TestSecretMountAnswersLikeTheDecoy(t *testing.T) {
	var e *testEnv
	e = newTestEnv(t, func(c *Config) {
		c.PublicMounts = map[string]http.Handler{subPrefix: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panelMount(e).ServeHTTP(w, r) })}
	})
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		want := do(t, method, e.public.URL, "/no/such/page", nil)
		if want.status != 404 {
			t.Fatalf("%s reference: %d", method, want.status)
		}
		for name, path := range map[string]string{
			"bare prefix":      subPrefix,
			"unknown path":     subPrefix + "x/y",
			"short token":      subPrefix + "abc",
			"token-like":       subPrefix + strings.Repeat("a", 43),
			"logo, no custom":  subPrefix + "brand/logo.svg",
			"logo, dir":        subPrefix + "brand/",
			"logo, other case": subPrefix + "brand/LOGO.svg",
		} {
			sameResponse(t, method+" "+name, want, do(t, method, e.public.URL, path, nil))
		}
	}
}

// The decoy answers an unknown path no faster than a lookup takes: every 404 has the same floor (here
// measured from outside, the floor is a lower bound).
func TestDecoyNotFoundHasAFloor(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.PublicMounts = map[string]http.Handler{subPrefix: NewDecoy("")} })
	for name, path := range map[string]string{"unknown path": "/zzz", "unknown token": subPrefix + strings.Repeat("a", 43), "bare prefix": subPrefix} {
		start := time.Now()
		if r := do(t, http.MethodGet, e.public.URL, path, nil); r.status != 404 {
			t.Fatalf("%s: %d", name, r.status)
		}
		if took := time.Since(start); took < notFoundFloor {
			t.Errorf("%s answered in %v, under the %v floor", name, took, notFoundFloor)
		}
	}
}
