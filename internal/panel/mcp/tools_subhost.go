//go:build !js

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// The subscription host puts the panel's public address on a subdomain one node serves. Every change is the owner's: a
// plan the owner approves with a fresh step-up, applied with only the confirm token. The tunnel SNI the panel dials stays
// inside the panel; no tool reads or returns it.

type subHostTunnelView struct {
	NodeID     string `json:"node_id"`
	Up         bool   `json:"up"`
	SinceUnix  int64  `json:"since_unix,omitempty"`
	DownReason string `json:"down_reason,omitempty"`
}

type subHostNodeView struct {
	NodeID string `json:"node_id"`
	Name   string `json:"name"`
	Via    string `json:"via,omitempty"`
}

type subHostView struct {
	State       string              `json:"state"`
	Domain      string              `json:"domain,omitempty"`
	NodeID      string              `json:"node_id,omitempty"`
	NextNodeID  string              `json:"next_node_id,omitempty"`
	PublicURL   string              `json:"public_url,omitempty"`
	Tunnels     []subHostTunnelView `json:"tunnels,omitempty"`
	Pending     []subHostNodeView   `json:"pending,omitempty"`
	CAARecord   string              `json:"caa_record,omitempty"`
	CAAStatus   string              `json:"caa_status,omitempty"`
	LackingLink []subHostNodeView   `json:"lacking_link,omitempty"`
}

func subHostState(s adminv1.SubscriptionHostState) string {
	switch s {
	case adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_ON:
		return "on"
	case adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_MOVING:
		return "moving"
	case adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_LEAVING:
		return "leaving"
	default:
		return "off"
	}
}

func subHostViewOf(h *adminv1.SubscriptionHost) subHostView {
	v := subHostView{State: subHostState(h.GetState()), Domain: clean(h.GetDomain(), 253), NodeID: clean(h.GetNodeId(), 64),
		NextNodeID: clean(h.GetNextNodeId(), 64), PublicURL: clean(h.GetPublicUrl(), 300), CAARecord: clean(h.GetCaaRecord(), 300), CAAStatus: clean(h.GetCaaStatus(), 64)}
	for _, t := range h.GetTunnels() {
		v.Tunnels = append(v.Tunnels, subHostTunnelView{NodeID: clean(t.GetNodeId(), 64), Up: t.GetUp(), SinceUnix: t.GetSinceUnix(), DownReason: clean(t.GetDownReason(), 32)})
	}
	for _, p := range h.GetPending() {
		v.Pending = append(v.Pending, subHostNodeView{NodeID: clean(p.GetNodeId(), 64), Name: nm(p.GetName()), Via: clean(p.GetVia(), 300)})
	}
	for _, n := range h.GetLackingLink() {
		v.LackingLink = append(v.LackingLink, subHostNodeView{NodeID: clean(n.GetNodeId(), 64), Name: nm(n.GetName())})
	}
	return v
}

func subscriptionHostTools() []toolDef {
	get := readTool("subscription_host_get", ProfileAdmin, procs(adminv1connect.SubscriptionHostServiceGetSubscriptionHostProcedure),
		"The subscription host: whether the panel's public address is a subdomain served by a node (state off, on, moving or leaving), "+
			"the domain, the serving node and the next one during a move, tunnel health (a down tunnel's down_reason is unreachable, "+
			"or not_recognised when the node answered as its site: clocks on the panel and node apart, an older agent), nodes still connected through it while leaving, "+
			"the CAA record to publish, and nodes whose agent cannot follow the panel to a new link address.",
		func(c *call, _ noArgs) (any, error) {
			h, err := c.subHost()
			if err != nil {
				return nil, err
			}
			return subHostViewOf(h), nil
		})
	return []toolDef{get, subHostSetPlanTool(), subHostSetApplyTool(), subHostMovePlanTool(), subHostMoveApplyTool(),
		subHostDisablePlanTool(), subHostDisableApplyTool()}
}

func (c *call) subHost() (*adminv1.SubscriptionHost, error) {
	r, err := c.cl.SubHost.GetSubscriptionHost(c.ctx, connect.NewRequest(&adminv1.GetSubscriptionHostRequest{}))
	if err != nil {
		return nil, apiError(err)
	}
	return r.Msg.GetHost(), nil
}

// subHostDomain is the shape check before the panel's own (sub_host_domain_invalid): a lower-case host name.
func subHostDomain(raw string) (string, error) {
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if d == "" || len(d) > 253 || !strings.Contains(d, ".") || strings.ContainsAny(d, "/:@ \t\r\n\x00") {
		return "", errors.New("domain: give a subdomain such as sub.example.com")
	}
	return d, nil
}

// subHostDNSFact checks under planning grants whether the domain points at the node: the "dns" fact and whether it matched.
func subHostDNSFact(c *call, nodeID, domain string) (string, bool, error) {
	r, err := c.cl.SubHost.CheckSubscriptionHostDomain(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.CheckSubscriptionHostDomainRequest{NodeId: nodeID, Domain: domain}))
	if err != nil {
		return "", false, scrubError(apiError(err))
	}
	if r.Msg.GetMatches() {
		return "matches", true, nil
	}
	return "mismatch: found " + clean(strings.Join(r.Msg.GetFound(), ", "), 300) + "; expected " + clean(strings.Join(r.Msg.GetExpected(), ", "), 300), false, nil
}

// subHostPlan stores a plan and answers with it; every subscription host plan needs the owner and moves the fleet's link.
func subHostPlan(c *call, base string, params any, reason, summary string, facts []Fact) (*mcp.CallToolResult, any, error) {
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, nil, errors.New("could not store the plan")
	}
	out, err := c.e.createPlan(c, base, encoded, reason, &planned{Summary: summary, Facts: facts, Danger: []string{dangerStepUp, dangerFleet}, Params: params})
	if err != nil {
		return nil, nil, scrubError(err)
	}
	return planResult(out)
}

func planToolDef[In any](base string, procedures []string, desc string, h func(c *call, in In) (*mcp.CallToolResult, any, error)) toolDef {
	return toolDef{
		name: base + "_plan", min: ProfileAdmin, procs: procedures, free: true, isPlan: true, danger: true, desc: desc,
		add: func(s *mcp.Server, e *env, desc string) {
			mcp.AddTool(s, &mcp.Tool{Name: base + "_plan", Description: desc, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}},
				func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
					c, err := e.begin(ctx, req, readTimeout)
					if err != nil {
						return nil, nil, err
					}
					defer c.done()
					return h(c, in)
				})
		},
	}
}

func applyToolDef[P any](base string, procedures []string, desc, replan string, run func(c *call, p P) (done, error)) toolDef {
	return toolDef{
		name: base + "_apply", min: ProfileAdmin, procs: procedures, danger: true, desc: desc,
		add: func(s *mcp.Server, e *env, desc string) {
			mcp.AddTool(s, &mcp.Tool{Name: base + "_apply", Description: desc, Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(false)}},
				func(ctx context.Context, req *mcp.CallToolRequest, in applyIn) (*mcp.CallToolResult, any, error) {
					c, err := e.begin(ctx, req, applyTimeout)
					if err != nil {
						return nil, nil, err
					}
					defer c.done()
					out, err := e.apply(c, base, in.ConfirmToken, func(c *call, pl Plan) (done, error) {
						var p P
						if err := strictJSON([]byte(pl.ParamsJSON), &p); err != nil {
							return done{}, failure("plan_unreadable", replan)
						}
						return run(c, p)
					})
					if err != nil {
						return nil, nil, scrubError(err)
					}
					return result(out)
				})
		},
	}
}

type subHostSetArgs struct {
	ReasonField
	NodeID       string `json:"node_id" jsonschema:"id or exact name of the node that will serve the subdomain"`
	Domain       string `json:"domain" jsonschema:"subdomain whose DNS points at the node, e.g. sub.example.com"`
	SkipDNSCheck bool   `json:"skip_dns_check,omitempty" jsonschema:"set only when DNS is proxied or not yet visible to the panel"`
}

type subHostSetParams struct {
	NodeID, Name, Domain string
	SkipDNSCheck         bool
}

func subHostSetPlanTool() toolDef {
	const base = "subscription_host_set"
	return planToolDef(base, procs(adminv1connect.SubscriptionHostServiceGetSubscriptionHostProcedure, adminv1connect.SubscriptionHostServiceCheckSubscriptionHostDomainProcedure),
		"Prepare to move the panel's public address onto a subdomain served by one node: subscriptions and the agents' link then reach the panel through that node. "+
			"The plan checks that the domain's DNS points at the node; skip_dns_check bypasses that. The owner approves with a fresh sign-in.",
		func(c *call, in subHostSetArgs) (*mcp.CallToolResult, any, error) {
			reason := (&in.ReasonField).take()
			domain, err := subHostDomain(in.Domain)
			if err != nil {
				return nil, nil, err
			}
			h, err := c.subHost()
			if err != nil {
				return nil, nil, scrubError(err)
			}
			if h.GetState() != adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_OFF {
				return nil, nil, errors.New("the subscription host is already " + subHostState(h.GetState()) + "; use subscription_host_move_plan or subscription_host_disable_plan")
			}
			id, name, err := c.nodeRef(in.NodeID)
			if err != nil {
				return nil, nil, err
			}
			dns, ok, err := subHostDNSFact(c, id, domain)
			if err != nil {
				return nil, nil, err
			}
			if !ok && !in.SkipDNSCheck {
				return nil, nil, errors.New("the domain's DNS does not point at the node (" + dns + "); fix the DNS record or set skip_dns_check")
			}
			if !ok {
				dns += " (check skipped)"
			}
			p := subHostSetParams{NodeID: id, Name: name, Domain: domain, SkipDNSCheck: in.SkipDNSCheck}
			return subHostPlan(c, base, p, reason, "Make the subdomain on this node the panel's public address: subscriptions and the agents' link move to it.",
				[]Fact{{Key: "node", Value: nm(name), Untrusted: true}, {Key: "domain", Value: domain, Untrusted: true}, {Key: "dns", Value: dns}})
		})
}

func subHostSetApplyTool() toolDef {
	return applyToolDef("subscription_host_set", procs(adminv1connect.SubscriptionHostServiceSetSubscriptionHostProcedure),
		"Apply an owner-approved subscription_host_set plan. Takes only the confirm_token.", "make a new subscription host plan",
		func(c *call, p subHostSetParams) (done, error) {
			if _, err := c.cl.SubHost.SetSubscriptionHost(c.ctx, connect.NewRequest(&adminv1.SetSubscriptionHostRequest{NodeId: p.NodeID, Domain: p.Domain, SkipDnsCheck: p.SkipDNSCheck})); err != nil {
				return done{}, apiError(err)
			}
			return doneWith("subscription_host_set", "The panel's public address is now "+clean(p.Domain, 253)+" on node "+nm(p.Name)+"."), nil
		})
}

type subHostMoveArgs struct {
	ReasonField
	Action string `json:"action" jsonschema:"start (a second node gets ready to take over), finish (complete the move or the leaving) or cancel"`
	NodeID string `json:"node_id,omitempty" jsonschema:"for start: id or exact name of the node to move to"`
	Force  bool   `json:"force,omitempty" jsonschema:"for finish: finish leaving even while nodes still connect through the subscription host"`
}

type subHostMoveParams struct {
	Action, NodeID, Name string
	Force                bool
}

func subHostMovePlanTool() toolDef {
	const base = "subscription_host_move"
	return planToolDef(base, procs(adminv1connect.SubscriptionHostServiceGetSubscriptionHostProcedure),
		"Prepare to move the subscription host to another node (action start), to complete a move or a leaving (finish; force finishes leaving while nodes still connect through it), "+
			"or to cancel either (cancel). The owner approves with a fresh sign-in.",
		func(c *call, in subHostMoveArgs) (*mcp.CallToolResult, any, error) {
			reason := (&in.ReasonField).take()
			h, err := c.subHost()
			if err != nil {
				return nil, nil, scrubError(err)
			}
			state := h.GetState()
			p := subHostMoveParams{Action: in.Action}
			var summary string
			facts := []Fact{{Key: "action", Value: in.Action}, {Key: "domain", Value: clean(h.GetDomain(), 253), Untrusted: true}}
			switch in.Action {
			case "start":
				if state != adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_ON {
					return nil, nil, errors.New("a move starts only while the subscription host is on; it is " + subHostState(state))
				}
				if in.Force {
					return nil, nil, errors.New("force applies only to finish")
				}
				id, name, err := c.nodeRef(in.NodeID)
				if err != nil {
					return nil, nil, err
				}
				if id == h.GetNodeId() {
					return nil, nil, errors.New("the subscription host is already on that node")
				}
				p.NodeID, p.Name = id, name
				summary = "Get a second node ready to take over the subscription host; the current node keeps serving until the move finishes."
				facts = append(facts, Fact{Key: "node", Value: nm(name), Untrusted: true})
			case "finish", "cancel":
				if state != adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_MOVING && state != adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_LEAVING {
					return nil, nil, errors.New("there is no move or leaving to " + in.Action + "; the subscription host is " + subHostState(state))
				}
				if in.NodeID != "" {
					return nil, nil, errors.New("node_id applies only to start")
				}
				if in.Action == "cancel" && in.Force {
					return nil, nil, errors.New("force applies only to finish")
				}
				p.Force = in.Force
				switch {
				case in.Action == "cancel":
					summary = "Cancel the subscription host's " + subHostState(state) + "; it stays on its current node."
				case state == adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_MOVING:
					summary = "Finish the move: the next node takes over the subscription host."
				default:
					summary = "Finish leaving: the panel goes back to its own public address and the subscription host turns off."
				}
				if p.Force {
					facts = append(facts, Fact{Key: "warning", Value: "force: nodes still connected through the subscription host may lose the panel until they reconnect"})
				}
			default:
				return nil, nil, errors.New("action must be start, finish or cancel")
			}
			return subHostPlan(c, base, p, reason, summary, facts)
		})
}

func subHostMoveApplyTool() toolDef {
	return applyToolDef("subscription_host_move", procs(adminv1connect.SubscriptionHostServiceStartSubscriptionHostMoveProcedure,
		adminv1connect.SubscriptionHostServiceFinishSubscriptionHostChangeProcedure, adminv1connect.SubscriptionHostServiceCancelSubscriptionHostChangeProcedure),
		"Apply an owner-approved subscription_host_move plan. Takes only the confirm_token.", "make a new subscription host move plan",
		func(c *call, p subHostMoveParams) (done, error) {
			var err error
			switch p.Action {
			case "start":
				_, err = c.cl.SubHost.StartSubscriptionHostMove(c.ctx, connect.NewRequest(&adminv1.StartSubscriptionHostMoveRequest{NodeId: p.NodeID}))
			case "finish":
				_, err = c.cl.SubHost.FinishSubscriptionHostChange(c.ctx, connect.NewRequest(&adminv1.FinishSubscriptionHostChangeRequest{Force: p.Force}))
			case "cancel":
				_, err = c.cl.SubHost.CancelSubscriptionHostChange(c.ctx, connect.NewRequest(&adminv1.CancelSubscriptionHostChangeRequest{}))
			default:
				return done{}, failure("plan_unreadable", "make a new subscription host move plan")
			}
			if err != nil {
				return done{}, apiError(err)
			}
			switch p.Action {
			case "start":
				return doneWith("subscription_host_move_started", "Node "+nm(p.Name)+" is getting ready to take over the subscription host."), nil
			case "finish":
				return doneWith("subscription_host_change_finished", "The subscription host change is finished."), nil
			default:
				return doneWith("subscription_host_change_cancelled", "The subscription host change is cancelled."), nil
			}
		})
}

type subHostDisableArgs struct {
	ReasonField
	PublicURL string `json:"public_url,omitempty" jsonschema:"https://host[:port] the panel goes back to; empty when no node but the host remains"`
}

type subHostDisableParams struct{ PublicURL string }

func subHostDisablePlanTool() toolDef {
	const base = "subscription_host_disable"
	return planToolDef(base, procs(adminv1connect.SubscriptionHostServiceGetSubscriptionHostProcedure),
		"Prepare to turn the subscription host off: the panel goes back to public_url and waits while nodes move their link there, then finish with subscription_host_move_plan action finish. "+
			"The owner approves with a fresh sign-in.",
		func(c *call, in subHostDisableArgs) (*mcp.CallToolResult, any, error) {
			reason := (&in.ReasonField).take()
			u := strings.TrimRight(strings.TrimSpace(in.PublicURL), "/")
			if len(u) > 300 || u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") || strings.ContainsAny(u, " \t\r\n\x00") {
				return nil, nil, errors.New("public_url must be http(s)://host[:port] or empty")
			}
			h, err := c.subHost()
			if err != nil {
				return nil, nil, scrubError(err)
			}
			if h.GetState() != adminv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_ON {
				return nil, nil, errors.New("the subscription host turns off only while it is on; it is " + subHostState(h.GetState()))
			}
			address := u
			if address == "" {
				address = "none: the panel keeps no public address of its own"
			}
			return subHostPlan(c, base, subHostDisableParams{PublicURL: u}, reason,
				"Turn the subscription host off: the panel's public address goes back to public_url while nodes move their link there.",
				[]Fact{{Key: "domain", Value: clean(h.GetDomain(), 253), Untrusted: true}, {Key: "public_url", Value: address, Untrusted: true}})
		})
}

func subHostDisableApplyTool() toolDef {
	return applyToolDef("subscription_host_disable", procs(adminv1connect.SubscriptionHostServiceDisableSubscriptionHostProcedure),
		"Apply an owner-approved subscription_host_disable plan. Takes only the confirm_token.", "make a new subscription host plan",
		func(c *call, p subHostDisableParams) (done, error) {
			if _, err := c.cl.SubHost.DisableSubscriptionHost(c.ctx, connect.NewRequest(&adminv1.DisableSubscriptionHostRequest{PublicUrl: p.PublicURL})); err != nil {
				return done{}, apiError(err)
			}
			return doneWith("subscription_host_leaving", "The subscription host is leaving; finish it once the nodes have moved."), nil
		})
}
