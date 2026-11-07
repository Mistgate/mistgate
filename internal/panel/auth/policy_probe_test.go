package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/probe/v1/probev1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The probes between nodes send test traffic, so a readonly role and a readonly token never start one; the operator profile
// (the helper role) and up may, and a token reaches them directly.
func TestPathProbeProcedurePolicy(t *testing.T) {
	// the strings of policy.go are the generated names (policy.go does not import the generated package: see there)
	for got, want := range map[string]string{
		PathProbeProcedure:    probev1connect.ProbeServicePathProbeProcedure,
		NodeTraceProcedure:    probev1connect.ProbeServiceNodeTraceProcedure,
		NodeDnsCheckProcedure: probev1connect.ProbeServiceNodeDnsCheckProcedure,
	} {
		if got != want {
			t.Errorf("policy constant %q differs from the generated procedure %q", got, want)
		}
	}
	for _, path := range []string{
		probev1connect.ProbeServicePathProbeProcedure,
		probev1connect.ProbeServiceNodeTraceProcedure,
		probev1connect.ProbeServiceNodeDnsCheckProcedure,
	} {
		if got := ProcedureRole(path); got != store.RoleHelper {
			t.Errorf("%s: ProcedureRole = %q, want helper (operator and up)", path, got)
		}
		if got, ok := TokenAccess(path); !ok || got != TokenAccessDirect {
			t.Errorf("%s: TokenAccess = %v, %v; want direct", path, got, ok)
		}
		if NeedsStepUp(path) {
			t.Errorf("%s needs a step-up", path)
		}
	}
}
