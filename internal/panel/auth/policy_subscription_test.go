package auth

import (
	"context"
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The subscription page every user sees: any token reads it; a change goes only through the owner's approved plan of a
// subscription_app tool, and that grant opens nothing else.
func TestSubscriptionSettingsThroughTheOwnersApprovalOnly(t *testing.T) {
	e := newTokenEnv(t)
	ro, roSecret := e.mkToken("ro", store.ProfileReadonly, 600)
	op, opSecret := e.mkToken("op", store.ProfileOperator, 600)
	_, adSecret := e.mkToken("ad", store.ProfileAdmin, 600)
	get, update := adminv1connect.SubscriptionServiceGetSubscriptionSettingsProcedure, adminv1connect.SubscriptionServiceUpdateSubscriptionSettingsProcedure
	mcp := func(planID string) context.Context {
		return e.s.WithApprovedStepUp(WithChannel(context.Background(), ChannelMCP), planID)
	}

	if got := e.code(get, roSecret); got != 200 {
		t.Errorf("a read-only token reads the settings: %d", got)
	}
	for _, s := range []string{opSecret, adSecret} {
		if got := e.code(update, s); got != 403 {
			t.Errorf("UpdateSubscriptionSettings over /api with a token: %d", got)
		}
		if got := e.code(update, s, callOpts{ctx: WithChannel(context.Background(), ChannelMCP)}); got != 403 {
			t.Errorf("UpdateSubscriptionSettings over MCP without a plan: %d", got)
		}
	}
	for _, tool := range []string{"subscription_app_upsert", "subscription_app_remove"} {
		p := e.planFor(op, tool, true, "applying")
		if got := e.code(update, opSecret, callOpts{ctx: mcp(p.ID)}); got != 200 {
			t.Errorf("an operator's approved %s plan: %d", tool, got)
		}
		if got := e.code(pStartRollout, opSecret, callOpts{ctx: mcp(p.ID)}); got != 403 {
			t.Errorf("a %s grant on StartRollout: %d", tool, got)
		}
	}
	// the level still applies: a read-only token's plan opens nothing
	if got := e.code(update, roSecret, callOpts{ctx: mcp(e.planFor(ro, "subscription_app_upsert", true, "applying").ID)}); got != 403 {
		t.Errorf("a read-only token with an approved plan: %d", got)
	}
	// another tool's grant does not open the settings, and a plan nobody approved opens nothing
	if got := e.code(update, opSecret, callOpts{ctx: mcp(e.planFor(op, "user_disable", true, "applying").ID)}); got != 403 {
		t.Errorf("a user_disable grant on the settings: %d", got)
	}
	if got := e.code(update, opSecret, callOpts{ctx: mcp(e.planFor(op, "subscription_app_upsert", true, "awaiting").ID)}); got != 403 {
		t.Errorf("an awaiting plan: %d", got)
	}
}
