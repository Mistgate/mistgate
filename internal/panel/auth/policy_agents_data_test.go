package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The node runtime, host history and path loss reads are open to every role and to API tokens directly: they return hashes cut
// short, states, counts and node names, never an address or a pin.
func TestAgentDataProcedurePolicy(t *testing.T) {
	for _, path := range []string{
		adminv1connect.NodeServiceGetNodeRuntimeProcedure,
		adminv1connect.NodeServiceGetNodeHostHistoryProcedure,
		adminv1connect.NodeServiceGetPathLossProcedure,
	} {
		if got := ProcedureRole(path); got != store.RoleReadonly {
			t.Errorf("%s: ProcedureRole = %q, want readonly", path, got)
		}
		if got, ok := TokenAccess(path); !ok || got != TokenAccessDirect {
			t.Errorf("%s: TokenAccess = %v, %v; want direct", path, got, ok)
		}
	}
}
