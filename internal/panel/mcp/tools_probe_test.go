//go:build !js

package mcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	probev1 "github.com/mistgate/mistgate/gen/mistgate/probe/v1"
)

// Fakes of the three probes. The fake panel answers with node names only; the tests check what the tools send (ids, the sweep
// sizes) and that nothing an address or a router would look like comes out.

func (w *world) PathProbe(_ context.Context, r *connect.Request[probev1.PathProbeRequest]) (*connect.Response[probev1.PathProbeResponse], error) {
	w.mu.Lock()
	w.probeReqs = append(w.probeReqs, r.Msg)
	w.mu.Unlock()
	if w.probeRateLimited {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("probe rate limit reached: try again in 42 s"))
	}
	out := &probev1.PathProbeResponse{FromName: "nl1", ToName: "de1", Kind: r.Msg.GetKind(), AtUnix: 1700000000}
	switch {
	case len(r.Msg.GetSizes()) > 0:
		out.Sizes = []*probev1.PathProbeSize{{Size: 1200, Sent: 10, Got: 10}, {Size: 1452, Sent: 10, Got: 0}}
		out.LargestSize = 1200
	case r.Msg.GetKind() == "udp":
		out.Ports = []*probev1.PathProbePort{{Port: 443, Sent: 150, Got: 149, Verdict: "ok"}}
	default:
		out.Ports = []*probev1.PathProbePort{{Port: 443, Verdict: "ours", Ms: 31}}
	}
	return connect.NewResponse(out), nil
}

func (w *world) NodeTrace(_ context.Context, r *connect.Request[probev1.NodeTraceRequest]) (*connect.Response[probev1.NodeTraceResponse], error) {
	w.mu.Lock()
	w.traceReqs = append(w.traceReqs, r.Msg)
	w.mu.Unlock()
	return connect.NewResponse(&probev1.NodeTraceResponse{FromName: "nl1", ToName: "de1", AtUnix: 1700000000, Reached: true,
		Hops: []*probev1.TraceHop{
			{Ttl: 1, RttMs: 1, Answered: true, Label: "hop 1"}, {Ttl: 2, Answered: false, Label: "hop 2"},
			{Ttl: 3, RttMs: 12, Answered: true, Label: "de1"}},
		ChangedAtHop: 3, Diff: "route changed at hop 3: the target is reached at hop 3, it was 4", PreviousUnix: 1699999000, Kept: 2}), nil
}

func (w *world) NodeDnsCheck(_ context.Context, r *connect.Request[probev1.NodeDnsCheckRequest]) (*connect.Response[probev1.NodeDnsCheckResponse], error) {
	w.mu.Lock()
	w.dnsReqs = append(w.dnsReqs, r.Msg)
	w.mu.Unlock()
	return connect.NewResponse(&probev1.NodeDnsCheckResponse{NodeName: "de1", Trend: "steady",
		Latest:  &probev1.DnsRun{AtUnix: 1700000000, P50Ms: 12, P95Ms: 40, Domains: 3},
		History: []*probev1.DnsRun{{AtUnix: 1699999400, P50Ms: 11, P95Ms: 35, Domains: 3}, {AtUnix: 1700000000, P50Ms: 12, P95Ms: 40, Domains: 3}}}), nil
}

func TestProbeToolsAreOperatorUpAndNotReadOnly(t *testing.T) {
	e := newTestEnv(t)
	for _, name := range []string{"path_probe", "node_trace", "node_dns_check"} {
		_, sRO := e.token(ProfileReadonly)
		if slices.Contains(toolNames(t, e.session(sRO)), name) {
			t.Errorf("readonly sees %s: it sends traffic", name)
		}
		_, sOp := e.token(ProfileOperator)
		if !slices.Contains(toolNames(t, e.session(sOp)), name) {
			t.Errorf("operator does not see %s", name)
		}
	}
	var def []toolDef
	for _, d := range registry {
		if d.name == "path_probe" || d.name == "node_trace" || d.name == "node_dns_check" {
			def = append(def, d)
		}
	}
	if len(def) != 3 {
		t.Fatalf("%d probe tools in the registry, want 3", len(def))
	}
	for _, d := range def {
		if !strings.Contains(d.desc, "rate limited") || !strings.Contains(d.desc, "never addresses") && !strings.Contains(d.desc, "No addresses") && !strings.Contains(d.desc, "never returns a third party") {
			t.Errorf("%s: the description does not say it is rate limited and returns no addresses", d.name)
		}
	}
}

func TestPathProbeSendsNodeIdsAndTheSweep(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	out := mustOK(t, s, "path_probe", map[string]any{"from": "nl1", "to": "de1", "kind": "udp", "mtu_sweep": true})
	noCanary(t, "path_probe", out)
	v := decode[PathProbeV](t, out)
	if v.From != "nl1" || v.To != "de1" || v.Largest != 1200 || v.MTU != 1228 || len(v.Sizes) != 2 {
		t.Errorf("sweep result = %+v", v)
	}
	req := e.w.probeReqs[0]
	if req.GetFromNodeId() != nodeB || req.GetToNodeId() != nodeA || !slices.Equal(req.GetSizes(), []uint32{256, 576, 1200, 1350, 1452}) {
		t.Errorf("request = %v", req)
	}
	tcp := decode[PathProbeV](t, mustOK(t, s, "path_probe", map[string]any{"from": nodeB, "to": "de1", "kind": "tls", "ports": []int{443}}))
	if len(tcp.Ports) != 1 || tcp.Ports[0].Verdict != "ours" || tcp.Ports[0].MS != 31 {
		t.Errorf("tls result = %+v", tcp)
	}
	if got := e.w.probeReqs[1].GetPorts(); !slices.Equal(got, []uint32{443}) {
		t.Errorf("ports sent = %v", got)
	}
	// no way to name a host: the arguments are node references only, and an unknown node or kind is refused before the panel is asked
	before := len(e.w.probeReqs)
	mustFail(t, s, "path_probe", map[string]any{"from": "nl1", "to": "203.0.113.5", "kind": "udp"})
	mustFail(t, s, "path_probe", map[string]any{"from": "nl1", "to": "http://example.com/", "kind": "tcp"})
	mustFail(t, s, "path_probe", map[string]any{"from": "nl1", "to": "de1", "kind": "icmp"})
	mustFail(t, s, "path_probe", map[string]any{"from": "nl1", "to": "de1", "kind": "udp", "ports": []int{70000}})
	for _, size := range []int{63, 1473, 4294967360} {
		if msg := mustFail(t, s, "path_probe", map[string]any{"from": "nl1", "to": "de1", "kind": "udp", "sizes": []int{size}}); !strings.Contains(msg, "sizes: 64 to 1472") {
			t.Errorf("size %d error = %q, want the supported range", size, msg)
		}
	}
	if len(e.w.probeReqs) != before {
		t.Errorf("the panel was asked for a bad request: %d calls", len(e.w.probeReqs)-before)
	}
}

func TestPathProbeRateLimitKeepsTheRetryTime(t *testing.T) {
	e := newTestEnv(t)
	e.w.probeRateLimited = true
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	msg := mustFail(t, s, "path_probe", map[string]any{"from": "nl1", "to": "de1", "kind": "udp"})
	if !strings.Contains(msg, "try again in 42 s") {
		t.Errorf("error = %q, want the retry time", msg)
	}
}

func TestNodeTraceAndDNSCheckShowNamesAndTrends(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	out := mustOK(t, s, "node_trace", map[string]any{"from": "nl1", "to": "de1"})
	noCanary(t, "node_trace", out)
	tr := decode[NodeTraceV](t, out)
	if !tr.Reached || len(tr.Hops) != 3 || tr.Hops[2].Label != "de1" || tr.Hops[1].Answered || tr.ChangedAtHop != 3 || !strings.Contains(tr.Diff, "route changed at hop 3") {
		t.Errorf("trace = %+v", tr)
	}
	out = mustOK(t, s, "node_dns_check", map[string]any{"node": "de1", "stored_only": true})
	noCanary(t, "node_dns_check", out)
	d := decode[NodeDNSV](t, out)
	if d.Node != "de1" || d.Latest == nil || d.Latest.P95MS != 40 || len(d.History) != 2 || d.Trend != "steady" {
		t.Errorf("dns = %+v", d)
	}
	if req := e.w.dnsReqs[0]; req.GetRun() || req.GetNodeId() != nodeA {
		t.Errorf("stored_only asked for a run: %v", req)
	}
	mustOK(t, s, "node_dns_check", map[string]any{"node": "de1"})
	if !e.w.dnsReqs[1].GetRun() {
		t.Error("node_dns_check did not ask for a run by default")
	}
}
