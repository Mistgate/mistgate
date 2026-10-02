package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/release"
)

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

type hashEntry struct {
	fileStamp
	sha string
}

// bundleState is one scan of the directory. It is never modified after it is published, so handlers can share it.
type bundleState struct {
	view     *adminv1.Bundle
	manifest *release.Manifest // the parsed manifest, even when untrusted (for display); nil when unreadable
	raw, sig []byte            // manifest.json and manifest.sig as read; what a rollout ships when trusted
	trusted  bool
	files    map[string]fileStamp // listed files, when trusted
	stamp    string               // fingerprint of the directory listing, to notice changes
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
	bs := s.scan()
	s.bmu.Lock()
	s.bundle = bs
	s.bmu.Unlock()
	return bs
}

// rescanIfChanged is the periodic poll: a scan only when the directory listing (names, sizes, mtimes) changed.
func (s *Service) rescanIfChanged() {
	stamp := dirStamp(s.dist)
	s.bmu.Lock()
	same := s.bundle != nil && s.bundle.stamp == stamp
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

// dirStamp is a fingerprint of the regular files in dir.
func dirStamp(dir string) string {
	if dir == "" {
		return "missing"
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "missing"
	}
	var parts []string
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%d:%d", e.Name(), fi.Size(), fi.ModTime().UnixNano()))
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
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
	bs := &bundleState{stamp: dirStamp(s.dist)}
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

// checkFile compares one listed file with its manifest entry: code is "" when it matches. The hash of a file is kept by
// (size, mtime), so the poll does not re-read 30 MB every minute.
func (s *Service) checkFile(f release.File) (fileStamp, string) {
	path := filepath.Join(s.dist, f.Name)
	fi, err := os.Lstat(path) // a symlink is not followed: the bundle is exactly the files in the directory
	if err != nil || !fi.Mode().IsRegular() {
		return fileStamp{}, "file_missing"
	}
	st := fileStamp{size: fi.Size(), modTime: fi.ModTime()}
	if st.size != f.Size {
		return st, "file_mismatch"
	}
	s.bmu.Lock()
	h, ok := s.hashes[f.Name]
	s.bmu.Unlock()
	if !ok || h.fileStamp != st {
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
		h = hashEntry{fileStamp: st, sha: hex.EncodeToString(sum.Sum(nil))}
		s.bmu.Lock()
		s.hashes[f.Name] = h
		s.bmu.Unlock()
	}
	if h.sha != f.SHA256 {
		return st, "file_mismatch"
	}
	return st, ""
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

// OpenNodeBinary opens a verified node binary from the current trusted bundle. The caller receives the signed
// manifest's digest as well and must verify the bytes it transfers: the descriptor is stable across path replacement,
// while an in-place write can still change bytes after this method returns.
func (s *Service) OpenNodeBinary(goos, goarch string) (*os.File, int64, string, error) {
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
	if s.serves >= maxServes {
		s.bmu.Unlock()
		return connect.NewError(connect.CodeResourceExhausted, errors.New("too many downloads, retry in a few seconds"))
	}
	s.serves++
	s.bmu.Unlock()
	defer func() {
		s.bmu.Lock()
		s.serves--
		s.bmu.Unlock()
	}()

	f, err := os.Open(filepath.Join(s.dist, entry.Name))
	if err != nil {
		s.log.Warn("update: bundle file unreadable", "file", entry.Name, "err", err)
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("bundle changed, retry"))
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != entry.Size || !fi.ModTime().Equal(b.files[entry.Name].modTime) {
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
			if err := send(buf[:n], total); err != nil {
				return err
			}
			sent = true
		}
		if rerr != nil { // EOF or ErrUnexpectedEOF: the last chunk has gone
			if !sent { // offset == size: one empty message tells the agent the total
				return send(nil, total)
			}
			return nil
		}
	}
}
