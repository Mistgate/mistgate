package decoy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func do(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHandler(t *testing.T) {
	h := Handler()
	root := do(h, "GET", "/")
	if root.Code != 200 || !strings.Contains(root.Body.String(), "Coming soon") {
		t.Fatalf("root: %d %q", root.Code, root.Body.String())
	}
	if do(h, "HEAD", "/").Body.Len() != 0 {
		t.Fatal("HEAD must not carry a body")
	}
	// Every miss is byte-identical, whatever the reason.
	want := do(h, "GET", "/nope")
	if want.Code != 404 {
		t.Fatalf("404 expected, got %d", want.Code)
	}
	for _, c := range []struct{ m, p string }{
		{"GET", "/index.html"}, {"GET", "/auth"}, {"POST", "/"}, {"POST", "/auth"}, {"DELETE", "/x"}, {"GET", "/a/../b"},
	} {
		got := do(h, c.m, c.p)
		if got.Code != 404 || got.Body.String() != want.Body.String() || got.Header().Get("Content-Type") != want.Header().Get("Content-Type") {
			t.Errorf("%s %s differs from the canonical 404", c.m, c.p)
		}
	}
	if got := do(NotFound(), "GET", "/"); got.Code != 404 || got.Body.String() != want.Body.String() {
		t.Error("NotFound must serve the same 404 page")
	}
	for _, rec := range []*httptest.ResponseRecorder{root, want} {
		if v := rec.Header().Get("Server"); v != "" {
			t.Errorf("Server header must be absent, got %q", v)
		}
	}
}
