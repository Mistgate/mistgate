package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	backupFormatVersion = 1
	manifestName        = "manifest.json"
	databaseName        = "mistgate.db"
	masterKeyName       = "master.key"
	maxManifestBytes    = 2 << 20
	maxBackupFiles      = 10000
	maxBackupBytes      = int64(50 << 30)
	maxEncryptedBytes   = maxBackupBytes + int64(256<<20)
	maxPlaintextBytes   = maxBackupBytes + maxManifestBytes + int64(maxBackupFiles*8192)
)

var errInvalidBackup = errors.New("backup: invalid or unsupported archive")

type backupManifest struct {
	FormatVersion int          `json:"format_version"`
	AppVersion    string       `json:"app_version"`
	CreatedAt     string       `json:"created_at"`
	Files         []backupFile `json:"files"`
}

type backupFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// preparedFile is one file of the archive: read from source when it is written (its hash was taken first, for the
// manifest, and must still match), or data held in memory (the master key).
type preparedFile struct {
	backupFile
	Mode   os.FileMode
	source string
	data   []byte
}

// stagePrefix names the private directory a backup works in (the snapshot and the encrypted archive), inside the data
// directory: the service may write nowhere else on disk, and /tmp may be a tmpfs counted against the panel's memory.
// The archive leaves such directories out.
const stagePrefix = ".mistgate-backup-"

// CreateEncryptedArchive snapshots the panel's durable data into an age-encrypted, versioned tar.gz stream. The files of
// the data directory are streamed into it, not copied first: their hashes are taken in a first pass (the manifest leads
// the archive), and a file that changes before it is written fails the backup.
func CreateEncryptedArchive(ctx context.Context, dataDir, snapshotDB string, masterKey []byte, appVersion, recipientText string, created time.Time, dst io.Writer) error {
	if len(masterKey) != vault.KeySize {
		return errors.New("backup: invalid master key")
	}
	recipient, err := parseRecipient(recipientText)
	if err != nil {
		return fmt.Errorf("backup: invalid recovery recipient: %w", err)
	}
	files, err := preparePayload(ctx, dataDir, snapshotDB, masterKey)
	if err != nil {
		return err
	}
	if len(files) > maxBackupFiles {
		return errors.New("backup: too many files")
	}
	manifest := backupManifest{FormatVersion: backupFormatVersion, AppVersion: appVersion, CreatedAt: created.UTC().Format(time.RFC3339Nano)}
	manifest.Files = make([]backupFile, 0, len(files))
	for _, f := range files {
		manifest.Files = append(manifest.Files, f.backupFile)
	}
	encodedManifest, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if len(encodedManifest) > maxManifestBytes {
		return errors.New("backup: manifest is too large")
	}
	return writeEncryptedTar(ctx, dst, recipient, files, encodedManifest, created)
}

func parseRecipient(text string) (age.Recipient, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "age1pq1") {
		return age.ParseHybridRecipient(text)
	}
	return age.ParseX25519Recipient(text)
}

func preparePayload(ctx context.Context, dataDir, snapshotDB string, masterKey []byte) ([]preparedFile, error) {
	root, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("backup: data directory is missing or is not a real directory")
	}
	var files []preparedFile
	err = filepath.WalkDir(root, func(source string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if source == root {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		rel, err := filepath.Rel(root, source)
		if err != nil {
			return err
		}
		archivePath := filepath.ToSlash(rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("backup: refusing symlink %q", archivePath)
		}
		if entry.IsDir() {
			if !strings.Contains(archivePath, "/") && strings.HasPrefix(archivePath, stagePrefix) {
				return filepath.SkipDir // a backup's own work directory, this one's or one left by a crash
			}
			return nil
		}
		if skipDataFile(archivePath) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("backup: refusing non-regular file %q", archivePath)
		}
		if err := validateArchivePath(archivePath); err != nil {
			return err
		}
		if archivePath == manifestName {
			return errors.New("backup: data directory contains a reserved archive path")
		}
		item, err := hashFile(ctx, source, archivePath, info.Mode())
		if err != nil {
			return err
		}
		files = append(files, item)
		if len(files) > maxBackupFiles {
			return errors.New("backup: too many files")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	db, err := hashFile(ctx, snapshotDB, databaseName, 0o600)
	if err != nil {
		return nil, err
	}
	keyHash := sha256.Sum256(masterKey)
	files = append(files, db, preparedFile{backupFile: backupFile{Path: masterKeyName, Size: int64(len(masterKey)), SHA256: hex.EncodeToString(keyHash[:])}, Mode: 0o600, data: masterKey})
	if len(files) > maxBackupFiles {
		return nil, errors.New("backup: too many files")
	}
	var total int64
	for _, f := range files {
		total += f.Size
		if total > maxBackupBytes {
			return nil, errors.New("backup: uncompressed data exceeds the size limit")
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for i := 1; i < len(files); i++ {
		if files[i-1].Path == files[i].Path {
			return nil, fmt.Errorf("backup: duplicate data path %q", files[i].Path)
		}
	}
	return files, nil
}

func skipDataFile(name string) bool {
	name = filepath.ToSlash(name)
	if name == databaseName || name == masterKeyName {
		return true
	}
	return strings.HasPrefix(name, databaseName+"-") || name == databaseName+"-journal"
}

// openRegular opens a regular file that is not a symlink, and was not swapped for one between the check and the open.
func openRegular(source, archivePath string) (*os.File, os.FileInfo, error) {
	linkInfo, err := os.Lstat(source)
	if err != nil || linkInfo.Mode()&os.ModeSymlink != 0 || !linkInfo.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("backup: refusing non-regular file %q", archivePath)
	}
	in, err := os.Open(source)
	if err != nil {
		return nil, nil, err
	}
	info, err := in.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(linkInfo, info) {
		in.Close()
		return nil, nil, fmt.Errorf("backup: source changed while opening %q", archivePath)
	}
	return in, info, nil
}

// hashFile reads a file once for the manifest: its size and SHA-256. writeEncryptedTar reads it again and checks both.
func hashFile(ctx context.Context, source, archivePath string, sourceMode os.FileMode) (preparedFile, error) {
	if err := validateArchivePath(archivePath); err != nil {
		return preparedFile{}, err
	}
	in, before, err := openRegular(source, archivePath)
	if err != nil {
		return preparedFile{}, err
	}
	defer in.Close()
	if before.Size() > maxBackupBytes {
		return preparedFile{}, errors.New("backup: file exceeds the size limit")
	}
	h := sha256.New()
	n, err := copyContext(ctx, h, in)
	if err != nil {
		return preparedFile{}, err
	}
	after, err := in.Stat()
	if err != nil {
		return preparedFile{}, err
	}
	if n != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return preparedFile{}, fmt.Errorf("backup: source changed while reading %q", archivePath)
	}
	mode := os.FileMode(0o600)
	if sourceMode.Perm()&0o111 != 0 {
		mode |= 0o100 // restore executable files for the service owner only
	}
	return preparedFile{backupFile: backupFile{Path: archivePath, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, Mode: mode, source: source}, nil
}

func writeEncryptedTar(ctx context.Context, dst io.Writer, recipient age.Recipient, files []preparedFile, manifest []byte, created time.Time) (retErr error) {
	encrypted, err := age.Encrypt(dst, recipient)
	if err != nil {
		return err
	}
	compressed := gzip.NewWriter(encrypted)
	tw := tar.NewWriter(compressed)
	if err := writeTarFile(tw, manifestName, manifest, 0o600, created); err != nil {
		return err
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if f.data != nil {
			if err := writeTarFile(tw, f.Path, f.data, f.Mode, created); err != nil {
				return err
			}
			continue
		}
		in, _, err := openRegular(f.source, f.Path)
		if err != nil {
			return err
		}
		header := &tar.Header{Name: f.Path, Mode: int64(f.Mode.Perm()), Size: f.Size, Typeflag: tar.TypeReg, ModTime: created.UTC(), Format: tar.FormatPAX}
		if err := tw.WriteHeader(header); err != nil {
			in.Close()
			return err
		}
		h := sha256.New()
		n, copyErr := copyContext(ctx, io.MultiWriter(tw, h), in)
		closeErr := in.Close()
		if copyErr != nil || closeErr != nil {
			return errors.Join(copyErr, closeErr) // a file that grew: the tar writer refuses the bytes past its size
		}
		if n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
			return fmt.Errorf("backup: %q changed while it was archived", f.Path)
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := compressed.Close(); err != nil {
		return err
	}
	if err := encrypted.Close(); err != nil {
		return err
	}
	return nil
}

func writeTarFile(tw *tar.Writer, name string, data []byte, mode os.FileMode, modified time.Time) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: int64(mode.Perm()), Size: int64(len(data)), Typeflag: tar.TypeReg, ModTime: modified.UTC(), Format: tar.FormatPAX}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func copyContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 128*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		nr, readErr := src.Read(buf)
		if nr > 0 {
			nw, writeErr := dst.Write(buf[:nr])
			total += int64(nw)
			if writeErr != nil {
				return total, writeErr
			}
			if nw != nr {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

// RestoreEncryptedArchive decrypts and validates a v1 backup into a new data directory.
// It refuses any existing target and never follows archive paths or archive links.
func RestoreEncryptedArchive(ctx context.Context, src io.Reader, identities []age.Identity, destination string) error {
	if len(identities) == 0 {
		return errors.New("backup: no recovery identity was supplied")
	}
	target, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if filepath.Clean(target) != target || filepath.Base(target) == "." || filepath.Base(target) == string(filepath.Separator) {
		return errors.New("backup: invalid restore destination")
	}
	if _, err := os.Lstat(target); err == nil {
		return errors.New("backup: restore destination already exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".mistgate-restore-")
	if err != nil {
		return err
	}
	if err := os.Chmod(stage, 0o700); err != nil {
		os.RemoveAll(stage)
		return err
	}
	keepStage := false
	defer func() {
		if !keepStage {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := extractEncryptedArchive(ctx, src, identities, stage); err != nil {
		return err
	}
	if err := validateRestoredData(ctx, stage); err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return errors.New("backup: restore destination appeared during restore")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(stage, target); err != nil {
		return fmt.Errorf("backup: install restored data directory: %w", err)
	}
	keepStage = true
	return nil
}

func extractEncryptedArchive(ctx context.Context, src io.Reader, identities []age.Identity, stage string) error {
	limitedCiphertext := &io.LimitedReader{R: src, N: maxEncryptedBytes + 1}
	decrypted, err := age.Decrypt(limitedCiphertext, identities...)
	if err != nil {
		return fmt.Errorf("backup: decrypt: %w", err)
	}
	gz, err := gzip.NewReader(decrypted)
	if err != nil {
		return fmt.Errorf("backup: open compressed archive: %w", err)
	}
	defer gz.Close()
	limitedPlaintext := &io.LimitedReader{R: gz, N: maxPlaintextBytes + 1}
	tr := tar.NewReader(limitedPlaintext)
	first, err := tr.Next()
	if err != nil || first == nil || first.Name != manifestName || first.Typeflag != tar.TypeReg || first.Size < 0 || first.Size > maxManifestBytes {
		return errInvalidBackup
	}
	manifestBytes, err := io.ReadAll(io.LimitReader(tr, maxManifestBytes+1))
	if err != nil || int64(len(manifestBytes)) != first.Size || len(manifestBytes) > maxManifestBytes {
		return errInvalidBackup
	}
	manifest, err := decodeManifest(manifestBytes)
	if err != nil {
		return err
	}
	expected := make(map[string]backupFile, len(manifest.Files))
	for _, file := range manifest.Files {
		if err := validateArchivePath(file.Path); err != nil || file.Path == manifestName || file.Size < 0 || file.Size > maxBackupBytes || len(file.SHA256) != sha256.Size*2 {
			return errInvalidBackup
		}
		if _, exists := expected[file.Path]; exists {
			return errInvalidBackup
		}
		if _, err := hex.DecodeString(file.SHA256); err != nil {
			return errInvalidBackup
		}
		expected[file.Path] = file
	}
	seen := make(map[string]bool, len(expected))
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("backup: read archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA || header.Size < 0 {
			return errInvalidBackup
		}
		name := header.Name
		if err := validateArchivePath(name); err != nil || name == manifestName || seen[name] {
			return errInvalidBackup
		}
		entry, ok := expected[name]
		if !ok || entry.Size != header.Size {
			return errInvalidBackup
		}
		total += header.Size
		if total > maxBackupBytes {
			return errors.New("backup: expanded archive exceeds the size limit")
		}
		fullPath := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(fullPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, copyErr := copyContextN(ctx, io.MultiWriter(out, h), tr, header.Size)
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil {
			return errors.Join(copyErr, closeErr)
		}
		if n != header.Size || hex.EncodeToString(h.Sum(nil)) != entry.SHA256 {
			return fmt.Errorf("backup: checksum mismatch for %q", name)
		}
		mode := os.FileMode(0o600)
		if header.Mode&0o111 != 0 {
			mode |= 0o100
		}
		if err := os.Chmod(fullPath, mode); err != nil {
			return err
		}
		seen[name] = true
	}
	if len(seen) != len(expected) {
		return errInvalidBackup
	}
	if err := drainZeroPadding(limitedPlaintext); err != nil {
		return fmt.Errorf("backup: finish archive: %w", err)
	}
	if limitedPlaintext.N == 0 || limitedCiphertext.N == 0 {
		return errors.New("backup: archive exceeds the size limit")
	}
	return nil
}

func drainZeroPadding(reader io.Reader) error {
	buf := make([]byte, 32*1024)
	for {
		n, err := reader.Read(buf)
		for _, b := range buf[:n] {
			if b != 0 {
				return errInvalidBackup
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func decodeManifest(data []byte) (backupManifest, error) {
	var manifest backupManifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return backupManifest{}, errInvalidBackup
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return backupManifest{}, errInvalidBackup
	}
	if manifest.FormatVersion != backupFormatVersion || len(manifest.Files) == 0 || len(manifest.Files) > maxBackupFiles {
		return backupManifest{}, errInvalidBackup
	}
	if _, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt); err != nil {
		return backupManifest{}, errInvalidBackup
	}
	return manifest, nil
}

func validateArchivePath(name string) error {
	if name == "" || len(name) > 4096 || strings.ContainsAny(name, "\\:\x00") || path.IsAbs(name) || path.Clean(name) != name {
		return errInvalidBackup
	}
	parts := strings.Split(name, "/")
	if len(parts) > 32 {
		return errInvalidBackup
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || len(part) > 255 {
			return errInvalidBackup
		}
	}
	return nil
}

func copyContextN(ctx context.Context, dst io.Writer, src io.Reader, limit int64) (int64, error) {
	return copyContext(ctx, dst, io.LimitReader(src, limit))
}

func validateRestoredData(ctx context.Context, root string) error {
	key, err := os.ReadFile(filepath.Join(root, masterKeyName))
	if err != nil {
		return errors.New("backup: master key is missing")
	}
	if len(key) != vault.KeySize {
		return errors.New("backup: master key has an unsupported size")
	}
	if _, err := vault.New(key); err != nil {
		return errors.New("backup: master key is invalid")
	}
	dbPath := filepath.Join(root, databaseName)
	if info, err := os.Lstat(dbPath); err != nil || !info.Mode().IsRegular() {
		return errors.New("backup: database file is missing or invalid")
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("backup: validate restored database: %w", err)
	}
	var check string
	queryErr := st.R.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check)
	closeErr := st.Close()
	if queryErr != nil {
		return fmt.Errorf("backup: check restored database: %w", queryErr)
	}
	if closeErr != nil {
		return fmt.Errorf("backup: close restored database: %w", closeErr)
	}
	if check != "ok" {
		return errors.New("backup: restored database integrity check failed")
	}
	return nil
}
