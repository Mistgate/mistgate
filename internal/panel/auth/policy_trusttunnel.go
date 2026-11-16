//go:build !js

package auth

// The TrustTunnelService (proto/mistgate/trusttunnel/v1) is a package of its own so the Cloudflare edition does not link its
// messages; its procedures are named here by path, and a test compares them with the generated names. A tt:// link carries the
// key's secret, so the key calls are the admin's write level like the AmneziaVPN and Telegram key calls, and closed to API tokens
// and the MCP (no tokenProcedures line).
const (
	TrustTunnelGetLinksProcedure  = "/mistgate.trusttunnel.v1.TrustTunnelService/GetTrustTunnelLinks"
	TrustTunnelIssueKeyProcedure  = "/mistgate.trusttunnel.v1.TrustTunnelService/IssueTrustTunnelKey"
	TrustTunnelRevokeKeyProcedure = "/mistgate.trusttunnel.v1.TrustTunnelService/RevokeTrustTunnelKey"
)

func init() {
	for _, p := range []string{TrustTunnelGetLinksProcedure, TrustTunnelIssueKeyProcedure, TrustTunnelRevokeKeyProcedure} {
		procedureLevels[p] = levelWrite
	}
}
