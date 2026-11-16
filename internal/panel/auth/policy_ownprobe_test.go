//go:build !js

package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/ownprobe/v1/ownprobev1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The owner-managed probes: the strings of policy_ownprobe.go are the generated names; every procedure is the owner's, no token
// profile reaches one, and the ones that create, revoke or switch probes ask for a fresh sign-in.
func TestOwnProbeProceduresAreOwnerOnlyAndClosedToTokens(t *testing.T) {
	for got, want := range map[string]string{
		OwnProbeGetProcedure:            ownprobev1connect.OwnProbeServiceGetProcedure,
		OwnProbeUpdateProcedure:         ownprobev1connect.OwnProbeServiceUpdateProcedure,
		OwnProbeCreateProcedure:         ownprobev1connect.OwnProbeServiceCreateProcedure,
		OwnProbeRevokeProcedure:         ownprobev1connect.OwnProbeServiceRevokeProcedure,
		OwnProbeConfirmNetworkProcedure: ownprobev1connect.OwnProbeServiceConfirmNetworkProcedure,
		OwnProbeCheckNowProcedure:       ownprobev1connect.OwnProbeServiceCheckNowProcedure,
		OwnProbeDisconnectProcedure:     ownprobev1connect.OwnProbeServiceDisconnectProcedure,
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
		if _, ok := TokenAccess(want); ok {
			t.Errorf("%s is open to tokens", want)
		}
		stepUp := want == ownprobev1connect.OwnProbeServiceUpdateProcedure || want == ownprobev1connect.OwnProbeServiceCreateProcedure ||
			want == ownprobev1connect.OwnProbeServiceRevokeProcedure || want == ownprobev1connect.OwnProbeServiceDisconnectProcedure
		if stepUpProcedures[want] != stepUp {
			t.Errorf("%s needs a step-up: %v, want %v", want, stepUpProcedures[want], stepUp)
		}
	}
}

func TestOwnProbeProceduresOverTheWire(t *testing.T) {
	e := newTokenEnv(t)
	_, ro := e.mkToken("ro", store.ProfileReadonly, 600)
	_, op := e.mkToken("op", store.ProfileOperator, 600)
	_, ad := e.mkToken("ad", store.ProfileAdmin, 600)
	for _, path := range []string{OwnProbeGetProcedure, OwnProbeUpdateProcedure, OwnProbeCreateProcedure, OwnProbeRevokeProcedure,
		OwnProbeConfirmNetworkProcedure, OwnProbeCheckNowProcedure, OwnProbeDisconnectProcedure} {
		for _, secret := range []string{ro, op, ad} {
			if got := e.code(path, secret); got != 403 {
				t.Errorf("%s over the API: %d", path, got)
			}
		}
	}
}
