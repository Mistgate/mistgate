package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestCheckPortsIsWriteLevelAndDirectForTokens(t *testing.T) {
	path := adminv1connect.NodeServiceCheckPortsProcedure
	if _, listed := procedureLevels[path]; !listed {
		t.Fatal("CheckPorts has no explicit level")
	}
	for role, want := range map[string]bool{store.RoleOwner: true, store.RoleHelper: true, store.RoleReadonly: false} {
		if got := roleAllows(role, levelOf(path)); got != want {
			t.Errorf("CheckPorts as %s: allowed = %v, want %v", role, got, want)
		}
	}
	if access, ok := TokenAccess(path); !ok || access != TokenAccessDirect {
		t.Errorf("CheckPorts token access = %d, %v; want direct", access, ok)
	}
	if NeedsStepUp(path) {
		t.Error("CheckPorts should not require step-up")
	}
}
