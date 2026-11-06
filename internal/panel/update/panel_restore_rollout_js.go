//go:build js

package update

import "context"

func (s *Service) StartPanelRestore(context.Context, PanelRestoreRequest, func() error) error {
	return ErrPanelUnsupported
}

func (s *Service) DeleteKeptData(context.Context, string) error {
	return ErrPanelUnsupported
}

func (s *Service) PanelUpdateRunning() bool { return false }
