package mcp

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

func nodeSettingsTools() []toolDef { return change(nodeUpdate) }

type directSubscriptionArg struct {
	InboundID string `json:"inbound_id" jsonschema:"inbound id from the node detail"`
	Included  bool   `json:"included" jsonschema:"whether the node's own address appears in subscriptions"`
}

type nodeUpdateArgs struct {
	ReasonField
	Node        string                  `json:"node" jsonschema:"node id or exact name"`
	Name        *string                 `json:"name,omitempty" jsonschema:"node label, 2 to 24 lowercase letters, digits or hyphens"`
	CountryCode *string                 `json:"country_code,omitempty" jsonschema:"two-letter country code, or empty to clear"`
	Location    *string                 `json:"location,omitempty" jsonschema:"operator-facing location text, up to 100 characters"`
	Provider    *string                 `json:"provider,omitempty" jsonschema:"operator-facing provider text, up to 100 characters"`
	Notes       *string                 `json:"notes,omitempty" jsonschema:"operator notes, up to 500 characters"`
	Direct      []directSubscriptionArg `json:"direct_in_subscriptions,omitempty" jsonschema:"per-inbound switch for whether the node's own address is in subscriptions"`
	BeforeHash  string                  `json:"before_hash,omitempty" jsonschema:"-"`
}

var nodeLabelPattern = regexp.MustCompile(`^[a-z0-9-]{2,24}$`)

func validateNodeText(field, value string, maxRunes int, newline bool) error {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%s must be at most %d characters", field, maxRunes)
	}
	for _, r := range value {
		if (r < 0x20 || r == 0x7f) && !(newline && strings.ContainsRune("\n\r\t", r)) {
			return fmt.Errorf("%s must be plain text", field)
		}
	}
	if err := visible(field, value); err != nil {
		return err
	}
	return rejectSecretShaped(field, value)
}

func prepareNodeUpdate(a nodeUpdateArgs, node *adminv1.Node, notes string, inbounds []*adminv1.Inbound) (nodeUpdateArgs, []directSubscriptionArg, error) {
	if a.Name != nil {
		n := strings.ToLower(strings.TrimSpace(*a.Name))
		if !nodeLabelPattern.MatchString(n) {
			return a, nil, errors.New("name must match [a-z0-9-]{2,24}")
		}
		a.Name = &n
	}
	if a.CountryCode != nil {
		cc := strings.ToUpper(strings.TrimSpace(*a.CountryCode))
		if cc != "" && (len(cc) != 2 || cc[0] < 'A' || cc[0] > 'Z' || cc[1] < 'A' || cc[1] > 'Z') {
			return a, nil, errors.New("country_code must be two letters or empty")
		}
		a.CountryCode = &cc
	}
	if a.Location != nil {
		if err := validateNodeText("location", *a.Location, 100, false); err != nil {
			return a, nil, err
		}
	}
	if a.Provider != nil {
		if err := validateNodeText("provider", *a.Provider, 100, false); err != nil {
			return a, nil, err
		}
	}
	if a.Notes != nil {
		if err := validateNodeText("notes", *a.Notes, 500, true); err != nil {
			return a, nil, err
		}
	}
	if len(a.Direct) > maxIDs {
		return a, nil, fmt.Errorf("direct_in_subscriptions: at most %d inbounds", maxIDs)
	}
	seen := map[string]bool{}
	var changed []directSubscriptionArg
	for _, item := range a.Direct {
		if err := validID("inbound_id", item.InboundID); err != nil {
			return a, nil, err
		}
		if seen[item.InboundID] {
			continue
		}
		seen[item.InboundID] = true
		var found *adminv1.Inbound
		for _, inbound := range inbounds {
			if inbound.GetId() == item.InboundID {
				found = inbound
				break
			}
		}
		if found == nil {
			return a, nil, errors.New("inbound_id is not an inbound on this node")
		}
		if found.GetDirectInSubscriptions() != item.Included {
			changed = append(changed, item)
		}
	}
	a.Direct = changed
	metadataChanged := (a.Name != nil && *a.Name != node.GetName()) || (a.CountryCode != nil && *a.CountryCode != node.GetCountryCode()) ||
		(a.Location != nil && *a.Location != node.GetLocation()) || (a.Provider != nil && *a.Provider != node.GetProvider()) || (a.Notes != nil && *a.Notes != notes)
	if !metadataChanged && len(changed) == 0 {
		return a, nil, errors.New("nothing to change: node settings already look like this")
	}
	return a, changed, nil
}

func nodeSettingsHash(node *adminv1.Node, notes string, inbounds []*adminv1.Inbound) string {
	var parts []string
	for _, in := range inbounds {
		parts = append(parts, in.GetId()+":"+fmt.Sprint(in.GetDirectInSubscriptions()))
	}
	slices.Sort(parts)
	return hashText(node.GetId() + "\x00" + node.GetName() + "\x00" + node.GetCountryCode() + "\x00" + node.GetLocation() + "\x00" + node.GetProvider() + "\x00" + notes + "\x00" + strings.Join(parts, ","))
}

var nodeUpdate = changeSpec[nodeUpdateArgs]{
	name: "node_update", min: ProfileAdmin, danger: true,
	procs: procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.NodeServiceGetNodeProcedure, adminv1connect.NodeServiceUpdateNodeProcedure, adminv1connect.ProfileServiceUpdateInboundProcedure),
	desc:  "Edit a node's label, country, location, provider or notes, and set per-inbound direct-in-subscriptions switches. The tool omits the node address and every setting that changes runtime behavior. Name, country and subscription switches affect what users receive; those changes wait for owner approval.",
	plan: func(c *call, a nodeUpdateArgs) (*planned, error) {
		id, name, err := c.nodeRef(a.Node)
		if err != nil {
			return nil, err
		}
		r, err := c.cl.Node.GetNode(c.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: id}))
		if err != nil {
			return nil, apiError(err)
		}
		node := r.Msg.GetNode()
		if node == nil {
			return nil, errors.New("node settings are unavailable")
		}
		a.Node = id
		a, changed, err := prepareNodeUpdate(a, node, r.Msg.GetNotes(), r.Msg.GetInbounds())
		if err != nil {
			return nil, err
		}
		for _, item := range changed {
			value := item.Included
			if _, err := c.cl.Profile.UpdateInbound(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.UpdateInboundRequest{InboundId: item.InboundID, DirectInSubscriptions: &value, ValidateOnly: true})); err != nil {
				return nil, apiError(err)
			}
		}
		facts := []Fact{{Key: "node", Value: nm(name), Untrusted: true}}
		if a.Name != nil && *a.Name != node.GetName() {
			facts = append(facts, safeTextChange("name", node.GetName(), *a.Name))
		}
		if a.CountryCode != nil && *a.CountryCode != node.GetCountryCode() {
			facts = append(facts, safeTextChange("country_code", node.GetCountryCode(), *a.CountryCode))
		}
		if a.Location != nil && *a.Location != node.GetLocation() {
			facts = append(facts, safeTextChange("location", node.GetLocation(), *a.Location))
		}
		if a.Provider != nil && *a.Provider != node.GetProvider() {
			facts = append(facts, safeTextChange("provider", node.GetProvider(), *a.Provider))
		}
		if a.Notes != nil && *a.Notes != r.Msg.GetNotes() {
			facts = append(facts, safeTextChange("notes", r.Msg.GetNotes(), *a.Notes))
		}
		for _, item := range changed {
			var inName, profile string
			for _, in := range r.Msg.GetInbounds() {
				if in.GetId() == item.InboundID {
					inName, profile = in.GetProtocol(), in.GetProfileName()
					break
				}
			}
			facts = append(facts, Fact{Key: "direct_in_subscriptions", Value: nm(profile) + " / " + nm(inName) + " (" + clean(item.InboundID, 60) + ") -> " + strconvBool(item.Included), Untrusted: true})
		}
		// NodeService.UpdateNode also accepts runtime-changing fields at the API boundary. The MCP tool keeps its
		// arguments to metadata, but still requires the owner grant for this exact procedure.
		danger := []string{dangerPage}
		a.BeforeHash = nodeSettingsHash(node, r.Msg.GetNotes(), r.Msg.GetInbounds())
		summary := "Update node metadata or subscription-facing inbound settings. The owner must approve this change."
		return &planned{Summary: summary, Facts: facts, Danger: danger, Params: a}, nil
	},
	apply: func(c *call, a nodeUpdateArgs, _ Plan) (done, error) {
		r, err := c.cl.Node.GetNode(c.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: a.Node}))
		if err != nil {
			return done{}, apiError(err)
		}
		if r.Msg.GetNode() == nil || nodeSettingsHash(r.Msg.GetNode(), r.Msg.GetNotes(), r.Msg.GetInbounds()) != a.BeforeHash {
			return done{}, failure("changed_since_plan", "node settings changed after the plan; make a new plan")
		}
		req := &adminv1.UpdateNodeRequest{NodeId: a.Node, Name: a.Name, CountryCode: a.CountryCode, Location: a.Location, Provider: a.Provider, Notes: a.Notes}
		if a.Name != nil || a.CountryCode != nil || a.Location != nil || a.Provider != nil || a.Notes != nil {
			if _, err := c.cl.Node.UpdateNode(c.ctx, connect.NewRequest(req)); err != nil {
				return done{}, apiError(err)
			}
		}
		for _, item := range a.Direct {
			value := item.Included
			if _, err := c.cl.Profile.UpdateInbound(c.ctx, connect.NewRequest(&adminv1.UpdateInboundRequest{InboundId: item.InboundID, DirectInSubscriptions: &value})); err != nil {
				return done{}, apiError(err)
			}
		}
		return doneWith("node_updated", "Node settings updated."), nil
	},
}

func strconvBool(v bool) string {
	if v {
		return "included"
	}
	return "excluded"
}
