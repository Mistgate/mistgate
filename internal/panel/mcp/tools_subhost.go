//go:build !js

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	subhostv1 "github.com/mistgate/mistgate/gen/mistgate/subhost/v1"
	"github.com/mistgate/mistgate/gen/mistgate/subhost/v1/subhostv1connect"
)

// The subscription host puts the panel's public address on a subdomain one node serves (design/adr/0018-panel-behind-nat.md).
// Every change is the owner's: a plan the owner approves with a fresh step-up, applied with only the confirm token. The bridge
// path and the link prefix stay inside the panel; no tool reads or returns them.

type subHostBridgeView struct {
	NodeID     string `json:"node_id"`
	Up         bool   `json:"up"`
	SinceUnix  int64  `json:"since_unix,omitempty"`
	RTTMs      uint32 `json:"rtt_ms,omitempty"`
	DownReason string `json:"down_reason,omitempty"`
}

type subHostNodeView struct {
	NodeID string `json:"node_id"`
	Name   string `json:"name"`
}

type subHostView struct {
	State       string              `json:"state"`
	Domain      string              `json:"domain,omitempty"`
	NodeID      string              `json:"node_id,omitempty"`
	NextNodeID  string              `json:"next_node_id,omitempty"`
	PublicURL   string              `json:"public_url,omitempty"`
	SiteName    string              `json:"site_name,omitempty"`
	Ready       bool                `json:"ready"`
	NotReady    []string            `json:"not_ready,omitempty"`
	Bridges     []subHostBridgeView `json:"bridges,omitempty"`
	Pending     []subHostNodeView   `json:"pending,omitempty"`
	CAARecord   string              `json:"caa_record,omitempty"`
	CAAStatus   string              `json:"caa_status,omitempty"`
	CAAWaived   bool                `json:"caa_waived,omitempty"`
	LackingLink []subHostNodeView   `json:"lacking_link,omitempty"`
}

func subHostState(s subhostv1.SubscriptionHostState) string {
	switch s {
	case subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_ON:
		return "on"
	case subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_MOVING:
		return "moving"
	case subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_LEAVING:
		return "leaving"
	default:
		return "off"
	}
}

func subHostViewOf(h *subhostv1.SubscriptionHost) subHostView {
	v := subHostView{State: subHostState(h.GetState()), Domain: clean(h.GetDomain(), 253), NodeID: clean(h.GetNodeId(), 64),
		NextNodeID: clean(h.GetNextNodeId(), 64), PublicURL: clean(h.GetPublicUrl(), 300), CAARecord: clean(h.GetCaaRecord(), 300), CAAStatus: clean(h.GetCaaStatus(), 64)}
	v.SiteName, v.Ready, v.CAAWaived = clean(h.GetSiteName(), 253), h.GetReady(), h.GetCaaWaived()
	for _, r := range h.GetNotReady() {
		v.NotReady = append(v.NotReady, clean(r, 32))
	}
	for _, b := range h.GetBridges() {
		v.Bridges = append(v.Bridges, subHostBridgeView{NodeID: clean(b.GetNodeId(), 64), Up: b.GetUp(), SinceUnix: b.GetSinceUnix(), RTTMs: b.GetRttMs(),
			DownReason: clean(b.GetDownReason(), 64)})
	}
	for _, p := range h.GetPending() {
		v.Pending = append(v.Pending, subHostNodeView{NodeID: clean(p.GetNodeId(), 64), Name: nm(p.GetName())})
	}
	for _, n := range h.GetLackingLink() {
		v.LackingLink = append(v.LackingLink, subHostNodeView{NodeID: clean(n.GetNodeId(), 64), Name: nm(n.GetName())})
	}
	return v
}

func subscriptionHostTools() []toolDef {
	get := readTool("subscription_host_get", ProfileAdmin, procs(subhostv1connect.SubscriptionHostServiceGetSubscriptionHostProcedure),
		"The subscription host: whether the panel's public address is a subdomain served by a node (state off, on, moving or leaving), "+
			"the domain, the serving node and the next one during a move, the name of the node's own site on 443, whether it is ready "+
			"(not_ready lists what is missing: host_offline, host_error, site_cert, bridge, panel_cert, caa), the health of the bridge the panel dials "+
			"(down_reason unreachable, tls, pin_mismatch, refused, no_verification or unfit:<reason>), nodes still connected through it while leaving, "+
			"the CAA record to publish and whether the owner proceeds without it (caa_waived), and nodes whose agent cannot follow the "+
			"panel to a new link address.",
		func(c *call, _ noArgs) (any, error) {
			h, err := c.subHost()
			if err != nil {
				return nil, err
			}
			return subHostViewOf(h), nil
		})
	return []toolDef{get, subHostSetPlanTool(), subHostSetApplyTool(), subHostMovePlanTool(), subHostMoveApplyTool(),
		subHostDisablePlanTool(), subHostDisableApplyTool(), subHostCAAWaivePlanTool(), subHostCAAWaiveApplyTool()}
}

// subHostSiteName is the optional site name, shaped like a domain.
func subHostSiteName(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	d, err := subHostDomain(raw)
	if err != nil {
		return "", errors.New("site_name: give a host name such as www.example.com")
	}
	return d, nil
}

func (c *call) subHost() (*subhostv1.SubscriptionHost, error) {
	r, err := c.cl.SubHost.GetSubscriptionHost(c.ctx, connect.NewRequest(&subhostv1.GetSubscriptionHostRequest{}))
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

// subHostDNSFact checks under planning grants whether the domain (and the site name) points at the node: the "dns" fact and whether it matched.
func subHostDNSFact(c *call, nodeID, domain, siteName string) (string, bool, error) {
	r, err := c.cl.SubHost.CheckSubscriptionHostDomain(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&subhostv1.CheckSubscriptionHostDomainRequest{
		NodeId: nodeID, Domain: domain, SiteName: siteName}))
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
	SiteName     string `json:"site_name,omitempty" jsonschema:"a second name pointing at the node for its own site on 443; needed when no inbound on 443 serves one"`
	SkipDNSCheck bool   `json:"skip_dns_check,omitempty" jsonschema:"set only when DNS is proxied or not yet visible to the panel"`
}

type subHostSetParams struct {
	NodeID, Name, Domain, SiteName string
	SkipDNSCheck                   bool
}

func subHostSetPlanTool() toolDef {
	const base = "subscription_host_set"
	return planToolDef(base, procs(subhostv1connect.SubscriptionHostServiceGetSubscriptionHostProcedure, subhostv1connect.SubscriptionHostServiceCheckSubscriptionHostDomainProcedure),
		"Prepare to move the panel's public address onto a subdomain served by one node: subscriptions and the agents' link then reach the panel through that node. "+
			"The plan checks that the domain's DNS points at the node; skip_dns_check bypasses that. The owner approves with a fresh sign-in.",
		func(c *call, in subHostSetArgs) (*mcp.CallToolResult, any, error) {
			reason := (&in.ReasonField).take()
			domain, err := subHostDomain(in.Domain)
			if err != nil {
				return nil, nil, err
			}
			site, err := subHostSiteName(in.SiteName)
			if err != nil {
				return nil, nil, err
			}
			h, err := c.subHost()
			if err != nil {
				return nil, nil, scrubError(err)
			}
			if h.GetState() != subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_OFF {
				return nil, nil, errors.New("the subscription host is already " + subHostState(h.GetState()) + "; use subscription_host_move_plan or subscription_host_disable_plan")
			}
			id, name, err := c.nodeRef(in.NodeID)
			if err != nil {
				return nil, nil, err
			}
			dns, ok, err := subHostDNSFact(c, id, domain, site)
			if err != nil {
				return nil, nil, err
			}
			if !ok && !in.SkipDNSCheck {
				return nil, nil, errors.New("the DNS does not point at the node (" + dns + "); fix the DNS record or set skip_dns_check")
			}
			if !ok {
				dns += " (check skipped)"
			}
			p := subHostSetParams{NodeID: id, Name: name, Domain: domain, SiteName: site, SkipDNSCheck: in.SkipDNSCheck}
			facts := []Fact{{Key: "node", Value: nm(name), Untrusted: true}, {Key: "domain", Value: domain, Untrusted: true}, {Key: "dns", Value: dns}}
			if site != "" {
				facts = append(facts, Fact{Key: "site_name", Value: site, Untrusted: true})
			}
			return subHostPlan(c, base, p, reason, "Make the subdomain on this node the panel's public address once the node is ready: subscriptions and the agents' link move to it.", facts)
		})
}

func subHostSetApplyTool() toolDef {
	return applyToolDef("subscription_host_set", procs(subhostv1connect.SubscriptionHostServiceSetSubscriptionHostProcedure),
		"Apply an owner-approved subscription_host_set plan. Takes only the confirm_token.", "make a new subscription host plan",
		func(c *call, p subHostSetParams) (done, error) {
			if _, err := c.cl.SubHost.SetSubscriptionHost(c.ctx, connect.NewRequest(&subhostv1.SetSubscriptionHostRequest{NodeId: p.NodeID, Domain: p.Domain, SiteName: p.SiteName,
				SkipDnsCheck: p.SkipDNSCheck})); err != nil {
				return done{}, apiError(err)
			}
			return doneWith("subscription_host_set", "The subscription host "+clean(p.Domain, 253)+" is on node "+nm(p.Name)+"; the panel's address moves to it once it is ready."), nil
		})
}

type subHostMoveArgs struct {
	ReasonField
	Action   string `json:"action" jsonschema:"start (a second node gets ready to take over), finish (complete the move or the leaving) or cancel"`
	NodeID   string `json:"node_id,omitempty" jsonschema:"for start: id or exact name of the node to move to"`
	SiteName string `json:"site_name,omitempty" jsonschema:"for start: the next node's own site name on 443, when no inbound there serves one"`
	Force    bool   `json:"force,omitempty" jsonschema:"for finish: finish even while nodes still connect through the subscription host (leaving) or DNS does not point at the next node yet (moving)"`
}

type subHostMoveParams struct {
	Action, NodeID, Name, SiteName string
	Force                          bool
}

func subHostMovePlanTool() toolDef {
	const base = "subscription_host_move"
	return planToolDef(base, procs(subhostv1connect.SubscriptionHostServiceGetSubscriptionHostProcedure),
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
				if state != subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_ON {
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
				site, err := subHostSiteName(in.SiteName)
				if err != nil {
					return nil, nil, err
				}
				p.NodeID, p.Name, p.SiteName = id, name, site
				summary = "Get a second node ready to take over the subscription host; the current node keeps serving until the move finishes."
				facts = append(facts, Fact{Key: "node", Value: nm(name), Untrusted: true})
			case "finish", "cancel":
				if state != subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_MOVING && state != subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_LEAVING {
					return nil, nil, errors.New("there is no move or leaving to " + in.Action + "; the subscription host is " + subHostState(state))
				}
				if in.NodeID != "" || in.SiteName != "" {
					return nil, nil, errors.New("node_id and site_name apply only to start")
				}
				if in.Action == "cancel" && in.Force {
					return nil, nil, errors.New("force applies only to finish")
				}
				p.Force = in.Force
				switch {
				case in.Action == "cancel":
					summary = "Cancel the subscription host's " + subHostState(state) + "; it stays on its current node."
				case state == subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_MOVING:
					summary = "Finish the move: the next node takes over the subscription host."
				default:
					summary = "Finish leaving: the panel goes back to its own public address and the subscription host turns off."
				}
				if p.Force {
					facts = append(facts, Fact{Key: "warning", Value: "force: nodes still connected through the subscription host may lose the panel until they reconnect, and a next node DNS does not point at may not serve the subdomain"})
				}
			default:
				return nil, nil, errors.New("action must be start, finish or cancel")
			}
			return subHostPlan(c, base, p, reason, summary, facts)
		})
}

func subHostMoveApplyTool() toolDef {
	return applyToolDef("subscription_host_move", procs(subhostv1connect.SubscriptionHostServiceStartSubscriptionHostMoveProcedure,
		subhostv1connect.SubscriptionHostServiceFinishSubscriptionHostChangeProcedure, subhostv1connect.SubscriptionHostServiceCancelSubscriptionHostChangeProcedure),
		"Apply an owner-approved subscription_host_move plan. Takes only the confirm_token.", "make a new subscription host move plan",
		func(c *call, p subHostMoveParams) (done, error) {
			var err error
			switch p.Action {
			case "start":
				_, err = c.cl.SubHost.StartSubscriptionHostMove(c.ctx, connect.NewRequest(&subhostv1.StartSubscriptionHostMoveRequest{NodeId: p.NodeID, SiteName: p.SiteName}))
			case "finish":
				_, err = c.cl.SubHost.FinishSubscriptionHostChange(c.ctx, connect.NewRequest(&subhostv1.FinishSubscriptionHostChangeRequest{Force: p.Force}))
			case "cancel":
				_, err = c.cl.SubHost.CancelSubscriptionHostChange(c.ctx, connect.NewRequest(&subhostv1.CancelSubscriptionHostChangeRequest{}))
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
	PublicURL string `json:"public_url,omitempty" jsonschema:"https://host[:port] the panel goes back to; empty only with force"`
	Force     bool   `json:"force,omitempty" jsonschema:"with an empty public_url: the panel will have no public address (nodes and subscriptions lose it)"`
}

type subHostDisableParams struct {
	PublicURL string
	Force     bool
}

func subHostDisablePlanTool() toolDef {
	const base = "subscription_host_disable"
	return planToolDef(base, procs(subhostv1connect.SubscriptionHostServiceGetSubscriptionHostProcedure),
		"Prepare to turn the subscription host off: the panel goes back to public_url and waits while nodes move their link there, then finish with subscription_host_move_plan action finish. "+
			"The owner approves with a fresh sign-in.",
		func(c *call, in subHostDisableArgs) (*mcp.CallToolResult, any, error) {
			reason := (&in.ReasonField).take()
			u := strings.TrimRight(strings.TrimSpace(in.PublicURL), "/")
			if len(u) > 300 || u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") || strings.ContainsAny(u, " \t\r\n\x00") {
				return nil, nil, errors.New("public_url must be http(s)://host[:port] or empty")
			}
			if u == "" && !in.Force {
				return nil, nil, errors.New("give public_url, or set force to leave the panel without a public address")
			}
			h, err := c.subHost()
			if err != nil {
				return nil, nil, scrubError(err)
			}
			if h.GetState() != subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_ON {
				return nil, nil, errors.New("the subscription host turns off only while it is on; it is " + subHostState(h.GetState()))
			}
			address := u
			if address == "" {
				address = "none: the panel keeps no public address of its own"
			}
			return subHostPlan(c, base, subHostDisableParams{PublicURL: u, Force: in.Force}, reason,
				"Turn the subscription host off: the panel's public address goes back to public_url while nodes move their link there.",
				[]Fact{{Key: "domain", Value: clean(h.GetDomain(), 253), Untrusted: true}, {Key: "public_url", Value: address, Untrusted: true}})
		})
}

func subHostDisableApplyTool() toolDef {
	return applyToolDef("subscription_host_disable", procs(subhostv1connect.SubscriptionHostServiceDisableSubscriptionHostProcedure),
		"Apply an owner-approved subscription_host_disable plan. Takes only the confirm_token.", "make a new subscription host plan",
		func(c *call, p subHostDisableParams) (done, error) {
			if _, err := c.cl.SubHost.DisableSubscriptionHost(c.ctx, connect.NewRequest(&subhostv1.DisableSubscriptionHostRequest{PublicUrl: p.PublicURL, Force: p.Force})); err != nil {
				return done{}, apiError(err)
			}
			return doneWith("subscription_host_leaving", "The subscription host is leaving; finish it once the nodes have moved."), nil
		})
}

type subHostCAAWaiveArgs struct {
	ReasonField
}

type subHostCAAWaiveParams struct{ Domain string }

func subHostCAAWaivePlanTool() toolDef {
	const base = "subscription_host_caa_waive"
	return planToolDef(base, procs(subhostv1connect.SubscriptionHostServiceGetSubscriptionHostProcedure),
		"Prepare to let the subscription host become ready without the CAA record (the caa step of readiness): whoever holds the "+
			"host can then get a certificate of their own for the subdomain. Publishing the record shown by subscription_host_get is "+
			"the safe way; this is for a DNS provider that cannot publish it. Lasts for this domain and the panel's ACME account. The "+
			"owner approves with a fresh sign-in.",
		func(c *call, in subHostCAAWaiveArgs) (*mcp.CallToolResult, any, error) {
			reason := (&in.ReasonField).take()
			h, err := c.subHost()
			if err != nil {
				return nil, nil, scrubError(err)
			}
			if st := h.GetState(); st != subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_ON && st != subhostv1.SubscriptionHostState_SUBSCRIPTION_HOST_STATE_MOVING {
				return nil, nil, errors.New("the CAA step is waived only while the subscription host is on or moving; it is " + subHostState(st))
			}
			if h.GetCaaWaived() {
				return nil, nil, errors.New("the CAA step is already waived for this domain")
			}
			if h.GetCaaStatus() == "present" {
				return nil, nil, errors.New("the CAA record is published: nothing to waive")
			}
			return subHostPlan(c, base, subHostCAAWaiveParams{Domain: clean(h.GetDomain(), 253)}, reason,
				"Let the subscription host become ready without the CAA record for its domain.",
				[]Fact{{Key: "domain", Value: clean(h.GetDomain(), 253), Untrusted: true},
					{Key: "warning", Value: "without the CAA record whoever holds the host can get a certificate of their own for the subdomain and read the subscriptions it carries"}})
		})
}

func subHostCAAWaiveApplyTool() toolDef {
	return applyToolDef("subscription_host_caa_waive", procs(subhostv1connect.SubscriptionHostServiceWaiveSubscriptionHostCAAProcedure),
		"Apply an owner-approved subscription_host_caa_waive plan. Takes only the confirm_token.", "make a new plan",
		func(c *call, p subHostCAAWaiveParams) (done, error) {
			h, err := c.subHost()
			if err != nil {
				return done{}, err
			}
			if h.GetDomain() != p.Domain {
				return done{}, failure("changed_since_plan", "the subscription host's domain changed since the plan: make a new one")
			}
			if _, err := c.cl.SubHost.WaiveSubscriptionHostCAA(c.ctx, connect.NewRequest(&subhostv1.WaiveSubscriptionHostCAARequest{})); err != nil {
				return done{}, apiError(err)
			}
			return doneWith("subscription_host_caa_waived", "The subscription host may become ready without the CAA record for "+clean(p.Domain, 253)+"."), nil
		})
}
