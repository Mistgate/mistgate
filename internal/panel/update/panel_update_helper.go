package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"debug/elf"
	"github.com/mistgate/mistgate/internal/buildinfo"
)

// RunPanelUpdateHelper is the hidden command started by a transient systemd unit or the fixed root-owned helper
// service. It executes outside the panel service's ProtectSystem sandbox, replaces only its own executable,
// restarts the configured unit and restores the backup if the new service does not start.
func RunPanelUpdateHelper(args []string) error {
	if !canRunPanelUpdateHelper() {
		return ErrPanelUnsupported
	}
	fs := flag.NewFlagSet("panel-update-helper", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "panel data directory")
	service := fs.String("service", "", "systemd service unit")
	digest := fs.String("sha256", "", "expected lowercase SHA-256 of the staged binary")
	fetchLatest := fs.Bool("fetch-latest", false, "fetch and verify the official latest release before installing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *dataDir == "" || !unitPattern.MatchString(*service) {
		return errors.New("panel-update-helper: invalid arguments")
	}
	if *fetchLatest && *digest != "" {
		return errors.New("panel-update-helper: --fetch-latest does not accept a caller-supplied digest")
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find panel executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	systemctl := func(args ...string) error {
		return runCommandWithTimeout("systemctl", 90*time.Second, args...)
	}
	if *fetchLatest {
		return installLatestPanelUpdate(context.Background(), PanelUpdateConfig{
			CurrentVersion: buildinfo.Version, DataDir: *dataDir, ServiceUnit: *service, Executable: exe, Enabled: true,
		}, systemctl)
	}
	if !digestPattern.MatchString(*digest) {
		return errors.New("panel-update-helper: invalid arguments")
	}
	return applyPanelUpdate(*dataDir, exe, *service, *digest, runtimeArch(), systemctl)
}

// installLatestPanelUpdate is the fixed privileged path used when the HTTP panel runs as an unprivileged account.
// It obtains the release digest from GitHub itself, then downloads and verifies the official asset before handing it
// to the same replacement, backup, health-check, and rollback routine used by root-run installations.
func installLatestPanelUpdate(ctx context.Context, cfg PanelUpdateConfig, systemctl func(...string) error) error {
	if cfg.OS == "" {
		cfg.OS = runtime.GOOS
	}
	if cfg.OS != "linux" || cfg.DataDir == "" || !unitPattern.MatchString(strings.TrimSpace(cfg.ServiceUnit)) || systemctl == nil {
		return ErrPanelUnsupported
	}
	if cfg.CurrentVersion == "" {
		cfg.CurrentVersion = buildinfo.Version
	}
	if cfg.Executable == "" {
		cfg.Executable, _ = os.Executable()
	}
	if cfg.Arch == "" {
		cfg.Arch = runtime.GOARCH
	}
	cfg.Enabled = true
	updater := NewGitHubPanelUpdater(cfg)
	if !updater.supported {
		return ErrPanelUnsupported
	}
	cfg = updater.cfg
	checkCtx, cancelCheck := context.WithTimeout(ctx, panelUpdateCheckTimeout)
	status, asset, err := updater.fetchStatus(checkCtx)
	cancelCheck()
	if err != nil {
		return fmt.Errorf("panel-update-helper: check latest release: %w", err)
	}
	if !status.Available {
		return ErrNoPanelUpdate
	}
	downloadCtx, cancelDownload := context.WithTimeout(ctx, 2*time.Minute)
	_, err = updater.downloadAndStage(downloadCtx, asset, status.Version)
	cancelDownload()
	if err != nil {
		return fmt.Errorf("panel-update-helper: download latest release: %w", err)
	}
	return applyPanelUpdate(cfg.DataDir, cfg.Executable, cfg.ServiceUnit, strings.TrimPrefix(asset.Digest, "sha256:"), cfg.Arch, systemctl)
}

func runtimeArch() string { return panelUpdateRuntimeArch() }

func applyPanelUpdate(dataDir, target, service, expectedSHA, arch string, systemctl func(...string) error) error {
	if !unitPattern.MatchString(service) || !digestPattern.MatchString(expectedSHA) || systemctl == nil {
		return errors.New("panel-update-helper: invalid update parameters")
	}
	dataDir, err := filepath.Abs(dataDir)
	if err != nil || dataDir == "." || filepath.Clean(dataDir) == string(filepath.Separator) {
		return errors.New("panel-update-helper: invalid data directory")
	}
	target, err = filepath.Abs(target)
	if err != nil || target == "." || !filepath.IsAbs(target) {
		return errors.New("panel-update-helper: invalid executable path")
	}
	stage := filepath.Join(dataDir, "panel-update.new")
	stageInfo, err := os.Lstat(stage)
	if err != nil {
		return fmt.Errorf("panel-update-helper: staged binary: %w", err)
	}
	if !stageInfo.Mode().IsRegular() || stageInfo.Size() <= 0 || stageInfo.Size() > panelUpdateMaxSize {
		return errors.New("panel-update-helper: staged binary is not a regular file of an allowed size")
	}
	if err := verifyFileSHA256(stage, expectedSHA); err != nil {
		return fmt.Errorf("panel-update-helper: %w", err)
	}
	if err := verifyPanelELF(stage, arch); err != nil {
		return fmt.Errorf("panel-update-helper: %w", err)
	}

	oldInfo, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("panel-update-helper: installed binary: %w", err)
	}
	if !oldInfo.Mode().IsRegular() || (runtime.GOOS != "windows" && oldInfo.Mode().Perm()&0o111 == 0) {
		return errors.New("panel-update-helper: installed executable is not a regular executable file")
	}
	previous := target + ".prev"
	if prevInfo, err := os.Lstat(previous); err == nil {
		if !prevInfo.Mode().IsRegular() {
			return errors.New("panel-update-helper: previous binary path is not a regular file")
		}
		if err := os.Remove(previous); err != nil {
			return fmt.Errorf("panel-update-helper: remove old previous binary: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("panel-update-helper: inspect previous binary: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".mistgate-update-*")
	if err != nil {
		return fmt.Errorf("panel-update-helper: prepare replacement: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := copyVerifiedPanelBinary(tmp, stage, expectedSHA, oldInfo.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("panel-update-helper: close replacement: %w", err)
	}
	if err := systemctl("stop", service); err != nil {
		return fmt.Errorf("panel-update-helper: stop panel before backup: %w", err)
	}
	backup, err := backupDataDirectory(dataDir)
	if err != nil {
		startErr := systemctl("start", service)
		return errors.Join(fmt.Errorf("panel-update-helper: back up panel data: %w", err), startErr)
	}
	if err := os.Rename(target, previous); err != nil {
		startErr := systemctl("start", service)
		_ = os.Remove(backup)
		return errors.Join(fmt.Errorf("panel-update-helper: keep previous binary: %w", err), startErr)
	}
	if err := os.Rename(tmpName, target); err != nil {
		restoreErr := os.Rename(previous, target)
		var startErr error
		if restoreErr == nil {
			startErr = systemctl("start", service)
		} else {
			startErr = promotePanelBackup(dataDir, backup)
		}
		return errors.Join(fmt.Errorf("panel-update-helper: replace binary: %w", err), restoreErr, startErr)
	}
	syncDirectory(filepath.Dir(target))

	if err := startPanelService(service, systemctl); err != nil {
		if stopErr := systemctl("stop", service); stopErr != nil {
			backupErr := promotePanelBackup(dataDir, backup)
			syncDirectory(filepath.Dir(target))
			return errors.Join(
				fmt.Errorf("panel update failed and rollback could not stop the panel: %w", err),
				fmt.Errorf("panel-update-helper: stop failed panel: %w", stopErr),
				backupErr,
			)
		}
		failed := target + ".failed"
		_ = os.Remove(failed)
		moveErr := os.Rename(target, failed)
		restoreErr := os.Rename(previous, target)
		var dataErr, backupErr, restartErr error
		if restoreErr == nil {
			dataErr = restoreDataDirectory(dataDir, backup)
		}
		if restoreErr == nil && dataErr == nil {
			backupErr = promotePanelBackup(dataDir, backup)
			restartErr = systemctl("start", service)
		} else {
			backupErr = promotePanelBackup(dataDir, backup)
		}
		syncDirectory(filepath.Dir(target))
		return errors.Join(fmt.Errorf("panel update failed and was rolled back: %w", err), moveErr, restoreErr, dataErr, backupErr, restartErr)
	}
	_ = os.Remove(stage)
	syncDirectory(dataDir)
	if err := promotePanelBackup(dataDir, backup); err != nil {
		fmt.Fprintf(os.Stderr, "panel update succeeded; data backup remains at %s: %v\n", backup, err)
	}
	return nil
}

func promotePanelBackup(dataDir, backup string) error {
	backupPath := dataDir + ".panel-update-backup.tar.gz"
	if err := os.Remove(backupPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(backup, backupPath); err != nil {
		return err
	}
	syncDirectory(filepath.Dir(backupPath))
	return nil
}

func copyVerifiedPanelBinary(dst *os.File, source, expectedSHA string, mode os.FileMode) error {
	if err := dst.Chmod(mode); err != nil {
		return fmt.Errorf("panel-update-helper: preserve executable permissions: %w", err)
	}
	if _, err := dst.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := dst.Truncate(0); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("panel-update-helper: open staged binary: %w", err)
	}
	defer in.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(in, panelUpdateMaxSize+1))
	if err != nil {
		return fmt.Errorf("panel-update-helper: copy staged binary: %w", err)
	}
	if n <= 0 || n > panelUpdateMaxSize || hex.EncodeToString(h.Sum(nil)) != expectedSHA {
		return errors.New("panel-update-helper: copied binary failed SHA-256 verification")
	}
	if err := dst.Sync(); err != nil {
		return fmt.Errorf("panel-update-helper: sync replacement: %w", err)
	}
	return nil
}

func verifyFileSHA256(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, panelUpdateMaxSize+1))
	if err != nil {
		return err
	}
	if n <= 0 || n > panelUpdateMaxSize || hex.EncodeToString(h.Sum(nil)) != expected {
		return errors.New("staged binary SHA-256 mismatch")
	}
	return nil
}

func verifyPanelELF(path, arch string) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("panel release is not a Linux ELF executable: %w", err)
	}
	defer f.Close()
	want := elf.EM_NONE
	switch arch {
	case "amd64":
		want = elf.EM_X86_64
	case "arm64":
		want = elf.EM_AARCH64
	default:
		return fmt.Errorf("panel self-update does not support architecture %q", arch)
	}
	if f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || f.Machine != want {
		return fmt.Errorf("panel release architecture mismatch: got %s/%s/%s", f.Class, f.Data, f.Machine)
	}
	return nil
}

func startPanelService(service string, systemctl func(...string) error) error {
	if err := systemctl("start", service); err != nil {
		return err
	}
	// Type=simple units are considered started once execve succeeds. Check that the process remains active briefly
	// before deleting the rollback path.
	for i := 0; i < 3; i++ {
		if err := systemctl("is-active", "--quiet", service); err != nil {
			return fmt.Errorf("systemd did not keep %s active: %w", service, err)
		}
		if i < 2 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	return nil
}

func backupDataDirectory(dataDir string) (string, error) {
	info, err := os.Lstat(dataDir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("panel data path must be a real directory")
	}
	parent, base := filepath.Dir(dataDir), filepath.Base(dataDir)
	f, err := os.CreateTemp(parent, "."+base+"-panel-backup-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	failed := true
	defer func() {
		if failed {
			_ = os.Remove(path)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return "", err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	err = filepath.WalkDir(dataDir, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel := "."
		if current != dataDir {
			var err error
			rel, err = filepath.Rel(dataDir, current)
			if err != nil {
				return err
			}
		}
		if filepath.Clean(rel) == "panel-update.new" || filepath.Clean(rel) == "panel-update-backup.tar.gz" {
			return nil
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(current)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(current)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, in)
		closeErr := in.Close()
		return errors.Join(copyErr, closeErr)
	})
	closeTarErr := tw.Close()
	closeGzipErr := gz.Close()
	syncErr := f.Sync()
	closeFileErr := f.Close()
	if err := errors.Join(err, closeTarErr, closeGzipErr, syncErr, closeFileErr); err != nil {
		return "", err
	}
	failed = false
	return path, nil
}

func restoreDataDirectory(dataDir, backup string) error {
	restoreDir, err := os.MkdirTemp(filepath.Dir(dataDir), "."+filepath.Base(dataDir)+"-restore-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(restoreDir) }()
	if err := extractDataBackup(backup, restoreDir); err != nil {
		return err
	}
	failedDir := fmt.Sprintf("%s.failed-%d", dataDir, time.Now().UnixNano())
	if _, err := os.Lstat(failedDir); err == nil {
		return errors.New("panel-update-helper: rollback staging path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(dataDir, failedDir); err != nil {
		return err
	}
	if err := os.Rename(restoreDir, dataDir); err != nil {
		restoreErr := os.Rename(failedDir, dataDir)
		return errors.Join(err, restoreErr)
	}
	_ = os.RemoveAll(failedDir)
	syncDirectory(filepath.Dir(dataDir))
	return nil
}

func extractDataBackup(backup, target string) error {
	f, err := os.Open(backup)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var links []*tar.Header
	type dirMode struct {
		path string
		mode os.FileMode
		uid  int
		gid  int
	}
	var dirs []dirMode
	var rootDir *tar.Header
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		rel := filepath.FromSlash(header.Name)
		clean := filepath.Clean(rel)
		if filepath.IsAbs(rel) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe path in panel data backup: %q", header.Name)
		}
		if clean == "." {
			if header.Typeflag != tar.TypeDir {
				return errors.New("invalid panel data backup root entry")
			}
			rootDir = header
			continue
		}
		path := filepath.Join(target, clean)
		if header.Typeflag == tar.TypeSymlink {
			links = append(links, header)
			continue // create links after regular entries so they cannot redirect extraction
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o700); err != nil {
				return err
			}
			dirs = append(dirs, dirMode{path: path, mode: os.FileMode(header.Mode).Perm(), uid: header.Uid, gid: header.Gid})
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > 1<<40 {
				return errors.New("invalid file size in panel data backup")
			}
			out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode).Perm())
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(out, tr, header.Size)
			syncErr := out.Sync()
			closeErr := out.Close()
			if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
				return err
			}
			if runtime.GOOS != "windows" {
				if err := os.Chown(path, header.Uid, header.Gid); err != nil {
					return err
				}
			}
			if err := os.Chmod(path, os.FileMode(header.Mode).Perm()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported file type in panel data backup: %q", header.Name)
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if runtime.GOOS != "windows" {
			if err := os.Chown(dirs[i].path, dirs[i].uid, dirs[i].gid); err != nil {
				return err
			}
		}
		if err := os.Chmod(dirs[i].path, dirs[i].mode); err != nil {
			return err
		}
	}
	if rootDir != nil {
		if runtime.GOOS != "windows" {
			if err := os.Chown(target, rootDir.Uid, rootDir.Gid); err != nil {
				return err
			}
		}
		if err := os.Chmod(target, os.FileMode(rootDir.Mode).Perm()); err != nil {
			return err
		}
	}
	for _, header := range links {
		rel := filepath.FromSlash(header.Name)
		path := filepath.Join(target, filepath.Clean(rel))
		if err := os.Symlink(header.Linkname, path); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(path string) {
	dir, err := os.Open(path)
	if err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
}

func runCommandWithTimeout(name string, timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return runCommand(ctx, name, args...)
}
