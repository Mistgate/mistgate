package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// ApiTokenService and ApprovalService are owner-only, all of them: a helper or a read-only admin must not see
// who holds a token or what an agent is waiting to do (the handlers add the step-up where it is noted).
func TestIntegrationsServicesAreOwnerOnly(t *testing.T) {
	for name, path := range map[string]string{
		"CreateApiToken": adminv1connect.ApiTokenServiceCreateApiTokenProcedure,
		"ListApiTokens":  adminv1connect.ApiTokenServiceListApiTokensProcedure,
		"RevokeApiToken": adminv1connect.ApiTokenServiceRevokeApiTokenProcedure,
		"ListApprovals":  adminv1connect.ApprovalServiceListApprovalsProcedure,
		"Approve":        adminv1connect.ApprovalServiceApproveProcedure,
		"Reject":         adminv1connect.ApprovalServiceRejectProcedure,
	} {
		if _, listed := procedureLevels[path]; !listed {
			t.Errorf("%s has no explicit level (it would silently be owner-only)", name)
		}
		l := levelOf(path)
		for role, want := range map[string]bool{store.RoleOwner: true, store.RoleHelper: false, store.RoleReadonly: false} {
			if got := roleAllows(role, l); got != want {
				t.Errorf("%s as %s: allowed = %v, want %v", name, role, got, want)
			}
		}
	}
}
