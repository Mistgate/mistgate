package decoy

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func zipArchive(t *testing.T, files []File, mode os.FileMode) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.Path, Method: zip.Deflate}
		h.SetMode(mode)
		h.Modified = time.Now().UTC()
		z, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := z.Write(f.Content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func tarArchive(t *testing.T, name string, kind byte, mode int64, body []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: kind, Mode: mode, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if len(body) > 0 {
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestBundleValidationTable(t *testing.T) {
	tooLarge := zipArchive(t, []File{{Path: "index.html", Content: bytes.Repeat([]byte("x"), MaxBundleBytes+1)}}, 0o644)
	valid := zipArchive(t, []File{{Path: "index.html", Content: []byte("home")}}, 0o644)
	symlink := zipArchive(t, []File{{Path: "index.html", Content: []byte("index.html")}}, os.ModeSymlink|0o777)
	executable := zipArchive(t, []File{{Path: "index.html", Content: []byte("home")}}, 0o755)

	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{name: "zip", data: valid, want: true},
		{name: "raw archive size", data: bytes.Repeat([]byte("x"), MaxBundleBytes+1)},
		{name: "expanded content size", data: tooLarge},
		{name: "parent path", data: zipArchive(t, []File{{Path: "../index.html", Content: []byte("x")}}, 0o644)},
		{name: "absolute path", data: zipArchive(t, []File{{Path: "/index.html", Content: []byte("x")}}, 0o644)},
		{name: "hidden path", data: zipArchive(t, []File{{Path: ".hidden", Content: []byte("x")}}, 0o644)},
		{name: "NUL path", data: zipArchive(t, []File{{Path: "index\x00.html", Content: []byte("x")}}, 0o644)},
		{name: "backslash path", data: zipArchive(t, []File{{Path: `dir\\index.html`, Content: []byte("x")}}, 0o644)},
		{name: "symlink", data: symlink},
		{name: "executable bit is not a refusal", data: executable, want: true},
		{name: "non-static tar entry", data: tarArchive(t, "pipe", tar.TypeFifo, 0, nil)},
		{name: "tar symlink", data: tarArchive(t, "link", tar.TypeSymlink, 0, nil)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseArchive(tc.data)
			if tc.want && err != nil {
				t.Fatalf("ParseArchive() error = %v", err)
			}
			if !tc.want && err == nil {
				t.Fatal("ParseArchive() accepted invalid bundle")
			}
		})
	}
}

func TestFromDirOmitsPathsHiddenByPanelRules(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"index.html":  "home",
		".env":        "ignored",
		".git/config": "ignored",
	} {
		name := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := FromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := bundle.Files()
	if len(files) != 1 || files[0].Path != "index.html" || string(files[0].Content) != "home" {
		t.Fatalf("FromDir() files = %+v, want only visible index.html", files)
	}
}

func TestBundleHashIgnoresArchiveOrderAndMetadata(t *testing.T) {
	first := zipArchive(t, []File{{Path: "index.html", Content: []byte("home")}, {Path: "style.css", Content: []byte("body{}")}}, 0o644)
	second := zipArchive(t, []File{{Path: "style.css", Content: []byte("body{}")}, {Path: "index.html", Content: []byte("home")}}, 0o600)
	a, err := ParseArchive(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseArchive(second)
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash() == "" || a.Hash() != b.Hash() {
		t.Fatalf("hashes differ for the same files: %q != %q", a.Hash(), b.Hash())
	}
	stored, err := ParseStoredArchive(a.Archive())
	if err != nil || stored.Hash() != a.Hash() {
		t.Fatalf("ParseStoredArchive() = %v, %v", stored, err)
	}
}

func TestBundleHandlerUsesPanelSiteRules(t *testing.T) {
	b, err := FromFiles([]File{
		{Path: "index.html", Content: []byte("home")},
		{Path: "section/index.html", Content: []byte("section")},
		{Path: "404.html", Content: []byte("missing")},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := BundleHandler(b)
	for _, tc := range []struct {
		method string
		path   string
		status int
		body   string
	}{
		{http.MethodGet, "/", http.StatusOK, "home"},
		{http.MethodHead, "/section/", http.StatusOK, ""},
		{http.MethodGet, "/absent", http.StatusNotFound, "missing"},
		{http.MethodGet, "/../secret", http.StatusNotFound, "missing"},
		{http.MethodPost, "/", http.StatusNotFound, "missing"},
		{http.MethodGet, "/robots.txt", http.StatusOK, robotsTXT},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || w.Body.String() != tc.body {
			t.Errorf("%s %s = %d %q, want %d %q", tc.method, tc.path, w.Code, w.Body.String(), tc.status, tc.body)
		}
	}
}

func TestSitePersistsActiveBundleAcrossRestart(t *testing.T) {
	files := []File{{Path: "index.html", Content: []byte("own site")}}
	b, err := FromFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	site, err := OpenSite(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := site.Apply(ModeOwn, b.Hash(), files); err != nil || !changed {
		t.Fatalf("Apply() = %v, %v", changed, err)
	}

	restarted, err := OpenSite(filepath.Dir(site.dir))
	if err != nil {
		t.Fatal(err)
	}
	mode, hash := restarted.State()
	if mode != ModeOwn || hash != b.Hash() {
		t.Fatalf("restarted state = %q %q, want own %q", mode, hash, b.Hash())
	}
	w := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK || w.Body.String() != "own site" {
		t.Fatalf("restarted handler = %d %q", w.Code, w.Body.String())
	}
	if changed, err := restarted.Apply(ModeBuiltin, "", nil); err != nil || !changed {
		t.Fatalf("Apply(builtin) = %v, %v", changed, err)
	}
	if changed, err := restarted.Apply(ModeBuiltin, "", nil); err != nil || changed {
		t.Fatalf("unchanged Apply(builtin) = %v, %v", changed, err)
	}
}
