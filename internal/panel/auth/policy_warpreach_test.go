//go:build !js

package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/warpreach/v1/warpreachv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The owner's list of WARP reach sites is for the owner only: not a helper, not a read-only admin, and no API token.
func TestWarpReachServiceIsOwnerOnly(t *testing.T) {
	for _, p := range []string{warpreachv1connect.WarpReachServiceGetWarpReachSettingsProcedure, warpreachv1connect.WarpReachServiceUpdateWarpReachSettingsProcedure} {
		l, ok := procedureLevels[p]
		if !ok || l != levelOwner {
			t.Errorf("%s level = %v (listed %v), want owner", p, l, ok)
		}
		if roleAllows(store.RoleHelper, l) || roleAllows(store.RoleReadonly, l) {
			t.Errorf("%s reaches a helper or a read-only admin", p)
		}
		if _, ok := tokenProcedures[p]; ok {
			t.Errorf("%s is reachable with an API token", p)
		}
	}
}
