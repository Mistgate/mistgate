package decoy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tarFiles(t *testing.T, entries ...tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, h := range entries {
		h := h
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if err := w.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := w.Write(bytes.Repeat([]byte("x"), int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func gz(t *testing.T, data []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write(data)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The admin words each refusal, so every bad bundle must fail with the reason a person can act on.
func TestBundleRefusalReasons(t *testing.T) {
	idx := File{Path: "index.html", Content: []byte("home")}
	manyDirs := make([]tar.Header, 0, maxArchiveEntries+2)
	for i := 0; i < maxArchiveEntries+2; i++ {
		manyDirs = append(manyDirs, tar.Header{Name: fmt.Sprintf("d%d/", i), Typeflag: tar.TypeDir, Mode: 0o755})
	}
	tests := []struct {
		name   string
		data   []byte
		reason string
	}{
		{"empty upload", nil, ReasonEmpty},
		{"archive over the cap", bytes.Repeat([]byte("x"), MaxBundleBytes+1), ReasonTooLarge},
		{"files over the cap", zipArchive(t, []File{idx, {Path: "big.bin", Content: bytes.Repeat([]byte("x"), MaxBundleBytes)}}, 0o644), ReasonTooLarge},
		{"not an archive", []byte("just some text, not an archive at all"), ReasonBadArchive},
		{"parent path", zipArchive(t, []File{idx, {Path: "a/../../x.html", Content: []byte("x")}}, 0o644), ReasonBadPath},
		{"absolute path", zipArchive(t, []File{idx, {Path: "/etc/x", Content: []byte("x")}}, 0o644), ReasonBadPath},
		{"tar parent path", tarFiles(t, tar.Header{Name: "../x.html", Size: 1, Mode: 0o644}), ReasonBadPath},
		{"tar symlink", tarFiles(t, tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}), ReasonNotStatic},
		{"tar hard link", tarFiles(t, tar.Header{Name: "link", Typeflag: tar.TypeLink, Linkname: "index.html"}), ReasonNotStatic},
		{"tar device", tarFiles(t, tar.Header{Name: "tty", Typeflag: tar.TypeChar}), ReasonNotStatic},
		{"no index", zipArchive(t, []File{{Path: "about.html", Content: []byte("x")}}, 0o644), ReasonNoIndex},
		{"two wrapping folders", zipArchive(t, []File{{Path: "a/index.html", Content: []byte("x")}, {Path: "b/x.html", Content: []byte("x")}}, 0o644), ReasonNoIndex},
		{"only hidden files", zipArchive(t, []File{{Path: ".env", Content: []byte("x")}}, 0o644), ReasonEmpty},
		{"duplicate path", tarFiles(t, tar.Header{Name: "index.html", Size: 1, Mode: 0o644}, tar.Header{Name: "index.html", Size: 1, Mode: 0o644}), ReasonDuplicate},
		{"file where a folder is needed", tarFiles(t, tar.Header{Name: "index.html", Size: 1, Mode: 0o644}, tar.Header{Name: "index.html/x.css", Size: 1, Mode: 0o644}), ReasonDuplicate},
		{"too many entries", gz(t, tarFiles(t, manyDirs...)), ReasonTooManyFiles},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseArchive(tc.data)
			if !errors.Is(err, ErrInvalidBundle) || ReasonOf(err) != tc.reason {
				t.Fatalf("ParseArchive() error = %v (reason %q), want reason %q", err, ReasonOf(err), tc.reason)
			}
			if got := ClientMessage(err); !strings.HasPrefix(got, "decoy_bundle_"+tc.reason) {
				t.Fatalf("ClientMessage() = %q", got)
			}
		})
	}
}

// A gzip stream of empty directory entries is tiny on the wire and huge when expanded; it must stop early.
func TestGzipTarBombStopsEarly(t *testing.T) {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	tw := tar.NewWriter(w)
	for i := 0; i < 3*maxArchiveEntries; i++ {
		if err := tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("d%d/", i), Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = w.Close()
	if b.Len() > MaxBundleBytes {
		t.Skipf("bomb too big to be an upload: %d", b.Len())
	}
	if _, err := ParseArchive(b.Bytes()); !errors.Is(err, ErrInvalidBundle) {
		t.Fatalf("ParseArchive() error = %v", err)
	}
}

// What a person's own tools produce must upload without fuss: a zipped folder, a macOS zip with its resource forks,
// a `tar czf`, and `git archive` with its global header.
func TestArchivesFromRealToolsAreAccepted(t *testing.T) {
	site := []File{{Path: "index.html", Content: []byte("home")}, {Path: "css/site.css", Content: []byte("body{}")}}
	plain, err := FromFiles(site)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := zipArchive(t, []File{{Path: "mysite/index.html", Content: []byte("home")}, {Path: "mysite/css/site.css", Content: []byte("body{}")}}, 0o644)
	mac := zipArchive(t, append([]File{
		{Path: "__MACOSX/._index.html", Content: []byte("fork")}, {Path: ".DS_Store", Content: []byte("junk")}, {Path: "css/.hidden", Content: []byte("x")},
	}, site...), 0o644)
	tgz := gz(t, tarFiles(t,
		tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755},
		tar.Header{Name: "./index.html", Size: 0, Mode: 0o644},
	))
	gitTar := tarFiles(t,
		tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "0123456789abcdef"}},
		tar.Header{Name: "index.html", Size: 0, Mode: 0o644},
	)
	for name, data := range map[string][]byte{"wrapped folder": wrapped, "macOS zip": mac} {
		b, err := ParseArchive(data)
		if err != nil || b.Hash() != plain.Hash() {
			t.Errorf("%s: err=%v hash=%v, want the plain site's hash", name, err, b.Hash())
		}
	}
	for name, data := range map[string][]byte{"tar.gz with ./ prefix": tgz, "git archive tar": gitTar} {
		b, err := ParseArchive(data)
		if err != nil || len(b.Files()) != 1 || b.Files()[0].Path != "index.html" {
			t.Errorf("%s: err=%v files=%v", name, err, b)
		}
	}
}

// The panel's own --decoy-dir is held to the same cap as an uploaded bundle, and says why when it is not.
func TestFromDirReasons(t *testing.T) {
	write := func(dir, name string, size int) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, bytes.Repeat([]byte("x"), size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	big := t.TempDir()
	write(big, "index.html", 10)
	write(big, "video.mp4", MaxBundleBytes)
	if _, err := FromDir(big); ReasonOf(err) != ReasonTooLarge {
		t.Fatalf("FromDir(too large) = %v", err)
	}
	none := t.TempDir()
	write(none, "about.html", 10)
	if _, err := FromDir(none); ReasonOf(err) != ReasonNoIndex {
		t.Fatalf("FromDir(no index) = %v", err)
	}
	ok := t.TempDir()
	write(ok, "index.html", 10)
	if err := os.Symlink("index.html", filepath.Join(ok, "link.html")); err == nil {
		if _, err := FromDir(ok); ReasonOf(err) != ReasonNotStatic {
			t.Fatalf("FromDir(symlink) = %v", err)
		}
	}
}
