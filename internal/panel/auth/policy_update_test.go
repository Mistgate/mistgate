package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// UpdateService: everybody reads the page; every change is owner-only (the handler adds the step-up).
func TestUpdateServiceRolePolicy(t *testing.T) {
	for _, tc := range []struct {
		name  string
		path  string
		owner bool
		read  bool
	}{
		{"GetUpdates", adminv1connect.UpdateServiceGetUpdatesProcedure, true, true},
		{"GetOsUpdates", adminv1connect.UpdateServiceGetOsUpdatesProcedure, true, true},
		{"CheckPanelUpdate", adminv1connect.UpdateServiceCheckPanelUpdateProcedure, true, true},
		{"SetUpdateChannel", adminv1connect.UpdateServiceSetUpdateChannelProcedure, true, false},
		{"InstallPanelUpdate", adminv1connect.UpdateServiceInstallPanelUpdateProcedure, true, false},
		{"StartRollout", adminv1connect.UpdateServiceStartRolloutProcedure, true, false},
		{"PauseRollout", adminv1connect.UpdateServicePauseRolloutProcedure, true, false},
		{"ResumeRollout", adminv1connect.UpdateServiceResumeRolloutProcedure, true, false},
		{"CancelRollout", adminv1connect.UpdateServiceCancelRolloutProcedure, true, false},
		{"StartOsRollout", adminv1connect.UpdateServiceStartOsRolloutProcedure, true, false},
		{"CancelOsRollout", adminv1connect.UpdateServiceCancelOsRolloutProcedure, true, false},
		{"RefreshOsUpdates", adminv1connect.UpdateServiceRefreshOsUpdatesProcedure, true, false},
		{"RollbackNode", adminv1connect.UpdateServiceRollbackNodeProcedure, true, false},
		{"RescanBundle", adminv1connect.UpdateServiceRescanBundleProcedure, true, false},
	} {
		if _, listed := procedureLevels[tc.path]; !listed {
			t.Errorf("%s has no explicit level (it would silently be owner-only)", tc.name)
		}
		l := levelOf(tc.path)
		for role, want := range map[string]bool{store.RoleOwner: tc.owner, store.RoleHelper: tc.read, store.RoleReadonly: tc.read} {
			if got := roleAllows(role, l); got != want {
				t.Errorf("%s as %s: allowed = %v, want %v", tc.name, role, got, want)
			}
		}
	}
	if access, ok := TokenAccess(adminv1connect.UpdateServiceGetOsUpdatesProcedure); !ok || access != TokenAccessDirect {
		t.Errorf("GetOsUpdates token access = %d, present %v, want direct", access, ok)
	}
	if NeedsStepUp(adminv1connect.UpdateServiceGetOsUpdatesProcedure) {
		t.Error("GetOsUpdates unexpectedly requires step-up")
	}
	// The OS update of a node: a token only plans it (the owner approves with a step-up); refreshing the lists is the owner's click.
	for _, p := range []string{adminv1connect.UpdateServiceStartOsRolloutProcedure, adminv1connect.UpdateServiceCancelOsRolloutProcedure} {
		if access, ok := TokenAccess(p); !ok || access != TokenAccessApproved || !NeedsStepUp(p) {
			t.Errorf("%s: token access %d (present %v), step-up %v; want approved with a step-up", p, access, ok, NeedsStepUp(p))
		}
	}
	if access, ok := TokenAccess(adminv1connect.UpdateServiceRefreshOsUpdatesProcedure); ok || NeedsStepUp(adminv1connect.UpdateServiceRefreshOsUpdatesProcedure) {
		t.Errorf("RefreshOsUpdates: token access %d (present %v) or a step-up; want neither", access, ok)
	}
	if access, ok := TokenAccess(adminv1connect.UpdateServiceSetUpdateChannelProcedure); ok {
		t.Errorf("SetUpdateChannel is available to tokens with access %d", access)
	}
	if !NeedsStepUp(adminv1connect.UpdateServiceSetUpdateChannelProcedure) {
		t.Error("SetUpdateChannel does not require step-up")
	}
}
