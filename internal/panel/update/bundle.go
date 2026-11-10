package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/asndb"
	"github.com/mistgate/mistgate/internal/release"
)

type writeDeadlineContextKey struct{}

// WithWriteDeadline passes the HTTP handler's per-response write deadline setter to streamed bundle responses.
func WithWriteDeadline(ctx context.Context, set func(time.Time) error) context.Context {
	if set == nil {
		return ctx
	}
	return context.WithValue(ctx, writeDeadlineContextKey{}, set)
}

// WriteDeadlineFromContext returns the HTTP response's write deadline setter when the handler attached one.
func WriteDeadlineFromContext(ctx context.Context) func(time.Time) error {
	set, _ := ctx.Value(writeDeadlineContextKey{}).(func(time.Time) error)
	return set
}

// The release bundle: <data-dir>/dist/{manifest.json, manifest.sig, <binaries>}.

const (
	errKeyPrefix = "updates.bundle.err."
	chunkSize    = 256 << 10
	maxServes    = 4 // concurrent FetchUpdate streams
)

// fileStamp is what a scan saw of one listed file; Serve compares it with the file it opens.
type fileStamp struct {
	size    int64
	modTime time.Time
}

// racyWindow is how close to "now" a file's modification time may be before its size and time stop proving that the
// file is the one that was read. A file rewritten with the same size within one timestamp tick (a few milliseconds on
// Linux, up to two seconds on FAT) looks unchanged; inode and ctime do not help, a rewrite in place keeps the inode and
// ctime ticks like mtime. So, as git does for its index, what was read that soon after the change is read again.
const racyWindow = 2 * time.Second

// settled reports that a file with this modification time, read at the instant at, cannot have been rewritten
// unnoticed since: a later rewrite would carry a later timestamp.
func settled(modTime, at time.Time) bool { return modTime.Before(at.Add(-racyWindow)) }

type hashEntry struct {
	fileStamp
	sha string
	at  time.Time // when the file was hashed, by the system clock the filesystem stamps with
}

// bundleState is one scan of the directory. It is never modified after it is published, so handlers can share it.
type bundleState struct {
	view     *adminv1.Bundle
	manifest *release.Manifest // the parsed manifest, even when untrusted (for display); nil when unreadable
	raw, sig []byte            // manifest.json and manifest.sig as read; what a rollout ships when trusted
	trusted  bool
	files    map[string]fileStamp // listed files, when trusted
	stamp    string               // fingerprint of the directory listing, to notice changes
	settled  bool                 // no file was changed so shortly before the scan that the stamp could miss a rewrite
}

func bundleErr(b *adminv1.Bundle, status adminv1.BundleStatus, code string, params map[string]string) *adminv1.Bundle {
	b.Status = status
	if code != "" {
		b.ErrorKey = errKeyPrefix + code
	}
	b.Params = params
	return b
}

// rescan reads the directory and publishes the result.
func (s *Service) rescan() *bundleState {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	return s.rescanLocked(true)
}

// rescanForRequest publishes the scanned manifest immediately and loads its ASN table in the background. The table can
// take several megabytes to verify and parse, so an admin refresh must not put that work on the request path.
func (s *Service) rescanForRequest() *bundleState {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	bs := s.rescanLocked(false)
	go s.reloadNetworkTable()
	return bs
}

func (s *Service) rescanLocked(loadTable bool) *bundleState {
	previous := s.current()
	bs := s.scan()
	s.bmu.Lock()
	s.bundle = bs
	s.bmu.Unlock()
	if loadTable {
		s.reloadNetworkTable()
	}
	if networkTableSHA(previous) != networkTableSHA(bs) {
		s.notifyNetworkBundleChanged()
	}
	return bs
}

func networkTableSHA(bs *bundleState) string {
	entry, ok := networkTableEntry(bs)
	if !ok {
		return ""
	}
	return entry.SHA256
}

func networkTableEntry(bs *bundleState) (release.File, bool) {
	if bs == nil || !bs.trusted || bs.manifest == nil {
		return release.File{}, false
	}
	for _, entry := range bs.manifest.Files {
		if entry.OS == "data" && entry.Arch == "any" && entry.Name == "mistgate-asndb-data-any" {
			return entry, true
		}
	}
	return release.File{}, false
}

func (s *Service) reloadNetworkTable() {
	s.networkTableMu.Lock()
	defer s.networkTableMu.Unlock()
	for {
		bs := s.current()
		entry, ok := networkTableEntry(bs)
		sha := ""
		if ok && bs.manifest != nil && s.now().Unix() <= bs.manifest.Expires {
			sha = entry.SHA256
		} else {
			ok = false
		}
		if sha == s.networkTableSHA {
			return
		}
		if !ok {
			s.networkTableSHA = ""
			s.networkTable.Store(nil)
			s.networkTableRejected.Store(nil)
			return
		}
		f, _, digest, err := s.OpenNodeBinary(context.Background(), "data", "any")
		var table *asndb.DB
		parseRejected := false
		if err == nil {
			data, readErr := io.ReadAll(f)
			closeErr := f.Close()
			if readErr != nil {
				err = readErr
			} else if closeErr != nil {
				err = closeErr
			} else {
				parse := s.parseNetworkTable
				if parse == nil {
					parse = asndb.Parse
				}
				table, err = parse(bytes.NewReader(data))
				parseRejected = err != nil
			}
			if err == nil && digest != sha {
				err = errors.New("ASN table digest changed while loading")
			}
		}
		// A newer rescan can publish while the file is parsed. Do not publish the stale table or let its digest hide
		// the newer one; the next loop loads the current manifest instead.
		_, currentSHA := s.NetworkTableFile()
		if currentSHA != sha {
			continue
		}
		if err == nil {
			s.networkTableSHA = sha
			s.networkTable.Store(table)
			s.networkTableRejected.Store(nil)
			return
		}
		s.networkTable.Store(nil)
		code := "asn_table_load_failed"
		if parseRejected {
			s.networkTableSHA = sha
			rejected := sha
			s.networkTableRejected.Store(&rejected)
			code = "asn_table_invalid"
		}
		s.log.Warn("update: trusted ASN table could not be loaded", "code", code, "sha256", sha, "err", err)
		return
	}
}

// NetworksTable returns the immutable panel-side table loaded off the request path. Nil means absent or invalid.
func (s *Service) NetworksTable() *asndb.DB { return s.networkTable.Load() }

// NetworkTableFile returns the trusted bundle file for node streams, when present.
func (s *Service) NetworkTableFile() (name, sha256 string) {
	bs := s.current()
	entry, ok := networkTableEntry(bs)
	if !ok || bs.manifest == nil || s.now().Unix() > bs.manifest.Expires {
		return "", ""
	}
	if rejected := s.networkTableRejected.Load(); rejected != nil && *rejected == entry.SHA256 {
		return "", ""
	}
	return entry.Name, entry.SHA256
}

// SetNetworkBundleChanged registers the callback that asks live streams to reconcile after the trusted table digest changes.
func (s *Service) SetNetworkBundleChanged(callback func()) {
	s.networkChangedMu.Lock()
	s.networkBundleChanged = callback
	s.networkChangedMu.Unlock()
}

func (s *Service) notifyNetworkBundleChanged() {
	s.networkChangedMu.RLock()
	callback := s.networkBundleChanged
	s.networkChangedMu.RUnlock()
	if callback != nil {
		callback()
	}
}

// rescanIfChanged scans again when the directory changed, when the last scan read a file too soon after its change to
// be sure of it, and when a trusted manifest has expired.
func (s *Service) rescanIfChanged() {
	stamp, _ := dirStamp(s.dist)
	s.bmu.Lock()
	b := s.bundle
	same := b != nil && b.stamp == stamp && b.settled && !(b.trusted && s.now().Unix() > b.manifest.Expires)
	s.bmu.Unlock()
	if !same {
		s.rescan()
	}
}

func (s *Service) current() *bundleState {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	return s.bundle
}

// dirStamp is a fingerprint of the regular files in dir, and the newest modification time among them.
func dirStamp(dir string) (string, time.Time) {
	if dir == "" {
		return "missing", time.Time{}
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "missing", time.Time{}
	}
	var newest time.Time
	var parts []string
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if fi.ModTime().After(newest) {
			newest = fi.ModTime()
		}
		parts = append(parts, fmt.Sprintf("%s:%d:%d", e.Name(), fi.Size(), fi.ModTime().UnixNano()))
	}
	sort.Strings(parts)
	return strings.Join(parts, "|"), newest
}

func readSmall(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("file too large")
	}
	return b, nil
}

// scan checks the bundle step by step; the first check that fails decides its status.
func (s *Service) scan() *bundleState {
	now := s.now()
	read := time.Now() // the system clock, not the service's: file times come from it
	stamp, newest := dirStamp(s.dist)
	bs := &bundleState{stamp: stamp, settled: settled(newest, read)}
	v := &adminv1.Bundle{ScannedUnix: now.Unix()}
	bs.view = v

	if s.dist == "" {
		bundleErr(v, adminv1.BundleStatus_BUNDLE_STATUS_MISSING, "no_manifest", nil)
		return bs
	}
	raw, err := readSmall(filepath.Join(s.dist, release.ManifestName), release.MaxManifestBytes)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		bundleErr(v, adminv1.BundleStatus_BUNDLE_STATUS_MISSING, "no_manifest", nil)
		return bs
	case err != nil:
		bundleErr(v, adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, "bad_manifest", nil)
		return bs
	}
	sig, sigErr := readSmall(filepath.Join(s.dist, release.SignatureName), 1024)
	m, perr := release.Parse(raw)
	if perr == nil { // shown even when it cannot be trusted: the owner sees what is in the directory
		bs.manifest, bs.raw = m, raw
		v.Version, v.Built, v.ExpiresUnix = m.Version, m.Built, m.Expires
		for _, f := range m.Files {
			v.Files = append(v.Files, &adminv1.BundleFile{Os: f.OS, Arch: f.Arch, Name: f.Name, Size: uint64(f.Size), Sha256: f.SHA256})
		}
	}
	switch {
	case sigErr != nil:
		bundleErr(v, adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, "no_signature", nil)
		return bs
	case perr != nil:
		code := "bad_manifest"
		if errors.Is(perr, release.ErrUnsupportedSchema) {
			code = "unsupported_schema"
		}
		bundleErr(v, adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, code, nil)
		return bs
	case s.cfg.Key == nil:
		bundleErr(v, adminv1.BundleStatus_BUNDLE_STATUS_NO_KEY, "", nil)
		return bs
	}
	if _, err := release.Verify(s.cfg.Key, raw, sig); err != nil {
		bundleErr(v, adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, "bad_signature", nil)
		return bs
	}
	if now.Unix() > m.Expires {
		bundleErr(v, adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, "expired", nil)
		return bs
	}
	stamps := map[string]fileStamp{}
	for _, f := range m.Files {
		st, code := s.checkFile(f)
		if code != "" {
			bundleErr(v, adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, code, map[string]string{"file": f.Name})
			return bs
		}
		stamps[f.Name] = st
	}
	bs.sig, bs.files, bs.trusted = sig, stamps, true
	v.Status = adminv1.BundleStatus_BUNDLE_STATUS_TRUSTED
	return bs
}

// checkFile compares one listed file with its manifest entry: code is "" when it matches. A cached hash is used only
// when the file was settled at the time it was hashed (racyWindow).
func (s *Service) checkFile(f release.File) (fileStamp, string) {
	path := filepath.Join(s.dist, f.Name)
	fi, err := os.Lstat(path) // a symlink is not followed: the bundle is exactly the files in the directory
	if err != nil || !fi.Mode().IsRegular() {
		return fileStamp{}, "file_missing"
	}
	st := fileStampOf(fi)
	if st.size != f.Size {
		return st, "file_mismatch"
	}
	s.bmu.Lock()
	h, ok := s.hashes[f.Name]
	s.bmu.Unlock()
	if !ok || h.fileStamp != st || !settled(st.modTime, h.at) {
		at := time.Now()
		fh, err := os.Open(path)
		if err != nil {
			return st, "file_missing"
		}
		sum := sha256.New()
		_, err = io.Copy(sum, fh)
		fh.Close()
		if err != nil {
			return st, "file_missing"
		}
		h = hashEntry{fileStamp: st, sha: hex.EncodeToString(sum.Sum(nil)), at: at}
		s.bmu.Lock()
		s.hashes[f.Name] = h
		s.bmu.Unlock()
	}
	if h.sha != f.SHA256 {
		return st, "file_mismatch"
	}
	return st, ""
}

func fileStampOf(fi os.FileInfo) fileStamp {
	return fileStamp{size: fi.Size(), modTime: fi.ModTime()}
}

// NodeBinary is fleet.Updates.NodeBinary: the absolute path of the platform's file of the current trusted bundle, ""
// without one. The path is the one the last scan verified; Serve re-checks the file itself, scp of the owner does not
// need to.
func (s *Service) NodeBinary(goos, goarch string) string {
	b := s.current()
	if b == nil || !b.trusted {
		return ""
	}
	f, err := b.manifest.FileFor(goos, goarch)
	if err != nil {
		return ""
	}
	p, err := filepath.Abs(filepath.Join(s.dist, f.Name))
	if err != nil {
		return ""
	}
	return filepath.ToSlash(p)
}

// OpenNodeBinary opens a verified node binary from the current trusted bundle (provision.BinarySource; the file is local,
// so the context is unused). The caller receives the signed manifest's digest as well and must verify the bytes it
// transfers: the descriptor is stable across path replacement, while an in-place write can still change bytes after this
// method returns.
func (s *Service) OpenNodeBinary(_ context.Context, goos, goarch string) (io.ReadCloser, int64, string, error) {
	b := s.current()
	if b == nil || !b.trusted || b.manifest == nil || s.now().Unix() > b.manifest.Expires {
		return nil, 0, "", errors.New("update: no current trusted bundle")
	}
	entry, err := b.manifest.FileFor(goos, goarch)
	if err != nil {
		return nil, 0, "", errors.New("update: the bundle has no binary for this platform")
	}
	path := filepath.Join(s.dist, entry.Name)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != entry.Size {
		return nil, 0, "", errors.New("update: the trusted node binary is missing or changed")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, "", errors.New("update: the trusted node binary is unavailable")
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() != entry.Size {
		f.Close()
		return nil, 0, "", errors.New("update: the trusted node binary is missing or changed")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil || hex.EncodeToString(h.Sum(nil)) != entry.SHA256 {
		f.Close()
		return nil, 0, "", errors.New("update: the trusted node binary failed its digest check")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, 0, "", errors.New("update: the trusted node binary cannot be rewound")
	}
	return f, entry.Size, entry.SHA256, nil
}

// Serve is fleet.Updates.Serve: it streams one file of the current trusted bundle to an agent that already
// authenticated with its node certificate. Only names in the trusted manifest are served, at most maxServes at a
// time; the descriptor is opened once, so a file replaced during a download cannot corrupt it.
func (s *Service) Serve(ctx context.Context, nodeID, name string, offset uint64, send func(chunk []byte, total uint64) error) error {
	return s.serve(ctx, nodeID, name, offset, nil, send)
}

// ServeWithWriteDeadline streams one file and bounds each response write with the handler's HTTP write deadline.
func (s *Service) ServeWithWriteDeadline(ctx context.Context, nodeID, name string, offset uint64,
	setWriteDeadline func(time.Time) error, send func(chunk []byte, total uint64) error) error {
	return s.serve(ctx, nodeID, name, offset, setWriteDeadline, send)
}

func (s *Service) serve(ctx context.Context, nodeID, name string, offset uint64,
	setWriteDeadline func(time.Time) error, send func(chunk []byte, total uint64) error) error {
	b := s.current()
	if b == nil || !b.trusted {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("no trusted bundle"))
	}
	entry, ok := b.manifest.FindName(name)
	if !ok {
		return connect.NewError(connect.CodeNotFound, errors.New("no such file"))
	}
	total := uint64(entry.Size)
	if offset > total {
		return connect.NewError(connect.CodeNotFound, errors.New("offset beyond the end of the file"))
	}
	s.bmu.Lock()
	if _, alreadyServing := s.servingNodes[nodeID]; alreadyServing {
		s.bmu.Unlock()
		return connect.NewError(connect.CodeResourceExhausted, errors.New("a download is already active for this node, retry later"))
	}
	if s.serves >= maxServes {
		s.bmu.Unlock()
		return connect.NewError(connect.CodeResourceExhausted, errors.New("too many downloads, retry in a few seconds"))
	}
	s.serves++
	s.servingNodes[nodeID] = struct{}{}
	s.bmu.Unlock()
	defer func() {
		s.bmu.Lock()
		s.serves--
		delete(s.servingNodes, nodeID)
		s.bmu.Unlock()
	}()

	f, err := os.Open(filepath.Join(s.dist, entry.Name))
	if err != nil {
		s.log.Warn("update: bundle file unreadable", "file", entry.Name, "err", err)
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle changed, retry"))
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fileStampOf(fi) != b.files[entry.Name] {
		go s.rescan() // the file is not the one that was verified: look again, the agent retries
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle changed, retry"))
	}
	if _, err := f.Seek(int64(offset), io.SeekStart); err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	buf := make([]byte, chunkSize)
	sent := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := io.ReadFull(f, buf)
		if n > 0 {
			if err := s.sendBundleChunk(ctx, setWriteDeadline, send, buf[:n], total); err != nil {
				return err
			}
			sent = true
		}
		if rerr != nil { // EOF or ErrUnexpectedEOF: the last chunk has gone
			if !sent { // offset == size: one empty message tells the agent the total
				return s.sendBundleChunk(ctx, setWriteDeadline, send, nil, total)
			}
			return nil
		}
	}
}

func (s *Service) sendBundleChunk(ctx context.Context, setWriteDeadline func(time.Time) error,
	send func([]byte, uint64) error, chunk []byte, total uint64) error {
	timeout := s.sendTimeout
	if timeout <= 0 {
		timeout = bundleSendStallTimeout
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	if setWriteDeadline != nil {
		setErr := setWriteDeadline(deadline)
		if setErr != nil && !errors.Is(setErr, http.ErrNotSupported) {
			return setErr
		}
		if setErr == nil {
			defer func() { _ = setWriteDeadline(time.Time{}) }()
		}
	}
	if err := send(chunk, total); err != nil {
		var timeoutErr interface{ Timeout() bool }
		if errors.Is(err, os.ErrDeadlineExceeded) || errors.As(err, &timeoutErr) && timeoutErr.Timeout() {
			return connect.NewError(connect.CodeUnavailable, fmt.Errorf("download send stalled, retry later: %w", err))
		}
		return err
	}
	return nil
}
