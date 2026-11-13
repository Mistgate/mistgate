package health

import (
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// An agent that cannot fall back by itself (no awg-fallback/1) and has no module gets the "switch to auto" offer; a new agent
// does not (its node is on the fallback or says something else), and neither does any other reason for an unavailable backend.
func TestSwitchToAutoIsOfferedToOldAgentsOnly(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	r := res("awg_backend", dFail, "", "mode", "kernel", "hint", "no_module", "reason", "the amneziawg kernel module is not loaded")
	r.DetailCode = "awg_backend.unavailable"
	e.report("de1", false, r)
	param := func() string {
		t.Helper()
		resp, err := rpc{e.s}.GetDoctor(e.ctx, req(&adminv1.GetDoctorRequest{NodeId: "de1"}))
		if err != nil || len(resp.Msg.Nodes) != 1 || len(resp.Msg.Nodes[0].Items) != 1 {
			t.Fatalf("GetDoctor: %+v, %v", resp, err)
		}
		return resp.Msg.Nodes[0].Items[0].Params["switch_auto"]
	}
	if param() != "1" {
		t.Fatal("an old agent in strict kernel mode was not offered the switch")
	}
	e.fl.set("de1", liveState{up: true, caps: []string{capDoctor, capAwgFallback}})
	if param() != "" {
		t.Fatal("an agent with awg-fallback/1 was offered the switch")
	}
	e.fl.set("de1", liveState{up: true, caps: []string{capDoctor}})
	other := res("awg_backend", dFail, "", "mode", "auto", "hint", "no_tun", "reason", "open /dev/net/tun: no such file or directory")
	other.DetailCode = "awg_backend.unavailable"
	e.report("de1", false, other)
	if param() != "" {
		t.Fatal("the switch was offered for a missing tun device")
	}
}

func TestAwgModuleRepairPlanIsDisruptive(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	resp, err := rpc{e.s}.ApplyFix(e.ctx, req(&adminv1.ApplyFixRequest{NodeId: "de1", FixId: "awg_module_repair", DryRun: true}))
	if err != nil || resp.Msg.Plan == nil || !resp.Msg.Plan.Disruptive || resp.Msg.Plan.TitleKey != "health.fix.awg_module_repair.plan" {
		t.Fatalf("plan = %+v, %v", resp, err)
	}
}
