package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestPanelCode(t *testing.T) {
	for msg, want := range map[string]string{
		"no trusted bundle":              "no_trusted_bundle",
		"node_offline":                   "node_offline",
		"group_not_empty: users=3":       "group_not_empty",
		"Agent too old: needs 1.2":       "agent_too_old",
		"":                               "",
		strings.Repeat("very long ", 10): "",
	} {
		if got, _ := panelCode(msg); got != want {
			t.Errorf("panelCode(%q) = %q, want %q", msg, got, want)
		}
	}
	if _, p := panelCode("group_not_empty: users=3"); p["users"] != "3" || p["detail"] != "users=3" {
		t.Errorf("params: %v", p)
	}
	if _, p := panelCode("no trusted bundle"); p != nil {
		t.Errorf("params of a bare code: %v", p)
	}
}

func factOf(t *testing.T, p PlanOut, key string) Fact {
	t.Helper()
	for _, f := range p.Facts {
		if f.Key == key {
			return f
		}
	}
	t.Fatalf("no fact %q in %+v", key, p.Facts)
	return Fact{}
}

func storedPlan(t *testing.T, e *testEnv, id string) Plan {
	t.Helper()
	p, err := e.plans.GetMCPPlan(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The owner's UI words a plan by codes, not by the agent's English: every fact the panel words itself carries one, the
// names stay apart as untrusted, and what came of the change is a code with its values.
func TestFactsAndOutcomesCarryCodes(t *testing.T) {
	e := newTestEnv(t)
	_, op := e.token(ProfileOperator)
	s := e.session(op)

	// user_create: quota, term, apps, nodes
	p := planOf(t, s, "user_create", map[string]any{"name": "zoe", "group_id": "grp_1", "quota_bytes": 100 << 30, "quota_reset": "month"})
	if f := factOf(t, p, "quota"); f.Code != "quota" || f.Params["bytes"] != "107374182400" || f.Params["reset"] != "month" {
		t.Errorf("quota: %+v", f)
	}
	if f := factOf(t, p, "term"); f.Code != "never" {
		t.Errorf("term: %+v", f)
	}
	if f := factOf(t, p, "apps"); f.Code != "default" {
		t.Errorf("apps: %+v", f)
	}
	if f := factOf(t, p, "nodes"); f.Code != "all" {
		t.Errorf("nodes: %+v", f)
	}
	applyOf(t, s, "user_create", p.ConfirmToken)
	if pl := storedPlan(t, e, p.PlanID); pl.OutcomeCode != "user_created" {
		t.Errorf("outcome: %q", pl.OutcomeCode)
	}

	// user_update: each field is a change with its raw values
	p = planOf(t, s, "user_update", map[string]any{"user_id": "usr_alice", "quota_bytes": 20 << 30, "expires_unix": 1800000000, "apps": map[string]any{"happ": true, "amnezia": false}})
	if f := factOf(t, p, "quota"); f.Code != "change" || f.Params["from"] != "10737418240" || f.Params["to"] != "21474836480" {
		t.Errorf("quota change: %+v", f)
	}
	if f := factOf(t, p, "expires"); f.Code != "change" || f.Params["from"] != "0" || f.Params["to"] != "1800000000" {
		t.Errorf("expires change: %+v", f)
	}
	if f := factOf(t, p, "apps"); f.Code != "change" || f.Params["from"] != "both" || f.Params["to"] != "happ" {
		t.Errorf("apps change: %+v", f)
	}

	// user_reset_traffic: the names alone (untrusted), the bytes and the effect as codes
	p = planOf(t, s, "user_reset_traffic", map[string]any{"user_ids": []string{"usr_n0", "usr_n1"}})
	if f := factOf(t, p, "users"); !f.Untrusted || f.Value != "n0, n1" || f.Code != "" {
		t.Errorf("users: %+v", f)
	}
	if f := factOf(t, p, "used"); f.Code != "bytes" || f.Params["bytes"] != "6442450944" {
		t.Errorf("used: %+v", f)
	}
	if f := factOf(t, p, "effect"); f.Code != "traffic_reset" {
		t.Errorf("effect: %+v", f)
	}
	applyOf(t, s, "user_reset_traffic", p.ConfirmToken)
	if pl := storedPlan(t, e, p.PlanID); pl.OutcomeCode != "traffic_reset" || pl.OutcomeParams != `{"n":"2"}` {
		t.Errorf("outcome: %q %q", pl.OutcomeCode, pl.OutcomeParams)
	}

	// user_disable
	p = planOf(t, s, "user_disable", map[string]any{"user_ids": []string{"usr_n0", "usr_n1", "usr_n2"}})
	if f := factOf(t, p, "effect"); f.Code != "disable" {
		t.Errorf("effect: %+v", f)
	}
	applyOf(t, s, "user_disable", p.ConfirmToken)
	if pl := storedPlan(t, e, p.PlanID); pl.OutcomeCode != "users_disabled" || pl.OutcomeParams != `{"n":"3"}` {
		t.Errorf("outcome: %q %q", pl.OutcomeCode, pl.OutcomeParams)
	}

	// alert_mute: the alert by kind and severity, the duration in seconds
	p = planOf(t, s, "alert_mute", map[string]any{"alert_id": "alr_1", "duration_s": 3600})
	if f := factOf(t, p, "alert"); f.Code != "alert" || f.Params["kind"] != "node_down" || f.Params["severity"] != "critical" {
		t.Errorf("alert: %+v", f)
	}
	if f := factOf(t, p, "duration"); f.Code != "seconds" || f.Params["n"] != "3600" {
		t.Errorf("duration: %+v", f)
	}

	// node_fix: the fix id is the code; a restart names its profile, as an untrusted param
	_, adm := e.token(ProfileAdmin)
	sa := e.session(adm)
	p = planOf(t, sa, "node_fix", map[string]any{"node": "de1", "fix_id": "restart_inbound", "params": map[string]any{"inbound_id": "inb_1"}})
	f := factOf(t, p, "fix")
	if f.Code != "restart_inbound" || f.Params["profile"] != "Main" || f.Params["port"] != "443" || !slices.Equal(f.UntrustedParams, []string{"profile"}) || f.Untrusted {
		t.Errorf("fix: %+v", f)
	}

	// a failure the panel refuses with a code keeps that code
	e.w.startErr = connect.NewError(connect.CodeFailedPrecondition, errors.New("no trusted bundle"))
	p = planOf(t, sa, "rollout_start", map[string]any{})
	if f := factOf(t, p, "effect"); f.Code != "canary" {
		t.Errorf("effect: %+v", f)
	}
	if f := factOf(t, p, "batch_size"); f.Code != "default" {
		t.Errorf("batch: %+v", f)
	}
	e.plans.decide(p.PlanID, true)
	if got := applyError(t, sa, "rollout_start", p.ConfirmToken); !strings.Contains(got, "no trusted bundle") {
		t.Errorf("the agent's error: %q", got)
	}
	if pl := storedPlan(t, e, p.PlanID); pl.Status != StatusFailed || pl.OutcomeCode != "no_trusted_bundle" || !strings.Contains(pl.Error, "no trusted bundle") {
		t.Errorf("failed plan: %+v", pl)
	}

	// the agent sees the codes too (harmless), and nothing in them is a secret
	b, _ := json.Marshal(p)
	noCanary(t, "plan with codes", string(b))
}
