//go:build !js

package backup

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/mistgate/mistgate/internal/panel/update"
)

type restoreState struct {
	mu             sync.Mutex
	panelData      func(context.Context, RestoreRequest, func() error) error
	deleteKeptData func(context.Context, string) error
	helperRunning  func() bool
	pendingRestore update.PanelRestoreRequest
	pending        atomic.Bool
	uploads        map[string]uploadRecord
	restores       map[string]restoreRecord
	activeStages   map[string]bool
	archiveWork    bool
}

func newRestoreState(cfg Config) restoreState {
	return restoreState{
		panelData: cfg.RestorePanelData, deleteKeptData: cfg.DeleteKeptData, helperRunning: cfg.RestoreHelperRunning,
		uploads: map[string]uploadRecord{}, restores: map[string]restoreRecord{}, activeStages: map[string]bool{},
	}
}

func (s *Service) panelOperationRunning() bool {
	return s.restore.pending.Load() || update.PanelOperationRunning(s.dataDir, s.now()) || update.PanelHelperRequestPending(s.dataDir, s.now())
}

func (s *Service) protectRestoreStage(name string) bool {
	return strings.HasPrefix(name, restoreStagePrefix) || strings.HasPrefix(name, uploadStagePrefix)
}

func (s *Service) initializeRestore() {
	s.reconstructPendingRestore()
	s.cleanupExpiredStages(s.now())
}

func (s *Service) reconcileRestore(ctx context.Context) {
	s.consumeRestoreResult(ctx)
	s.reconcilePendingRestore(ctx)
}

func (s *Service) maintainRestore(ctx context.Context) {
	s.cleanupExpiredStages(s.now())
	s.reconcileRestore(ctx)
}

func (s *Service) reconstructPendingRestore() {
	request, requestErr := update.ReadPanelRestoreRequest(s.dataDir)
	state, active, exists, stateErr := update.ReadPanelOperationState(s.dataDir, s.now())
	if requestErr == nil {
		s.restore.pendingRestore = request
		s.restore.pending.Store(true)
		s.restore.activeStages[filepath.Join(s.dataDir, request.StageDir)] = true
		return
	}
	if stateErr == nil && exists && state.Kind == "restore" && state.RestoreRequest != nil {
		request = *state.RestoreRequest
		if request.RequestedUnix == 0 {
			request.RequestedUnix = state.StartedUnix
		}
		s.restore.pendingRestore = request
		s.restore.pending.Store(true)
		s.restore.activeStages[filepath.Join(s.dataDir, request.StageDir)] = true
		return
	}
	if active && stateErr == nil && state.Kind == "restore" {
		s.restore.pending.Store(true)
	}
}
