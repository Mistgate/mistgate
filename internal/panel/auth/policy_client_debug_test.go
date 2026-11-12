//go:build !js

package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/ops/v1/opsv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The logs people send for support: asking for them, withdrawing the request and the list are the owner's. A helper, a readonly
// admin and every token profile are refused, over the API too.
func TestClientDebugProceduresAreOwnerOnlyAndClosedToTokens(t *testing.T) {
	for got, want := range map[string]string{
		OpsRequestClientDebugProcedure: opsv1connect.OpsServiceRequestClientDebugProcedure,
		OpsCancelClientDebugProcedure:  opsv1connect.OpsServiceCancelClientDebugProcedure,
		OpsListClientDebugProcedure:    opsv1connect.OpsServiceListClientDebugProcedure,
	} {
		if got != want {
			t.Errorf("policy constant %q differs from the generated procedure %q", got, want)
		}
		if role := ProcedureRole(want); role != store.RoleOwner {
			t.Errorf("%s: ProcedureRole = %q, want owner", want, role)
		}
		for _, role := range []string{store.RoleHelper, store.RoleReadonly} {
			if roleAllows(role, levelOf(want)) {
				t.Errorf("%s may call %s", role, want)
			}
		}
		if !roleAllows(store.RoleOwner, levelOf(want)) {
			t.Errorf("the owner may not call %s", want)
		}
		if _, ok := TokenAccess(want); ok {
			t.Errorf("%s is open to tokens", want)
		}
	}
}

func TestClientDebugProceduresOverTheWire(t *testing.T) {
	e := newTokenEnv(t)
	_, ro := e.mkToken("ro", store.ProfileReadonly, 600)
	_, op := e.mkToken("op", store.ProfileOperator, 600)
	_, ad := e.mkToken("ad", store.ProfileAdmin, 600)
	for _, path := range []string{OpsRequestClientDebugProcedure, OpsCancelClientDebugProcedure, OpsListClientDebugProcedure} {
		for _, secret := range []string{ro, op, ad} {
			if got := e.code(path, secret); got != 403 {
				t.Errorf("%s over the API: %d", path, got)
			}
		}
	}
}
