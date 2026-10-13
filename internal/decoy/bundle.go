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
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// MaxBundleBytes caps the sum of the file contents and, separately, an uploaded archive. The same cap applies to a
	// bundle an owner uploads for one node and to the snapshot of the panel's own --decoy-dir.
	MaxBundleBytes        = 2 << 20
	maxBundleFiles        = 4096
	maxArchiveEntries     = 2 * maxBundleFiles // files, directories and skipped hidden entries together
	maxStoredArchiveBytes = MaxBundleBytes + 512<<10
	// maxTarStream bounds what a gzip stream may expand to: every entry at 1 KiB of tar framing plus the contents.
	maxTarStream = MaxBundleBytes + 2*maxArchiveEntries*512
)

// ErrInvalidBundle is the base of every bundle refusal; the concrete error is a *BundleError.
var ErrInvalidBundle = errors.New("invalid decoy bundle")

// Reasons a bundle is refused. They are stable: the admin shows a sentence for each.
const (
	ReasonEmpty        = "empty"          // nothing to serve
	ReasonBadArchive   = "bad_archive"    // not a readable ZIP, TAR or TAR.GZ
	ReasonTooLarge     = "too_large"      // the archive or its files are over MaxBundleBytes
	ReasonTooManyFiles = "too_many_files" // more than 4096 entries
	ReasonBadPath      = "bad_path"       // "..", an absolute path, a backslash, a colon or a NUL byte in a name
	ReasonNotStatic    = "not_static"     // a symlink, device, FIFO or other entry that is not a plain file
	ReasonNoIndex      = "no_index"       // no index.html at the top level
	ReasonDuplicate    = "duplicate"      // one path twice, or a file where a directory is needed
)

// BundleError says why a bundle was refused; Path names the offending entry when there is one.
type BundleError struct {
	Reason string
	Path   string
}

func (e *BundleError) Error() string {
	if e.Path == "" {
		return "invalid decoy bundle: " + e.Reason
	}
	return "invalid decoy bundle: " + e.Reason + " (" + e.Path + ")"
}

func (e *BundleError) Is(target error) bool { return target == ErrInvalidBundle }

// ClientMessage is the coded message the admin and the MCP tools show: "decoy_bundle_<reason>" or
// "decoy_bundle_<reason>: path=<entry>" (the convention of the panel's refusal codes, web/src/lib/errors.ts).
func (e *BundleError) ClientMessage() string {
	m := "decoy_bundle_" + e.Reason
	if e.Path != "" {
		m += ": path=" + url.QueryEscape(e.Path)
	}
	return m
}

// ReasonOf returns the reason of a refusal, ReasonBadArchive for an error that is not a *BundleError.
func ReasonOf(err error) string {
	var be *BundleError
	if errors.As(err, &be) {
		return be.Reason
	}
	return ReasonBadArchive
}

// ClientMessage is the coded message for any bundle error.
func ClientMessage(err error) string {
	var be *BundleError
	if errors.As(err, &be) {
		return be.ClientMessage()
	}
	return "decoy_bundle_" + ReasonBadArchive
}

func refuse(reason, entry string) error {
	if len(entry) > 80 {
		entry = entry[:80]
	}
	return &BundleError{Reason: reason, Path: strings.ToValidUTF8(entry, "?")}
}

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

// ParseArchive accepts a ZIP, TAR or gzip-compressed TAR upload. The upload and the expanded contents are each capped at
// 2 MiB. Dot-files and macOS "__MACOSX" entries are dropped (the site never serves them), a single wrapping folder is
// removed when index.html is inside it, and symlinks, devices, ".." and absolute paths are refused.
func ParseArchive(data []byte) (*Bundle, error) {
	if len(data) == 0 {
		return nil, refuse(ReasonEmpty, "")
	}
	if len(data) > MaxBundleBytes {
		return nil, refuse(ReasonTooLarge, "")
	}
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{'P', 'K', 3, 4}) {
		return parseZip(data, MaxBundleBytes, true)
	}
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, refuse(ReasonBadArchive, "")
		}
		defer gz.Close()
		return parseTar(io.LimitReader(gz, maxTarStream+1), MaxBundleBytes)
	}
	return parseTar(bytes.NewReader(data), MaxBundleBytes)
}

// ParseStoredArchive reads the canonical ZIP kept by the panel and node. Its ZIP metadata may exceed the file-content cap.
func ParseStoredArchive(data []byte) (*Bundle, error) {
	if len(data) == 0 {
		return nil, refuse(ReasonEmpty, "")
	}
	if len(data) > maxStoredArchiveBytes {
		return nil, refuse(ReasonTooLarge, "")
	}
	return parseZip(data, MaxBundleBytes, false)
}

// FromFiles validates files received over the agent protocol and makes a canonical bundle.
func FromFiles(files []File) (*Bundle, error) {
	if len(files) == 0 {
		return nil, refuse(ReasonEmpty, "")
	}
	if len(files) > maxBundleFiles {
		return nil, refuse(ReasonTooManyFiles, "")
	}
	out := make([]File, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	var total int64
	for _, f := range files {
		if !validFilePath(f.Path) {
			return nil, refuse(ReasonBadPath, f.Path)
		}
		if _, ok := seen[f.Path]; ok {
			return nil, refuse(ReasonDuplicate, f.Path)
		}
		seen[f.Path] = struct{}{}
		total += int64(len(f.Content))
		if total > MaxBundleBytes {
			return nil, refuse(ReasonTooLarge, "")
		}
		out = append(out, File{Path: f.Path, Content: bytes.Clone(f.Content)})
	}
	if name := fileDirectoryConflict(seen); name != "" {
		return nil, refuse(ReasonDuplicate, name)
	}
	if _, ok := seen["index.html"]; !ok {
		return nil, refuse(ReasonNoIndex, "")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	b := &Bundle{files: out, hash: hashFiles(out)}
	archive, err := zipFiles(out)
	if err != nil || len(archive) > maxStoredArchiveBytes {
		return nil, refuse(ReasonTooLarge, "")
	}
	b.archive = archive
	return b, nil
}

// FromDir snapshots a local --decoy-dir bundle. Paths the panel hides by its dot-path rule are omitted; symlinks and
// non-regular files are refused. The directory is held to the same 2 MiB cap as an uploaded bundle.
func FromDir(dir string) (*Bundle, error) {
	if dir == "" {
		return nil, refuse(ReasonEmpty, "")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, refuse(ReasonBadPath, "")
	}
	st, err := os.Lstat(root)
	if err != nil || st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return nil, refuse(ReasonBadArchive, "")
	}
	files := make([]File, 0)
	var total int64
	err = filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return refuse(ReasonBadArchive, "")
		}
		if name == root {
			return nil
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return refuse(ReasonBadPath, "")
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
			return refuse(ReasonBadArchive, "")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return refuse(ReasonNotStatic, rel)
		}
		if info.IsDir() {
			if !validDirPath(rel) {
				return refuse(ReasonBadPath, rel)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return refuse(ReasonNotStatic, rel)
		}
		if !validFilePath(rel) {
			return refuse(ReasonBadPath, rel)
		}
		if len(files) == maxBundleFiles {
			return refuse(ReasonTooManyFiles, "")
		}
		if info.Size() < 0 || info.Size() > MaxBundleBytes-total {
			return refuse(ReasonTooLarge, "")
		}
		f, err := os.Open(name)
		if err != nil {
			return refuse(ReasonBadArchive, rel)
		}
		openedInfo, statErr := f.Stat()
		if statErr != nil || !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() {
			_ = f.Close()
			return refuse(ReasonNotStatic, rel)
		}
		content, readErr := io.ReadAll(io.LimitReader(f, MaxBundleBytes+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil {
			return refuse(ReasonBadArchive, rel)
		}
		total += int64(len(content))
		if total > MaxBundleBytes {
			return refuse(ReasonTooLarge, "")
		}
		files = append(files, File{Path: rel, Content: content})
		return nil
	})
	if err != nil {
		var be *BundleError
		if errors.As(err, &be) {
			return nil, be
		}
		return nil, refuse(ReasonBadArchive, "")
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

// archiveEntry cleans an archive entry name. skip is true for what the site never serves (dot-files, "__MACOSX", the
// archive root); a name that could escape the site root is an error, never skipped.
func archiveEntry(raw string) (name string, skip bool, err error) {
	name = archivePath(raw)
	if name == "" || name == "." {
		return "", true, nil
	}
	if strings.ContainsAny(name, "\\:") || strings.ContainsRune(name, 0) || strings.HasPrefix(name, "/") || len(name) > 1024 {
		return "", false, refuse(ReasonBadPath, raw)
	}
	segments := strings.Split(name, "/")
	for _, s := range segments {
		if s == "" || s == ".." || s == "." {
			return "", false, refuse(ReasonBadPath, raw)
		}
	}
	if segments[0] == "__MACOSX" || hasHiddenPathSegment(name) {
		return name, true, nil
	}
	return name, false, nil
}

// finishArchive turns the collected files into a bundle: a single wrapping folder is removed when the site's index.html
// is inside it, which is what zipping a folder produces.
func finishArchive(files []File, seen map[string]struct{}) (*Bundle, error) {
	if len(files) == 0 {
		return nil, refuse(ReasonEmpty, "")
	}
	if name := fileDirectoryConflict(seen); name != "" {
		return nil, refuse(ReasonDuplicate, name)
	}
	if _, ok := seen["index.html"]; !ok {
		prefix := files[0].Path[:max(strings.IndexByte(files[0].Path, '/')+1, 0)]
		wrapped := prefix != ""
		for _, f := range files {
			wrapped = wrapped && strings.HasPrefix(f.Path, prefix)
		}
		if _, ok := seen[prefix+"index.html"]; wrapped && ok {
			for i := range files {
				files[i].Path = strings.TrimPrefix(files[i].Path, prefix)
			}
		}
	}
	return FromFiles(files)
}

// parseZip reads an archive. lenient is true for an upload (hidden entries are dropped and a wrapping folder removed);
// the stored canonical ZIP holds neither and is read strictly.
func parseZip(data []byte, maxTotal int64, lenient bool) (*Bundle, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, refuse(ReasonBadArchive, "")
	}
	if len(r.File) == 0 {
		return nil, refuse(ReasonEmpty, "")
	}
	if len(r.File) > maxArchiveEntries {
		return nil, refuse(ReasonTooManyFiles, "")
	}
	files := make([]File, 0, len(r.File))
	seen := make(map[string]struct{}, len(r.File))
	var total int64
	for _, zf := range r.File {
		name, skip, err := archiveEntry(zf.Name)
		if err != nil {
			return nil, err
		}
		if !lenient && skip {
			return nil, refuse(ReasonBadPath, zf.Name)
		}
		if zf.FileInfo().IsDir() {
			continue
		}
		if !zf.Mode().IsRegular() {
			return nil, refuse(ReasonNotStatic, zf.Name)
		}
		if skip {
			continue
		}
		if _, ok := seen[name]; ok {
			return nil, refuse(ReasonDuplicate, name)
		}
		seen[name] = struct{}{}
		if zf.UncompressedSize64 > uint64(maxTotal-total) {
			return nil, refuse(ReasonTooLarge, "")
		}
		f, err := zf.Open()
		if err != nil {
			return nil, refuse(ReasonBadArchive, name)
		}
		content, readErr := io.ReadAll(io.LimitReader(f, maxTotal-total+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || uint64(len(content)) != zf.UncompressedSize64 {
			return nil, refuse(ReasonBadArchive, name)
		}
		total += int64(len(content))
		files = append(files, File{Path: name, Content: content})
	}
	if lenient {
		return finishArchive(files, seen)
	}
	return FromFiles(files)
}

func parseTar(r io.Reader, maxTotal int64) (*Bundle, error) {
	tr := tar.NewReader(r)
	files := make([]File, 0)
	seen := make(map[string]struct{})
	var total int64
	for entries := 0; ; entries++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, refuse(ReasonBadArchive, "")
		}
		if entries >= maxArchiveEntries {
			return nil, refuse(ReasonTooManyFiles, "")
		}
		if h.Typeflag == tar.TypeXGlobalHeader { // `git archive` writes one; it holds no file
			continue
		}
		name, skip, err := archiveEntry(h.Name)
		if err != nil {
			return nil, err
		}
		switch h.Typeflag {
		case tar.TypeDir:
		case tar.TypeReg, tar.TypeRegA:
			if skip {
				continue
			}
			if h.Size < 0 || h.Size > maxTotal-total {
				return nil, refuse(ReasonTooLarge, "")
			}
			if _, ok := seen[name]; ok {
				return nil, refuse(ReasonDuplicate, name)
			}
			seen[name] = struct{}{}
			content, err := io.ReadAll(io.LimitReader(tr, h.Size+1))
			if err != nil || int64(len(content)) != h.Size {
				return nil, refuse(ReasonBadArchive, name)
			}
			total += int64(len(content))
			files = append(files, File{Path: name, Content: content})
		default:
			return nil, refuse(ReasonNotStatic, h.Name)
		}
	}
	return finishArchive(files, seen)
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

// fileDirectoryConflict returns a path that is a file and also the parent of another file, or "".
func fileDirectoryConflict(files map[string]struct{}) string {
	for name := range files {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, ok := files[parent]; ok {
				return parent
			}
		}
	}
	return ""
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
