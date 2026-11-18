//go:build !js

package mcp

import (
	"strings"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/warp"
)

type warpStatusProbeResult struct {
	OK        bool   `json:"ok"`
	LatencyMS uint32 `json:"latency_ms"`
}

type warpStatusResult struct {
	NodeID            string                 `json:"node_id"`
	NodeName          string                 `json:"node_name"`
	HasAccount        bool                   `json:"has_account"`
	Enabled           bool                   `json:"enabled"`
	Paused            bool                   `json:"paused"`
	AgentState        string                 `json:"agent_state"`
	Colo              string                 `json:"colo"`
	LastHandshakeAgeS *uint64                `json:"last_handshake_age_s"`
	ProbeCloudflare   *warpStatusProbeResult `json:"probe_cloudflare"`
	ProbeOther        *warpStatusProbeResult `json:"probe_other"`
	RegisteredUnix    int64                  `json:"registered_unix"`
	HasToken          bool                   `json:"has_token"`
	Profiles          []string               `json:"profiles"`
}

func TestWarpStatusProjectsSafeFieldsAndResolvesNodes(t *testing.T) {
	e := newTestEnv(t)
	e.w.warp[nodeA].Health.LastHandshakeUnix = e.now().Add(-95 * time.Second).Unix()
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)

	for _, ref := range []string{nodeA, "de1"} {
		out := mustOK(t, s, "warp_status", map[string]any{"node": ref})
		if strings.Contains(out, "198.51.100.19") || strings.Contains(out, "198.51.100.20") ||
			strings.Contains(out, "warp-public-key-fixture") || strings.Contains(out, "cf-account-id-fixture") {
			t.Fatalf("WARP details leaked for %q: %s", ref, out)
		}
		v := decode[warpStatusResult](t, out)
		if v.NodeID != nodeA || v.NodeName != "de1" || !v.HasAccount || !v.Enabled || v.Paused || v.AgentState != "up" ||
			v.Colo != "DE" || v.LastHandshakeAgeS == nil || *v.LastHandshakeAgeS != 95 ||
			v.ProbeCloudflare == nil || !v.ProbeCloudflare.OK || v.ProbeCloudflare.LatencyMS != 24 ||
			v.ProbeOther == nil || v.ProbeOther.OK || v.ProbeOther.LatencyMS != 87 ||
			v.RegisteredUnix != 1700000000 || !v.HasToken || len(v.Profiles) != 2 || v.Profiles[0] != "Main" || v.Profiles[1] != "Backup" {
			t.Errorf("WARP status for %q: %+v", ref, v)
		}
	}
	if got := mustFail(t, s, "warp_status", map[string]any{"node": "missing"}); !strings.Contains(got, "node not found") {
		t.Errorf("unknown node: %q", got)
	}
}

func TestWarpRestartPlanAndApply(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)

	p := planOf(t, s, "warp_restart", map[string]any{"node": "de1"})
	if p.NeedsApproval || !strings.Contains(p.Summary, "few seconds") || !strings.Contains(p.Summary, "WARP") {
		t.Fatalf("restart plan: %+v", p)
	}
	profiles := factOf(t, p, "profiles")
	if profiles.Value != "Main, Backup" || !profiles.Untrusted {
		t.Fatalf("restart profiles fact: %+v", profiles)
	}
	if len(e.w.warpRestarts) != 0 {
		t.Fatal("restart plan changed the node")
	}
	if !strings.Contains(mustOK(t, s, "warp_restart_apply", map[string]any{"confirm_token": p.ConfirmToken}), "restarted") {
		t.Fatal("restart apply did not report success")
	}
	if got := len(e.w.warpRestarts); got != 1 || e.w.warpRestarts[0].GetNodeId() != nodeA {
		t.Fatalf("RestartWarp calls: %+v", e.w.warpRestarts)
	}
	applyOf(t, s, "warp_restart", p.ConfirmToken)
	if got := len(e.w.warpRestarts); got != 1 {
		t.Fatalf("replayed restart apply called RestartWarp %d times", got)
	}
}

func TestWarpRestartPlanRefusesUnsafeStates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testEnv)
		ref   string
		wants string
	}{
		{
			name: "offline",
			setup: func(e *testEnv) {
				e.w.warp[nodeB] = &adminv1.GetWarpResponse{Account: &adminv1.WarpAccount{NodeId: nodeB, Enabled: true}}
			},
			ref: "nl1", wants: "node is offline",
		},
		{
			name: "no account",
			setup: func(e *testEnv) {
				e.w.nodeStatuses[nodeB] = adminv1.NodeStatus_NODE_STATUS_ONLINE
			},
			ref: "nl1", wants: "node has no WARP account",
		},
		{
			name: "paused",
			setup: func(e *testEnv) {
				e.w.warp[nodeA].Account.Enabled = false
			},
			ref: "de1", wants: "WARP account is paused",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			tc.setup(e)
			_, secret := e.token(ProfileAdmin)
			s := e.session(secret)
			if got := mustFail(t, s, "warp_restart_plan", map[string]any{"node": tc.ref}); !strings.Contains(got, tc.wants) {
				t.Errorf("refusal: %q", got)
			}
			if len(e.plans.byID) != 0 || len(e.w.warpRestarts) != 0 {
				t.Fatal("unsafe restart created a plan or called RestartWarp")
			}
		})
	}
}

func TestWarpReregisterWaitsForOwnerAndReplacesOnce(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	p := planOf(t, s, "warp_reregister", map[string]any{"node": "de1", "reason": "replace a degraded exit"})
	if !p.NeedsApproval || len(p.Danger) != 1 || p.Danger[0] != dangerStepUp {
		t.Fatalf("reregister plan: %+v", p)
	}
	stored := storedPlan(t, e, p.PlanID)
	if stored.Status != StatusAwaiting || stored.Reason != "replace a degraded exit" {
		t.Fatalf("stored reregister plan: %+v", stored)
	}
	terms := factOf(t, p, "terms_url")
	if terms.Value != warp.TOSURL {
		t.Fatalf("terms fact: %+v", terms)
	}
	profiles := factOf(t, p, "profiles")
	if profiles.Value != "Main, Backup" || !profiles.Untrusted {
		t.Fatalf("reregister profiles fact: %+v", profiles)
	}
	if got := applyError(t, s, "warp_reregister", p.ConfirmToken); !strings.Contains(got, "waiting for the owner") {
		t.Fatalf("apply before approval: %q", got)
	}
	if len(e.w.warpRegistrations) != 0 {
		t.Fatal("registration ran before owner approval")
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	applyOf(t, s, "warp_reregister", p.ConfirmToken)
	if got := len(e.w.warpRegistrations); got != 1 {
		t.Fatalf("RegisterWarp calls: %d", got)
	}
	req := e.w.warpRegistrations[0]
	if req.GetNodeId() != nodeA || !req.GetReplaceExisting() || !req.GetAcceptTos() || req.GetTosUrlShown() != warp.TOSURL {
		t.Fatalf("RegisterWarp request: %+v", req)
	}
	applyOf(t, s, "warp_reregister", p.ConfirmToken)
	if got := len(e.w.warpRegistrations); got != 1 {
		t.Fatalf("replayed reregister apply called RegisterWarp %d times", got)
	}
}

func TestWarpReregisterRejectedOrExpiredPlanNeverRuns(t *testing.T) {
	for _, tc := range []struct {
		name    string
		decide  bool
		advance time.Duration
		wants   string
	}{
		{name: "rejected", decide: false, wants: "rejected by the owner"},
		{name: "expired", decide: true, advance: 11 * time.Minute, wants: "expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			_, secret := e.token(ProfileAdmin)
			s := e.session(secret)
			p := planOf(t, s, "warp_reregister", map[string]any{"node": nodeA})
			if !tc.decide || tc.advance == 0 {
				if err := e.plans.decide(p.PlanID, tc.decide); err != nil {
					t.Fatal(err)
				}
			}
			if tc.advance > 0 {
				e.advance(tc.advance)
			}
			if got := applyError(t, s, "warp_reregister", p.ConfirmToken); !strings.Contains(got, tc.wants) {
				t.Errorf("apply of %s plan: %q", tc.name, got)
			}
			if len(e.w.warpRegistrations) != 0 {
				t.Fatal("RegisterWarp ran for an unapproved or expired plan")
			}
		})
	}
}

func TestWarpToolsProfiles(t *testing.T) {
	e := newTestEnv(t)
	want := map[Profile]int{ProfileReadonly: 49, ProfileOperator: 102, ProfileAdmin: 197}
	for profile, n := range want {
		_, secret := e.token(profile)
		if got := len(toolNames(t, e.session(secret))); got != n {
			t.Errorf("%s sees %d tools, want %d", profile, got, n)
		}
	}
}

type warpReachResult struct {
	ReachState string `json:"reach_state"`
	Reach      *struct {
		CheckedUnix int64  `json:"checked_unix"`
		Colo        string `json:"colo"`
		Domains     []struct {
			Domain        string `json:"domain"`
			Verdict       string `json:"verdict"`
			HTTPStatus    uint32 `json:"http_status"`
			LatencyMS     uint32 `json:"latency_ms"`
			BlockedStreak uint32 `json:"blocked_streak"`
		} `json:"domains"`
	} `json:"reach"`
}

// The reach of the owner's sites is told with its colo and per-site verdicts, and the states that say why it is missing.
func TestWarpStatusShowsTheReachOfTheOwnersSites(t *testing.T) {
	e := newTestEnv(t)
	e.w.warp[nodeA].ReachState = "ready"
	e.w.warp[nodeA].Reach = &adminv1.WarpReachView{CheckedUnix: 1700000500, Colo: "FRA", Domains: []*adminv1.WarpReachDomain{
		{Domain: "notes.example.com", Verdict: "blocked", HttpStatus: 403, LatencyMs: 95, BlockedStreak: 2},
		{Domain: "chat.example.com", Verdict: "ok", HttpStatus: 200, LatencyMs: 310},
		{Domain: "gone.example.com", Verdict: "unreachable"}}}
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	out := mustOK(t, s, "warp_status", map[string]any{"node": "de1"})
	v := decode[warpReachResult](t, out)
	if v.ReachState != "ready" || v.Reach == nil || v.Reach.Colo != "FRA" || v.Reach.CheckedUnix != 1700000500 || len(v.Reach.Domains) != 3 ||
		v.Reach.Domains[0].Domain != "notes.example.com" || v.Reach.Domains[0].Verdict != "blocked" || v.Reach.Domains[0].HTTPStatus != 403 ||
		v.Reach.Domains[0].BlockedStreak != 2 || v.Reach.Domains[2].Verdict != "unreachable" {
		t.Fatalf("reach = %s", out)
	}

	e.w.warp[nodeA].Reach, e.w.warp[nodeA].ReachState = nil, "agent_too_old"
	out = mustOK(t, s, "warp_status", map[string]any{"node": "de1"})
	if !strings.Contains(out, `"reach_state":"agent_too_old"`) || strings.Contains(out, `"reach":`) {
		t.Fatalf("an agent without warp-reach/1: %s", out)
	}
}
