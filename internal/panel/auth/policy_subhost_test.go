//go:build !js

package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/subhost/v1/subhostv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The subscription host's procedures: the generated names, the owner's alone; every change needs step-up and reaches a token
// only through an approved plan; reading is direct, checking a domain is planning.
func TestSubHostPolicy(t *testing.T) {
	change := map[string]string{
		SubHostSetProcedure:    subhostv1connect.SubscriptionHostServiceSetSubscriptionHostProcedure,
		SubHostMoveProcedure:   subhostv1connect.SubscriptionHostServiceStartSubscriptionHostMoveProcedure,
		SubHostFinishProcedure: subhostv1connect.SubscriptionHostServiceFinishSubscriptionHostChangeProcedure,
		SubHostCancelProcedure: subhostv1connect.SubscriptionHostServiceCancelSubscriptionHostChangeProcedure,
		SubHostOffProcedure:    subhostv1connect.SubscriptionHostServiceDisableSubscriptionHostProcedure,
	}
	read := map[string]string{
		SubHostGetProcedure:   subhostv1connect.SubscriptionHostServiceGetSubscriptionHostProcedure,
		SubHostCheckProcedure: subhostv1connect.SubscriptionHostServiceCheckSubscriptionHostDomainProcedure,
	}
	for got, want := range mapsJoin(change, read) {
		if got != want {
			t.Errorf("policy constant %q differs from the generated procedure %q", got, want)
		}
		if roleAllows(store.RoleHelper, levelOf(want)) || !roleAllows(store.RoleOwner, levelOf(want)) {
			t.Errorf("%s must be the owner's alone", want)
		}
	}
	for p := range change {
		if access, ok := TokenAccess(p); !NeedsStepUp(p) || !ok || access != TokenAccessApproved {
			t.Errorf("%s: step-up %v, token access %v", p, NeedsStepUp(p), access)
		}
	}
	if access, _ := TokenAccess(SubHostGetProcedure); access != TokenAccessDirect {
		t.Error("reading must be direct")
	}
	if access, _ := TokenAccess(SubHostCheckProcedure); access != TokenAccessPlanning {
		t.Error("the domain check must be planning")
	}
}

func mapsJoin(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
