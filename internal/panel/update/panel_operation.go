//go:build !js

package update

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	panelOperationLockSuffix = ".mistgate-operation.lock"
	panelOperationStaleAfter = 15 * time.Minute
	panelOperationHeartbeat  = time.Minute
)

type PanelOperationState struct {
	ID             string               `json:"id"`
	Kind           string               `json:"kind"`
	StartedUnix    int64                `json:"started_unix"`
	HeartbeatUnix  int64                `json:"heartbeat_unix"`
	RestoreRequest *PanelRestoreRequest `json:"restore_request,omitempty"`
}

// ReadPanelOperationState reads the helper's root-owned state file. A malformed or unreadable file is an error and
// callers must treat it as busy. exists distinguishes a stale file, which the next helper run removes, from no file.
func ReadPanelOperationState(dataDir string, now time.Time) (state PanelOperationState, active, exists bool, err error) {
	path := panelOperationLockPath(dataDir)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return PanelOperationState{}, false, false, nil
	}
	if err != nil {
		return PanelOperationState{}, true, true, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<10 {
		return PanelOperationState{}, true, true, errors.New("panel operation lock is not a small regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return PanelOperationState{}, true, true, err
	}
	defer f.Close()
	if opened, statErr := f.Stat(); statErr != nil || !os.SameFile(info, opened) {
		return PanelOperationState{}, true, true, errors.New("panel operation lock changed while it was read")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil || !validPanelOperationState(state) {
		return PanelOperationState{}, true, true, errors.New("panel operation lock is invalid")
	}
	last := state.HeartbeatUnix
	if last == 0 {
		last = state.StartedUnix
	}
	if now.IsZero() {
		now = time.Now()
	}
	return state, now.Sub(time.Unix(last, 0)) < panelOperationStaleAfter, true, nil
}

// PanelOperationRunning reports a fresh or unreadable operation lock. A stale lock is ignored until a helper run
// removes it; the panel never needs write access beside its data directory.
func PanelOperationRunning(dataDir string, now time.Time) bool {
	if strings.TrimSpace(dataDir) == "" {
		return false
	}
	_, active, _, err := ReadPanelOperationState(dataDir, now)
	return active || err != nil
}

func panelOperationLockPath(dataDir string) string {
	return filepath.Clean(dataDir) + panelOperationLockSuffix
}

type panelOperationLock struct {
	dataDir string
	state   PanelOperationState
	stop    chan struct{}
	done    chan struct{}
	once    sync.Once
}

func acquirePanelOperationLock(dataDir, kind string, restore *PanelRestoreRequest, now time.Time) (*panelOperationLock, error) {
	if now.IsZero() {
		now = time.Now()
	}
	if kind != "update" && kind != "restore" && kind != "delete-kept" {
		return nil, errors.New("panel-update-helper: invalid operation kind")
	}
	if restore != nil && (kind != "restore" || !validPanelRestoreRequest(*restore)) {
		return nil, errors.New("panel-update-helper: invalid restore operation metadata")
	}
	var idBytes [16]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return nil, err
	}
	state := PanelOperationState{ID: hex.EncodeToString(idBytes[:]), Kind: kind,
		StartedUnix: now.UTC().Unix(), HeartbeatUnix: now.UTC().Unix(), RestoreRequest: restore}
	path := panelOperationLockPath(dataDir)
	for attempts := 0; attempts < 2; attempts++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			encoded, marshalErr := json.Marshal(state)
			if marshalErr == nil {
				_, marshalErr = f.Write(encoded)
			}
			if marshalErr == nil {
				marshalErr = f.Sync()
			}
			closeErr := f.Close()
			if err := errors.Join(marshalErr, closeErr); err != nil {
				_ = os.Remove(path)
				return nil, err
			}
			syncDirectory(filepath.Dir(path))
			lock := &panelOperationLock{dataDir: dataDir, state: state, stop: make(chan struct{}), done: make(chan struct{})}
			go lock.heartbeat()
			return lock, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		old, active, exists, readErr := ReadPanelOperationState(dataDir, now)
		if readErr != nil {
			return nil, fmt.Errorf("panel-update-helper: inspect existing operation lock: %w", readErr)
		}
		if !exists || active {
			return nil, errors.New("panel-update-helper: another panel operation is running")
		}
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			return nil, errors.New("panel-update-helper: stale operation lock changed")
		}
		current, currentActive, currentExists, currentErr := ReadPanelOperationState(dataDir, now)
		if currentErr != nil || !currentExists || currentActive || current.ID != old.ID {
			return nil, errors.New("panel-update-helper: operation lock changed while stale lock was inspected")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
		syncDirectory(filepath.Dir(path))
	}
	return nil, errors.New("panel-update-helper: could not acquire operation lock")
}

func (l *panelOperationLock) heartbeat() {
	defer close(l.done)
	ticker := time.NewTicker(panelOperationHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			l.state.HeartbeatUnix = time.Now().UTC().Unix()
			_ = writePanelOperationState(l.dataDir, l.state)
		}
	}
}

func (l *panelOperationLock) release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		close(l.stop)
		<-l.done
		path := panelOperationLockPath(l.dataDir)
		current, _, exists, err := ReadPanelOperationState(l.dataDir, time.Now())
		if err == nil && exists && current.ID == l.state.ID {
			_ = os.Remove(path)
			syncDirectory(filepath.Dir(path))
		}
	})
}

func writePanelOperationState(dataDir string, state PanelOperationState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	dir := filepath.Dir(panelOperationLockPath(dataDir))
	tmp, err := os.CreateTemp(dir, ".mistgate-operation-lock-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o644); err != nil {
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
	if err := os.Rename(name, panelOperationLockPath(dataDir)); err != nil {
		return err
	}
	syncDirectory(dir)
	return nil
}

func validPanelOperationState(state PanelOperationState) bool {
	if len(state.ID) != 32 || state.StartedUnix <= 0 || state.HeartbeatUnix <= 0 ||
		(state.Kind != "update" && state.Kind != "restore" && state.Kind != "delete-kept") {
		return false
	}
	if _, err := hex.DecodeString(state.ID); err != nil {
		return false
	}
	if state.RestoreRequest != nil && (state.Kind != "restore" || !validPanelRestoreRequest(*state.RestoreRequest)) {
		return false
	}
	return true
}

// PanelHelperRequestPending reports whether a fresh helper request is waiting for its privileged unit to start.
func PanelHelperRequestPending(dataDir string, now time.Time) bool {
	if strings.TrimSpace(dataDir) == "" {
		return false
	}
	for _, name := range []string{panelUpdateRequestName, panelRestoreRequestName, panelKeptDeleteRequestName} {
		info, err := os.Lstat(filepath.Join(dataDir, name))
		if err == nil && info.Mode().IsRegular() && (now.IsZero() || now.Sub(info.ModTime()) < panelOperationStaleAfter) {
			return true
		}
	}
	return false
}

func panelRequestModified(dataDir, name string) (time.Time, bool) {
	info, err := os.Lstat(filepath.Join(dataDir, name))
	if err != nil || !info.Mode().IsRegular() {
		return time.Time{}, false
	}
	return info.ModTime(), true
}

func validateHelperRequestFile(path string, target any) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<10 {
		return nil, errors.New("panel helper request is not a small regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if opened, err := f.Stat(); err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("panel helper request changed while it was read")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 4<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return nil, err
	}
	return info, nil
}

func requestPath(dataDir, name string) string { return filepath.Join(dataDir, name) }

func validKeptDeleteName(dataDir, name string) bool {
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\x00") {
		return false
	}
	prefix := filepath.Base(filepath.Clean(dataDir)) + ".before-restore-"
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+15 {
		return false
	}
	stamp := strings.TrimPrefix(name, prefix)
	parsed, err := time.Parse("20060102-150405", stamp)
	return err == nil && parsed.Format("20060102-150405") == stamp
}
