package fleet

import (
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// A write answers with the same card the list shows.
func TestWriteCardIsTheListCard(t *testing.T) {
	x, a := newL3Env(t)
	res, err := (nodeService{x.f}).UpdateNode(x.ctx, connect.NewRequest(&adminv1.UpdateNodeRequest{NodeId: a.nodeID, Notes: ptr("moved")}))
	if err != nil {
		t.Fatal(err)
	}
	list, err := (nodeService{x.f}).ListNodes(x.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var listed *adminv1.Node
	for _, n := range list.Msg.Nodes {
		if n.Id == a.nodeID {
			listed = n
		}
	}
	if listed == nil || !proto.Equal(listed, res.Msg.Node) {
		t.Fatalf("list card %v, write card %v", listed, res.Msg.Node)
	}
}
