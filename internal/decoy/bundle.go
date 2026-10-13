package decoy

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// MaxBundleBytes limits the sum of file contents and the uploaded archive.
	MaxBundleBytes        = 2 << 20
	maxBundleFiles        = 4096
	maxStoredArchiveBytes = MaxBundleBytes + 512<<10
)

var ErrInvalidBundle = errors.New("invalid decoy bundle")

// File is one regular file in a decoy bundle. Paths are slash-separated and relative to the site root.
type File struct {
	Path    string
	Content []byte
}

// Bundle is a validated, immutable static site. Hash is independent of archive order and metadata.
type Bundle struct {
	files   []File
	hash    string
	archive []byte
}

// ParseArchive accepts a ZIP, TAR or gzip-compressed TAR upload. The upload and expanded contents are each capped at 2 MB.
func ParseArchive(data []byte) (*Bundle, error) {
	if len(data) == 0 || len(data) > MaxBundleBytes {
		return nil, ErrInvalidBundle
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{'P', 'K', 3, 4}) {
		return parseZip(data, MaxBundleBytes)
	}
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, ErrInvalidBundle
		}
		defer gz.Close()
		return parseTar(gz, MaxBundleBytes)
	}
	return parseTar(bytes.NewReader(data), MaxBundleBytes)
}

// ParseStoredArchive reads the canonical ZIP kept by the panel and node. Its ZIP metadata may exceed the file-content cap.
func ParseStoredArchive(data []byte) (*Bundle, error) {
	if len(data) == 0 || len(data) > maxStoredArchiveBytes {
		return nil, ErrInvalidBundle
	}
	return parseZip(data, MaxBundleBytes)
}

// FromFiles validates files received over the agent protocol and makes a canonical bundle.
func FromFiles(files []File) (*Bundle, error) {
	if len(files) == 0 || len(files) > maxBundleFiles {
		return nil, ErrInvalidBundle
	}
	out := make([]File, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	var total int64
	for _, f := range files {
		if !validFilePath(f.Path) {
			return nil, ErrInvalidBundle
		}
		if _, ok := seen[f.Path]; ok {
			return nil, ErrInvalidBundle
		}
		seen[f.Path] = struct{}{}
		total += int64(len(f.Content))
		if total > MaxBundleBytes {
			return nil, ErrInvalidBundle
		}
		out = append(out, File{Path: f.Path, Content: bytes.Clone(f.Content)})
	}
	if hasFileDirectoryConflict(seen) {
		return nil, ErrInvalidBundle
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	b := &Bundle{files: out, hash: hashFiles(out)}
	archive, err := zipFiles(out)
	if err != nil || len(archive) > maxStoredArchiveBytes {
		return nil, ErrInvalidBundle
	}
	b.archive = archive
	return b, nil
}

// FromDir snapshots a local --decoy-dir bundle. Paths the panel hides by its dot-path rule are omitted; symlinks and
// non-regular files are refused.
func FromDir(dir string) (*Bundle, error) {
	if dir == "" {
		return nil, ErrInvalidBundle
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, ErrInvalidBundle
	}
	st, err := os.Lstat(root)
	if err != nil || st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return nil, ErrInvalidBundle
	}
	files := make([]File, 0)
	var total int64
	err = filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return ErrInvalidBundle
		}
		if name == root {
			return nil
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return ErrInvalidBundle
		}
		rel = filepath.ToSlash(rel)
		if hasHiddenPathSegment(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(name)
		if err != nil {
			return ErrInvalidBundle
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidBundle
		}
		if info.IsDir() {
			if !validDirPath(rel) {
				return ErrInvalidBundle
			}
			return nil
		}
		if !info.Mode().IsRegular() || !validFilePath(rel) || info.Mode().Perm()&0o111 != 0 {
			return ErrInvalidBundle
		}
		if len(files) == maxBundleFiles || info.Size() < 0 || info.Size() > MaxBundleBytes-total {
			return ErrInvalidBundle
		}
		f, err := os.Open(name)
		if err != nil {
			return ErrInvalidBundle
		}
		openedInfo, statErr := f.Stat()
		if statErr != nil || !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
			_ = f.Close()
			return ErrInvalidBundle
		}
		content, readErr := io.ReadAll(io.LimitReader(f, MaxBundleBytes+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil {
			return ErrInvalidBundle
		}
		total += int64(len(content))
		if total > MaxBundleBytes {
			return ErrInvalidBundle
		}
		files = append(files, File{Path: rel, Content: content})
		return nil
	})
	if err != nil {
		return nil, ErrInvalidBundle
	}
	return FromFiles(files)
}

func (b *Bundle) Hash() string {
	if b == nil {
		return ""
	}
	return b.hash
}

func (b *Bundle) Files() []File {
	if b == nil {
		return nil
	}
	out := make([]File, len(b.files))
	for i, f := range b.files {
		out[i] = File{Path: f.Path, Content: bytes.Clone(f.Content)}
	}
	return out
}

// Archive returns a copy of the stable ZIP representation used for content-addressed storage.
func (b *Bundle) Archive() []byte {
	if b == nil {
		return nil
	}
	return bytes.Clone(b.archive)
}

func parseZip(data []byte, maxTotal int64) (*Bundle, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(r.File) == 0 || len(r.File) > maxBundleFiles {
		return nil, ErrInvalidBundle
	}
	files := make([]File, 0, len(r.File))
	seen := make(map[string]struct{}, len(r.File))
	var total int64
	for _, zf := range r.File {
		name := archivePath(zf.Name)
		if zf.FileInfo().IsDir() {
			if !validDirPath(name) {
				return nil, ErrInvalidBundle
			}
			continue
		}
		if !zf.Mode().IsRegular() || zf.Mode().Perm()&0o111 != 0 || !validFilePath(name) {
			return nil, ErrInvalidBundle
		}
		if _, ok := seen[name]; ok {
			return nil, ErrInvalidBundle
		}
		seen[name] = struct{}{}
		if zf.UncompressedSize64 > uint64(maxTotal-total) {
			return nil, ErrInvalidBundle
		}
		f, err := zf.Open()
		if err != nil {
			return nil, ErrInvalidBundle
		}
		content, readErr := io.ReadAll(io.LimitReader(f, maxTotal-total+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || uint64(len(content)) != zf.UncompressedSize64 {
			return nil, ErrInvalidBundle
		}
		total += int64(len(content))
		files = append(files, File{Path: name, Content: content})
	}
	if len(files) == 0 || hasFileDirectoryConflict(seen) {
		return nil, ErrInvalidBundle
	}
	return FromFiles(files)
}

func parseTar(r io.Reader, maxTotal int64) (*Bundle, error) {
	tr := tar.NewReader(r)
	files := make([]File, 0)
	seen := make(map[string]struct{})
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, ErrInvalidBundle
		}
		name := archivePath(h.Name)
		switch h.Typeflag {
		case tar.TypeDir:
			if !validDirPath(name) {
				return nil, ErrInvalidBundle
			}
		case tar.TypeReg, tar.TypeRegA:
			if !validFilePath(name) || h.Mode&0o111 != 0 || h.Size < 0 || h.Size > maxTotal-total {
				return nil, ErrInvalidBundle
			}
			if _, ok := seen[name]; ok {
				return nil, ErrInvalidBundle
			}
			seen[name] = struct{}{}
			content, err := io.ReadAll(io.LimitReader(tr, h.Size+1))
			if err != nil || int64(len(content)) != h.Size {
				return nil, ErrInvalidBundle
			}
			total += int64(len(content))
			files = append(files, File{Path: name, Content: content})
		default:
			return nil, ErrInvalidBundle
		}
		if len(files) > maxBundleFiles {
			return nil, ErrInvalidBundle
		}
	}
	if len(files) == 0 || hasFileDirectoryConflict(seen) {
		return nil, ErrInvalidBundle
	}
	return FromFiles(files)
}

func validDirPath(name string) bool {
	if name == "" || name == "." {
		return true
	}
	return validSitePath(name)
}

func hasHiddenPathSegment(name string) bool {
	for _, segment := range strings.Split(name, "/") {
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

func archivePath(name string) string {
	name = strings.TrimSuffix(name, "/")
	for strings.HasPrefix(name, "./") {
		name = strings.TrimPrefix(name, "./")
	}
	return name
}

func validFilePath(name string) bool { return name != "" && validSitePath(name) }

func validSitePath(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\:") || strings.ContainsRune(name, '\x00') || strings.HasPrefix(name, "/") || path.Clean(name) != name || !fs.ValidPath(name) || len(name) > 1024 {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == ".." || segment == "." || strings.HasPrefix(segment, ".") {
			return false
		}
	}
	return true
}

func hasFileDirectoryConflict(files map[string]struct{}) bool {
	for name := range files {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, ok := files[parent]; ok {
				return true
			}
		}
	}
	return false
}

func hashFiles(files []File) string {
	h := sha256.New()
	var size [8]byte
	for _, f := range files {
		_, _ = io.WriteString(h, f.Path)
		_, _ = h.Write([]byte{0})
		binary.BigEndian.PutUint64(size[:], uint64(len(f.Content)))
		_, _ = h.Write(size[:])
		_, _ = h.Write(f.Content)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func zipFiles(files []File) ([]byte, error) {
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.Path, Method: zip.Deflate}
		h.SetMode(0o644)
		h.Modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
		z, err := w.CreateHeader(h)
		if err != nil {
			_ = w.Close()
			return nil, err
		}
		if _, err := z.Write(f.Content); err != nil {
			_ = w.Close()
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// BundleHandler serves a validated site using the same index, dot-path, robots and 404 rules as the panel decoy directory.
func BundleHandler(b *Bundle) http.Handler {
	if b == nil {
		return Handler()
	}
	files := make(map[string][]byte, len(b.files))
	for _, f := range b.files {
		files[f.Path] = f.Content
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			bundleNotFound(w, r, files)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" || strings.HasSuffix(r.URL.Path, "/") {
			name += "index.html"
		}
		if !validSitePath(name) {
			bundleNotFound(w, r, files)
			return
		}
		content, ok := files[name]
		if !ok {
			if _, exists := files[path.Join(name, "index.html")]; exists {
				name = path.Join(name, "index.html")
				content, ok = files[name]
			}
		}
		if !ok {
			if name == "robots.txt" {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Header().Set("Content-Length", fmt.Sprint(len(robotsTXT)))
				w.WriteHeader(http.StatusOK)
				if r.Method != http.MethodHead {
					_, _ = io.WriteString(w, robotsTXT)
				}
				return
			}
			bundleNotFound(w, r, files)
			return
		}
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(content))
	})
}

func bundleNotFound(w http.ResponseWriter, r *http.Request, files map[string][]byte) {
	body := notFoundHTML
	if custom, ok := files["404.html"]; ok {
		body = string(custom)
	}
	write(w, r, http.StatusNotFound, body)
}

const robotsTXT = "User-agent: GPTBot\nUser-agent: ChatGPT-User\nUser-agent: OAI-SearchBot\nUser-agent: ClaudeBot\n" +
	"User-agent: Claude-Web\nUser-agent: anthropic-ai\nUser-agent: CCBot\nUser-agent: Google-Extended\n" +
	"User-agent: Bytespider\nUser-agent: PerplexityBot\nUser-agent: Amazonbot\nUser-agent: Applebot-Extended\n" +
	"User-agent: meta-externalagent\nUser-agent: cohere-ai\nUser-agent: Diffbot\nDisallow: /\n\nUser-agent: *\nAllow: /\n"
