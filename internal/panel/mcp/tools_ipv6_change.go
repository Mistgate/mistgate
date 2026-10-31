package mcp

import (
	"errors"
	"strings"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// ipv6EntryV is the IPv6 entry of a node in node_get: no address, only what an agent needs to decide.
type ipv6EntryV struct {
	On                bool        `json:"on"`
	Supported         bool        `json:"supported"`
	UnavailableReason string      `json:"unavailable_reason,omitempty"`
	LastCheck         *Ipv6CheckV `json:"last_check,omitempty"`
}

func ipv6EntryView(x *adminv1.NodeIpv6) *ipv6EntryV {
	v := &ipv6EntryV{On: x.GetEntry() != "", Supported: x.GetEntrySupported(), UnavailableReason: clean(x.GetUnavailableReason(), 24)}
	if c := x.GetLastCheck(); c != nil {
		v.LastCheck = ipv6CheckView("", "", c)
		v.LastCheck.NodeID, v.LastCheck.Node = "", "" // it is the node's own
	}
	return v
}

// The reasons a node cannot take an IPv6 entry, in the words of an agent (the panel's codes are the keys).
var ipv6Reasons = map[string]string{
	"no_ipv6":      "the server has no IPv6 address",
	"ipv6_broken":  "the server has an IPv6 address but outbound IPv6 does not work there, so nothing would come in either",
	"agent_old":    "the node's agent does not say which IPv6 address the server uses: update the agent",
	"not_checked":  "the node's doctor has not reported its IPv6 yet",
	"main_is_ipv6": "the node's address is an IPv6 address already",
}

const (
	ipv6Enable  = "enable"
	ipv6Disable = "disable"
)

type nodeIPv6Args struct {
	ReasonField
	Node   string `json:"node" jsonschema:"node id or exact name"`
	Action string `json:"action" jsonschema:"enable or disable"`
}

type nodeIPv6StoredArgs struct {
	Node    string `json:"node"`
	Action  string `json:"action"`
	Address string `json:"address,omitempty"`
}

func ipv6PlanAddress(x *adminv1.NodeIpv6) string {
	for _, a := range x.GetAddresses() {
		if a.GetAddress() == x.GetSrc() {
			return a.GetAddress()
		}
	}
	if len(x.GetAddresses()) > 0 {
		return x.GetAddresses()[0].GetAddress()
	}
	return ""
}

var nodeIPv6 = changeSpec[nodeIPv6Args]{
	name: "node_ipv6", min: ProfileAdmin, danger: true,
	procs: procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.NodeServiceGetNodeProcedure,
		adminv1connect.NodeServiceCheckIpv6Procedure, adminv1connect.NodeServiceUpdateIpv6EntryProcedure),
	desc: "Turn a node's IPv6 entry on or off (action enable or disable). The entry adds a server \"<node> · IPv6\" next to each of the node's servers in everyone's subscription: it connects to the node's IPv6 address, for people whose network blocks the node's IPv4 address; apps without IPv6 skip it. " +
		"It is not the same as IPv6 for clients (whether people's traffic may leave the node over IPv6). Always needs the owner's approval in the admin panel: it changes what people's subscriptions hold. " +
		"enable first checks the address from outside (another node with working IPv6 sends the port-check packets to it; see node_ipv6_check) and turns the entry on only if the check passes; a node that cannot take an entry is refused with the reason. " +
		"Static AmneziaWG keys (.conf, vpn://), servers behind a CDN and servers with a certificate for the IPv4 address get no IPv6 server. The plan never shows an address.",
	plan: func(c *call, a nodeIPv6Args) (*planned, error) {
		if a.Action != ipv6Enable && a.Action != ipv6Disable {
			return nil, errors.New("action must be enable or disable")
		}
		id, name, err := c.nodeRef(a.Node)
		if err != nil {
			return nil, err
		}
		r, err := c.cl.Node.GetNode(c.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: id}))
		if err != nil {
			return nil, apiError(err)
		}
		x := r.Msg.GetNode().GetIpv6()
		if x == nil {
			return nil, errors.New("the panel did not say anything about the node's IPv6")
		}
		var summary, effect string
		facts := []Fact{{Key: "node", Value: nm(name), Untrusted: true}, {Key: "action", Value: a.Action}}
		switch a.Action {
		case ipv6Enable:
			switch {
			case x.GetEntry() != "":
				return nil, errors.New("the node's IPv6 entry is already on")
			case !x.GetEntrySupported():
				return nil, errors.New("the node cannot take an IPv6 entry: " + ipv6Reasons[clean(x.GetUnavailableReason(), 24)])
			}
			if ipv6PlanAddress(x) == "" {
				return nil, errors.New("the panel did not give a stable IPv6 address")
			}
			var servers []string
			for _, in := range r.Msg.GetInbounds() {
				if in.GetState() != adminv1.InboundState_INBOUND_STATE_DISABLED {
					servers = append(servers, nm(in.GetProfileName()))
				}
			}
			summary = "Turn on the IPv6 entry of one node: everyone who may use the node gets one more server, \"<node> · IPv6\", next to each of its servers. The owner has to approve it first."
			facts = append(facts,
				Fact{Key: "address", Value: "the IPv6 address the server itself uses for outbound IPv6 (not shown)"},
				Fact{Key: "profiles", Value: strings.Join(servers, ", "), Untrusted: true},
				Fact{Key: "check", Value: "before it is turned on the address is checked from outside, by another node with working IPv6 (else the panel, else Globalping); if the check does not pass, nothing changes"})
			effect = "At the next subscription refresh (every 12 hours by default) every person who may use this node gets the extra servers marked IPv6; apps without IPv6 skip them and the node's main address does not change. " +
				"Static AmneziaWG keys, servers behind a CDN and servers with a certificate for the IPv4 address get none."
		default:
			if x.GetEntry() == "" {
				return nil, errors.New("the node's IPv6 entry is already off")
			}
			summary = "Turn off the IPv6 entry of one node: its \"· IPv6\" servers leave the subscriptions. The owner has to approve it first."
			effect = "At the next subscription refresh the \"· IPv6\" servers of the node leave everyone's subscription; anyone connected through one has to pick another server."
		}
		facts = append(facts, Fact{Key: "effect", Value: effect})
		stored := nodeIPv6StoredArgs{Node: id, Action: a.Action}
		if a.Action == ipv6Enable {
			stored.Address = ipv6PlanAddress(x)
		}
		return &planned{Summary: summary, Facts: facts, Danger: []string{dangerFleet},
			Params: stored}, nil
	},
	applyStored: func(c *call, pl Plan) (done, error) {
		var a nodeIPv6StoredArgs
		if err := strictJSON([]byte(pl.ParamsJSON), &a); err != nil {
			return done{}, failure("plan_unreadable", "the stored plan is unreadable: make a new plan")
		}
		if a.Action == ipv6Enable {
			r, err := c.cl.Node.GetNode(c.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: a.Node}))
			if err != nil {
				return done{}, apiError(err)
			}
			stable := false
			for _, address := range r.Msg.GetNode().GetIpv6().GetAddresses() {
				if address.GetAddress() == a.Address {
					stable = true
					break
				}
			}
			if a.Address == "" || !stable {
				return done{}, failure("changed_since_plan", "the node's stable IPv6 address changed after the plan; make a new plan")
			}
		}
		action := adminv1.Ipv6EntryAction_IPV6_ENTRY_ACTION_DISABLE
		if a.Action == ipv6Enable {
			action = adminv1.Ipv6EntryAction_IPV6_ENTRY_ACTION_ENABLE
		}
		r, err := c.cl.Node.UpdateIpv6Entry(c.ctx, connect.NewRequest(&adminv1.UpdateIpv6EntryRequest{NodeId: a.Node, Action: action, Address: a.Address}))
		if err != nil {
			return done{}, apiError(err)
		}
		if code := clean(r.Msg.GetErrorCode(), 40); code != "" {
			return done{}, failure("ipv6_check_failed", "The IPv6 entry stays off: the address did not pass the check from outside ("+code+"). Nothing changed; node_ipv6_check shows which port failed.", "code", code)
		}
		if a.Action == ipv6Enable {
			return doneWith("ipv6_entry_on", "The IPv6 entry is on: the address passed the check from outside. People get the \"· IPv6\" servers at their next subscription refresh.", "node", a.Node), nil
		}
		return doneWith("ipv6_entry_off", "The IPv6 entry is off: the \"· IPv6\" servers leave the subscriptions at their next refresh.", "node", a.Node), nil
	},
	apply: func(_ *call, _ nodeIPv6Args, _ Plan) (done, error) {
		return done{}, failure("plan_unreadable", "the stored plan is unreadable: make a new plan")
	},
}
