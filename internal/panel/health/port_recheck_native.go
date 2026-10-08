//go:build !js

package health

import (
	"context"
	"time"
)

func (s *Service) runPortRechecks(ctx context.Context) {
	if s.cfg.CheckPorts == nil {
		return
	}
	t := time.NewTicker(portCheckPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runOnePortRecheck(ctx)
		}
	}
}
