package auth

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// An API token and an MCP token (synthetic ids "token:" and "mcp:", with the owner role of an admin-profile token), a helper
// and a readonly admin are refused as the owner; a token and nobody at all are refused as a signed-in admin too.
func TestOwnerAndSignedInSession(t *testing.T) {
	token := store.Admin{ID: "token:tok_1", Role: store.RoleOwner}
	mcp := store.Admin{ID: "mcp:tok_1", Role: store.RoleOwner}
	helper := store.Admin{ID: "adm_h", Role: store.RoleHelper}
	readonly := store.Admin{ID: "adm_r", Role: store.RoleReadonly}
	owner := store.Admin{ID: "adm_o", Role: store.RoleOwner}
	flagged := context.WithValue(WithAdmin(context.Background(), owner), principalKey{}, Principal{Token: true})

	for name, ctx := range map[string]context.Context{
		"token":          WithAdmin(context.Background(), token),
		"mcp":            WithAdmin(context.Background(), mcp),
		"helper":         WithAdmin(context.Background(), helper),
		"readonly":       WithAdmin(context.Background(), readonly),
		"owner, flagged": flagged,
		"nobody":         context.Background(),
	} {
		if _, err := OwnerSession(ctx); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("OwnerSession(%s) = %v", name, err)
		}
	}
	for name, ctx := range map[string]context.Context{
		"token": WithAdmin(context.Background(), token), "mcp": WithAdmin(context.Background(), mcp),
		"owner, flagged": flagged, "nobody": context.Background(),
	} {
		if _, err := SignedInSession(ctx); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("SignedInSession(%s) = %v", name, err)
		}
	}
	if a, err := OwnerSession(WithAdmin(context.Background(), owner)); err != nil || a.ID != owner.ID {
		t.Errorf("the owner: %v, %v", a, err)
	}
	if a, err := SignedInSession(WithAdmin(context.Background(), helper)); err != nil || a.ID != helper.ID {
		t.Errorf("a helper is signed in: %v, %v", a, err)
	}
}
