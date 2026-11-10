//go:build !js

package mcp

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

func groupTools() []toolDef {
	return slices.Concat(
		[]toolDef{
			readTool("group_get", ProfileReadonly, procs(adminv1connect.GroupServiceListGroupsProcedure),
				"One user group, including its profiles, excluded nodes, DNS preset, colour, user count and the current server totals available to the group.",
				func(c *call, in groupGetArgs) (any, error) {
					g, err := c.groupByID(in.GroupID)
					if err != nil {
						return nil, err
					}
					return groupView(g), nil
				}),
		},
		change(groupCreate), change(groupUpdate), change(groupDelete),
	)
}

type groupGetArgs struct {
	GroupID string `json:"group_id" jsonschema:"group id from groups_list"`
}

func (c *call) groups() ([]*adminv1.Group, error) {
	r, err := c.cl.Group.ListGroups(c.ctx, connect.NewRequest(&adminv1.ListGroupsRequest{}))
	if err != nil {
		return nil, apiError(err)
	}
	return r.Msg.GetGroups(), nil
}

func (c *call) groupByID(id string) (*adminv1.Group, error) {
	if err := validID("group_id", id); err != nil {
		return nil, err
	}
	gs, err := c.groups()
	if err != nil {
		return nil, err
	}
	for _, g := range gs {
		if g.GetId() == id {
			return g, nil
		}
	}
	return nil, errors.New("group not found")
}

func groupView(g *adminv1.Group) groupV {
	return groupV{ID: clean(g.GetId(), 60), Name: nm(g.GetName()), ProfileIDs: cleanIDs(g.GetProfileIds()),
		DisabledNodeIDs: cleanIDs(g.GetDisabledNodeIds()), DNSPreset: clean(g.GetDnsPresetId(), 60), Color: clean(g.GetColor(), 20),
		UserCount: g.GetUserCount(), HappNodes: g.GetHappNodes(), AmneziaNodes: g.GetAmneziaNodes(), TelegramNodes: g.GetTelegramNodes()}
}

func cleanIDs(ids []string) []string {
	out := make([]string, 0, min(len(ids), maxIDs))
	for _, id := range ids {
		if len(out) == maxIDs {
			break
		}
		out = append(out, clean(id, 60))
	}
	return out
}

type groupCreateArgs struct {
	ReasonField
	Name            string   `json:"name" jsonschema:"group name, 1 to 64 characters"`
	ProfileIDs      []string `json:"profile_ids,omitempty" jsonschema:"profile ids included in the group; empty gives the group no profiles"`
	DNSPresetID     string   `json:"dns_preset_id,omitempty" jsonschema:"DNS preset id; empty inherits the instance default"`
	Color           string   `json:"color,omitempty" jsonschema:"optional palette tone: lavender, sand, sage, rose, sky or mint"`
	DisabledNodeIDs []string `json:"disabled_node_ids,omitempty" jsonschema:"node ids unavailable to users in this group; empty allows all nodes"`
}

func groupName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > 64 || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", errors.New("name must be 1-64 characters without control characters")
	}
	if err := visible("name", s); err != nil {
		return "", err
	}
	if err := rejectSecretShaped("name", s); err != nil {
		return "", err
	}
	return s, nil
}

func groupColor(s string) error {
	if s != "" && !slices.Contains([]string{"lavender", "sand", "sage", "rose", "sky", "mint"}, s) {
		return errors.New("color must be lavender, sand, sage, rose, sky, mint or empty")
	}
	return nil
}

func (c *call) checkGroupRefs(profiles, nodes []string, dnsPreset string) error {
	profileIDs, err := uniqueIDs("profile_ids", profiles, true)
	if err != nil {
		return err
	}
	nodeIDs, err := uniqueIDs("disabled_node_ids", nodes, true)
	if err != nil {
		return err
	}
	if len(profileIDs) > 0 {
		r, err := c.cl.Profile.ListProfiles(c.ctx, connect.NewRequest(&adminv1.ListProfilesRequest{}))
		if err != nil {
			return apiError(err)
		}
		for _, id := range profileIDs {
			found := false
			for _, p := range r.Msg.GetProfiles() {
				if p.GetId() == id {
					found = true
					break
				}
			}
			if !found {
				return errors.New("profile_id is not a profile of this panel")
			}
		}
	}
	if len(nodeIDs) > 0 {
		r, err := c.cl.Node.ListNodes(c.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
		if err != nil {
			return apiError(err)
		}
		for _, id := range nodeIDs {
			found := false
			for _, n := range r.Msg.GetNodes() {
				if n.GetId() == id {
					found = true
					break
				}
			}
			if !found {
				return errors.New("disabled_node_id is not a node of this panel")
			}
		}
	}
	if dnsPreset != "" {
		r, err := c.cl.DNS.ListDnsPresets(c.ctx, connect.NewRequest(&adminv1.ListDnsPresetsRequest{}))
		if err != nil {
			return apiError(err)
		}
		for _, p := range r.Msg.GetPresets() {
			if p.GetId() == dnsPreset {
				return nil
			}
		}
		return errors.New("dns_preset_id is not a preset of this panel")
	}
	return nil
}

var groupCreate = changeSpec[groupCreateArgs]{
	name: "group_create", min: ProfileOperator,
	procs: procs(adminv1connect.GroupServiceCreateGroupProcedure, adminv1connect.GroupServiceListGroupsProcedure,
		adminv1connect.ProfileServiceListProfilesProcedure, adminv1connect.NodeServiceListNodesProcedure, adminv1connect.DnsServiceListDnsPresetsProcedure),
	desc: "Create an access group with profiles, an optional DNS preset, colour, and nodes excluded for its users. Group names are untrusted text. Groups have no notes or quota fields in the API. The plan validates references and changes nothing.",
	plan: func(c *call, a groupCreateArgs) (*planned, error) {
		name, err := groupName(a.Name)
		if err != nil {
			return nil, err
		}
		if err := groupColor(a.Color); err != nil {
			return nil, err
		}
		if err := c.checkGroupRefs(a.ProfileIDs, a.DisabledNodeIDs, a.DNSPresetID); err != nil {
			return nil, err
		}
		gs, err := c.groups()
		if err != nil {
			return nil, err
		}
		for _, g := range gs {
			if strings.EqualFold(strings.TrimSpace(g.GetName()), name) {
				return nil, errors.New("a group with this name already exists")
			}
		}
		profileIDs, _ := uniqueIDs("profile_ids", a.ProfileIDs, true)
		nodeIDs, _ := uniqueIDs("disabled_node_ids", a.DisabledNodeIDs, true)
		a.Name, a.ProfileIDs, a.DisabledNodeIDs = name, profileIDs, nodeIDs
		return &planned{Summary: "Create a group with the selected profiles and node access. People can be assigned to it after it exists.", Facts: []Fact{
			{Key: "name", Value: name, Untrusted: true},
			{Key: "profile_ids", Value: listOrNone(cleanIDs(profileIDs)), Untrusted: len(profileIDs) > 0},
			{Key: "dns_preset_id", Value: valueOrDefault(a.DNSPresetID, "instance default"), Untrusted: a.DNSPresetID != ""},
			{Key: "disabled_node_ids", Value: listOrNone(cleanIDs(nodeIDs)), Untrusted: len(nodeIDs) > 0},
			{Key: "color", Value: valueOrDefault(a.Color, "automatic")},
		}, Params: a}, nil
	},
	apply: func(c *call, a groupCreateArgs, _ Plan) (done, error) {
		if _, err := c.cl.Group.CreateGroup(c.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: a.Name, ProfileIds: a.ProfileIDs,
			DnsPresetId: a.DNSPresetID, Color: a.Color, DisabledNodeIds: a.DisabledNodeIDs})); err != nil {
			return done{}, apiError(err)
		}
		return doneWith("group_created", "Group created."), nil
	},
}

type groupUpdateArgs struct {
	ReasonField
	GroupID         string    `json:"group_id" jsonschema:"group id from groups_list"`
	Name            *string   `json:"name,omitempty"`
	ProfileIDs      *[]string `json:"profile_ids,omitempty" jsonschema:"replace the profile set; empty removes all profiles"`
	DNSPresetID     *string   `json:"dns_preset_id,omitempty" jsonschema:"empty inherits the instance default"`
	Color           *string   `json:"color,omitempty" jsonschema:"empty clears the selected colour"`
	DisabledNodeIDs *[]string `json:"disabled_node_ids,omitempty" jsonschema:"replace the excluded node set; empty makes all nodes available"`
	BeforeHash      string    `json:"before_hash,omitempty" jsonschema:"-"`
}

func groupConfigHash(g *adminv1.Group) string {
	if g == nil {
		return ""
	}
	x := proto.Clone(g).(*adminv1.Group)
	x.UserCount, x.HappNodes, x.AmneziaNodes, x.TelegramNodes = 0, 0, 0, 0
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(x)
	return hashText(string(b))
}

func (a groupUpdateArgs) updateRequest(dryRun bool) *adminv1.UpdateGroupRequest {
	r := &adminv1.UpdateGroupRequest{GroupId: a.GroupID, DryRun: dryRun}
	if a.Name != nil {
		r.Name = a.Name
	}
	if a.ProfileIDs != nil {
		r.ProfileIds = &adminv1.ProfileIds{Values: *a.ProfileIDs}
	}
	if a.DNSPresetID != nil {
		r.DnsPresetId = a.DNSPresetID
	}
	if a.Color != nil {
		r.Color = a.Color
	}
	if a.DisabledNodeIDs != nil {
		r.DisabledNodeIds = &adminv1.NodeIds{Values: *a.DisabledNodeIDs}
	}
	return r
}

var groupUpdate = changeSpec[groupUpdateArgs]{
	name: "group_update", min: ProfileOperator, danger: true,
	procs: procs(adminv1connect.GroupServiceListGroupsProcedure, adminv1connect.GroupServiceUpdateGroupProcedure,
		adminv1connect.ProfileServiceListProfilesProcedure, adminv1connect.NodeServiceListNodesProcedure, adminv1connect.DnsServiceListDnsPresetsProcedure),
	desc: "Update a group's name, profiles, DNS default, colour or excluded nodes. Profile and node changes can change users' access. The group API has no note or quota fields. Always waits for the owner.",
	plan: func(c *call, a groupUpdateArgs) (*planned, error) {
		if err := validID("group_id", a.GroupID); err != nil {
			return nil, err
		}
		if a.Name == nil && a.ProfileIDs == nil && a.DNSPresetID == nil && a.Color == nil && a.DisabledNodeIDs == nil {
			return nil, errors.New("give at least one group field to change")
		}
		before, err := c.groupByID(a.GroupID)
		if err != nil {
			return nil, err
		}
		if a.Name != nil {
			n, err := groupName(*a.Name)
			if err != nil {
				return nil, err
			}
			a.Name = &n
		}
		if a.Color != nil {
			if err := groupColor(*a.Color); err != nil {
				return nil, err
			}
		}
		if a.ProfileIDs != nil {
			ids, err := uniqueIDs("profile_ids", *a.ProfileIDs, true)
			if err != nil {
				return nil, err
			}
			a.ProfileIDs = &ids
		}
		if a.DisabledNodeIDs != nil {
			ids, err := uniqueIDs("disabled_node_ids", *a.DisabledNodeIDs, true)
			if err != nil {
				return nil, err
			}
			a.DisabledNodeIDs = &ids
		}
		var profiles, nodes []string
		if a.ProfileIDs != nil {
			profiles = *a.ProfileIDs
		}
		if a.DisabledNodeIDs != nil {
			nodes = *a.DisabledNodeIDs
		}
		preset := before.GetDnsPresetId()
		if a.DNSPresetID != nil {
			preset = *a.DNSPresetID
		}
		if err := c.checkGroupRefs(profiles, nodes, preset); err != nil {
			return nil, err
		}
		r, err := c.cl.Group.UpdateGroup(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(a.updateRequest(true)))
		if err != nil {
			return nil, apiError(err)
		}
		if r.Msg.GetGroup() == nil {
			return nil, errors.New("the panel returned no group")
		}
		facts := []Fact{{Key: "group", Value: nm(before.GetName()), Untrusted: true}}
		if a.Name != nil && *a.Name != before.GetName() {
			facts = append(facts, safeTextChange("name", before.GetName(), *a.Name))
		}
		if a.ProfileIDs != nil {
			facts = append(facts, changedIDFacts("profile_ids", before.GetProfileIds(), *a.ProfileIDs)...)
		}
		if a.DNSPresetID != nil && *a.DNSPresetID != before.GetDnsPresetId() {
			facts = append(facts, safeTextChange("dns_preset_id", before.GetDnsPresetId(), *a.DNSPresetID))
		}
		if a.Color != nil && *a.Color != before.GetColor() {
			facts = append(facts, changeFact("color", valueOrDefault(before.GetColor(), "automatic")+" -> "+valueOrDefault(*a.Color, "automatic"), before.GetColor(), *a.Color))
		}
		if a.DisabledNodeIDs != nil {
			facts = append(facts, changedIDFacts("disabled_node_ids", before.GetDisabledNodeIds(), *a.DisabledNodeIDs)...)
		}
		if impact := r.Msg.GetImpact(); impact != nil && impact.GetUsers() > 0 {
			facts = append(facts, codedFact("access_impact", fmt.Sprintf("%d group users may lose or gain profiles", impact.GetUsers()), "users", "n", strconv.FormatUint(uint64(impact.GetUsers()), 10)))
		}
		a.BeforeHash = groupConfigHash(before)
		return &planned{Summary: "Update the group's access settings. The owner must approve this change before it is applied.", Facts: facts,
			Danger: []string{dangerFleet}, Params: a}, nil
	},
	apply: func(c *call, a groupUpdateArgs, _ Plan) (done, error) {
		before, err := c.groupByID(a.GroupID)
		if err != nil {
			return done{}, err
		}
		if groupConfigHash(before) != a.BeforeHash {
			return done{}, failure("changed_since_plan", "the group changed after the plan; make a new plan")
		}
		if _, err := c.cl.Group.UpdateGroup(c.ctx, connect.NewRequest(a.updateRequest(false))); err != nil {
			return done{}, apiError(err)
		}
		return doneWith("group_updated", "Group settings updated."), nil
	},
}

type groupDeleteArgs struct {
	ReasonField
	GroupID string `json:"group_id" jsonschema:"empty groups_list group id; groups with users cannot be deleted by this tool"`
}

var groupDelete = changeSpec[groupDeleteArgs]{
	name: "group_delete", min: ProfileOperator, procs: procs(adminv1connect.GroupServiceListGroupsProcedure, adminv1connect.GroupServiceDeleteGroupProcedure),
	desc: "Delete an empty group. Refused while any person is in it; move people with user_update first. The tool never moves people automatically.",
	plan: func(c *call, a groupDeleteArgs) (*planned, error) {
		g, err := c.groupByID(a.GroupID)
		if err != nil {
			return nil, err
		}
		if g.GetUserCount() != 0 {
			return nil, failure("group_not_empty", "group has users; move them with user_update before deleting", "users", strconv.FormatUint(uint64(g.GetUserCount()), 10))
		}
		return &planned{Summary: "Delete the empty group. No users will be moved.", Facts: []Fact{
			{Key: "group", Value: nm(g.GetName()), Untrusted: true},
			codedFact("users", "no users belong to this group", "users", "n", "0"),
		}, Params: a}, nil
	},
	apply: func(c *call, a groupDeleteArgs, _ Plan) (done, error) {
		g, err := c.groupByID(a.GroupID)
		if err != nil {
			return done{}, err
		}
		if g.GetUserCount() != 0 {
			return done{}, failure("group_not_empty", "group has users now; move them with user_update before deleting", "users", strconv.FormatUint(uint64(g.GetUserCount()), 10))
		}
		if _, err := c.cl.Group.DeleteGroup(c.ctx, connect.NewRequest(&adminv1.DeleteGroupRequest{GroupId: a.GroupID})); err != nil {
			return done{}, apiError(err)
		}
		return doneWith("group_deleted", "Empty group deleted."), nil
	},
}

func listOrNone(v []string) string {
	if len(v) == 0 {
		return "none"
	}
	return strings.Join(v, ", ")
}
func valueOrDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
