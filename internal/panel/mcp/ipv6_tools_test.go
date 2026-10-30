package mcp

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
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

func supportedIPv6() *adminv1.NodeIpv6 {
	return &adminv1.NodeIpv6{EntrySupported: true, Src: canaryAddr6, Addresses: []*adminv1.Ipv6Address{{Address: canaryAddr6, PrefixLen: 64}},
		LastCheck: &adminv1.CheckIpv6Response{Address: canaryAddr6, Passed: true, Sender: "nl1", CheckedUnix: 1790842000,
			Verdicts: []*adminv1.Ipv6PortVerdict{{Network: "tcp", Port: 443, Verdict: "ours"}}}}
}

// node_get says whether the entry is on and what the latest check said, and never the address.
func TestNodeGetShowsTheIPv6EntryWithoutTheAddress(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	if n := decode[NodeGetV](t, mustOK(t, s, "node_get", map[string]any{"node": "de1"})); n.IPv6 != nil {
		t.Errorf("a node the panel said nothing of: %+v", n.IPv6)
	}
	x := supportedIPv6()
	x.Entry = canaryAddr6
	e.w.ipv6Node = map[string]*adminv1.NodeIpv6{nodeA: x, nodeB: {UnavailableReason: "no_ipv6"}}
	out := mustOK(t, s, "node_get", map[string]any{"node": "de1"})
	noCanary(t, "node_get", out)
	n := decode[NodeGetV](t, out)
	if n.IPv6 == nil || !n.IPv6.On || !n.IPv6.Supported || n.IPv6.LastCheck == nil || !n.IPv6.LastCheck.Passed || n.IPv6.LastCheck.Sender != "nl1" || len(n.IPv6.LastCheck.Ports) != 1 {
		t.Errorf("ipv6 = %+v", n.IPv6)
	}
	other := decode[NodeGetV](t, mustOK(t, s, "node_get", map[string]any{"node": "nl1"}))
	if other.IPv6 == nil || other.IPv6.On || other.IPv6.Supported || other.IPv6.UnavailableReason != "no_ipv6" || other.IPv6.LastCheck != nil {
		t.Errorf("ipv6 of a node without IPv6 = %+v", other.IPv6)
	}
}

func TestNodeIPv6EnableWaitsForTheOwnerThenTurnsTheEntryOn(t *testing.T) {
	e := newTestEnv(t)
	e.w.ipv6Node = map[string]*adminv1.NodeIpv6{nodeA: supportedIPv6()}
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)

	p := planOf(t, s, "node_ipv6", map[string]any{"node": "de1", "action": "enable", "reason": "people on one operator cannot reach de1"})
	if !p.NeedsApproval || len(p.Danger) != 1 || p.Danger[0] != dangerFleet {
		t.Fatalf("plan: %+v", p)
	}
	if st := storedPlan(t, e, p.PlanID); st.Status != StatusAwaiting || st.Tool != "node_ipv6" {
		t.Fatalf("stored plan: %+v", st)
	}
	if f := factOf(t, p, "node"); f.Value != "de1" || !f.Untrusted {
		t.Errorf("node fact: %+v", f)
	}
	if f := factOf(t, p, "profiles"); f.Value != "Main" || !f.Untrusted {
		t.Errorf("profiles fact: %+v", f)
	}
	if got := factOf(t, p, "address").Value; strings.Contains(got, "2001") || !strings.Contains(got, "not shown") {
		t.Errorf("address fact: %q", got)
	}
	if eff := factOf(t, p, "effect").Value; !strings.Contains(eff, "AmneziaWG") || !strings.Contains(eff, "12 hours") {
		t.Errorf("effect: %q", eff)
	}
	if len(e.w.ipv6Updates) != 0 {
		t.Fatal("the plan changed something")
	}
	if got := applyError(t, s, "node_ipv6", p.ConfirmToken); !strings.Contains(got, "waiting for the owner") {
		t.Fatalf("apply before approval: %q", got)
	}
	if len(e.w.ipv6Updates) != 0 {
		t.Fatal("apply before approval changed something")
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	ap := applyOf(t, s, "node_ipv6", p.ConfirmToken)
	if ap.Status != StatusApplied || !strings.Contains(ap.Result, "IPv6 entry is on") {
		t.Fatalf("apply: %+v", ap)
	}
	if len(e.w.ipv6Updates) != 1 || e.w.ipv6Updates[0].GetNodeId() != nodeA || e.w.ipv6Updates[0].GetAction() != adminv1.Ipv6EntryAction_IPV6_ENTRY_ACTION_ENABLE || e.w.ipv6Updates[0].GetAddress() != "" {
		t.Fatalf("UpdateIpv6Entry: %+v", e.w.ipv6Updates)
	}
	// the change goes out under the plan's grant, once
	if calls := e.w.calls("/mistgate.admin.v1.NodeService/UpdateIpv6Entry"); len(calls) != 1 || calls[0].Approved != p.PlanID || calls[0].Channel != "mcp" {
		t.Errorf("calls: %+v", calls)
	}
	applyOf(t, s, "node_ipv6", p.ConfirmToken)
	if len(e.w.ipv6Updates) != 1 {
		t.Error("a replay changed the node again")
	}
	// now it is on: enabling again is refused at the plan, disabling is not
	if got := mustFail(t, s, "node_ipv6_plan", map[string]any{"node": "de1", "action": "enable"}); !strings.Contains(got, "already on") {
		t.Errorf("enable while on: %q", got)
	}
	d := planOf(t, s, "node_ipv6", map[string]any{"node": "de1", "action": "disable"})
	if !d.NeedsApproval || !strings.Contains(factOf(t, d, "effect").Value, "leave everyone") {
		t.Errorf("disable plan: %+v", d)
	}
	if err := e.plans.decide(d.PlanID, true); err != nil {
		t.Fatal(err)
	}
	if ap := applyOf(t, s, "node_ipv6", d.ConfirmToken); !strings.Contains(ap.Result, "IPv6 entry is off") || e.w.ipv6Updates[1].GetAction() != adminv1.Ipv6EntryAction_IPV6_ENTRY_ACTION_DISABLE {
		t.Errorf("disable: %+v %+v", ap, e.w.ipv6Updates)
	}
	if got := mustFail(t, s, "node_ipv6_plan", map[string]any{"node": "de1", "action": "disable"}); !strings.Contains(got, "already off") {
		t.Errorf("disable while off: %q", got)
	}
}

func TestNodeIPv6PlanRefusesWhatTheNodeCannotTake(t *testing.T) {
	e := newTestEnv(t)
	e.w.ipv6Node = map[string]*adminv1.NodeIpv6{nodeA: {UnavailableReason: "ipv6_broken"}, nodeB: supportedIPv6()}
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	if got := mustFail(t, s, "node_ipv6_plan", map[string]any{"node": "de1", "action": "enable"}); !strings.Contains(got, "outbound IPv6 does not work") {
		t.Errorf("a node with broken IPv6: %q", got)
	}
	for _, args := range []map[string]any{{"node": "nl1", "action": "rotate"}, {"node": "nl1"}, {"action": "enable"}, {"node": "nope", "action": "enable"}} {
		mustFail(t, s, "node_ipv6_plan", args)
	}
	if len(e.plans.byID) != 0 {
		t.Error("a refused plan was stored")
	}
	// the tools are the admin's alone
	for _, p := range []Profile{ProfileReadonly, ProfileOperator} {
		_, sec := e.token(p)
		for _, name := range toolNames(t, e.session(sec)) {
			if strings.HasPrefix(name, "node_ipv6") {
				t.Errorf("%s sees %s", p, name)
			}
		}
	}
}

// A check that fails at the apply leaves the entry off and says so; the owner's approval is not the check.
func TestNodeIPv6EnableThatFailsTheCheckSaysSo(t *testing.T) {
	e := newTestEnv(t)
	e.w.ipv6Node = map[string]*adminv1.NodeIpv6{nodeA: supportedIPv6()}
	e.w.ipv6UpdateCode = "check_failed"
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	p := planOf(t, s, "node_ipv6", map[string]any{"node": "de1", "action": "enable"})
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	got := applyError(t, s, "node_ipv6", p.ConfirmToken)
	if !strings.Contains(got, "stays off") || !strings.Contains(got, "check_failed") {
		t.Errorf("apply: %q", got)
	}
}
