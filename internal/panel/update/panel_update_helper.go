//go:build !js

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
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/buildinfo"
)

// systemctlFunc runs systemctl with the arguments and returns its trimmed standard output.
type systemctlFunc func(args ...string) (string, error)

// Health check of a replaced panel (test seams): it must stay active, without systemd restarting it, this long.
var (
	panelHealthWindow = 45 * time.Second
	panelHealthPoll   = time.Second
)

// RunPanelUpdateHelper is the hidden command started by a transient systemd unit or the fixed root-owned helper
// service. It executes outside the panel service's ProtectSystem sandbox, fetches and verifies the release the owner
// confirmed, replaces only its own executable, restarts the configured unit and restores the backup if the new
// service does not stay healthy.
func RunPanelUpdateHelper(args []string) error {
	if !canRunPanelUpdateHelper() {
		return ErrPanelUnsupported
	}
	fs := flag.NewFlagSet("panel-update-helper", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "", "panel data directory")
	service := fs.String("service", "", "systemd service unit")
	fetchLatest := fs.Bool("fetch-latest", false, "fetch and verify the confirmed signed release before installing")
	restore := fs.Bool("restore", false, "swap in the confirmed, verified restore directory")
	deleteKept := fs.Bool("delete-kept", false, "delete the confirmed kept data directory")
	auto := fs.Bool("auto", false, "select the mode from the single confirmed request file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *auto {
		updateExists := regularRequestExists(filepath.Join(*dataDir, panelUpdateRequestName))
		restoreExists := regularRequestExists(filepath.Join(*dataDir, panelRestoreRequestName))
		deleteExists := regularRequestExists(filepath.Join(*dataDir, panelKeptDeleteRequestName))
		if countTrue(updateExists, restoreExists, deleteExists) != 1 {
			return errors.New("panel-update-helper: expected exactly one confirmed request")
		}
		*fetchLatest, *restore, *deleteKept = updateExists, restoreExists, deleteExists
	}
	if fs.NArg() != 0 || *dataDir == "" || !unitPattern.MatchString(*service) || countTrue(*fetchLatest, *restore, *deleteKept) != 1 {
		return errors.New("panel-update-helper: invalid arguments")
	}
	absDataDir, err := filepath.Abs(*dataDir)
	if err != nil || filepath.Clean(absDataDir) == string(filepath.Separator) {
		return errors.New("panel-update-helper: invalid data directory")
	}
	systemctl := func(args ...string) (string, error) {
		return runCommandWithTimeout("systemctl", 90*time.Second, args...)
	}
	kind := "update"
	var restoreMetadata *PanelRestoreRequest
	if *restore {
		kind = "restore"
		if request, err := ReadPanelRestoreRequest(absDataDir); err == nil {
			restoreMetadata = &request
		}
	} else if *deleteKept {
		kind = "delete-kept"
	}
	lock, err := acquirePanelOperationLock(absDataDir, kind, restoreMetadata, time.Now())
	if err != nil {
		return fmt.Errorf("panel-update-helper: %w", err)
	}
	defer lock.release()
	if err := recoverPanelRestoreJournal(absDataDir, *service, systemctl); err != nil {
		return err
	}
	if *restore {
		request, err := takePanelRestoreRequest(absDataDir)
		if err != nil {
			return fmt.Errorf("panel-update-helper: %w", err)
		}
		lock.state.RestoreRequest = &request
		lock.state.HeartbeatUnix = time.Now().UTC().Unix()
		_ = writePanelOperationState(absDataDir, lock.state)
		return applyPanelRestore(absDataDir, *service, request, systemctl)
	}
	if *deleteKept {
		request, err := takePanelKeptDeleteRequest(absDataDir)
		if err != nil {
			return fmt.Errorf("panel-update-helper: %w", err)
		}
		return applyPanelKeptDataDelete(absDataDir, request)
	}
	// Only the key compiled into this root-owned binary: the data directory belongs to the panel's service user.
	key, err := buildinfo.ReleasePublicKey()
	if err != nil {
		return fmt.Errorf("panel-update-helper: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find panel executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return installLatestPanelUpdate(context.Background(), PanelUpdateConfig{
		CurrentBuilt: buildinfo.BuiltUnix(), Key: key, DataDir: absDataDir, ServiceUnit: *service, Executable: exe, Enabled: true,
	}, systemctl)
}

func countTrue(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func regularRequestExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// applyPanelRestore stops the panel before it inspects, writes or changes the staged tree. The previous directory stays
// beside the live one after success; a journal lets the next helper run finish or undo an interrupted swap.
func applyPanelRestore(dataDir, service string, request PanelRestoreRequest, systemctl systemctlFunc) error {
	if dataDir == "" || !unitPattern.MatchString(strings.TrimSpace(service)) || systemctl == nil || !validPanelRestoreRequest(request) {
		return ErrPanelUnsupported
	}
	absoluteData, err := filepath.Abs(dataDir)
	if err != nil || filepath.Clean(absoluteData) == string(filepath.Separator) || filepath.Base(absoluteData) == "." {
		return errors.New("panel-update-helper: invalid data directory")
	}
	dataInfo, err := os.Lstat(absoluteData)
	if err != nil || !dataInfo.IsDir() || dataInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("panel-update-helper: panel data path must be a real directory")
	}
	runSystemctl := func(args ...string) error {
		_, err := systemctl(args...)
		return err
	}
	recordFailure := func(cause error) error {
		result := PanelRestoreResult{Outcome: "failed", Source: request.Source, FileName: request.FileName,
			MadeByVersion: request.MadeByVersion, CompletedUnix: time.Now().UTC().Unix()}
		if err := writePanelRestoreResultOwned(absoluteData, result, dataInfo); err != nil {
			return errors.Join(cause, fmt.Errorf("panel-update-helper: record failed restore: %w", err))
		}
		return cause
	}
	if err := runSystemctl("stop", service); err != nil {
		return recordFailure(fmt.Errorf("panel-update-helper: stop panel before restore: %w", err))
	}
	restartPanel := func(cause error) error {
		return errors.Join(cause, runSystemctl("start", service))
	}
	stagePath := filepath.Join(absoluteData, request.StageDir)
	stageInfo, err := os.Lstat(stagePath)
	if err != nil || !stageInfo.IsDir() || stageInfo.Mode()&os.ModeSymlink != 0 {
		return restartPanel(recordFailure(errors.New("panel-update-helper: staged restore path must be a real directory inside the panel data directory")))
	}
	stageOutside := absoluteData + ".restore-stage-" + request.StageDir
	if _, err := os.Lstat(stageOutside); err == nil {
		return restartPanel(recordFailure(errors.New("panel-update-helper: restore staging path already exists")))
	} else if !errors.Is(err, os.ErrNotExist) {
		return restartPanel(recordFailure(err))
	}
	kept, err := newKeptRestorePath(absoluteData, time.Now().UTC())
	if err != nil {
		return restartPanel(recordFailure(err))
	}
	if _, err := os.Lstat(kept); err == nil {
		return restartPanel(recordFailure(errors.New("panel-update-helper: kept restore path already exists")))
	} else if !errors.Is(err, os.ErrNotExist) {
		return restartPanel(recordFailure(err))
	}
	if err := normalizeRestoreOwnership(stagePath, dataInfo); err != nil {
		_ = os.RemoveAll(stagePath)
		return restartPanel(recordFailure(fmt.Errorf("panel-update-helper: prepare restored data ownership: %w", err)))
	}
	markerName := ".mistgate-restore-marker-" + strings.TrimPrefix(request.StageDir, PanelRestoreStagePrefix)
	if err := createRestoreStageMarker(stagePath, markerName, request.StageDir); err != nil {
		_ = os.RemoveAll(stagePath)
		return restartPanel(recordFailure(fmt.Errorf("panel-update-helper: mark staged restore: %w", err)))
	}
	journal := panelRestoreJournal{Version: restoreJournalVersion, Phase: "prepared", DataDir: absoluteData,
		StageName: request.StageDir, StagePath: stagePath, StageOutside: stageOutside, KeptPath: kept,
		MarkerName: markerName, Request: request}
	if _, err := os.Lstat(restoreJournalPath(absoluteData)); err == nil {
		_ = os.RemoveAll(stagePath)
		return restartPanel(recordFailure(errors.New("panel-update-helper: an interrupted restore journal needs recovery")))
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = os.RemoveAll(stagePath)
		return restartPanel(recordFailure(err))
	}
	if err := swapRestoreDirectories(&journal); err != nil {
		if errors.Is(err, errRestoreJournalInterruption) {
			return err // The test seam models a process death: leave the durable journal and filesystem exactly as found.
		}
		rbErr := rollbackRestoreJournal(&journal)
		if rbErr != nil {
			return errors.Join(fmt.Errorf("panel-update-helper: swap restore data: %w", err), rbErr)
		}
		failed := recordFailure(fmt.Errorf("panel-update-helper: swap restore data: %w", err))
		journalErr := removeRestoreJournal(absoluteData)
		return restartPanel(errors.Join(failed, journalErr))
	}
	if err := removeRestoreStageMarker(absoluteData, markerName); err != nil {
		err = errors.Join(fmt.Errorf("panel-update-helper: remove restore stage marker: %w", err), rollbackRestoreJournal(&journal))
		return restartPanel(recordFailure(err))
	}
	if err := startPanelService(service, systemctl); err != nil {
		if stopErr := runSystemctl("stop", service); stopErr != nil {
			return errors.Join(fmt.Errorf("panel restore failed and rollback could not stop the panel: %w", err), stopErr)
		}
		if rbErr := rollbackRestoreJournal(&journal); rbErr != nil {
			return errors.Join(fmt.Errorf("panel restore failed and rollback could not restore the previous directory: %w", err), rbErr)
		}
		failed := recordFailure(fmt.Errorf("panel restore failed and was rolled back: %w", err))
		journalErr := removeRestoreJournal(absoluteData)
		return restartPanel(errors.Join(failed, journalErr))
	}
	marker := PanelRestoreResult{Outcome: "restored", Source: request.Source, FileName: request.FileName,
		MadeByVersion: request.MadeByVersion, CompletedUnix: time.Now().UTC().Unix()}
	if err := writePanelRestoreResultOwned(absoluteData, marker, dataInfo); err != nil {
		_ = runSystemctl("stop", service)
		if rbErr := rollbackRestoreJournal(&journal); rbErr != nil {
			return errors.Join(fmt.Errorf("panel-update-helper: write restore result: %w", err), rbErr)
		}
		failed := recordFailure(fmt.Errorf("panel-update-helper: write restore result: %w", err))
		journalErr := removeRestoreJournal(absoluteData)
		return restartPanel(errors.Join(failed, journalErr))
	}
	if err := commitRestoreJournal(&journal); err != nil {
		if errors.Is(err, errRestoreJournalInterruption) {
			return err
		}
		_ = runSystemctl("stop", service)
		if rbErr := rollbackRestoreJournal(&journal); rbErr != nil {
			return errors.Join(fmt.Errorf("panel-update-helper: commit restore: %w", err), rbErr)
		}
		failed := recordFailure(fmt.Errorf("panel-update-helper: commit restore: %w", err))
		journalErr := removeRestoreJournal(absoluteData)
		return restartPanel(errors.Join(failed, journalErr))
	}
	if err := removeRestoreJournal(absoluteData); err != nil {
		return fmt.Errorf("panel-update-helper: finalize restore journal: %w", err)
	}
	return nil
}

func normalizeRestoreOwnership(root string, current os.FileInfo) error {
	uid, gid, ok := panelDataOwner(current)
	if !ok || runtime.GOOS == "windows" {
		return nil
	}
	return filepath.WalkDir(root, func(path string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return lchownPath(path, uid, gid)
	})
}

func newKeptRestorePath(dataDir string, now time.Time) (string, error) {
	for offset := 0; offset < 120; offset++ {
		path := dataDir + ".before-restore-" + now.Add(time.Duration(offset)*time.Second).UTC().Format("20060102-150405")
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("panel-update-helper: no unused kept restore name is available")
}

func applyPanelKeptDataDelete(dataDir string, request PanelKeptDeleteRequest) error {
	absoluteData, err := filepath.Abs(dataDir)
	if err != nil || filepath.Clean(absoluteData) == string(filepath.Separator) || filepath.Base(absoluteData) == "." ||
		!validKeptDeleteName(absoluteData, request.Name) {
		return errors.New("panel-update-helper: invalid kept-data deletion request")
	}
	owner, err := os.Lstat(absoluteData)
	if err != nil || !owner.IsDir() || owner.Mode()&os.ModeSymlink != 0 {
		return errors.New("panel-update-helper: panel data path must be a real directory")
	}
	kept := filepath.Join(filepath.Dir(absoluteData), request.Name)
	result := PanelKeptDeleteResult{Outcome: "failed", Name: request.Name, CompletedUnix: time.Now().UTC().Unix()}
	finishFailure := func(cause error) error {
		if writeErr := writePanelKeptDeleteResult(absoluteData, result, owner); writeErr != nil {
			return errors.Join(cause, fmt.Errorf("panel-update-helper: record kept-data deletion result: %w", writeErr))
		}
		return cause
	}
	info, err := os.Lstat(kept)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return finishFailure(errors.New("panel-update-helper: kept data path is not a real directory"))
	}
	if err := rejectSymlinksInTree(kept); err != nil {
		return finishFailure(fmt.Errorf("panel-update-helper: kept data contains a symlink: %w", err))
	}
	if err := os.RemoveAll(kept); err != nil {
		return finishFailure(fmt.Errorf("panel-update-helper: remove kept data: %w", err))
	}
	syncDirectory(filepath.Dir(kept))
	result.Outcome = "deleted"
	if err := writePanelKeptDeleteResult(absoluteData, result, owner); err != nil {
		return fmt.Errorf("panel-update-helper: record kept-data deletion result: %w", err)
	}
	return nil
}

func rejectSymlinksInTree(root string) error {
	return filepath.WalkDir(root, func(path string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink at %s", filepath.Base(path))
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular file at %s", filepath.Base(path))
		}
		return nil
	})
}

func panelDataOwner(info os.FileInfo) (int, int, bool) {
	sys := info.Sys()
	if sys == nil {
		return 0, 0, false
	}
	value := reflect.ValueOf(sys)
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return 0, 0, false
	}
	uid, gid := value.FieldByName("Uid"), value.FieldByName("Gid")
	if !uid.IsValid() || !gid.IsValid() || !uid.CanUint() || !gid.CanUint() {
		return 0, 0, false
	}
	return int(uid.Uint()), int(gid.Uint()), true
}

// installLatestPanelUpdate is the privileged path. It takes the version and digest the owner confirmed from the
// request file, verifies the release's signed panel manifest itself, requires it to name exactly that release and to
// be newer than this build, then downloads and checks the binary before handing it to the replacement, backup,
// health-check and rollback routine.
func installLatestPanelUpdate(ctx context.Context, cfg PanelUpdateConfig, systemctl systemctlFunc) error {
	if cfg.OS == "" {
		cfg.OS = runtime.GOOS
	}
	if cfg.OS != "linux" || cfg.DataDir == "" || !unitPattern.MatchString(strings.TrimSpace(cfg.ServiceUnit)) || systemctl == nil {
		return ErrPanelUnsupported
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
	want, err := takePanelUpdateRequest(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("panel-update-helper: %w", err)
	}
	checkCtx, cancelCheck := context.WithTimeout(ctx, panelUpdateCheckTimeout)
	status, rel, err := updater.fetchStatus(checkCtx)
	cancelCheck()
	if err != nil {
		return fmt.Errorf("panel-update-helper: check latest release: %w", err)
	}
	if !status.Available {
		return ErrNoPanelUpdate
	}
	if status.Version != want.Version || status.SHA256 != want.SHA256 {
		return fmt.Errorf("panel-update-helper: %w", ErrPanelChanged)
	}
	downloadCtx, cancelDownload := context.WithTimeout(ctx, 2*time.Minute)
	_, err = updater.downloadAndStage(downloadCtx, rel)
	cancelDownload()
	if err != nil {
		return fmt.Errorf("panel-update-helper: download latest release: %w", err)
	}
	return applyPanelUpdate(cfg.DataDir, cfg.Executable, cfg.ServiceUnit, rel.file.SHA256, cfg.Arch, systemctl)
}

func applyPanelUpdate(dataDir, target, service, expectedSHA, arch string, systemctlOut systemctlFunc) error {
	if !unitPattern.MatchString(service) || !digestPattern.MatchString(expectedSHA) || systemctlOut == nil {
		return errors.New("panel-update-helper: invalid update parameters")
	}
	systemctl := func(args ...string) error {
		_, err := systemctlOut(args...)
		return err
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

	if err := startPanelService(service, systemctlOut); err != nil {
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

// startPanelService starts the replaced panel and waits for it to prove itself before the rollback path is deleted. A
// Type=simple unit counts as started once execve succeeds, and with Restart=on-failure a crash a few seconds in shows
// only as a restart: the unit must stay active for panelHealthWindow with systemd's restart counter unchanged (an
// explicit start resets it).
func startPanelService(service string, systemctl systemctlFunc) error {
	if _, err := systemctl("start", service); err != nil {
		return err
	}
	restarts, err := systemctl("show", "--property=NRestarts", "--value", service)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(panelHealthWindow)
	for {
		if _, err := systemctl("is-active", "--quiet", service); err != nil {
			return fmt.Errorf("systemd did not keep %s active: %w", service, err)
		}
		now, err := systemctl("show", "--property=NRestarts", "--value", service)
		if err != nil {
			return err
		}
		if now != restarts {
			return fmt.Errorf("systemd restarted %s after the update (restarts %s, then %s)", service, restarts, now)
		}
		if !time.Now().Before(deadline) {
			return nil
		}
		time.Sleep(panelHealthPoll)
	}
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

func runCommandWithTimeout(name string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := runCommand(ctx, name, args...)
	return strings.TrimSpace(string(out)), err
}
