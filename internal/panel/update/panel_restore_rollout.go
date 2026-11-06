//go:build !js

package update

import (
	"context"

	"github.com/mistgate/mistgate/internal/panel/edition"
)

// RestorePanelData reserves the panel-update/rollout gate before asking the updater's systemd helper to swap verified
// restore data into place. The helper's Installing state holds the reservation after this call returns.
func (s *Service) StartPanelRestore(ctx context.Context, request PanelRestoreRequest, beforeStart func() error) error {
	if err := edition.Check(s.cfg.Edge, edition.PanelUpdate); err != nil {
		return err
	}
	if err := s.beginPanelUpdate(ctx); err != nil {
		return err
	}
	defer s.finishPanelUpdate()
	if beforeStart != nil {
		if err := beforeStart(); err != nil {
			return err
		}
	}
	restorer, ok := s.cfg.PanelUpdater.(interface {
		Restore(context.Context, PanelRestoreRequest) error
	})
	if !ok {
		return ErrPanelUnsupported
	}
	return restorer.Restore(ctx, request)
}

// DeleteKeptData reserves the shared panel-update and rollout gate while the privileged helper deletes a kept directory.
func (s *Service) DeleteKeptData(ctx context.Context, name string) error {
	if err := edition.Check(s.cfg.Edge, edition.PanelUpdate); err != nil {
		return err
	}
	if err := s.beginPanelUpdate(ctx); err != nil {
		return err
	}
	defer s.finishPanelUpdate()
	deleter, ok := s.cfg.PanelUpdater.(interface {
		DeleteKeptData(context.Context, string) error
	})
	if !ok {
		return ErrPanelUnsupported
	}
	return deleter.DeleteKeptData(ctx, name)
}

// PanelUpdateRunning reports whether the shared helper is scheduled or running. The restore coordinator uses it to
// recover if systemd accepted a restore request but the helper never started.
func (s *Service) PanelUpdateRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.panelBusy()
}
