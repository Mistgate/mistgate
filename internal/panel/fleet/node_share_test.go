package fleet

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/provision"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func (e *env) createNode(req *adminv1.CreateEnrollmentRequest) (*adminv1.Node, error) {
	e.t.Helper()
	resp, err := nodeService{e.f}.CreateEnrollment(e.ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg.Node, nil
}

func (e *env) sharing(name string) *adminv1.NodeShareSummary {
	e.t.Helper()
	resp, err := nodeService{e.f}.ListNodes(e.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
	if err != nil {
		e.t.Fatal(err)
	}
	for _, n := range resp.Msg.Nodes {
		if n.Name == name {
			return n.Sharing
		}
	}
	e.t.Fatalf("no node %s", name)
	return nil
}

// The first node of a panel is handed to every group and each later one to none, unless the request says; the answer of the
// create call and the nodes list both carry it.
func TestNewNodeIsHandedOut(t *testing.T) {
	e := newEnv(t)
	e.exec(`INSERT INTO user_group (id, name, created_at) VALUES ('grp_family', 'family', 1), ('grp_friends', 'friends', 1)`)

	de1, err := e.createNode(&adminv1.CreateEnrollmentRequest{Name: "de1", Address: "de1.example.com"})
	if err != nil || de1.Sharing.GetMode() != adminv1.NodeShareMode_NODE_SHARE_MODE_ALL || de1.Sharing.GroupsReceiving != 2 || de1.Sharing.GroupsTotal != 2 {
		t.Fatalf("first node: %v %v", de1.GetSharing(), err)
	}
	nl1, err := e.createNode(&adminv1.CreateEnrollmentRequest{Name: "nl1", Address: "nl1.example.com"})
	if err != nil || nl1.Sharing.GetMode() != adminv1.NodeShareMode_NODE_SHARE_MODE_NONE || nl1.Sharing.GroupsReceiving != 0 || nl1.Sharing.Idle {
		t.Fatalf("second node: %v %v", nl1.GetSharing(), err)
	}
	fi1, err := e.createNode(&adminv1.CreateEnrollmentRequest{Name: "fi1", Address: "fi1.example.com",
		Sharing: &adminv1.NodeSharing{Mode: adminv1.NodeShareMode_NODE_SHARE_MODE_LISTED, GroupIds: []string{"grp_friends"}}})
	if err != nil || fi1.Sharing.GetMode() != adminv1.NodeShareMode_NODE_SHARE_MODE_LISTED || fi1.Sharing.GroupsReceiving != 1 {
		t.Fatalf("listed node: %v %v", fi1.GetSharing(), err)
	}
	se1, err := e.createNode(&adminv1.CreateEnrollmentRequest{Name: "se1", Address: "se1.example.com", Sharing: &adminv1.NodeSharing{Mode: adminv1.NodeShareMode_NODE_SHARE_MODE_ALL}})
	if err != nil || se1.Sharing.GetMode() != adminv1.NodeShareMode_NODE_SHARE_MODE_ALL {
		t.Fatalf("a later node asked for everyone: %v %v", se1.GetSharing(), err)
	}
	if got := e.sharing("fi1"); got.GroupsReceiving != 1 || got.ChangedUnix == 0 {
		t.Errorf("the list: %v", got)
	}
	// the audit says how it ended up, not the groups of a node that asked for none
	if n := e.count(`SELECT count(*) FROM audit WHERE action = 'node.create_enrollment' AND params LIKE '%"sharing":"none"%'`); n != 1 {
		t.Errorf("audit rows that say none: %d", n)
	}
	if n := e.count(`SELECT count(*) FROM audit WHERE action = 'node.create_enrollment' AND params LIKE '%"groups":"grp_friends"%' AND params LIKE '%"sharing":"listed"%'`); n != 1 {
		t.Errorf("audit rows that say listed with a group: %d", n)
	}
}

func TestNewNodeShareRefusals(t *testing.T) {
	e := newEnv(t)
	e.exec(`INSERT INTO user_group (id, name, created_at) VALUES ('grp_family', 'family', 1)`)
	old := e.enroll("de1")
	for name, req := range map[string]*adminv1.CreateEnrollmentRequest{
		"a group that is not there": {Name: "nl1", Address: "nl1.example.com", Sharing: &adminv1.NodeSharing{Mode: adminv1.NodeShareMode_NODE_SHARE_MODE_LISTED, GroupIds: []string{"grp_gone"}}},
		"listed without a group":    {Name: "nl1", Address: "nl1.example.com", Sharing: &adminv1.NodeSharing{Mode: adminv1.NodeShareMode_NODE_SHARE_MODE_LISTED}},
		"groups with all":           {Name: "nl1", Address: "nl1.example.com", Sharing: &adminv1.NodeSharing{Mode: adminv1.NodeShareMode_NODE_SHARE_MODE_ALL, GroupIds: []string{"grp_family"}}},
		"groups without a mode":     {Name: "nl1", Address: "nl1.example.com", Sharing: &adminv1.NodeSharing{GroupIds: []string{"grp_family"}}},
		"sharing on a re-enrolment": {NodeId: old.nodeID, Sharing: &adminv1.NodeSharing{Mode: adminv1.NodeShareMode_NODE_SHARE_MODE_NONE}},
	} {
		if _, err := e.createNode(req); err == nil || (connect.CodeOf(err) != connect.CodeInvalidArgument && connect.CodeOf(err) != connect.CodeNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := e.count(`SELECT count(*) FROM node`); n != 1 {
		t.Errorf("a refused request made a node: %d nodes", n)
	}
	// a re-enrolment without a share is fine and leaves the node's share alone
	if _, err := e.createNode(&adminv1.CreateEnrollmentRequest{NodeId: old.nodeID}); err != nil {
		t.Errorf("re-enrolment: %v", err)
	}
}

// A node made by an SSH install is handed out as the job asked, decided when the node is made.
func TestProvisionedNodeIsHandedOut(t *testing.T) {
	e := newEnv(t)
	e.exec(`INSERT INTO user_group (id, name, created_at) VALUES ('grp_family', 'family', 1)`)
	now := time.Now().UTC()
	add := func(id, name string, c store.NodeShareChoice) {
		t.Helper()
		_, _, err := e.f.CreateProvisionEnrollment(e.ctx, provision.NodeSpec{ID: id, Name: name, Address: name + ".example.com", Sharing: c}, "adm_test", now, now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
	}
	add("nod_de1", "de1", store.NodeShareChoice{})
	add("nod_nl1", "nl1", store.NodeShareChoice{})
	add("nod_fi1", "fi1", store.NodeShareChoice{Mode: store.ShareListed, GroupIDs: []string{"grp_family"}})
	for name, want := range map[string]adminv1.NodeShareMode{"de1": adminv1.NodeShareMode_NODE_SHARE_MODE_ALL, "nl1": adminv1.NodeShareMode_NODE_SHARE_MODE_NONE, "fi1": adminv1.NodeShareMode_NODE_SHARE_MODE_LISTED} {
		if got := e.sharing(name).Mode; got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
	// resuming the job asks again with the same node: the share it already has stays
	add("nod_nl1", "nl1", store.NodeShareChoice{Mode: store.ShareAll})
	if got := e.sharing("nl1").Mode; got != adminv1.NodeShareMode_NODE_SHARE_MODE_NONE {
		t.Errorf("a resumed install changed the share: %v", got)
	}
}

// idle is a label for a node with a running server that nobody has received for more than a day, and nothing else.
func TestIdleNodeLabel(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	active := store.NodeRow{State: "active"}
	running := map[string]string{"inb_1": "active"}
	held := store.NodeShare{Hold: true, Groups: 2, ChangedAt: now.Add(-25 * time.Hour)}
	for _, c := range []struct {
		name    string
		n       store.NodeRow
		enabled map[string]string
		share   store.NodeShare
		want    bool
	}{
		{"held for more than a day with a server", active, running, held, true},
		{"held for less than a day", active, running, store.NodeShare{Hold: true, Groups: 2, ChangedAt: now.Add(-23 * time.Hour)}, false},
		{"no server on it", active, nil, held, false},
		{"not enrolled yet", store.NodeRow{State: "pending"}, running, held, false},
		{"given to a group", active, running, store.NodeShare{Hold: true, Receiving: 1, Groups: 2, ChangedAt: held.ChangedAt}, false},
		{"handed to everyone", active, running, store.NodeShare{Groups: 2, Receiving: 2, ChangedAt: held.ChangedAt}, false},
	} {
		if got := shareMsg(c.n, c.enabled, c.share, now).Idle; got != c.want {
			t.Errorf("%s: idle = %v", c.name, got)
		}
	}
	if m := shareMsg(active, running, held, now); !strings.Contains(m.Mode.String(), "NONE") || m.GroupsTotal != 2 {
		t.Errorf("message: %v", m)
	}
}
