//go:build !js

package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	PanelRestoreResultFileName    = "panel-restore.result"
	PanelKeptDeleteResultFileName = "panel-kept-delete.result"
	PanelRestoreStagePrefix       = ".mistgate-backup-restore-"
)

// PanelKeptDeleteRequest names exactly one prior data directory for the root helper to remove.
type PanelKeptDeleteRequest struct {
	Name string `json:"name"`
}

type PanelKeptDeleteResult struct {
	Outcome       string `json:"outcome"`
	Name          string `json:"name"`
	CompletedUnix int64  `json:"completed_unix"`
}

// PanelRestoreResult is the helper's durable, non-secret result marker in the active data directory.
type PanelRestoreResult struct {
	Outcome       string `json:"outcome"`
	Source        string `json:"source"`
	FileName      string `json:"file_name"`
	MadeByVersion string `json:"made_by_version"`
	CompletedUnix int64  `json:"completed_unix"`
}

// ReadPanelRestoreResult reads the durable helper result after the panel has restarted.
func ReadPanelRestoreResult(dataDir string) (PanelRestoreResult, error) {
	path := filepath.Join(dataDir, PanelRestoreResultFileName)
	info, err := os.Lstat(path)
	if err != nil {
		return PanelRestoreResult{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<10 {
		return PanelRestoreResult{}, errors.New("panel restore result is not a small regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return PanelRestoreResult{}, err
	}
	defer f.Close()
	if opened, err := f.Stat(); err != nil || !os.SameFile(info, opened) {
		return PanelRestoreResult{}, errors.New("panel restore result changed while it was read")
	}
	var result PanelRestoreResult
	decoder := json.NewDecoder(io.LimitReader(f, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || (result.Outcome != "restored" && result.Outcome != "failed") || result.CompletedUnix <= 0 {
		return PanelRestoreResult{}, errors.New("panel restore result is invalid")
	}
	return result, nil
}

func writePanelRestoreRequest(dataDir string, request PanelRestoreRequest) error {
	if request.RequestedUnix == 0 {
		request.RequestedUnix = time.Now().UTC().Unix()
	}
	if !validPanelRestoreRequest(request) {
		return errors.New("panel restore request is invalid")
	}
	return writeSmallJSON(dataDir, panelRestoreRequestName, ".panel-restore-request-*", request)
}

func takePanelRestoreRequest(dataDir string) (PanelRestoreRequest, error) {
	path := filepath.Join(dataDir, panelRestoreRequestName)
	info, err := os.Lstat(path)
	if err != nil {
		return PanelRestoreRequest{}, fmt.Errorf("no confirmed panel restore request: %w", err)
	}
	defer os.Remove(path)
	if !info.Mode().IsRegular() || info.Size() > 4<<10 {
		return PanelRestoreRequest{}, errors.New("the panel restore request is not a small regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return PanelRestoreRequest{}, err
	}
	defer f.Close()
	if opened, err := f.Stat(); err != nil || !os.SameFile(info, opened) {
		return PanelRestoreRequest{}, errors.New("the panel restore request changed while it was read")
	}
	var request PanelRestoreRequest
	decoder := json.NewDecoder(io.LimitReader(f, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || !validPanelRestoreRequest(request) {
		return PanelRestoreRequest{}, errors.New("the panel restore request is invalid")
	}
	return request, nil
}

// ReadPanelRestoreRequest reconstructs an accepted restore after the panel process restarts.
func ReadPanelRestoreRequest(dataDir string) (PanelRestoreRequest, error) {
	var request PanelRestoreRequest
	path := filepath.Join(dataDir, panelRestoreRequestName)
	info, err := validateHelperRequestFile(path, &request)
	if err != nil {
		return PanelRestoreRequest{}, err
	}
	if !validPanelRestoreRequest(request) {
		return PanelRestoreRequest{}, errors.New("the panel restore request is invalid")
	}
	if request.RequestedUnix == 0 {
		request.RequestedUnix = info.ModTime().UTC().Unix()
	}
	return request, nil
}

func validPanelRestoreRequest(request PanelRestoreRequest) bool {
	return validPanelRestoreStageDir(request.StageDir) &&
		validPanelRestoreMetadata(request.Source, request.FileName, request.MadeByVersion)
}

func validPanelRestoreMetadata(source, fileName, version string) bool {
	return (source == "upload" || source == "r2") && validRestoreText(fileName, 255, false) &&
		filepath.Base(fileName) == fileName && !strings.ContainsAny(fileName, "/\\") && validRestoreText(version, 128, true)
}

func validPanelRestoreStageDir(name string) bool {
	if len(name) != len(PanelRestoreStagePrefix)+32 || !strings.HasPrefix(name, PanelRestoreStagePrefix) || filepath.Base(name) != name {
		return false
	}
	for _, r := range strings.TrimPrefix(name, PanelRestoreStagePrefix) {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

func validRestoreText(value string, max int, allowEmpty bool) bool {
	if len(value) > max || (!allowEmpty && strings.TrimSpace(value) == "") {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func writePanelRestoreResult(dataDir string, result PanelRestoreResult) error {
	if (result.Outcome != "restored" && result.Outcome != "failed") || result.CompletedUnix <= 0 ||
		!validPanelRestoreMetadata(result.Source, result.FileName, result.MadeByVersion) {
		return errors.New("panel restore result is invalid")
	}
	return writeSmallJSON(dataDir, PanelRestoreResultFileName, ".panel-restore-result-*", result)
}

// WritePanelRestoreResult records a helper result when a scheduled restore helper exits without starting.
func WritePanelRestoreResult(dataDir string, result PanelRestoreResult) error {
	return writePanelRestoreResult(dataDir, result)
}

// RemovePanelRestoreRequest removes a stale helper request after the updater confirms that no helper is running.
func RemovePanelRestoreRequest(dataDir string) error {
	path := filepath.Join(dataDir, panelRestoreRequestName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("panel restore request is not a regular file")
	}
	return os.Remove(path)
}

func writePanelRestoreResultOwned(dataDir string, result PanelRestoreResult, owner os.FileInfo) error {
	if !validPanelRestoreResult(result) {
		return errors.New("panel restore result is invalid")
	}
	uid, gid, ok := panelDataOwner(owner)
	if !ok || runtime.GOOS == "windows" {
		return writeSmallJSON(dataDir, PanelRestoreResultFileName, ".panel-restore-result-*", result)
	}
	return writeSmallJSONOwned(dataDir, PanelRestoreResultFileName, ".panel-restore-result-*", result, uid, gid)
}

func validPanelRestoreResult(result PanelRestoreResult) bool {
	return (result.Outcome == "restored" || result.Outcome == "failed") && result.CompletedUnix > 0 &&
		validPanelRestoreMetadata(result.Source, result.FileName, result.MadeByVersion)
}

func writePanelKeptDeleteRequest(dataDir string, request PanelKeptDeleteRequest) error {
	if !validKeptDeleteName(dataDir, request.Name) {
		return errors.New("panel kept-data deletion request is invalid")
	}
	return writeSmallJSON(dataDir, panelKeptDeleteRequestName, ".panel-kept-delete-request-*", request)
}

func takePanelKeptDeleteRequest(dataDir string) (PanelKeptDeleteRequest, error) {
	path := filepath.Join(dataDir, panelKeptDeleteRequestName)
	var request PanelKeptDeleteRequest
	_, err := validateHelperRequestFile(path, &request)
	if err != nil {
		return PanelKeptDeleteRequest{}, err
	}
	if !validKeptDeleteName(dataDir, request.Name) {
		return PanelKeptDeleteRequest{}, errors.New("panel kept-data deletion request is invalid")
	}
	if err := os.Remove(path); err != nil {
		return PanelKeptDeleteRequest{}, err
	}
	return request, nil
}

func writePanelKeptDeleteResult(dataDir string, result PanelKeptDeleteResult, owner os.FileInfo) error {
	if (result.Outcome != "deleted" && result.Outcome != "failed") || result.CompletedUnix <= 0 || !validKeptDeleteName(dataDir, result.Name) {
		return errors.New("panel kept-data deletion result is invalid")
	}
	uid, gid, ok := panelDataOwner(owner)
	if !ok || runtime.GOOS == "windows" {
		return writeSmallJSON(dataDir, PanelKeptDeleteResultFileName, ".panel-kept-delete-result-*", result)
	}
	return writeSmallJSONOwned(dataDir, PanelKeptDeleteResultFileName, ".panel-kept-delete-result-*", result, uid, gid)
}

func ReadPanelKeptDeleteResult(dataDir string) (PanelKeptDeleteResult, error) {
	var result PanelKeptDeleteResult
	_, err := validateHelperRequestFile(filepath.Join(dataDir, PanelKeptDeleteResultFileName), &result)
	if err != nil {
		return PanelKeptDeleteResult{}, err
	}
	if (result.Outcome != "deleted" && result.Outcome != "failed") || result.CompletedUnix <= 0 || !validKeptDeleteName(dataDir, result.Name) {
		return PanelKeptDeleteResult{}, errors.New("panel kept-data deletion result is invalid")
	}
	return result, nil
}

func writeSmallJSONOwned(dir, name, pattern string, value any, uid, gid int) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := chownOpenFile(tmp, uid, gid); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		return err
	}
	syncDirectory(dir)
	return nil
}

// Restore asks the same privileged helper path as a panel update to swap in a verified restore stage.
func (u *GitHubPanelUpdater) Restore(ctx context.Context, request PanelRestoreRequest) error {
	if !u.supported {
		return ErrPanelUnsupported
	}
	if !u.install.TryLock() {
		return errors.New("a panel update or restore is already running")
	}
	defer u.install.Unlock()
	if PanelOperationRunning(u.cfg.DataDir, u.now()) || PanelHelperRequestPending(u.cfg.DataDir, u.now()) {
		return errors.New("a panel update or restore is already running")
	}
	if !unitPattern.MatchString(strings.TrimSpace(u.cfg.ServiceUnit)) {
		return ErrPanelUnsupported
	}
	if u.cfg.UseRootHelperService {
		if err := u.rootHelperLayoutError(); err != nil {
			return err
		}
	}
	if err := clearPanelHelperRequests(u.cfg.DataDir); err != nil {
		return fmt.Errorf("clear stale panel helper requests: %w", err)
	}
	if request.RequestedUnix == 0 {
		request.RequestedUnix = u.now().UTC().Unix()
	}
	if err := writePanelRestoreRequest(u.cfg.DataDir, request); err != nil {
		return fmt.Errorf("record panel restore request: %w", err)
	}
	status := u.Status()
	status.ErrorKey = ""
	u.setStatus(status, true, "")
	runCtx, cancelRun := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelRun()
	unit, started := panelUpdateHelperService, ""
	var err error
	if u.cfg.UseRootHelperService {
		started = u.unitProps(runCtx, unit)["InactiveExitTimestampMonotonic"]
		_, err = u.runner(runCtx, "systemctl", "start", "--no-block", unit)
	} else {
		unit = fmt.Sprintf("mistgate-panel-update-%d.service", u.now().UnixNano())
		args := []string{
			"--unit=" + unit, "--collect", "--no-block", "--property=Type=oneshot", "--property=TimeoutStartSec=" + panelUpdateApplyTimeoutArg, "--",
			u.cfg.Executable, "panel-update-helper", "--restore", "--data-dir", u.cfg.DataDir, "--service", strings.TrimSpace(u.cfg.ServiceUnit),
		}
		_, err = u.runner(runCtx, "systemd-run", args...)
	}
	if err != nil {
		_ = os.Remove(filepath.Join(u.cfg.DataDir, panelRestoreRequestName))
		u.setStatus(status, false, "schedule_failed")
		return fmt.Errorf("schedule panel restore: %w", err)
	}
	u.mu.Lock()
	u.seq++
	seq := u.seq
	u.mu.Unlock()
	go u.watchHelper(unit, started, seq)
	return nil
}

// DeleteKeptData asks the same privileged helper to remove one validated kept directory and waits for its durable result.
func (u *GitHubPanelUpdater) DeleteKeptData(ctx context.Context, name string) error {
	if !u.supported {
		return ErrPanelUnsupported
	}
	if !validKeptDeleteName(u.cfg.DataDir, name) {
		return errors.New("invalid kept-data directory name")
	}
	if !u.install.TryLock() {
		return errors.New("a panel update, restore or kept-data deletion is already running")
	}
	defer u.install.Unlock()
	if PanelOperationRunning(u.cfg.DataDir, u.now()) || PanelHelperRequestPending(u.cfg.DataDir, u.now()) {
		return errors.New("a panel update, restore or kept-data deletion is already running")
	}
	if !unitPattern.MatchString(strings.TrimSpace(u.cfg.ServiceUnit)) {
		return ErrPanelUnsupported
	}
	if u.cfg.UseRootHelperService {
		if err := u.rootHelperLayoutError(); err != nil {
			return err
		}
	}
	if err := clearPanelHelperRequests(u.cfg.DataDir); err != nil {
		return fmt.Errorf("clear stale panel helper requests: %w", err)
	}
	resultPath := filepath.Join(u.cfg.DataDir, PanelKeptDeleteResultFileName)
	if info, err := os.Lstat(resultPath); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("kept-data deletion result is not a regular file")
		}
		if err := os.Remove(resultPath); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := writePanelKeptDeleteRequest(u.cfg.DataDir, PanelKeptDeleteRequest{Name: name}); err != nil {
		return fmt.Errorf("record kept-data deletion request: %w", err)
	}
	status := u.Status()
	status.Installing = true
	u.setStatus(status, true, "")
	unit := panelUpdateHelperService
	runCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	var err error
	if u.cfg.UseRootHelperService {
		_, err = u.runner(runCtx, "systemctl", "start", "--no-block", unit)
	} else {
		unit = fmt.Sprintf("mistgate-panel-update-%d.service", u.now().UnixNano())
		args := []string{
			"--unit=" + unit, "--collect", "--no-block", "--property=Type=oneshot", "--property=TimeoutStartSec=" + panelUpdateApplyTimeoutArg, "--",
			u.cfg.Executable, "panel-update-helper", "--delete-kept", "--data-dir", u.cfg.DataDir, "--service", strings.TrimSpace(u.cfg.ServiceUnit),
		}
		_, err = u.runner(runCtx, "systemd-run", args...)
	}
	cancel()
	if err != nil {
		_ = os.Remove(filepath.Join(u.cfg.DataDir, panelKeptDeleteRequestName))
		u.setStatus(status, false, "schedule_failed")
		return fmt.Errorf("schedule kept-data deletion: %w", err)
	}
	defer func() { u.setStatus(status, false, "") }()
	waitCtx, cancelWait := context.WithTimeout(context.WithoutCancel(ctx), panelUpdateApplyTimeout)
	defer cancelWait()
	for {
		result, err := ReadPanelKeptDeleteResult(u.cfg.DataDir)
		if err == nil {
			if result.Name != name {
				return errors.New("kept-data deletion result does not match the request")
			}
			_ = os.Remove(resultPath)
			if result.Outcome != "deleted" {
				return errors.New("the kept data directory could not be deleted")
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read kept-data deletion result: %w", err)
		}
		select {
		case <-waitCtx.Done():
			return errors.New("timed out waiting for kept-data deletion")
		case <-time.After(u.watchEvery):
		}
	}
}
