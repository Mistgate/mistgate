//go:build !js

package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	decoypkg "github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func libraryBundle(t *testing.T, home string, extra ...decoypkg.File) *decoypkg.Bundle {
	t.Helper()
	b, err := decoypkg.FromFiles(append([]decoypkg.File{{Path: "index.html", Content: []byte(home)}}, extra...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func serveDecoy(d *decoy, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	d.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

// The panel's own public site follows the library: a site of the library with the rules of a directory (index, 404.html,
// robots.txt, nested pages), and a change of the setting or a new version is served within the cache time, with no restart.
func TestPanelServesALibrarySiteAndFollowsChanges(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	dir := t.TempDir()
	writeFile(t, dir, "index.html", "directory home")
	d := newDecoy(dir, nil, st)
	lib := d.lib.(*libraryDecoy)
	lib.ttl = 0 // every request reads the setting: the test does not wait for the cache

	if w := serveDecoy(d, "/"); w.Body.String() != "directory home" {
		t.Fatalf("with nothing chosen the panel serves its directory: %q", w.Body.String())
	}
	v1 := libraryBundle(t, "bakery v1", decoypkg.File{Path: "404.html", Content: []byte("custom gone")}, decoypkg.File{Path: "menu/index.html", Content: []byte("the menu")}, decoypkg.File{Path: "style.css", Content: []byte("p{}")})
	site, err := st.CreateDecoySite(ctx, time.Now(), store.NewDecoySite{Name: "bakery", State: store.DecoyStateReady, Bundle: v1})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPanelDecoySource(ctx, "site:"+site.ID); err != nil {
		t.Fatal(err)
	}
	if w := serveDecoy(d, "/"); w.Code != 200 || w.Body.String() != "bakery v1" {
		t.Fatalf("library site home = %d %q", w.Code, w.Body.String())
	}
	if w := serveDecoy(d, "/menu/"); w.Body.String() != "the menu" {
		t.Fatalf("nested index = %q", w.Body.String())
	}
	if w := serveDecoy(d, "/style.css"); w.Header().Get("Content-Type") != "text/css; charset=utf-8" || w.Header().Get("Last-Modified") != "" {
		t.Fatalf("style = %v", w.Header())
	}
	if w := serveDecoy(d, "/missing"); w.Code != 404 || w.Body.String() != "custom gone" {
		t.Fatalf("unknown path = %d %q, want the site's 404.html", w.Code, w.Body.String())
	}
	if w := serveDecoy(d, "/.hidden"); w.Code != 404 {
		t.Fatalf("dot path = %d", w.Code)
	}
	if w := serveDecoy(d, "/robots.txt"); w.Code != 200 || w.Body.Len() == 0 {
		t.Fatalf("robots.txt = %d %q", w.Code, w.Body.String())
	}

	// a new version of the site, no restart
	if _, _, _, err := st.AddDecoySiteVersion(ctx, time.Now(), site.ID, libraryBundle(t, "bakery v2"), "admin"); err != nil {
		t.Fatal(err)
	}
	if w := serveDecoy(d, "/"); w.Body.String() != "bakery v2" {
		t.Fatalf("after a new version: %q", w.Body.String())
	}
	if w := serveDecoy(d, "/missing"); w.Body.String() != notFoundHTML {
		t.Fatalf("the new version has no 404.html, so the built-in one answers: %q", w.Body.String())
	}
	// the setting changes: the built-in page, then the directory again
	if err := st.SetPanelDecoySource(ctx, "builtin"); err != nil {
		t.Fatal(err)
	}
	if w := serveDecoy(d, "/"); w.Body.String() != comingSoonHTML {
		t.Fatalf("built-in: %q", w.Body.String())
	}
	if err := st.SetPanelDecoySource(ctx, "dir"); err != nil {
		t.Fatal(err)
	}
	if w := serveDecoy(d, "/"); w.Body.String() != "directory home" {
		t.Fatalf("dir: %q", w.Body.String())
	}
	// a site that was chosen and then deleted from under the setting never leaves the panel without a site
	if err := st.SetPanelDecoySource(ctx, "site:"+site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `DELETE FROM decoy_site WHERE id = ?`, site.ID); err != nil {
		t.Fatal(err)
	}
	if w := serveDecoy(d, "/"); w.Body.String() != "directory home" {
		t.Fatalf("site gone: %q", w.Body.String())
	}
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A request does not read the database each time: the choice is kept for the cache time.
func TestLibraryChoiceIsCached(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	d := newDecoy("", nil, st)
	lib := d.lib.(*libraryDecoy)
	if w := serveDecoy(d, "/"); w.Body.String() != comingSoonHTML {
		t.Fatalf("nothing chosen, no directory: %q", w.Body.String())
	}
	if err := st.SetPanelDecoySource(ctx, "builtin"); err != nil {
		t.Fatal(err)
	}
	if kind, _ := lib.resolve(); kind != "" {
		t.Fatalf("the choice was read again inside the cache time: %q", kind)
	}
	lib.at = time.Time{}
	if kind, _ := lib.resolve(); kind != "builtin" {
		t.Fatalf("after the cache time the choice is %q, want builtin", kind)
	}
}

// A secret mount answers with the 404 of the site the panel shows, so it cannot be told from a stranger's path.
func TestMountsAnswerWithTheLibrarySites404(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	site, err := st.CreateDecoySite(ctx, time.Now(), store.NewDecoySite{Name: "bakery", State: store.DecoyStateReady,
		Bundle: libraryBundle(t, "home", decoypkg.File{Path: "404.html", Content: []byte("custom gone")}, decoypkg.File{Path: "429.html", Content: []byte("custom slow")})})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPanelDecoySource(ctx, "site:"+site.ID); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	NewDecoyFor("", st).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/anything", nil))
	if w.Code != 404 || w.Body.String() != "custom gone" {
		t.Fatalf("mount 404 = %d %q", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	NewTooManyFor("", st)(w, httptest.NewRequest(http.MethodGet, "/", nil), time.Second)
	if w.Code != 429 || w.Body.String() != "custom slow" {
		t.Fatalf("mount 429 = %d %q", w.Code, w.Body.String())
	}
}
