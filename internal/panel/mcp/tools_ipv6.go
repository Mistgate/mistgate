package mcp

import (
	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// The IPv6 entry of a node (node.proto CheckIpv6, UpdateIpv6Entry): an extra server "<node> · IPv6" in people's subscriptions,
// for those whose network blocks the node's IPv4 address. The tools never show an address of a node, the IPv6 one included.
//
//   - node_ipv6_check sends test packets between the owner's nodes to the address the node itself uses for outbound IPv6. It
//     changes nothing and writes nothing but the audit row, so it is a read like relay_check: no approval, the admin profile
//     only because the panel's CheckIpv6 is the owner's.

func ipv6Tools() []toolDef {
	return []toolDef{
		readToolWithin(applyTimeout, "node_ipv6_check", ProfileAdmin, procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.NodeServiceCheckIpv6Procedure),
			"Check from outside that the node's IPv6 address answers, and that what answers is the node itself: another node whose IPv6 works (else the panel, else Globalping) "+
				"sends the same test packets as node_ports to the address instead of the main one: UDP datagrams to each of the node's UDP servers (counted at the node) and a TLS handshake to each of its TCP servers. "+
				"It changes nothing on any host and stores nothing; it can take up to a minute. "+
				"passed is true when every TCP port is ours (the connection reached this node) and every UDP port is ok. sender is the node that sent, panel, or globalping (it only proves a connection opens, not that it reached this node). "+
				"error_code is set when the check could not run: node_offline, agent_too_old, nothing_to_check, no_ipv6_verifier (no other node has working IPv6 and Globalping cannot connect) or failed. "+
				"A node that cannot take an IPv6 entry is refused with the reason: no_ipv6, ipv6_broken, agent_old, not_checked or main_is_ipv6. Never shows the address.",
			func(c *call, in nodeArg) (any, error) {
				id, name, err := c.nodeRef(in.Node)
				if err != nil {
					return nil, err
				}
				r, err := c.cl.Node.CheckIpv6(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.CheckIpv6Request{NodeId: id}))
				if err != nil {
					return nil, apiError(err)
				}
				return ipv6CheckView(id, name, r.Msg), nil
			}),
	}
}

// Ipv6CheckV is node_ipv6_check: the verdict of every port, never an address.
type Ipv6CheckV struct {
	NodeID      string      `json:"node_id"`
	Node        string      `json:"node"`
	Passed      bool        `json:"passed"`
	ErrorCode   string      `json:"error_code,omitempty"`
	Sender      string      `json:"sender,omitempty"`
	CheckedUnix int64       `json:"checked_unix,omitempty"`
	Ports       []ipv6PortV `json:"ports"`
}

type ipv6PortV struct {
	Network string `json:"network"`
	Port    uint32 `json:"port"`
	Verdict string `json:"verdict"`
}

func ipv6CheckView(id, name string, r *adminv1.CheckIpv6Response) *Ipv6CheckV {
	v := &Ipv6CheckV{NodeID: id, Node: nm(name), Passed: r.GetPassed(), ErrorCode: clean(r.GetErrorCode(), 40),
		Sender: nm(r.GetSender()), CheckedUnix: r.GetCheckedUnix(), Ports: []ipv6PortV{}}
	for _, p := range r.GetVerdicts() {
		v.Ports = append(v.Ports, ipv6PortV{Network: clean(p.GetNetwork(), 4), Port: p.GetPort(), Verdict: clean(p.GetVerdict(), 20)})
	}
	return v
}
