package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// RelayService is the owner's alone: a helper and a readonly admin are refused by role, and no API token is offered any of
// its procedures (a token of the admin profile included).
func TestRelayProceduresAreOwnerOnlyAndClosedToTokens(t *testing.T) {
	for _, path := range []string{
		adminv1connect.RelayServiceListRelaysProcedure, adminv1connect.RelayServiceCreateRelayProcedure,
		adminv1connect.RelayServiceUpdateRelayProcedure, adminv1connect.RelayServiceDeleteRelayProcedure,
	} {
		if got := ProcedureRole(path); got != store.RoleOwner {
			t.Errorf("%s: ProcedureRole = %q, want owner", path, got)
		}
		if _, ok := TokenAccess(path); ok {
			t.Errorf("%s is on the token allow-list", path)
		}
	}
}
