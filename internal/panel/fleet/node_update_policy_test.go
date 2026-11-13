package fleet

import (
	"slices"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The resolvers the agent's engines use are the owner's call, like the address; a helper may send the stored list back.
func TestUpdateNodeResolversAreOwnerOnly(t *testing.T) {
	x, a := newL3Env(t)
	service := nodeService{x.f}
	helper := auth.WithAdmin(x.ctx, store.Admin{ID: "adm_bob", Role: store.RoleHelper})
	owner := auth.WithAdmin(x.ctx, store.Admin{ID: "adm_alice", Role: store.RoleOwner})
	update := func(role string, values ...string) error {
		ctx := helper
		if role == "owner" {
			ctx = owner
		}
		_, err := service.UpdateNode(ctx, connect.NewRequest(&adminv1.UpdateNodeRequest{NodeId: a.nodeID, DnsResolvers: &adminv1.DnsResolvers{Values: values}}))
		return err
	}
	if err := update("helper", "203.0.113.9"); code(err) != connect.CodePermissionDenied {
		t.Fatalf("a helper changed the resolvers: %v", err)
	}
	if n, _ := x.st.Node(x.ctx, a.nodeID); len(n.DNSResolvers) != 0 {
		t.Fatalf("stored resolvers = %v", n.DNSResolvers)
	}
	if err := update("owner", "203.0.113.9"); err != nil {
		t.Fatalf("owner: %v", err)
	}
	if err := update("helper", "203.0.113.9"); err != nil { // the same list: a form that saves all its fields
		t.Fatalf("a helper sending the stored list back: %v", err)
	}
	if err := update("helper", "203.0.113.9", "203.0.113.10"); code(err) != connect.CodePermissionDenied {
		t.Fatalf("a helper changed the list: %v", err)
	}
	if n, _ := x.st.Node(x.ctx, a.nodeID); !slices.Equal(n.DNSResolvers, []string{"203.0.113.9"}) {
		t.Fatalf("stored resolvers = %v", n.DNSResolvers)
	}
}
