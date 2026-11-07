//go:build !js

package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/ops/v1/opsv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The debug capture of a node (OpsService): the strings of policy_ops.go are the generated names; every procedure is the owner's
// (an admin-profile token is the owner role); starting and reading one are direct for an admin-profile token (the MCP tools put a plan in
// front of the start), and stopping one is closed to tokens.
func TestDebugCaptureProcedurePolicy(t *testing.T) {
	for got, want := range map[string]string{
		OpsStartDebugCaptureProcedure: opsv1connect.OpsServiceStartDebugCaptureProcedure,
		OpsStopDebugCaptureProcedure:  opsv1connect.OpsServiceStopDebugCaptureProcedure,
		OpsListDebugCapturesProcedure: opsv1connect.OpsServiceListDebugCapturesProcedure,
		OpsGetDebugCaptureProcedure:   opsv1connect.OpsServiceGetDebugCaptureProcedure,
	} {
		if got != want {
			t.Errorf("policy constant %q differs from the generated procedure %q", got, want)
		}
		if role := ProcedureRole(want); role != store.RoleOwner {
			t.Errorf("%s: ProcedureRole = %q, want owner", want, role)
		}
		if NeedsStepUp(want) {
			t.Errorf("%s needs a step-up", want)
		}
	}
	for path, want := range map[string]int{
		opsv1connect.OpsServiceStartDebugCaptureProcedure: TokenAccessDirect,
		opsv1connect.OpsServiceListDebugCapturesProcedure: TokenAccessDirect,
		opsv1connect.OpsServiceGetDebugCaptureProcedure:   TokenAccessDirect,
	} {
		if got, ok := TokenAccess(path); !ok || got != want {
			t.Errorf("%s: TokenAccess = %d, %v; want %d", path, got, ok, want)
		}
	}
	if _, ok := TokenAccess(opsv1connect.OpsServiceStopDebugCaptureProcedure); ok {
		t.Error("StopDebugCapture is open to tokens")
	}
	if _, ok := grantProcedures["node_debug_capture"]; ok {
		t.Error("a start needs no owner grant: it is not an approved procedure")
	}
}
