//go:build !js

package auth

import "github.com/mistgate/mistgate/gen/mistgate/warpreach/v1/warpreachv1connect"

// The WarpReachService (proto/mistgate/warpreach/v1, the owner's list of sites WARP is checked against) is a package of its own,
// kept out of the Cloudflare Workers (the app mounts a refusing stub there). Owner only, in the signed-in UI or the API: no token
// reaches it.
func init() {
	for _, p := range []string{
		warpreachv1connect.WarpReachServiceGetWarpReachSettingsProcedure,
		warpreachv1connect.WarpReachServiceUpdateWarpReachSettingsProcedure,
	} {
		procedureLevels[p] = levelOwner
	}
}
