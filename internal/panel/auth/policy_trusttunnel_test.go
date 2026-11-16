//go:build !js

package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/trusttunnel/v1/trusttunnelv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The tt:// key calls: the strings of policy_trusttunnel.go are the generated names; each is the admin's write level (owner and
// helper, not read-only), needs no step-up, and no token profile reaches one.
func TestTrustTunnelKeyPolicy(t *testing.T) {
	for got, want := range map[string]string{
		TrustTunnelGetLinksProcedure:  trusttunnelv1connect.TrustTunnelServiceGetTrustTunnelLinksProcedure,
		TrustTunnelIssueKeyProcedure:  trusttunnelv1connect.TrustTunnelServiceIssueTrustTunnelKeyProcedure,
		TrustTunnelRevokeKeyProcedure: trusttunnelv1connect.TrustTunnelServiceRevokeTrustTunnelKeyProcedure,
	} {
		if got != want {
			t.Errorf("policy constant %q differs from the generated procedure %q", got, want)
		}
		if _, listed := procedureLevels[want]; !listed || levelOf(want) != levelWrite {
			t.Errorf("%s level = %v (listed %v), want write", want, levelOf(want), listed)
		}
		for role, allowed := range map[string]bool{store.RoleOwner: true, store.RoleHelper: true, store.RoleReadonly: false} {
			if roleAllows(role, levelOf(want)) != allowed {
				t.Errorf("%s as %s: allowed = %v, want %v", want, role, !allowed, allowed)
			}
		}
		if NeedsStepUp(want) {
			t.Errorf("%s unexpectedly needs step-up", want)
		}
		if _, allowed := TokenAccess(want); allowed {
			t.Errorf("%s must stay closed to API tokens and MCP", want)
		}
	}
}
