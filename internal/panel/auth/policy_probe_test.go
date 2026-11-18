package auth

import (
	"slices"
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
		PathSpeedProcedure:    probev1connect.ProbeServicePathSpeedProcedure,

		PathSpeedResultsProcedure: probev1connect.ProbeServicePathSpeedResultsProcedure,

		ChannelMeasurePlanProcedure:   probev1connect.ProbeServiceChannelMeasurePlanProcedure,
		ChannelMeasureStartProcedure:  probev1connect.ProbeServiceChannelMeasureStartProcedure,
		ChannelMeasureStatusProcedure: probev1connect.ProbeServiceChannelMeasureStatusProcedure,
		ChannelMeasureCancelProcedure: probev1connect.ProbeServiceChannelMeasureCancelProcedure,
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

// The kept figures of a speed test move nothing: any signed-in admin reads them, and a token reads them directly.
func TestPathSpeedResultsAreReadOnly(t *testing.T) {
	path := probev1connect.ProbeServicePathSpeedResultsProcedure
	if got := ProcedureRole(path); got != store.RoleReadonly {
		t.Errorf("ProcedureRole = %q, want readonly", got)
	}
	if got, ok := TokenAccess(path); !ok || got != TokenAccessDirect {
		t.Errorf("TokenAccess = %v, %v; want direct", got, ok)
	}
}

// path_speed moves bulk traffic between two nodes: the owner only, and for a token only through the approved plan of path_speed
// (the grant of any other tool cannot open it, and a bare token cannot call it).
func TestPathSpeedProcedureNeedsTheOwnersApproval(t *testing.T) {
	path := probev1connect.ProbeServicePathSpeedProcedure
	if got := ProcedureRole(path); got != store.RoleOwner {
		t.Errorf("ProcedureRole = %q, want owner", got)
	}
	if got, ok := TokenAccess(path); !ok || got != TokenAccessApproved {
		t.Errorf("TokenAccess = %v, %v; want approved (never direct)", got, ok)
	}
	if got := grantProcedures["path_speed"]; len(got) != 1 || got[0] != path {
		t.Errorf("the path_speed grant = %v, want only PathSpeed", got)
	}
	for tool, paths := range grantProcedures {
		if tool != "path_speed" && slices.Contains(paths, path) {
			t.Errorf("the plan of %s may open PathSpeed", tool)
		}
	}
}

// The channel measure fills the links of a node and its partners: the owner starts and stops it, a token starts it only through the
// approved plan of channel_measure (and no other tool's grant opens it), never stops it; the plan and the status are plain reads.
func TestChannelMeasureProceduresNeedTheOwnersApproval(t *testing.T) {
	start := probev1connect.ProbeServiceChannelMeasureStartProcedure
	if got := ProcedureRole(start); got != store.RoleOwner {
		t.Errorf("Start: ProcedureRole = %q, want owner", got)
	}
	if got, ok := TokenAccess(start); !ok || got != TokenAccessApproved {
		t.Errorf("Start: TokenAccess = %v, %v; want approved (never direct)", got, ok)
	}
	if got := grantProcedures["channel_measure"]; len(got) != 1 || got[0] != start {
		t.Errorf("the channel_measure grant = %v, want only ChannelMeasureStart", got)
	}
	for tool, paths := range grantProcedures {
		for _, p := range []string{start, probev1connect.ProbeServiceChannelMeasureCancelProcedure} {
			if tool != "channel_measure" && slices.Contains(paths, p) {
				t.Errorf("the plan of %s may open %s", tool, p)
			}
		}
	}
	cancel := probev1connect.ProbeServiceChannelMeasureCancelProcedure
	if got := ProcedureRole(cancel); got != store.RoleOwner {
		t.Errorf("Cancel: ProcedureRole = %q, want owner", got)
	}
	if _, ok := TokenAccess(cancel); ok {
		t.Error("a token may call ChannelMeasureCancel")
	}
	for _, path := range []string{probev1connect.ProbeServiceChannelMeasurePlanProcedure, probev1connect.ProbeServiceChannelMeasureStatusProcedure} {
		if got := ProcedureRole(path); got != store.RoleReadonly {
			t.Errorf("%s: ProcedureRole = %q, want readonly", path, got)
		}
		if got, ok := TokenAccess(path); !ok || got != TokenAccessDirect {
			t.Errorf("%s: TokenAccess = %v, %v; want direct", path, got, ok)
		}
	}
}
