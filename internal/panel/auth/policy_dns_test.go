package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// DNS per server: what a node offers on the user page is the owner's call (like the presets); a person's picks are read by
// everyone and removed by a helper too (a user edit). A token reaches none of it.
func TestNodeDNSProcedureRoles(t *testing.T) {
	for path, want := range map[string]string{
		adminv1connect.DnsServiceListNodeDnsOptionsProcedure:  store.RoleReadonly,
		adminv1connect.DnsServiceSetNodeDnsOptionsProcedure:   store.RoleOwner,
		adminv1connect.DnsServiceGetUserDnsChoicesProcedure:   store.RoleReadonly,
		adminv1connect.DnsServiceResetUserDnsChoicesProcedure: store.RoleHelper,
	} {
		if got := ProcedureRole(path); got != want {
			t.Errorf("%s needs %s, want %s", path, got, want)
		}
		if _, ok := TokenAccess(path); ok {
			t.Errorf("%s is open to tokens", path)
		}
	}
}
