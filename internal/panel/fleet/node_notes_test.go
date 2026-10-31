package fleet

import (
	"testing"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func TestNodeNotesInAdminList(t *testing.T) {
	e := newEnv(t)
	id, _, _ := e.createEnrollment("de1", "203.0.113.10")
	otherID, _, _ := e.createEnrollment("fi1", "203.0.113.11")
	nodes, _ := e.adminClients()

	for _, notes := range []string{"Renew in March\n备用节点", ""} {
		updated, err := nodes.UpdateNode(e.ctx, connect.NewRequest(&adminv1.UpdateNodeRequest{NodeId: id, Notes: &notes}))
		if err != nil {
			t.Fatal(err)
		}
		if updated.Msg.Node.Notes != notes {
			t.Errorf("updated notes = %q, want %q", updated.Msg.Node.Notes, notes)
		}
		listed, err := nodes.ListNodes(e.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		byID := map[string]string{}
		for _, n := range listed.Msg.Nodes {
			byID[n.Id] = n.Notes
		}
		if len(byID) != 2 || byID[id] != notes || byID[otherID] != "" {
			t.Errorf("listed notes = %#v, want only %s to have %q", byID, id, notes)
		}
		detail, err := nodes.GetNode(e.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: id}))
		if err != nil {
			t.Fatal(err)
		}
		if detail.Msg.Notes != notes || detail.Msg.Node.Notes != notes {
			t.Errorf("detail notes = %q / %q, want %q", detail.Msg.Notes, detail.Msg.Node.Notes, notes)
		}
	}
}
