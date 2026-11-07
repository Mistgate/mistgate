//go:build js

package fleet

import (
	"context"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// The Cloudflare edition cannot start a debug capture (OpsService is VPS-only), so a report that somehow arrives is dropped.
func (f *Fleet) recordDebugReport(context.Context, string, *agentv1.DebugCaptureReport) {}
