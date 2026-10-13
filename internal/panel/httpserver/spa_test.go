package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/mistgate/mistgate/internal/buildinfo"
)

func withBuildVersion(t *testing.T, v string) {
	t.Helper()
	old := buildinfo.Version
	buildinfo.Version = v
	t.Cleanup(func() { buildinfo.Version = old })
}

// The app shell tells the tab which panel version served it (the admin compares that with X-Mistgate-Version) and is
// revalidated, never kept: a tab opened after an update must get the new bundle.
func TestSPAShellVersionAndCache(t *testing.T) {
	withBuildVersion(t, `1.2.3"><x`)
	s := &Server{dist: fstest.MapFS{"index.html": {Data: []byte(`<!doctype html><html><head><base href="/"></head><body>app</body></html>`)}}}
	rec := httptest.NewRecorder()
	s.spa("/k/").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	body := rec.Body.String()
	if want := `<head><meta name="mistgate-version" content="1.2.3&#34;&gt;&lt;x"><base href="/k/">`; !strings.Contains(body, want) {
		t.Errorf("shell: %q", body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}

	// A stack that already said no-store (securityHeaders) keeps it.
	rec = httptest.NewRecorder()
	rec.Header().Set("Cache-Control", "no-store")
	s.spa("/").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store kept", got)
	}
}

// Only a signed-in admin gets the version header; the public sign-in procedures and the 401 stay silent.
func TestAPIVersionHeaderOnlyWhenSignedIn(t *testing.T) {
	withBuildVersion(t, "1.2.3")
	e := newTestEnv(t)
	cookie := helperSession(t, e.st)
	r := postMe(t, e.public.URL, testPrefix, map[string]string{"Cookie": cookie})
	if r.status != 200 || r.header.Get(versionHeader) != "1.2.3" {
		t.Errorf("signed in: %d %v", r.status, r.header)
	}
	if r := postMe(t, e.public.URL, testPrefix, nil); r.header.Get(versionHeader) != "" {
		t.Errorf("signed out: %v", r.header)
	}
}
