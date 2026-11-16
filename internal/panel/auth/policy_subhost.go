//go:build !js

package auth

import "github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"

// The SubscriptionHostService moves the panel's public address onto a node: the owner's alone, and every change needs a
// fresh sign-in. API tokens and the MCP may read it directly, check a domain while planning, and change it only through an
// approved plan. The Cloudflare edition does not serve it (edition.SubscriptionHost).
func init() {
	read := []string{adminv1connect.SubscriptionHostServiceGetSubscriptionHostProcedure, adminv1connect.SubscriptionHostServiceCheckSubscriptionHostDomainProcedure}
	change := []string{
		adminv1connect.SubscriptionHostServiceSetSubscriptionHostProcedure,
		adminv1connect.SubscriptionHostServiceStartSubscriptionHostMoveProcedure,
		adminv1connect.SubscriptionHostServiceFinishSubscriptionHostChangeProcedure,
		adminv1connect.SubscriptionHostServiceCancelSubscriptionHostChangeProcedure,
		adminv1connect.SubscriptionHostServiceDisableSubscriptionHostProcedure,
	}
	for _, p := range append(read, change...) {
		procedureLevels[p] = levelOwner
	}
	for _, p := range change {
		stepUpProcedures[p] = true
		tokenProcedures[p] = TokenAccessApproved
	}
	tokenProcedures[adminv1connect.SubscriptionHostServiceGetSubscriptionHostProcedure] = TokenAccessDirect
	tokenProcedures[adminv1connect.SubscriptionHostServiceCheckSubscriptionHostDomainProcedure] = TokenAccessPlanning
	grantProcedures["subscription_host_set"] = []string{adminv1connect.SubscriptionHostServiceSetSubscriptionHostProcedure}
	grantProcedures["subscription_host_move"] = []string{
		adminv1connect.SubscriptionHostServiceStartSubscriptionHostMoveProcedure,
		adminv1connect.SubscriptionHostServiceFinishSubscriptionHostChangeProcedure,
		adminv1connect.SubscriptionHostServiceCancelSubscriptionHostChangeProcedure,
	}
	grantProcedures["subscription_host_disable"] = []string{adminv1connect.SubscriptionHostServiceDisableSubscriptionHostProcedure}
}
