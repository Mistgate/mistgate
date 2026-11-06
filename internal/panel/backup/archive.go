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
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	backupFormatVersion = 1
	manifestName        = "manifest.json"
	databaseName        = "mistgate.db"
	masterKeyName       = "master.key"
	maxManifestBytes    = 2 << 20
	maxBackupFiles      = 10000
	maxBackupBytes      = int64(2 << 30)
	maxEncryptedBytes   = int64(2 << 30)
)

var errArchiveTooLarge = errors.New("backup: encrypted archive exceeds the size limit")
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

const snapshotStagePrefix = stagePrefix + "snapshot-"

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
	return CreateEncryptedArchiveWithRecipient(ctx, dataDir, snapshotDB, masterKey, appVersion, recipient, created, dst)
}

// CreateEncryptedArchiveWithRecipient snapshots the panel's durable data into an age-encrypted, versioned tar.gz stream
// using a prepared age recipient. CreateEncryptedArchive is the recovery-recipient form used by scheduled backups.
func CreateEncryptedArchiveWithRecipient(ctx context.Context, dataDir, snapshotDB string, masterKey []byte, appVersion string, recipient age.Recipient, created time.Time, dst io.Writer) error {
	if len(masterKey) != vault.KeySize {
		return errors.New("backup: invalid master key")
	}
	if recipient == nil {
		return errors.New("backup: missing encryption recipient")
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
	return writeEncryptedTar(ctx, &encryptedArchiveLimitWriter{dst: dst, remaining: maxEncryptedBytes}, recipient, files, encodedManifest, created)
}

type encryptedArchiveLimitWriter struct {
	dst       io.Writer
	remaining int64
}

func (w *encryptedArchiveLimitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errArchiveTooLarge
	}
	n, err := w.dst.Write(p)
	w.remaining -= int64(n)
	return n, err
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
