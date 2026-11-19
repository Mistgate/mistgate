//go:build !js

package auth

// The SubscriptionHostService (proto/mistgate/subhost/v1, design/adr/0018-panel-behind-nat.md) moves the panel's public address
// onto a node: the owner's alone, and every change needs a fresh sign-in. API tokens and the MCP may read it directly, check a
// domain while planning, and change it only through an approved plan. A package of its own, so the Cloudflare edition (which
// does not offer it, edition.SubscriptionHost) links none of it: the procedures are named here by path, and a test compares
// them with the generated names.
const (
	SubHostGetProcedure    = "/mistgate.subhost.v1.SubscriptionHostService/GetSubscriptionHost"
	SubHostSetProcedure    = "/mistgate.subhost.v1.SubscriptionHostService/SetSubscriptionHost"
	SubHostMoveProcedure   = "/mistgate.subhost.v1.SubscriptionHostService/StartSubscriptionHostMove"
	SubHostFinishProcedure = "/mistgate.subhost.v1.SubscriptionHostService/FinishSubscriptionHostChange"
	SubHostCancelProcedure = "/mistgate.subhost.v1.SubscriptionHostService/CancelSubscriptionHostChange"
	SubHostOffProcedure    = "/mistgate.subhost.v1.SubscriptionHostService/DisableSubscriptionHost"
	SubHostCheckProcedure  = "/mistgate.subhost.v1.SubscriptionHostService/CheckSubscriptionHostDomain"
)

func init() {
	change := []string{SubHostSetProcedure, SubHostMoveProcedure, SubHostFinishProcedure, SubHostCancelProcedure, SubHostOffProcedure}
	for _, p := range append([]string{SubHostGetProcedure, SubHostCheckProcedure}, change...) {
		procedureLevels[p] = levelOwner
	}
	for _, p := range change {
		stepUpProcedures[p] = true
		tokenProcedures[p] = TokenAccessApproved
	}
	tokenProcedures[SubHostGetProcedure] = TokenAccessDirect
	tokenProcedures[SubHostCheckProcedure] = TokenAccessPlanning
	grantProcedures["subscription_host_set"] = []string{SubHostSetProcedure}
	grantProcedures["subscription_host_move"] = []string{SubHostMoveProcedure, SubHostFinishProcedure, SubHostCancelProcedure}
	grantProcedures["subscription_host_disable"] = []string{SubHostOffProcedure, SubHostFinishProcedure, SubHostCancelProcedure}
}
