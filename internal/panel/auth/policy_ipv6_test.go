package auth

import (
	"context"
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

var ipv6Procedures = []string{adminv1connect.NodeServiceCheckIpv6Procedure}

// The IPv6 entry of a node sends test packets to an address of it: the owner's alone for a signed-in admin.
func TestIPv6EntryProceduresAreOwnerOnly(t *testing.T) {
	for _, path := range ipv6Procedures {
		if got := ProcedureRole(path); got != store.RoleOwner {
			t.Errorf("%s: ProcedureRole = %q, want owner", path, got)
		}
	}
}

// A token reaches them only through the MCP tools: the check in process (its raw response carries the address that was checked),
// narrowed to the admin profile by the level.
func TestIPv6EntryProceduresOfATokenAreMCPOnly(t *testing.T) {
	want := map[string]int{adminv1connect.NodeServiceCheckIpv6Procedure: TokenAccessPlanning}
	for _, path := range ipv6Procedures {
		if got, ok := TokenAccess(path); !ok || got != want[path] {
			t.Errorf("%s: token access %d (listed %v), want %d", path, got, ok, want[path])
		}
		if got := TokenProcedureRole(path); got != store.RoleOwner {
			t.Errorf("%s: token role %q, want owner", path, got)
		}
	}
	e := newTokenEnv(t)
	_, ro := e.mkToken("ro", store.ProfileReadonly, 600)
	_, op := e.mkToken("op", store.ProfileOperator, 600)
	_, ad := e.mkToken("ad", store.ProfileAdmin, 600)
	planning := WithPlanning(WithChannel(context.Background(), ChannelMCP))
	for _, path := range ipv6Procedures {
		for _, secret := range []string{ro, op, ad} {
			if got := e.code(path, secret); got != 403 {
				t.Errorf("%s over the API: %d", path, got)
			}
		}
		for who, pair := range map[string]struct {
			secret string
			want   int
		}{"readonly": {ro, 403}, "operator": {op, 403}, "admin": {ad, 200}} {
			if got := e.code(path, pair.secret, callOpts{ctx: planning}); got != pair.want {
				t.Errorf("%s as %s while planning: %d, want %d", path, who, got, pair.want)
			}
		}
	}
}
