//go:build js

package health

import (
	"context"
	"time"
)

// sweepProbes does nothing on the Cloudflare edition: it has no path probes (retention_probe.go).
func (s *Service) sweepProbes(context.Context, time.Time) {}

// sweepDebugCaptures does nothing on the Cloudflare edition: it cannot start a debug capture (retention_probe.go).
func (s *Service) sweepDebugCaptures(context.Context, time.Time) {}

// sweepScheduledRuns does nothing on the Cloudflare edition: it runs no scheduled task (retention_probe.go).
func (s *Service) sweepScheduledRuns(context.Context, time.Time) {}
