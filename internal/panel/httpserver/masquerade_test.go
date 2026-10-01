package httpserver

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

const meRPC = "api/mistgate.admin.v1.AuthService/Me"

// isAdminAPI tells a response from the admin API (a Connect JSON error for an
// unauthenticated Me call) from anything else.
func isAdminAPI(r response) bool {
	return r.status == http.StatusUnauthorized && strings.Contains(r.body, `"unauthenticated"`)
}

func postMe(t *testing.T, base, pathPrefix string, hdr map[string]string) response {
	t.Helper()
	h := map[string]string{"Content-Type": "application/json"}
	for k, v := range hdr {
		h[k] = v
	}
	req, _ := http.NewRequest(http.MethodPost, base+pathPrefix+meRPC, strings.NewReader("{}"))
	for k, v := range h {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return response{resp.StatusCode, resp.Header, string(b)}
}

func sameResponse(t *testing.T, what string, a, b response) {
	t.Helper()
	if a.status != b.status || a.body != b.body {
		t.Errorf("%s: differs\n  a: %d %q\n  b: %d %q", what, a.status, a.body, b.status, b.body)
	}
	if len(a.header) != len(b.header) {
		t.Errorf("%s: header sets differ\n  a: %v\n  b: %v", what, a.header, b.header)
	}
	for k, v := range a.header {
		if strings.Join(v, ",") != strings.Join(b.header[k], ",") {
			t.Errorf("%s: header %s differs: %v vs %v", what, k, v, b.header[k])
		}
	}
}

func TestAdminIsUnreachableWithoutHostOrPrefix(t *testing.T) {
	e := newTestEnv(t)
	base := e.public.URL

	// Positive controls: the three ways in.
	if r := postMe(t, base, testPrefix, nil); !isAdminAPI(r) {
		t.Fatalf("prefix mode does not reach the admin: %+v", r)
	}
	if r := postMe(t, base, "/", map[string]string{"Host": testAdminHst}); !isAdminAPI(r) {
		t.Fatalf("host mode does not reach the admin: %+v", r)
	}
	if r := postMe(t, base, "/", map[string]string{"Host": strings.ToUpper(testAdminHst) + ":8443"}); !isAdminAPI(r) {
		t.Fatalf("host matching must ignore case and port: %+v", r)
	}
	if r := postMe(t, e.admin.URL, "/", nil); !isAdminAPI(r) {
		t.Fatalf("admin listener does not reach the admin: %+v", r)
	}

	// Everything else on the main listener is the decoy, never the API.
	random := do(t, http.MethodGet, base, "/qwertyuiopasdfghjklzxcvb/"+meRPC, nil)
	for name, p := range map[string]string{
		"no prefix":            "/" + meRPC,
		"api root":             "/api/",
		"wrong prefix":         "/" + strings.Repeat("a", 24) + "/" + meRPC,
		"prefix minus a char":  testPrefix[:len(testPrefix)-2] + "/" + meRPC,
		"prefix plus a char":   testPrefix[:len(testPrefix)-1] + "x/" + meRPC,
		"prefix without slash": "/" + testSecret,
		"upper-case prefix":    "/" + strings.ToUpper(testSecret) + "/" + meRPC,
		"mixed-case prefix":    "/" + strings.ToUpper(testSecret[:5]) + testSecret[5:] + "/" + meRPC,
		"prefix as a suffix":   "/x" + testPrefix + meRPC,
	} {
		r := postMe(t, base, p, nil)
		if isAdminAPI(r) {
			t.Errorf("%s: reached the admin API", name)
		}
		g := do(t, http.MethodGet, base, p, nil)
		sameResponse(t, name+" vs random path", g, random)
	}
	// Wrong host is the decoy too (the host is the secret in mode a).
	for _, h := range []string{"example.test", "k7q2x9.example.tes", "x.k7q2x9.example.test", "k7q2x9.example.test.evil"} {
		r := postMe(t, base, "/", map[string]string{"Host": h})
		if isAdminAPI(r) {
			t.Errorf("host %q reached the admin", h)
		}
	}
}

func TestPathNormalisationBypassAttemptsAreDecoy(t *testing.T) {
	e := newTestEnv(t)
	base := e.public.URL
	random := do(t, http.MethodGet, base, "/nothing-here", nil)
	if random.status != http.StatusNotFound {
		t.Fatalf("random path: %d", random.status)
	}
	secret := testSecret
	rpc := "/mistgate.admin.v1.AuthService/Me"
	attempts := map[string]string{
		"double slash before prefix": "//" + secret + "/api" + rpc,
		"double slash inside":        testPrefix + "/api" + rpc,
		"dot segment":                "/./" + secret + "/api" + rpc,
		"dot-dot into prefix":        "/x/../" + secret + "/api" + rpc,
		"dot-dot inside":             testPrefix + "api/../api" + rpc,
		"dot-dot out and back":       testPrefix + "../" + secret + "/api" + rpc,
		"trailing dot segment":       testPrefix + "api/.",
		"encoded dot":                "/%2e/" + secret + "/api" + rpc,
		"encoded dot-dot":            "/%2e%2e/" + secret + "/api" + rpc,
		"upper-case encoded":         "/%2E%2E/" + secret + "/api" + rpc,
		"encoded slash after":        "/" + secret + "%2fapi" + rpc,
		"encoded slash inside":       testPrefix + "api%2fmistgate.admin.v1.AuthService%2fMe",
		"encoded backslash":          "/" + secret + "%5capi" + rpc,
		"raw backslash":              "/" + secret + "\\api" + rpc,
		"encoded NUL":                testPrefix + "api%00" + rpc,
		"encoded newline":            testPrefix + "api%0a" + rpc,
		"semicolon param":            testPrefix + "api;x=1" + rpc,
		"encoded prefix char":        "/%66aketestsecret2345672345/api" + rpc,
	}
	for name, p := range attempts {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req, err := http.NewRequest(method, base+p, strings.NewReader("{}"))
			if err != nil {
				continue // the client refused to build it; nothing reaches the server
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := noRedirect.Do(req)
			if err != nil {
				continue
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			got := response{resp.StatusCode, resp.Header, string(b)}
			if isAdminAPI(got) || resp.StatusCode == http.StatusOK || resp.StatusCode >= 300 && resp.StatusCode < 400 {
				t.Errorf("%s (%s %s): status %d body %.80q", name, method, p, got.status, got.body)
				continue
			}
			if method == http.MethodGet {
				got.header.Del("Date")
				sameResponse(t, name, got, random)
			}
		}
	}
	// And the canonical form of the same thing works.
	if r := postMe(t, base, testPrefix, nil); !isAdminAPI(r) {
		t.Fatalf("canonical control failed: %+v", r)
	}
}

func TestCanonicalPath(t *testing.T) {
	ok := []string{"/", "/a", "/a/", "/a/b.css", "/%20x", "/caf%C3%A9", "/a-b_c~d"}
	bad := []string{"", "a", "//", "/a//b", "/a/./b", "/a/../b", "/a/..", "/a/.", "/./", "/%2e", "/%2E%2e/", "/a%2fb", "/a%2Fb", "/a%5cb", "/a\\b", "/a%00", "/a%1f", "/a;b", "/a%7f", "/%41", "/caf%c3%a9"}
	for _, p := range ok {
		r, _ := http.NewRequest("GET", "http://x"+p, nil)
		if !canonicalPath(r) {
			t.Errorf("%q rejected", p)
		}
	}
	for _, p := range bad {
		r, err := http.NewRequest("GET", "http://x"+p, nil)
		if err != nil {
			continue
		}
		if p == "" || p == "a" { // no path / relative path: URL has an empty path
			r.URL.Path = p
		}
		if canonicalPath(r) {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestNoFingerprintHeaders(t *testing.T) {
	e := newTestEnv(t)
	for name, tc := range map[string]struct {
		base, path string
		hdr        map[string]string
	}{
		"decoy 200":      {e.public.URL, "/", nil},
		"decoy 404":      {e.public.URL, "/nope", nil},
		"wrong prefix":   {e.public.URL, "/" + strings.Repeat("z", 24) + "/", nil},
		"bad path":       {e.public.URL, "/a/../b", nil},
		"admin spa":      {e.public.URL, testPrefix, nil},
		"admin asset":    {e.public.URL, testPrefix + "assets/app-abc123.js", nil},
		"admin api":      {e.public.URL, testPrefix + "api/x", nil},
		"admin listener": {e.admin.URL, "/", nil},
	} {
		r := do(t, http.MethodGet, tc.base, tc.path, tc.hdr)
		for _, h := range []string{"Server", "X-Powered-By", "Via", "X-Request-Id", "X-Generator"} {
			if v := r.header.Get(h); v != "" {
				t.Errorf("%s: header %s = %q", name, h, v)
			}
		}
		for h := range r.header {
			if strings.HasPrefix(strings.ToLower(h), "x-mistgate") || strings.Contains(strings.ToLower(h), "connect") && r.status < 300 {
				t.Errorf("%s: fingerprintable header %s", name, h)
			}
		}
	}
	// the decoy carries no security-header tells either: it looks like any small static site.
	r := do(t, http.MethodGet, e.public.URL, "/", nil)
	if r.header.Get("Content-Security-Policy") != "" || r.header.Get("X-Robots-Tag") != "" {
		t.Errorf("decoy sends admin headers: %v", r.header)
	}
	if !strings.Contains(r.body, "Coming soon") {
		t.Errorf("built-in decoy: %q", r.body)
	}
}

func TestDecoyDir(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", "<h1>Example Studio</h1>")
	write("404.html", "<h1>Lost at sea</h1>")
	write("about/index.html", "<h1>About us</h1>")
	write("css/site.css", "body{}")
	write(".env", "SECRET=1")
	write(".git/config", "[core]")
	e := newTestEnv(t, func(c *Config) { c.DecoyDir = dir })
	base := e.public.URL

	for p, want := range map[string]string{"/": "Example Studio", "/index.html": "Example Studio", "/about": "About us", "/about/": "About us", "/css/site.css": "body{}"} {
		if r := do(t, http.MethodGet, base, p, nil); r.status != 200 || !strings.Contains(r.body, want) {
			t.Errorf("%s: %d %q", p, r.status, r.body)
		}
	}
	if r := do(t, http.MethodGet, base, "/css/site.css", nil); !strings.HasPrefix(r.header.Get("Content-Type"), "text/css") || r.header.Get("Last-Modified") != "" || r.header.Get("Etag") != "" {
		t.Errorf("css headers: %v", r.header)
	}
	missing := do(t, http.MethodGet, base, "/missing", nil)
	if missing.status != 404 || !strings.Contains(missing.body, "Lost at sea") {
		t.Errorf("custom 404: %d %q", missing.status, missing.body)
	}
	for _, p := range []string{"/.env", "/.git/config", "/about/../.env", "/%2e%2e/etc/passwd", "/" + strings.Repeat("a", 24) + "/", "/" + strings.ToUpper(testSecret) + "/", testPrefix[:len(testPrefix)-1]} {
		sameResponse(t, p, do(t, http.MethodGet, base, p, nil), missing)
	}
	// Non-GET on the decoy is the same 404.
	if r := do(t, http.MethodPost, base, "/", nil); r.status != 404 || r.body != missing.body {
		t.Errorf("POST /: %d", r.status)
	}
	// The admin still works next to a custom decoy.
	if r := postMe(t, base, testPrefix, nil); !isAdminAPI(r) {
		t.Errorf("admin broken by decoy dir: %+v", r)
	}
}

func TestAdminSPAServing(t *testing.T) {
	e := newTestEnv(t)
	base := e.public.URL

	for _, p := range []string{testPrefix, testPrefix + "login", testPrefix + "setup", testPrefix + "nodes/abc", testPrefix + "index.html"} {
		r := do(t, http.MethodGet, base, p, nil)
		if r.status != 200 || !strings.Contains(r.body, `<base href="`+testPrefix+`">`) || strings.Contains(r.body, `<base href="/">`) {
			t.Errorf("%s: %d %q", p, r.status, r.body)
		}
		if r.header.Get("Cache-Control") != "no-store" || !strings.Contains(r.header.Get("Content-Security-Policy"), "frame-ancestors 'none'") ||
			r.header.Get("X-Content-Type-Options") != "nosniff" || r.header.Get("Referrer-Policy") == "" {
			t.Errorf("%s: security headers: %v", p, r.header)
		}
	}
	// host mode and the admin listener use base "/" (no rewrite needed).
	if r := do(t, http.MethodGet, base, "/login", map[string]string{"Host": testAdminHst}); r.status != 200 || !strings.Contains(r.body, `<base href="/">`) {
		t.Errorf("host mode spa: %d %q", r.status, r.body)
	}
	if r := do(t, http.MethodGet, e.admin.URL, "/login", nil); r.status != 200 || !strings.Contains(r.body, `<base href="/">`) {
		t.Errorf("listener spa: %d %q", r.status, r.body)
	}
	// Assets are served, cached forever; a missing asset is a 404, not the app shell.
	if r := do(t, http.MethodGet, base, testPrefix+"assets/app-abc123.js", nil); r.status != 200 || r.body != "console.log(1)" || !strings.Contains(r.header.Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d %v", r.status, r.header)
	}
	for _, p := range []string{testPrefix + "assets/missing.js", testPrefix + ".gitkeep", testPrefix + "API/mistgate.admin.v1.AuthService/Me", testPrefix + "Api/x", testPrefix + "api"} {
		if r := do(t, http.MethodGet, base, p, nil); r.status != 404 || strings.Contains(r.body, "<base") {
			t.Errorf("%s: %d %q", p, r.status, r.body)
		}
	}
	// The upper-case API path does not bypass authentication either.
	if r := postMe(t, base, testPrefix, nil); !isAdminAPI(r) {
		t.Fatalf("control: %+v", r)
	}
	req, _ := http.NewRequest(http.MethodPost, base+testPrefix+"API/mistgate.admin.v1.AuthService/Me", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if resp, err := noRedirect.Do(req); err != nil || resp.StatusCode != 404 {
		t.Errorf("POST /API/...: %v %v", resp, err)
	}
}

func TestSPANotBuilt(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.Dist = fstest.MapFS{} })
	r := do(t, http.MethodGet, e.public.URL, testPrefix, nil)
	if r.status != 200 || !strings.Contains(r.body, "not built") {
		t.Errorf("UI-not-built page: %d %q", r.status, r.body)
	}
}

func TestCrossOriginProtection(t *testing.T) {
	e := newTestEnv(t)
	for name, tc := range map[string]struct {
		hdr  map[string]string
		want bool // reaches the admin
	}{
		"no browser headers (scripts, curl)": {nil, true},
		"same origin":                        {map[string]string{"Origin": e.public.URL}, true},
		"trusted origin (dev SPA)":           {map[string]string{"Origin": testOrigin}, true},
		"foreign origin":                     {map[string]string{"Origin": "https://evil.example"}, false},
		"fetch metadata cross-site":          {map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
		"fetch metadata same-site":           {map[string]string{"Sec-Fetch-Site": "same-site"}, false},
		"fetch metadata same-origin":         {map[string]string{"Sec-Fetch-Site": "same-origin"}, true},
	} {
		r := postMe(t, e.public.URL, testPrefix, tc.hdr)
		if got := isAdminAPI(r); got != tc.want {
			t.Errorf("%s: reached admin = %v, want %v (status %d)", name, got, tc.want, r.status)
		}
	}
}
