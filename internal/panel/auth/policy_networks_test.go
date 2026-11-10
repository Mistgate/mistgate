package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestNetworkProcedurePolicy(t *testing.T) {
	read := adminv1connect.NodeServiceGetNetworksProcedure
	if got := ProcedureRole(read); got != store.RoleReadonly {
		t.Errorf("GetNetworks role = %q, want readonly", got)
	}
	if got, ok := TokenAccess(read); !ok || got != TokenAccessDirect {
		t.Errorf("GetNetworks token access = %v, %v; want direct", got, ok)
	}
	for _, path := range []string{OpsGetNetworkSettingsProcedure, OpsSetNetworkSettingsProcedure} {
		if got := ProcedureRole(path); got != store.RoleOwner {
			t.Errorf("%s role = %q, want owner", path, got)
		}
		if _, ok := TokenAccess(path); ok {
			t.Errorf("%s is open to API tokens", path)
		}
	}
	if !NeedsStepUp(OpsSetNetworkSettingsProcedure) {
		t.Error("SetNetworkSettings does not require step-up")
	}
	if NeedsStepUp(OpsGetNetworkSettingsProcedure) {
		t.Error("GetNetworkSettings requires step-up")
	}
}
