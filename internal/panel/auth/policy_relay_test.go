package auth

import (
	"context"
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

var relayProcedures = []string{
	adminv1connect.RelayServiceListRelaysProcedure, adminv1connect.RelayServiceCreateRelayProcedure,
	adminv1connect.RelayServiceUpdateRelayProcedure, adminv1connect.RelayServiceDeleteRelayProcedure,
	adminv1connect.RelayServiceCheckRelayProcedure, adminv1connect.RelayServiceListRelayCandidatesProcedure,
}

// RelayService is the owner's alone for a signed-in admin: a helper and a readonly admin are refused by role.
func TestRelayProceduresAreOwnerOnly(t *testing.T) {
	for _, path := range relayProcedures {
		if got := ProcedureRole(path); got != store.RoleOwner {
			t.Errorf("%s: ProcedureRole = %q, want owner", path, got)
		}
	}
}

// A token reaches the relays only through the MCP tools: the reads and the check in process (their raw response names the exit's
// address), the changes through the owner's approval. The reads are narrowed to a readonly token because the MCP views leave the
// address out; the check stays the admin profile's, like the procedure.
func TestRelayProceduresOfATokenAreMCPOnly(t *testing.T) {
	want := map[string]int{
		adminv1connect.RelayServiceListRelaysProcedure:          TokenAccessPlanning,
		adminv1connect.RelayServiceListRelayCandidatesProcedure: TokenAccessPlanning,
		adminv1connect.RelayServiceCheckRelayProcedure:          TokenAccessPlanning,
		adminv1connect.RelayServiceCreateRelayProcedure:         TokenAccessApproved,
		adminv1connect.RelayServiceUpdateRelayProcedure:         TokenAccessApproved,
		adminv1connect.RelayServiceDeleteRelayProcedure:         TokenAccessApproved,
	}
	for _, path := range relayProcedures {
		if got, ok := TokenAccess(path); !ok || got != want[path] {
			t.Errorf("%s: token access %d (listed %v), want %d", path, got, ok, want[path])
		}
	}
	for path, role := range map[string]string{
		adminv1connect.RelayServiceListRelaysProcedure:          store.RoleReadonly,
		adminv1connect.RelayServiceListRelayCandidatesProcedure: store.RoleReadonly,
		adminv1connect.RelayServiceCheckRelayProcedure:          store.RoleOwner,
		adminv1connect.RelayServiceCreateRelayProcedure:         store.RoleOwner,
		adminv1connect.RelayServiceUpdateRelayProcedure:         store.RoleOwner,
		adminv1connect.RelayServiceDeleteRelayProcedure:         store.RoleOwner,
	} {
		if got := TokenProcedureRole(path); got != role {
			t.Errorf("%s: token role %q, want %q", path, got, role)
		}
	}
}

// The behaviour behind the table: over /api every relay procedure is refused to every profile; in process, the reads work for a
// readonly token under the planning grant, the check only for the admin profile, and the changes never under planning.
func TestRelayProceduresOverTheWire(t *testing.T) {
	e := newTokenEnv(t)
	_, ro := e.mkToken("ro", store.ProfileReadonly, 600)
	_, op := e.mkToken("op", store.ProfileOperator, 600)
	_, ad := e.mkToken("ad", store.ProfileAdmin, 600)
	for _, path := range relayProcedures {
		for _, secret := range []string{ro, op, ad} {
			if got := e.code(path, secret); got != 403 {
				t.Errorf("%s over the API: %d", path, got)
			}
		}
	}
	planning := WithPlanning(WithChannel(context.Background(), ChannelMCP))
	for _, tc := range []struct {
		path          string
		ro, op, admin int
	}{
		{adminv1connect.RelayServiceListRelaysProcedure, 200, 200, 200},
		{adminv1connect.RelayServiceListRelayCandidatesProcedure, 200, 200, 200},
		{adminv1connect.RelayServiceCheckRelayProcedure, 403, 403, 200},
		{adminv1connect.RelayServiceCreateRelayProcedure, 403, 403, 403},
		{adminv1connect.RelayServiceUpdateRelayProcedure, 403, 403, 403},
		{adminv1connect.RelayServiceDeleteRelayProcedure, 403, 403, 403},
	} {
		for who, pair := range map[string][2]any{"readonly": {ro, tc.ro}, "operator": {op, tc.op}, "admin": {ad, tc.admin}} {
			if got := e.code(tc.path, pair[0].(string), callOpts{ctx: planning}); got != pair[1].(int) {
				t.Errorf("%s as %s while planning: %d, want %d", tc.path, who, got, pair[1])
			}
		}
	}
}
