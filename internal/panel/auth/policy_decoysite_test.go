//go:build !js

package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/decoysite/v1/decoysitev1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Every call of the decoy library is the owner's and closed to API tokens and the MCP.
func TestDecoySiteServiceIsOwnerOnly(t *testing.T) {
	var n int
	for path, l := range procedureLevels {
		if len(path) < len(decoysitev1connect.DecoySiteServiceName)+2 || path[1:len(decoysitev1connect.DecoySiteServiceName)+1] != decoysitev1connect.DecoySiteServiceName {
			continue
		}
		n++
		if l != levelOwner {
			t.Errorf("%s level = %v, want owner", path, l)
		}
		if roleAllows(store.RoleHelper, l) || roleAllows(store.RoleReadonly, l) {
			t.Errorf("%s reaches a helper or a read-only admin", path)
		}
		if _, allowed := TokenAccess(path); allowed {
			t.Errorf("%s must stay closed to API tokens and MCP", path)
		}
	}
	if n != 17 {
		t.Fatalf("decoy library procedures with a level = %d, want 17", n)
	}
}
