package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestFleetConnectionsProcedurePolicy(t *testing.T) {
	path := adminv1connect.NodeServiceGetFleetConnectionsProcedure
	if got := ProcedureRole(path); got != store.RoleReadonly {
		t.Errorf("ProcedureRole = %q, want readonly", got)
	}
	if got, ok := TokenAccess(path); !ok || got != TokenAccessDirect {
		t.Errorf("TokenAccess = %v, %v; want direct", got, ok)
	}
}
