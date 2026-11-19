package fleet

import (
	"bytes"
	"log/slog"
	"strings"
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

// The write is done by the time the card is read: a card that cannot be read is logged and comes without its counters,
// it does not fail the RPC.
func TestWriteSurvivesAFailedCardRead(t *testing.T) {
	x, a := newL3Env(t)
	var logs bytes.Buffer
	x.f.log = slog.New(slog.NewTextHandler(&logs, nil))
	x.exec(`DROP TABLE user_group_disabled_node`) // NodeShares reads it; the write itself does not
	res, err := (nodeService{x.f}).UpdateNode(x.ctx, connect.NewRequest(&adminv1.UpdateNodeRequest{NodeId: a.nodeID, Notes: ptr("moved")}))
	if err != nil {
		t.Fatalf("a failed card read failed the write: %v", err)
	}
	if res.Msg.Node.Id != a.nodeID || res.Msg.Node.Notes != "moved" {
		t.Fatalf("card = %v", res.Msg.Node)
	}
	if !strings.Contains(logs.String(), "node_card_failed") {
		t.Fatalf("the failed read was not logged: %q", logs.String())
	}
	if _, err := (nodeService{x.f}).ListNodes(x.ctx, connect.NewRequest(&adminv1.ListNodesRequest{})); err == nil {
		t.Fatal("ListNodes answered although the cards cannot be read")
	}
}
