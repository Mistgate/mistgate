//go:build !js

package mcp

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// nodeSharingV is who a node is handed out to: node_get and nodes_get carry it. The groups are those whose disabled list lacks the
// node; people are the people of a group (every one of them, whatever app or nodes they picked).
type nodeSharingV struct {
	Mode            string `json:"mode"` // all, listed or none
	GroupsReceiving uint32 `json:"groups_receiving"`
	GroupsTotal     uint32 `json:"groups_total"`
	PeopleReceiving uint32 `json:"people_receiving"`
	ChangedUnix     int64  `json:"changed_unix,omitempty"`
	// Idle: nobody receives a node that has a running server, for more than a day. A label, not an alert.
	Idle   bool             `json:"idle,omitempty"`
	Groups []nodeSharingGrV `json:"groups,omitempty"`
}

type nodeSharingGrV struct {
	GroupID  string `json:"group_id"`
	Group    string `json:"group"`
	Receives bool   `json:"receives"`
	People   uint32 `json:"people"`
}

func nodeSharingView(n *adminv1.Node, groups []*adminv1.Group) *nodeSharingV {
	sum := n.GetSharing()
	v := &nodeSharingV{Mode: enumName("NODE_SHARE_MODE_", sum.GetMode().String()), GroupsReceiving: sum.GetGroupsReceiving(), GroupsTotal: sum.GetGroupsTotal(),
		ChangedUnix: sum.GetChangedUnix(), Idle: sum.GetIdle()}
	for i, g := range groups {
		on := !slices.Contains(g.GetDisabledNodeIds(), n.GetId())
		if on {
			v.PeopleReceiving += g.GetUserCount() + g.GetExtraUserCount() // a person in two groups that receive the node is counted twice here
		}
		if i < maxIDs {
			v.Groups = append(v.Groups, nodeSharingGrV{GroupID: clean(g.GetId(), 60), Group: nm(g.GetName()), Receives: on, People: g.GetUserCount() + g.GetExtraUserCount()})
		}
	}
	return v
}

type nodeSharingSetArgs struct {
	ReasonField
	Node       string   `json:"node" jsonschema:"node id or exact name"`
	Mode       string   `json:"mode" jsonschema:"all (every group, groups made later too), listed (only group_ids; groups made later do not get it) or none (no group)"`
	GroupIDs   []string `json:"group_ids,omitempty" jsonschema:"with mode listed: ids of the groups that receive the node, from groups_list"`
	NodeID     string   `json:"node_id,omitempty" jsonschema:"-"`
	BeforeHash string   `json:"before_hash,omitempty" jsonschema:"-"`
}

var shareModes = map[string]adminv1.NodeShareMode{
	"all": adminv1.NodeShareMode_NODE_SHARE_MODE_ALL, "listed": adminv1.NodeShareMode_NODE_SHARE_MODE_LISTED, "none": adminv1.NodeShareMode_NODE_SHARE_MODE_NONE,
}

func (a nodeSharingSetArgs) request(dryRun bool) *adminv1.SetNodeSharingRequest {
	return &adminv1.SetNodeSharingRequest{NodeId: a.NodeID, Mode: shareModes[a.Mode], GroupIds: a.GroupIDs, DryRun: dryRun}
}

// nodeShareNow is how a node is handed out now: its mode and the ids of the groups that receive it.
func (c *call) nodeShareNow(nodeID string) (mode string, receiving []string, err error) {
	nodes, err := c.listNodes()
	if err != nil {
		return "", nil, err
	}
	i := slices.IndexFunc(nodes, func(n *adminv1.Node) bool { return n.GetId() == nodeID })
	if i < 0 {
		return "", nil, errors.New("node not found")
	}
	groups, err := c.groups()
	if err != nil {
		return "", nil, err
	}
	for _, g := range groups {
		if !slices.Contains(g.GetDisabledNodeIds(), nodeID) {
			receiving = append(receiving, g.GetId())
		}
	}
	slices.Sort(receiving)
	return enumName("NODE_SHARE_MODE_", nodes[i].GetSharing().GetMode().String()), receiving, nil
}

// sharingHash is the state a plan was made against.
func sharingHash(mode string, receiving []string) string {
	return hashText(mode + "|" + strings.Join(receiving, ","))
}

var nodeSharingSet = changeSpec[nodeSharingSetArgs]{
	name: "node_sharing_set", min: ProfileOperator, danger: true,
	procs: procs(adminv1connect.GroupServiceSetNodeSharingProcedure, adminv1connect.GroupServiceListGroupsProcedure,
		adminv1connect.NodeServiceListNodesProcedure),
	desc: "Choose which groups a node is handed out to: all (every group, groups made later too), listed (only group_ids; groups made later do not get it) or none (no group, until the owner gives it to one). " +
		"It changes what people receive: the plan says how many people gain and lose the node. The first node of a panel goes to every group and each later one to none; this changes that later. Always waits for the owner.",
	plan: func(c *call, a nodeSharingSetArgs) (*planned, error) {
		id, name, err := c.nodeRef(a.Node)
		if err != nil {
			return nil, err
		}
		a.Mode = strings.ToLower(strings.TrimSpace(a.Mode))
		mode, ok := shareModes[a.Mode]
		if !ok {
			return nil, errors.New("mode must be all, listed or none")
		}
		groupIDs, err := uniqueIDs("group_ids", a.GroupIDs, true)
		if err != nil {
			return nil, err
		}
		if (mode == adminv1.NodeShareMode_NODE_SHARE_MODE_LISTED) != (len(groupIDs) > 0) {
			return nil, errors.New("group_ids go with the mode listed, and the mode listed needs at least one")
		}
		groups, err := c.groups()
		if err != nil {
			return nil, err
		}
		for _, gid := range groupIDs {
			if !slices.ContainsFunc(groups, func(g *adminv1.Group) bool { return g.GetId() == gid }) {
				return nil, errors.New("group_id is not a group of this panel")
			}
		}
		a.NodeID, a.GroupIDs = id, groupIDs
		was, receiving, err := c.nodeShareNow(id)
		if err != nil {
			return nil, err
		}
		r, err := c.cl.Group.SetNodeSharing(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(a.request(true)))
		if err != nil {
			return nil, apiError(err)
		}
		var names []string
		for _, g := range groups {
			if slices.Contains(r.Msg.GetGroupIds(), g.GetId()) {
				names = append(names, nm(g.GetName()))
			}
		}
		facts := []Fact{
			{Key: "node", Value: nm(name), Untrusted: true},
			changeFact("sharing", was+" -> "+a.Mode, was, a.Mode),
			{Key: "groups_receiving", Value: listOrNone(names), Untrusted: true},
			codedFact("people", fmt.Sprintf("%d people gain the node, %d lose it", r.Msg.GetPeopleGain(), r.Msg.GetPeopleLose()), "node_sharing_people",
				"gain", strconv.FormatUint(uint64(r.Msg.GetPeopleGain()), 10), "lose", strconv.FormatUint(uint64(r.Msg.GetPeopleLose()), 10)),
		}
		a.BeforeHash = sharingHash(was, receiving)
		return &planned{Summary: "Change which groups the node is handed out to. The owner must approve this change before it is applied.", Facts: facts,
			Danger: []string{dangerFleet}, Params: a}, nil
	},
	apply: func(c *call, a nodeSharingSetArgs, _ Plan) (done, error) {
		mode, receiving, err := c.nodeShareNow(a.NodeID)
		if err != nil {
			return done{}, err
		}
		if sharingHash(mode, receiving) != a.BeforeHash {
			return done{}, failure("changed_since_plan", "the node's sharing changed after the plan; make a new plan")
		}
		if _, err := c.cl.Group.SetNodeSharing(c.ctx, connect.NewRequest(a.request(false))); err != nil {
			return done{}, apiError(err)
		}
		return doneWith("node_sharing_set", "The node's sharing was updated."), nil
	},
}
