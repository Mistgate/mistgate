package auth

import (
	"context"
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The client catalogue: the list of devices below the line is a read for every token (the MCP tool outdated_devices); the table
// itself is read in the panel only, and changing it is the owner's at the panel with a step-up: no token, no MCP session and no
// plan reaches it.
func TestClientCompatReadsForTokensAndChangesForTheOwnerOnly(t *testing.T) {
	e := newTokenEnv(t)
	_, roSecret := e.mkToken("ro", store.ProfileReadonly, 600)
	op, opSecret := e.mkToken("op", store.ProfileOperator, 600)
	_, adSecret := e.mkToken("ad", store.ProfileAdmin, 600)
	list, get, update := adminv1connect.ClientCompatServiceListOutdatedDevicesProcedure, adminv1connect.ClientCompatServiceGetClientCompatProcedure, adminv1connect.ClientCompatServiceUpdateClientCompatProcedure
	mcp := func(planID string) context.Context {
		return e.s.WithApprovedStepUp(WithChannel(context.Background(), ChannelMCP), planID)
	}

	for _, s := range []string{roSecret, opSecret, adSecret} {
		if got := e.code(list, s); got != 200 {
			t.Errorf("ListOutdatedDevices with a token: %d", got)
		}
		if got := e.code(get, s); got != 403 {
			t.Errorf("GetClientCompat with a token: %d", got) // the table is for the panel, not for tokens
		}
		if got := e.code(update, s); got != 403 {
			t.Errorf("UpdateClientCompat over /api with a token: %d", got)
		}
		if got := e.code(update, s, callOpts{ctx: WithChannel(context.Background(), ChannelMCP)}); got != 403 {
			t.Errorf("UpdateClientCompat over MCP without a plan: %d", got)
		}
	}
	// no approved plan of any tool opens it either
	if got := e.code(update, opSecret, callOpts{ctx: mcp(e.planFor(op, "subscription_app_upsert", true, "applying").ID)}); got != 403 {
		t.Errorf("a subscription_app grant on the client table: %d", got)
	}
	// the update is a step-up procedure and an owner-level one
	if !stepUpProcedures[update] || levelOf(update) != levelOwner {
		t.Errorf("UpdateClientCompat: step-up %v, level %v", stepUpProcedures[update], levelOf(update))
	}
	if levelOf(list) != levelRead || levelOf(get) != levelRead {
		t.Errorf("the reads are levels %v and %v", levelOf(list), levelOf(get))
	}
}
