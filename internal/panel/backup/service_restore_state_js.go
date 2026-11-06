//go:build js

package backup

import "context"

type restoreState struct{}

func newRestoreState(Config) restoreState { return restoreState{} }

func (s *Service) panelOperationRunning() bool { return false }

func (s *Service) protectRestoreStage(string) bool { return false }

func (s *Service) initializeRestore() {}

func (s *Service) reconcileRestore(context.Context) {}

func (s *Service) maintainRestore(context.Context) {}
