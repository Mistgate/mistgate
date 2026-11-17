package httpserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	nodedecoy "github.com/mistgate/mistgate/internal/decoy"
)

func TestDecoyDoesNotFollowSymlinksOutsideRoot(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "decoy")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.html")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "outside.html")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("cannot create symlink: %v", err)
		}
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	newDecoy(dir, nil, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/outside.html", nil))
	if w.Code != http.StatusNotFound || w.Body.String() != notFoundHTML {
		t.Fatalf("symlink response: %d %q", w.Code, w.Body.String())
	}
}

// The panel's built-in page and 404 are the node's: the library previews one text.
func TestDecoyBuiltinPagesAreTheNodes(t *testing.T) {
	w := httptest.NewRecorder()
	newDecoy("", nil, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	node := httptest.NewRecorder()
	nodedecoy.Handler().ServeHTTP(node, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Body.String() != node.Body.String() || w.Body.String() != nodedecoy.BuiltinHTML() {
		t.Fatalf("panel page %q, node page %q", w.Body.String(), node.Body.String())
	}
	w, node = httptest.NewRecorder(), httptest.NewRecorder()
	newDecoy("", nil, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/nope", nil))
	nodedecoy.Handler().ServeHTTP(node, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if w.Body.String() != node.Body.String() {
		t.Fatal("the 404 differs from the node's")
	}
}
