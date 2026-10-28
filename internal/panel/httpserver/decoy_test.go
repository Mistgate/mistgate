package httpserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
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
	newDecoy(dir, nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/outside.html", nil))
	if w.Code != http.StatusNotFound || w.Body.String() != notFoundHTML {
		t.Fatalf("symlink response: %d %q", w.Code, w.Body.String())
	}
}
