package mcp

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

type ipv6CheckResult struct {
	NodeID      string `json:"node_id"`
	Node        string `json:"node"`
	Passed      bool   `json:"passed"`
	ErrorCode   string `json:"error_code"`
	Sender      string `json:"sender"`
	CheckedUnix int64  `json:"checked_unix"`
	Ports       []struct {
		Network string `json:"network"`
		Port    int    `json:"port"`
		Verdict string `json:"verdict"`
	} `json:"ports"`
}

// node_ipv6_check is a read of the admin profile: it sends test packets between the owner's nodes, changes nothing and makes no
// plan. It never shows the address that was checked, the IPv6 one included.
func TestNodeIPv6CheckIsAnAdminReadWithoutAPlanAndNoAddress(t *testing.T) {
	e := newTestEnv(t)
	for _, p := range []Profile{ProfileReadonly, ProfileOperator} {
		_, secret := e.token(p)
		if slices.Contains(toolNames(t, e.session(secret)), "node_ipv6_check") {
			t.Errorf("%s sees node_ipv6_check", p)
		}
	}
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	out := mustOK(t, s, "node_ipv6_check", map[string]any{"node": "de1"})
	noCanary(t, "node_ipv6_check", out)
	r := decode[ipv6CheckResult](t, out)
	if r.NodeID != nodeA || r.Node != "de1" || !r.Passed || r.ErrorCode != "" || r.Sender != "nl1" || len(r.Ports) != 2 || r.Ports[0].Network != "tcp" || r.Ports[0].Verdict != "ours" || r.Ports[1].Port != 8443 {
		t.Errorf("result = %+v", r)
	}
	e.w.mu.Lock()
	reqs := e.w.ipv6Reqs
	e.w.mu.Unlock()
	if len(reqs) != 1 || reqs[0].GetNodeId() != nodeA || reqs[0].GetAddress() != "" {
		t.Errorf("CheckIpv6 requests = %+v: the node alone, the panel picks the address", reqs)
	}
	if len(e.plans.byID) != 0 || len(e.audits("mcp_plan")) != 0 {
		t.Error("node_ipv6_check made a plan")
	}
	if got := mustFail(t, s, "node_ipv6_check", map[string]any{"node": "nope"}); !strings.Contains(got, "node not found") {
		t.Errorf("unknown node: %q", got)
	}

	// a node that cannot take an entry is refused with the reason; the panel's words reach the agent
	e.w.mu.Lock()
	e.w.ipv6Err = connect.NewError(connect.CodeFailedPrecondition, errors.New("ipv6_broken"))
	e.w.mu.Unlock()
	if got := mustFail(t, s, "node_ipv6_check", map[string]any{"node": "de1"}); !strings.Contains(got, "ipv6_broken") {
		t.Errorf("refusal: %q", got)
	}
}
