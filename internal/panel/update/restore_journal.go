//go:build !js

package update

import (
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

const restoreJournalVersion = 1

var restoreJournalHook func(string) error
var restoreRenameExchange = renameExchange
var errRestoreJournalInterruption = errors.New("simulated restore helper interruption")

type panelRestoreJournal struct {
	Version      int                 `json:"version"`
	Phase        string              `json:"phase"`
	DataDir      string              `json:"data_dir"`
	StageName    string              `json:"stage_name"`
	StagePath    string              `json:"stage_path"`
	StageOutside string              `json:"stage_outside"`
	KeptPath     string              `json:"kept_path"`
	MarkerName   string              `json:"marker_name"`
	Request      PanelRestoreRequest `json:"request"`
}

func restoreJournalPath(dataDir string) string {
	return filepath.Clean(dataDir) + ".mistgate-restore-journal"
}

func writeRestoreJournal(j panelRestoreJournal) error {
	encoded, err := json.Marshal(j)
	if err != nil {
		return err
	}
	path := restoreJournalPath(j.DataDir)
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mistgate-restore-journal-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
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
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return syncDirectoryDurable(dir)
}

func readRestoreJournal(dataDir string) (panelRestoreJournal, error) {
	path := restoreJournalPath(dataDir)
	info, err := os.Lstat(path)
	if err != nil {
		return panelRestoreJournal{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8<<10 {
		return panelRestoreJournal{}, errors.New("panel restore journal is not a small regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return panelRestoreJournal{}, err
	}
	defer f.Close()
	if opened, err := f.Stat(); err != nil || !os.SameFile(info, opened) {
		return panelRestoreJournal{}, errors.New("panel restore journal changed while it was read")
	}
	var j panelRestoreJournal
	decoder := json.NewDecoder(io.LimitReader(f, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&j); err != nil || !validRestoreJournal(filepath.Clean(dataDir), j) {
		return panelRestoreJournal{}, errors.New("panel restore journal is invalid")
	}
	return j, nil
}

func validRestoreJournal(dataDir string, j panelRestoreJournal) bool {
	if j.Version != restoreJournalVersion || j.DataDir != dataDir || !validPanelRestoreStageDir(j.StageName) || !validPanelRestoreRequest(j.Request) ||
		j.Request.StageDir != j.StageName || j.StagePath != filepath.Join(dataDir, j.StageName) ||
		j.StageOutside != dataDir+".restore-stage-"+j.StageName ||
		j.MarkerName != ".mistgate-restore-marker-"+strings.TrimPrefix(j.StageName, PanelRestoreStagePrefix) ||
		!strings.HasPrefix(j.KeptPath, dataDir+".before-restore-") || len(j.KeptPath) != len(dataDir)+len(".before-restore-")+15 {
		return false
	}
	stamp := strings.TrimPrefix(j.KeptPath, dataDir+".before-restore-")
	parsed, err := time.Parse("20060102-150405", stamp)
	if err != nil || parsed.Format("20060102-150405") != stamp {
		return false
	}
	switch j.Phase {
	case "prepared", "stage-moved", "exchange-intent", "old-kept", "swapped", "kept", "health", "rolling-back", "rolled-back", "committed":
		return true
	default:
		return false
	}
}

func restoreCheckpoint(phase string) error {
	if restoreJournalHook != nil {
		if err := restoreJournalHook(phase); err != nil {
			return errors.Join(errRestoreJournalInterruption, err)
		}
	}
	return nil
}

func createRestoreStageMarker(stagePath, markerName, stageName string) error {
	file, err := os.OpenFile(filepath.Join(stagePath, markerName), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(stageName); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func hasRestoreStageMarker(dir, markerName, stageName string) bool {
	data, err := os.ReadFile(filepath.Join(dir, markerName))
	return err == nil && string(data) == stageName
}

func removeRestoreStageMarker(dir, markerName string) error {
	err := os.Remove(filepath.Join(dir, markerName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil {
		return syncDirectoryDurable(dir)
	}
	return err
}

func renameExchangeUnsupported(err error) bool { return isRenameExchangeUnsupported(err) }

func swapRestoreDirectories(j *panelRestoreJournal) error {
	parent := filepath.Dir(j.DataDir)
	if err := writeRestoreJournal(*j); err != nil {
		return fmt.Errorf("write restore journal: %w", err)
	}
	if err := restoreCheckpoint("prepared"); err != nil {
		return err
	}
	if err := os.Rename(j.StagePath, j.StageOutside); err != nil {
		return fmt.Errorf("move staged restore beside data directory: %w", err)
	}
	if err := syncDirectoryDurable(parent); err != nil {
		return err
	}
	j.Phase = "stage-moved"
	if err := writeRestoreJournal(*j); err != nil {
		return err
	}
	if err := restoreCheckpoint("stage-moved"); err != nil {
		return err
	}
	j.Phase = "exchange-intent"
	if err := writeRestoreJournal(*j); err != nil {
		return err
	}
	if err := restoreCheckpoint("exchange-intent"); err != nil {
		return err
	}
	if err := restoreRenameExchange(j.StageOutside, j.DataDir); err == nil {
		if err := syncDirectoryDurable(parent); err != nil {
			return err
		}
		j.Phase = "swapped"
		if err := writeRestoreJournal(*j); err != nil {
			return err
		}
		if err := restoreCheckpoint("swapped"); err != nil {
			return err
		}
		if err := os.Rename(j.StageOutside, j.KeptPath); err != nil {
			return fmt.Errorf("keep previous data directory: %w", err)
		}
		if err := syncDirectoryDurable(parent); err != nil {
			return err
		}
	} else if renameExchangeUnsupported(err) {
		if err := os.Rename(j.DataDir, j.KeptPath); err != nil {
			return fmt.Errorf("keep previous data directory: %w", err)
		}
		if err := syncDirectoryDurable(parent); err != nil {
			return err
		}
		j.Phase = "old-kept"
		if err := writeRestoreJournal(*j); err != nil {
			return err
		}
		if err := restoreCheckpoint("old-kept"); err != nil {
			return err
		}
		if err := os.Rename(j.StageOutside, j.DataDir); err != nil {
			return fmt.Errorf("install restored data directory: %w", err)
		}
		if err := syncDirectoryDurable(parent); err != nil {
			return err
		}
		j.Phase = "swapped"
		if err := writeRestoreJournal(*j); err != nil {
			return err
		}
		if err := restoreCheckpoint("swapped"); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("exchange restored data directory: %w", err)
	}
	j.Phase = "kept"
	if err := writeRestoreJournal(*j); err != nil {
		return err
	}
	return restoreCheckpoint("kept")
}

func commitRestoreJournal(j *panelRestoreJournal) error {
	j.Phase = "health"
	if err := writeRestoreJournal(*j); err != nil {
		return err
	}
	if err := restoreCheckpoint("health"); err != nil {
		return err
	}
	j.Phase = "committed"
	if err := writeRestoreJournal(*j); err != nil {
		return err
	}
	return restoreCheckpoint("committed")
}

// recoverPanelRestoreJournal runs before each privileged helper operation. An uncommitted swap is undone; a committed
// swap is finalized. The marker identifies the staged tree if the process died after renameat2 but before journaling it.
func recoverPanelRestoreJournal(dataDir, service string, systemctl systemctlFunc) error {
	j, err := readRestoreJournal(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("panel-update-helper: recover restore journal: %w", err)
	}
	if j.Phase == "committed" {
		if _, err := os.Lstat(j.KeptPath); err != nil {
			return fmt.Errorf("panel-update-helper: committed restore lost its kept data directory: %w", err)
		}
		return removeRestoreJournal(j.DataDir)
	}
	if _, err := systemctl("stop", service); err != nil {
		return fmt.Errorf("panel-update-helper: stop panel for restore recovery: %w", err)
	}
	if err := rollbackRestoreJournal(&j); err != nil {
		return fmt.Errorf("panel-update-helper: undo interrupted restore: %w", err)
	}
	dataInfo, err := os.Lstat(j.DataDir)
	if err != nil || !dataInfo.IsDir() || dataInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("panel-update-helper: recovered panel data path is not a real directory")
	}
	result := PanelRestoreResult{Outcome: "failed", Source: j.Request.Source, FileName: j.Request.FileName,
		MadeByVersion: j.Request.MadeByVersion, CompletedUnix: time.Now().UTC().Unix()}
	if err := writePanelRestoreResultOwned(j.DataDir, result, dataInfo); err != nil {
		return fmt.Errorf("panel-update-helper: record interrupted restore: %w", err)
	}
	if err := RemovePanelRestoreRequest(j.DataDir); err != nil {
		return err
	}
	if err := removeRestoreJournal(j.DataDir); err != nil {
		return err
	}
	return systemctlStartAfterRestore(service, systemctl)
}

func rollbackRestoreJournal(j *panelRestoreJournal) error {
	parent := filepath.Dir(j.DataDir)
	if j.Phase != "rolling-back" && j.Phase != "rolled-back" {
		j.Phase = "rolling-back"
		if err := writeRestoreJournal(*j); err != nil {
			return err
		}
		if err := restoreCheckpoint("rolling-back"); err != nil {
			return err
		}
	}
	markerAtLive := hasRestoreStageMarker(j.DataDir, j.MarkerName, j.StageName)
	markerAtOutside := hasRestoreStageMarker(j.StageOutside, j.MarkerName, j.StageName)
	markerAtKept := hasRestoreStageMarker(j.KeptPath, j.MarkerName, j.StageName)
	liveExists := isRealDirectory(j.DataDir)
	keptExists := isRealDirectory(j.KeptPath)
	outsideExists := isRealDirectory(j.StageOutside)
	var oldPath, failedPath string
	deterministicFailed := j.DataDir + ".restore-failed-" + strings.TrimPrefix(j.StageName, PanelRestoreStagePrefix)
	switch {
	case markerAtLive:
		if keptExists {
			oldPath = j.KeptPath
		} else if outsideExists {
			oldPath = j.StageOutside
		}
	case markerAtOutside:
		// The exchange has not occurred, or the fallback is between its two renames.
		if keptExists {
			oldPath = j.KeptPath
		} else if liveExists {
			_ = os.RemoveAll(j.StageOutside)
			_ = os.RemoveAll(j.StagePath)
			if err := syncDirectoryDurable(parent); err != nil {
				return err
			}
			j.Phase = "rolled-back"
			if err := writeRestoreJournal(*j); err != nil {
				return err
			}
			return nil
		}
	case markerAtKept:
		// A previous rollback exchanged the old tree back into place and left the failed stage at kept.
		failedPath = j.KeptPath
	case keptExists:
		// The stage marker was removed immediately before health checking; the kept path is the old tree.
		oldPath = j.KeptPath
	case j.Phase == "prepared" || j.Phase == "stage-moved" || j.Phase == "exchange-intent" && !liveExists:
		if liveExists {
			if outsideExists {
				_ = os.RemoveAll(j.StageOutside)
			}
			_ = os.RemoveAll(j.StagePath)
			j.Phase = "rolled-back"
			if err := writeRestoreJournal(*j); err != nil {
				return err
			}
			return nil
		}
	case !liveExists && keptExists:
		oldPath = j.KeptPath
	case liveExists && !keptExists && !outsideExists:
		// No directory move happened, or rollback already completed before its final journal update.
		if err := os.RemoveAll(deterministicFailed); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.RemoveAll(j.StagePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := syncDirectoryDurable(parent); err != nil {
			return err
		}
		j.Phase = "rolled-back"
		if err := writeRestoreJournal(*j); err != nil {
			return err
		}
		return nil
	}
	if oldPath != "" {
		if liveExists && failedPath == "" {
			if err := restoreRenameExchange(oldPath, j.DataDir); err == nil {
				if err := syncDirectoryDurable(parent); err != nil {
					return err
				}
				failedPath = oldPath
			} else if renameExchangeUnsupported(err) {
				failedPath = j.DataDir + ".restore-failed-" + strings.TrimPrefix(j.StageName, PanelRestoreStagePrefix)
				if _, err := os.Lstat(failedPath); err == nil {
					return errors.New("restore rollback path already exists")
				}
				if err := os.Rename(j.DataDir, failedPath); err != nil {
					return err
				}
				if err := syncDirectoryDurable(parent); err != nil {
					return err
				}
				if err := os.Rename(oldPath, j.DataDir); err != nil {
					return err
				}
				if err := syncDirectoryDurable(parent); err != nil {
					return err
				}
			} else {
				return err
			}
		} else if !liveExists {
			if err := os.Rename(oldPath, j.DataDir); err != nil {
				return err
			}
			if err := syncDirectoryDurable(parent); err != nil {
				return err
			}
		}
	}
	if failedPath == "" {
		failedPath = j.DataDir + ".restore-failed-" + strings.TrimPrefix(j.StageName, PanelRestoreStagePrefix)
		if _, err := os.Lstat(failedPath); errors.Is(err, os.ErrNotExist) {
			failedPath = ""
		}
	}
	if failedPath != "" && failedPath != j.DataDir {
		if err := os.RemoveAll(failedPath); err != nil {
			return err
		}
	}
	if j.StageOutside != oldPath {
		if err := os.RemoveAll(j.StageOutside); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.RemoveAll(j.StagePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncDirectoryDurable(parent); err != nil {
		return err
	}
	j.Phase = "rolled-back"
	if err := writeRestoreJournal(*j); err != nil {
		return err
	}
	return nil
}

func isRealDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func removeRestoreJournal(dataDir string) error {
	err := os.Remove(restoreJournalPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil {
		if err := syncDirectoryDurable(filepath.Dir(restoreJournalPath(dataDir))); err != nil {
			return err
		}
	}
	return err
}

func syncDirectoryDurable(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}

func systemctlStartAfterRestore(service string, systemctl systemctlFunc) error {
	return startPanelService(service, systemctl)
}
