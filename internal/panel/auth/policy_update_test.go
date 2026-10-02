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
		{"CheckPanelUpdate", adminv1connect.UpdateServiceCheckPanelUpdateProcedure, true, true},
		{"InstallPanelUpdate", adminv1connect.UpdateServiceInstallPanelUpdateProcedure, true, false},
		{"StartRollout", adminv1connect.UpdateServiceStartRolloutProcedure, true, false},
		{"PauseRollout", adminv1connect.UpdateServicePauseRolloutProcedure, true, false},
		{"ResumeRollout", adminv1connect.UpdateServiceResumeRolloutProcedure, true, false},
		{"CancelRollout", adminv1connect.UpdateServiceCancelRolloutProcedure, true, false},
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
}
