package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The bandwidth test makes a node push up to 1 GB through a public server: owner only, and closed to API tokens and MCP
// whatever the token's profile (no MCP tool wraps it either, so there is nothing to approve).
func TestMeasureBandwidthIsOwnerOnlyAndClosedToTokens(t *testing.T) {
	path := adminv1connect.NodeServiceMeasureBandwidthProcedure
	if _, listed := procedureLevels[path]; !listed {
		t.Fatal("MeasureBandwidth has no explicit level (it would silently be owner-only)")
	}
	l := levelOf(path)
	for role, want := range map[string]bool{store.RoleOwner: true, store.RoleHelper: false, store.RoleReadonly: false} {
		if got := roleAllows(role, l); got != want {
			t.Errorf("as %s: allowed = %v, want %v", role, got, want)
		}
	}
	if got := ProcedureRole(path); got != store.RoleOwner {
		t.Errorf("ProcedureRole = %q", got)
	}
	if access, ok := TokenAccess(path); ok {
		t.Errorf("open to API tokens (access %d)", access)
	}
	if NeedsStepUp(path) {
		t.Error("a plain click on the node's settings must not need a passkey touch")
	}
}
