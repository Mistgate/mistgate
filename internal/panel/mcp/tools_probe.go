//go:build !js

package mcp

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	probev1 "github.com/mistgate/mistgate/gen/mistgate/probe/v1"
	"github.com/mistgate/mistgate/gen/mistgate/probe/v1/probev1connect"
)

// Tools that run a probe between your nodes: they send test traffic (bounded, rate-limited), unlike every read tool, so they are
// not marked read-only. Every target is one of your own nodes, named by id or exact name and resolved by the panel; there is no
// way to name a host or a URL. Results carry node names, counts and timings: never an address, a certificate pin or a router.

const (
	probeToolTimeout = 50 * time.Second
	traceToolTimeout = 85 * time.Second // a walk of 30 hops of 1.5 s; under applyTimeout
)

// sweepSizes is the MTU sweep of mtu_sweep: payload sizes of common paths (1452 is a 1500 MTU less 48 bytes of headers).
var sweepSizes = []uint32{256, 576, 1200, 1350, 1452}

const probeLimits = "It sends real traffic between your nodes, so it is rate limited: one probe per node pair at a time, at most 6 per node in 5 minutes and 12 per token in 10 minutes (a refused call says when to retry); do not repeat it to watch a number move. "

func probeTools() []toolDef {
	return append(change(pathSpeed), []toolDef{
		probeTool("path_probe", probeToolTimeout, procs(adminv1connect.NodeServiceListNodesProcedure, probev1connect.ProbeServicePathProbeProcedure),
			"Probe the path from one of your nodes (from) to another (to) now, to tell a bad node from a bad path or a filtered port. Both are node ids or exact names; the targets are always the ports the to node itself serves, never a host you name. "+
				"kind udp: the sender sends 150 tagged 256-byte datagrams per port at 25 a second and the target counts them (about 6 s): per port sent, got and a verdict ok, lossy or broken. "+
				"kind tcp: a plain TCP connect to each TCP port the to node serves (443, the ACME port 80 and so on), with the milliseconds to the verdict (ours = connected, refused, timeout). "+
				"kind tls: a TLS handshake to the to node's TLS ports with its own server name and certificate pin, the verdict ours only for the real listener (other, refused or timeout otherwise) and the milliseconds including the handshake. "+
				"ports narrows the probe to some of the target's ports (a port it does not serve is refused). udp only: mtu_sweep (or sizes, up to 5 payload sizes of 64 to 1472) sends ten don't-fragment datagrams of each size to one port and reports sent and got per size and largest_size, the largest that mostly arrived: the path MTU is that plus 28 bytes of headers. "+
				"The sender must be online with an agent that supports probes (error agent_too_old otherwise); a udp target must be online too. Results are kept 7 days. "+probeLimits+"Node names only, never addresses.",
			func(c *call, in pathProbeArg) (any, error) { return c.pathProbe(in) }),

		probeTool("node_trace", traceToolTimeout, procs(adminv1connect.NodeServiceListNodesProcedure, probev1connect.ProbeServiceNodeTraceProcedure),
			"Trace the route from one of your nodes (from) to another (to): a TCP SYN walk with a growing TTL, one hop at a time (up to about 45 s). Returns per hop the round trip in milliseconds and whether a router answered (answered false = it stayed silent, which many routers do on purpose), and reached when the target itself answered; the target's hop carries its node name, every other hop is labelled hop N because the panel never returns a third party's address. "+
				"Also compares the trace with the previous one of the same pair (the last 20 are kept): diff says whether the route length changed (changed_at_hop), a hop stopped answering, or a round trip jumped. Because no router address is kept, a route that changed without changing its length or answering pattern shows as same as before. "+
				probeLimits+"Linux nodes with a probe-capable agent only.",
			func(c *call, in nodeTraceArg) (any, error) { return c.nodeTrace(in) }),

		probeTool("node_dns_check", probeToolTimeout, procs(adminv1connect.NodeServiceListNodesProcedure, probev1connect.ProbeServiceNodeDnsCheckProcedure),
			"How fast and reliable a node's own DNS resolver is, from the doctor's resolver check: lookup timing (p50 and p95 in milliseconds) and how many of the control domains failed, for the newest run, with the history of the last 24 runs (oldest first) and a trend (steady, slower, faster, unknown until 4 runs). "+
				"Unless stored_only is true it asks the node to run the check now (a handful of lookups of public control domains from the node, a few seconds). The stored doctor results are folded into the history too. "+probeLimits+"No addresses.",
			func(c *call, in nodeDNSArg) (any, error) { return c.nodeDNSCheck(in) }),
	}...)
}

// probeTool is readToolWithin for a tool that acts on the network: not read-only, never destructive, not idempotent (every
// call sends traffic), and closed to the world outside the owner's nodes.
func probeTool[In any](name string, timeout time.Duration, procs []string, desc string, h func(c *call, in In) (any, error)) toolDef {
	return toolDef{
		name: name, min: ProfileOperator, procs: procs, free: true, desc: desc,
		add: func(s *mcp.Server, e *env, desc string) {
			mcp.AddTool(s, &mcp.Tool{
				Name: name, Description: desc,
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: ptr(false), IdempotentHint: false, OpenWorldHint: ptr(false)},
			}, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
				c, err := e.begin(ctx, req, timeout)
				if err != nil {
					return nil, nil, err
				}
				defer c.done()
				v, err := h(c, in)
				if err != nil {
					return nil, nil, scrubError(err)
				}
				return result(v)
			})
		},
	}
}

// probeError is apiError that keeps the retry time of the probe limiter, which apiError words as a plain "slow down".
func probeError(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) && ce.Code() == connect.CodeResourceExhausted {
		return failure("rate_limited", clean(ce.Message(), maxError))
	}
	return apiError(err)
}

type pathProbeArg struct {
	From     string `json:"from" jsonschema:"the sending node: id or exact name"`
	To       string `json:"to" jsonschema:"the target node: id or exact name; its own ports are probed"`
	Kind     string `json:"kind" jsonschema:"udp, tcp or tls"`
	Ports    []int  `json:"ports,omitempty" jsonschema:"some of the target's own ports (at most 8); empty = all it serves for that kind"`
	MTUSweep bool   `json:"mtu_sweep,omitempty" jsonschema:"udp only: sweep the sizes 256, 576, 1200, 1350 and 1452 with the don't-fragment flag on one port"`
	Sizes    []int  `json:"sizes,omitempty" jsonschema:"udp only: your own sweep sizes, at most 5 payload sizes of 64 to 1472; implies the sweep"`
}

type nodeTraceArg struct {
	From string `json:"from" jsonschema:"the node the walk starts from: id or exact name"`
	To   string `json:"to" jsonschema:"the target node: id or exact name"`
}

type nodeDNSArg struct {
	Node       string `json:"node" jsonschema:"node id or exact name"`
	StoredOnly bool   `json:"stored_only,omitempty" jsonschema:"true: do not run the check, show the stored history only"`
}

type probePortV struct {
	Port    uint32 `json:"port"`
	Sent    uint32 `json:"sent,omitempty"`
	Got     uint32 `json:"got,omitempty"`
	Verdict string `json:"verdict"`
	MS      uint32 `json:"ms,omitempty"`
}

type probeSizeV struct {
	Size uint32 `json:"size"`
	Sent uint32 `json:"sent"`
	Got  uint32 `json:"got"`
}

// PathProbeV is the path_probe result.
type PathProbeV struct {
	From    string       `json:"from"`
	To      string       `json:"to"`
	Kind    string       `json:"kind"`
	AtUnix  int64        `json:"at_unix"`
	Error   string       `json:"error,omitempty"`
	Ports   []probePortV `json:"ports,omitempty"`
	Sizes   []probeSizeV `json:"sizes,omitempty"`
	Largest uint32       `json:"largest_size,omitempty"`
	// MTU is largest_size plus the 28 bytes of the IPv4 and UDP headers: the path MTU when the sweep found an edge.
	MTU uint32 `json:"path_mtu_at_least,omitempty"`
}

func (c *call) pathProbe(in pathProbeArg) (any, error) {
	if in.Kind != "udp" && in.Kind != "tcp" && in.Kind != "tls" {
		return nil, errors.New("kind: udp, tcp or tls")
	}
	from, _, err := c.nodeRef(in.From)
	if err != nil {
		return nil, err
	}
	to, _, err := c.nodeRef(in.To)
	if err != nil {
		return nil, err
	}
	req := &probev1.PathProbeRequest{FromNodeId: from, ToNodeId: to, Kind: in.Kind}
	for _, p := range in.Ports {
		if p < 1 || p > 65535 {
			return nil, errors.New("ports: 1 to 65535")
		}
		req.Ports = append(req.Ports, uint32(p))
	}
	for _, s := range in.Sizes {
		if s < 0 {
			return nil, errors.New("sizes: 64 to 1472")
		}
		req.Sizes = append(req.Sizes, uint32(s))
	}
	if in.MTUSweep && len(req.Sizes) == 0 {
		req.Sizes = append(req.Sizes, sweepSizes...)
	}
	r, err := c.cl.Probe.PathProbe(c.ctx, connect.NewRequest(req))
	if err != nil {
		return nil, probeError(err)
	}
	m := r.Msg
	v := &PathProbeV{From: nm(m.GetFromName()), To: nm(m.GetToName()), Kind: m.GetKind(), AtUnix: m.GetAtUnix(), Error: clean(m.GetError(), 40), Largest: m.GetLargestSize()}
	for _, p := range m.GetPorts() {
		v.Ports = append(v.Ports, probePortV{Port: p.GetPort(), Sent: p.GetSent(), Got: p.GetGot(), Verdict: clean(p.GetVerdict(), 20), MS: p.GetMs()})
	}
	for _, s := range m.GetSizes() {
		v.Sizes = append(v.Sizes, probeSizeV{Size: s.GetSize(), Sent: s.GetSent(), Got: s.GetGot()})
	}
	if v.Largest > 0 {
		v.MTU = v.Largest + 28
	}
	return v, nil
}

type traceHopV struct {
	TTL      uint32 `json:"ttl"`
	RTTMS    uint32 `json:"rtt_ms,omitempty"`
	Answered bool   `json:"answered"`
	Label    string `json:"label"`
}

// NodeTraceV is the node_trace result.
type NodeTraceV struct {
	From         string      `json:"from"`
	To           string      `json:"to"`
	AtUnix       int64       `json:"at_unix"`
	Error        string      `json:"error,omitempty"`
	Reached      bool        `json:"reached"`
	Hops         []traceHopV `json:"hops,omitempty"`
	ChangedAtHop uint32      `json:"changed_at_hop,omitempty"`
	Diff         string      `json:"diff,omitempty"`
	PreviousUnix int64       `json:"previous_unix,omitempty"`
	Kept         uint32      `json:"traces_kept,omitempty"`
}

func (c *call) nodeTrace(in nodeTraceArg) (any, error) {
	from, _, err := c.nodeRef(in.From)
	if err != nil {
		return nil, err
	}
	to, _, err := c.nodeRef(in.To)
	if err != nil {
		return nil, err
	}
	r, err := c.cl.Probe.NodeTrace(c.ctx, connect.NewRequest(&probev1.NodeTraceRequest{FromNodeId: from, ToNodeId: to}))
	if err != nil {
		return nil, probeError(err)
	}
	m := r.Msg
	v := &NodeTraceV{From: nm(m.GetFromName()), To: nm(m.GetToName()), AtUnix: m.GetAtUnix(), Error: clean(m.GetError(), 40), Reached: m.GetReached(),
		ChangedAtHop: m.GetChangedAtHop(), Diff: clean(m.GetDiff(), 200), PreviousUnix: m.GetPreviousUnix(), Kept: m.GetKept()}
	for _, h := range m.GetHops() {
		v.Hops = append(v.Hops, traceHopV{TTL: h.GetTtl(), RTTMS: h.GetRttMs(), Answered: h.GetAnswered(), Label: nm(h.GetLabel())})
	}
	return v, nil
}

type dnsRunV struct {
	AtUnix  int64  `json:"at_unix"`
	P50MS   uint32 `json:"p50_ms"`
	P95MS   uint32 `json:"p95_ms"`
	Failed  uint32 `json:"failed"`
	Domains uint32 `json:"domains"`
}

// NodeDNSV is the node_dns_check result.
type NodeDNSV struct {
	Node    string    `json:"node"`
	Error   string    `json:"error,omitempty"`
	Latest  *dnsRunV  `json:"latest,omitempty"`
	History []dnsRunV `json:"history"`
	Trend   string    `json:"trend"`
}

func (c *call) nodeDNSCheck(in nodeDNSArg) (any, error) {
	id, _, err := c.nodeRef(in.Node)
	if err != nil {
		return nil, err
	}
	r, err := c.cl.Probe.NodeDnsCheck(c.ctx, connect.NewRequest(&probev1.NodeDnsCheckRequest{NodeId: id, Run: !in.StoredOnly}))
	if err != nil {
		return nil, probeError(err)
	}
	m := r.Msg
	run := func(d *probev1.DnsRun) dnsRunV {
		return dnsRunV{AtUnix: d.GetAtUnix(), P50MS: d.GetP50Ms(), P95MS: d.GetP95Ms(), Failed: d.GetFailed(), Domains: d.GetDomains()}
	}
	v := &NodeDNSV{Node: nm(m.GetNodeName()), Error: clean(m.GetError(), 40), History: []dnsRunV{}, Trend: clean(m.GetTrend(), 20)}
	if m.GetLatest() != nil {
		l := run(m.GetLatest())
		v.Latest = &l
	}
	for _, d := range m.GetHistory() {
		v.History = append(v.History, run(d))
	}
	return v, nil
}
