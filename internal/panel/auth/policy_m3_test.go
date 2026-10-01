package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The tunnel protocol services: devices are write access, the AWG helpers are reads, WARP is the owner only (the handler adds
// the step-up to register, import and delete).
func TestM3ServiceRolePolicy(t *testing.T) {
	const (
		write = iota // owner and helper
		read         // every role
		owner        // owner only
	)
	for _, tc := range []struct {
		name string
		path string
		who  int
	}{
		{"CreateAwgDevice", adminv1connect.DeviceServiceCreateAwgDeviceProcedure, write},
		{"GetDeviceConfigs", adminv1connect.DeviceServiceGetDeviceConfigsProcedure, write},
		{"RotateDeviceKeys", adminv1connect.DeviceServiceRotateDeviceKeysProcedure, write},
		{"RenameDevice", adminv1connect.DeviceServiceRenameDeviceProcedure, write},
		{"ListMimicryPresets", adminv1connect.AwgServiceListMimicryPresetsProcedure, read},
		{"GenerateObfuscation", adminv1connect.AwgServiceGenerateObfuscationProcedure, read},
		{"GetWarp", adminv1connect.WarpServiceGetWarpProcedure, owner},
		{"RegisterWarp", adminv1connect.WarpServiceRegisterWarpProcedure, owner},
		{"ImportWarp", adminv1connect.WarpServiceImportWarpProcedure, owner},
		{"SetWarpEnabled", adminv1connect.WarpServiceSetWarpEnabledProcedure, owner},
		{"RefreshWarp", adminv1connect.WarpServiceRefreshWarpProcedure, owner},
		{"DeleteWarp", adminv1connect.WarpServiceDeleteWarpProcedure, owner},
		{"GetWarpRegistrationParams", adminv1connect.WarpServiceGetWarpRegistrationParamsProcedure, owner},
		{"UpdateWarpRegistrationParams", adminv1connect.WarpServiceUpdateWarpRegistrationParamsProcedure, owner},
	} {
		if _, listed := procedureLevels[tc.path]; !listed {
			t.Errorf("%s has no explicit level (it would silently be owner-only)", tc.name)
		}
		l := levelOf(tc.path)
		want := map[string]bool{
			store.RoleOwner:    true,
			store.RoleHelper:   tc.who != owner,
			store.RoleReadonly: tc.who == read,
		}
		for role, w := range want {
			if got := roleAllows(role, l); got != w {
				t.Errorf("%s as %s: allowed = %v, want %v", tc.name, role, got, w)
			}
		}
	}
}
