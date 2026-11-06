//go:build !js

package backup

import (
	"archive/tar"
	"cmp"
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
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/update"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	maxPlaintextBytes  = maxBackupBytes + maxManifestBytes + int64(maxBackupFiles*8192)
	extractStagePrefix = update.PanelRestoreStagePrefix + "extract-"
)

// RestoreEncryptedArchive decrypts and validates a v1 backup into a new data directory.
// It refuses any existing target and never follows archive paths or archive links.
func RestoreEncryptedArchive(ctx context.Context, src io.Reader, identities []age.Identity, destination string) error {
	_, err := RestoreEncryptedArchiveWithInfo(ctx, src, identities, destination)
	return err
}

// ArchiveInfo is the public, non-secret summary carried by a verified archive manifest.
type ArchiveInfo struct {
	AppVersion string
	CreatedAt  time.Time
}

// RestoreEncryptedArchiveWithInfo is RestoreEncryptedArchive plus the verified manifest's public metadata.
func RestoreEncryptedArchiveWithInfo(ctx context.Context, src io.Reader, identities []age.Identity, destination string) (ArchiveInfo, error) {
	if len(identities) == 0 {
		return ArchiveInfo{}, errors.New("backup: no recovery identity was supplied")
	}
	target, err := filepath.Abs(destination)
	if err != nil {
		return ArchiveInfo{}, err
	}
	if filepath.Clean(target) != target || filepath.Base(target) == "." || filepath.Base(target) == string(filepath.Separator) {
		return ArchiveInfo{}, errors.New("backup: invalid restore destination")
	}
	if _, err := os.Lstat(target); err == nil {
		return ArchiveInfo{}, errors.New("backup: restore destination already exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ArchiveInfo{}, err
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return ArchiveInfo{}, err
	}
	stage, err := os.MkdirTemp(parent, extractStagePrefix)
	if err != nil {
		return ArchiveInfo{}, err
	}
	if err := os.Chmod(stage, 0o700); err != nil {
		os.RemoveAll(stage)
		return ArchiveInfo{}, err
	}
	keepStage := false
	defer func() {
		if !keepStage {
			_ = os.RemoveAll(stage)
		}
	}()
	var manifest backupManifest
	if err := extractEncryptedArchive(ctx, src, identities, stage, &manifest); err != nil {
		return ArchiveInfo{}, err
	}
	if err := validateRestoredData(ctx, stage, manifest.AppVersion); err != nil {
		return ArchiveInfo{}, err
	}
	if _, err := os.Lstat(target); err == nil {
		return ArchiveInfo{}, errors.New("backup: restore destination appeared during restore")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ArchiveInfo{}, err
	}
	if err := os.Rename(stage, target); err != nil {
		return ArchiveInfo{}, fmt.Errorf("backup: install restored data directory: %w", err)
	}
	keepStage = true
	createdAt, _ := time.Parse(time.RFC3339Nano, manifest.CreatedAt)
	return ArchiveInfo{AppVersion: manifest.AppVersion, CreatedAt: createdAt}, nil
}

// extractEncryptedArchive checks and unpacks an archive into stage; out gets its manifest.
func extractEncryptedArchive(ctx context.Context, src io.Reader, identities []age.Identity, stage string, out *backupManifest) error {
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
	*out = manifest
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

func copyContextN(ctx context.Context, dst io.Writer, src io.Reader, limit int64) (int64, error) {
	return copyContext(ctx, dst, io.LimitReader(src, limit))
}

// validateRestoredData checks the unpacked data before it becomes a data directory: a usable master key, and a database
// this binary can run (not newer than its migrations; opening it applies those it lacks) that passes an integrity check.
func validateRestoredData(ctx context.Context, root, appVersion string) error {
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
	if have, know, err := store.SchemaVersions(ctx, dbPath); err != nil {
		return fmt.Errorf("backup: read the restored database version: %w", err)
	} else if have > know {
		return fmt.Errorf("backup: the archive comes from a newer Mistgate (%s): its database schema is %d, this binary (%s) knows %d at most; restore it with that version or a newer one",
			cmp.Or(appVersion, "version unknown"), have, buildinfo.Version, know)
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("backup: validate restored database: %w", err)
	}
	sealedErr := validateSealedDatabaseValues(ctx, st.R, key)
	var check string
	queryErr := st.R.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check)
	closeErr := st.Close()
	if sealedErr != nil {
		return sealedErr
	}
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
