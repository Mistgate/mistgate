//go:build !js

package mcp

import (
	"context"
	"strings"
	"testing"
)

// The smaller changes: ready, unassign, revert and delete, each a plan the owner approves.
func TestDecoySmallChangesNeedTheOwner(t *testing.T) {
	e := newTestEnv(t)
	lib := e.w.lib
	_, ad := e.token(ProfileAdmin)
	s := e.session(ad)
	approve := func(tool string, args map[string]any) ApplyOut {
		t.Helper()
		p := planOf(t, s, tool, args)
		if !p.NeedsApproval {
			t.Fatalf("%s needs no approval: %+v", tool, p)
		}
		if out := applyError(t, s, tool, p.ConfirmToken); !strings.Contains(out, "waiting for the owner") {
			t.Fatalf("%s applied before the approval: %s", tool, out)
		}
		if err := e.plans.decide(p.PlanID, true); err != nil {
			t.Fatal(err)
		}
		return applyOf(t, s, tool, p.ConfirmToken)
	}

	// ready: only a draft
	draft := lib.put("Tea", "n", "draft", bakery(t), "no_404")
	ready := lib.put("Bakery", "n", "ready", bakery(t))
	if out := mustFail(t, s, "decoy_site_ready_plan", map[string]any{"site_id": ready.id}); !strings.Contains(out, "already ready") {
		t.Errorf("ready of a ready site: %s", out)
	}
	if got := approve("decoy_site_ready", map[string]any{"site_id": draft.id}); got.Status != "applied" || draft.state != "ready" {
		t.Fatalf("ready = %+v, state %s", got, draft.state)
	}

	// unassign: only nodes that stand on the site, back to the built-in page
	ready.nodes, ready.panel = []string{nodeA}, true
	if out := mustFail(t, s, "decoy_site_unassign_plan", map[string]any{"site_id": ready.id, "nodes": []any{"nl1"}}); !strings.Contains(out, "does not serve this site") {
		t.Errorf("unassign a node that is not on it: %s", out)
	}
	if out := mustFail(t, s, "decoy_site_unassign_plan", map[string]any{"site_id": ready.id, "nodes": []any{"de1"}, "fallback": "proxy"}); !strings.Contains(out, "fallback must be") {
		t.Errorf("bad fallback: %s", out)
	}
	if got := approve("decoy_site_unassign", map[string]any{"site_id": ready.id, "nodes": []any{"de1"}, "panel": true}); got.Status != "applied" || len(ready.nodes) != 0 || ready.panel {
		t.Fatalf("unassign = %+v, nodes %v panel %v", got, ready.nodes, ready.panel)
	}

	// revert: an earlier version, with the list of files that change
	ready.versions = append(ready.versions, withFile(t, bakery(t), "late.html", "x"))
	ready.nodes = []string{nodeB}
	if out := mustFail(t, s, "decoy_site_revert_plan", map[string]any{"site_id": ready.id, "version": 2}); !strings.Contains(out, "current one") {
		t.Errorf("revert to the current version: %s", out)
	}
	if out := mustFail(t, s, "decoy_site_revert_plan", map[string]any{"site_id": ready.id, "version": 9}); !strings.Contains(out, "decoy_version_not_found") {
		t.Errorf("revert to a version that is not there: %s", out)
	}
	p := planOf(t, s, "decoy_site_revert", map[string]any{"site_id": ready.id, "version": 1})
	var list string
	for _, f := range p.Facts {
		if f.Key == "file_changes" {
			list += f.Value
		}
	}
	if !strings.Contains(list, "deleted late.html: 1 bytes") || !strings.Contains(p.Summary, "1 node(s)") {
		t.Errorf("revert plan: %s / %s", p.Summary, list)
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	if out := applyOf(t, s, "decoy_site_revert", p.ConfirmToken); out.Status != "applied" || len(ready.versions) != 3 {
		t.Fatalf("revert = %+v, versions %d", out, len(ready.versions))
	}

	// delete: not while it stands anywhere
	if out := mustFail(t, s, "decoy_site_delete_plan", map[string]any{"site_id": ready.id}); !strings.Contains(out, "decoy_site_in_use") {
		t.Errorf("delete of a site in use: %s", out)
	}
	ready.nodes = nil
	if got := approve("decoy_site_delete", map[string]any{"site_id": ready.id}); got.Status != "applied" || lib.sites[ready.id] != nil {
		t.Fatalf("delete = %+v", got)
	}
	// every approved call carried its plan's grant
	for _, c := range e.w.calls("/mistgate.decoysite.v1.DecoySiteService/DeleteDecoySite") {
		if c.Approved == "" {
			t.Errorf("DeleteDecoySite without a grant: %+v", c)
		}
	}
}

// In the Cloudflare edition the library does not exist: every tool says so, and nothing is stored.
func TestDecoyToolsAnswerUnavailableInTheEdge(t *testing.T) {
	e := newTestEnv(t)
	e.w.lib.edge = true
	_, ad := e.token(ProfileAdmin)
	s := e.session(ad)
	index := fileArgs("index.html", "<p>x</p>")
	for tool, args := range map[string]map[string]any{
		"decoy_sites_list":         {},
		"decoy_site_get":           {"site_id": "dsi_a"},
		"decoy_site_file":          {"site_id": "dsi_a", "path": "index.html"},
		"decoy_site_draft_save":    {"name": "N", "note": "n", "files": []any{index}},
		"decoy_site_ready_plan":    {"site_id": "dsi_a"},
		"decoy_site_assign_plan":   {"site_id": "dsi_a", "nodes": []any{"de1"}},
		"decoy_site_unassign_plan": {"site_id": "dsi_a", "nodes": []any{"de1"}},
		"decoy_site_version_plan":  {"site_id": "dsi_a", "files": []any{index}},
		"decoy_site_revert_plan":   {"site_id": "dsi_a", "version": 1},
		"decoy_site_delete_plan":   {"site_id": "dsi_a"},
	} {
		if out := mustFail(t, s, tool, args); !strings.Contains(out, "unavailable_in_edition") {
			t.Errorf("%s: %s", tool, out)
		}
	}
	if len(e.plans.byID) != 0 {
		t.Error("a plan was stored")
	}
}

// What the assistant saves is marked as the assistant's, and it needs no plan either.
func TestDecoyDraftFromTheAssistantIsMarkedAsAssistant(t *testing.T) {
	e := newTestEnv(t)
	runtime, err := NewAssistant(Config{Plans: e.plans, Auth: e.auth, API: e.w.api(e.auth), Now: e.now})
	if err != nil {
		t.Fatal(err)
	}
	session, err := runtime.OpenAssistant(context.Background(), AssistantIdentity{TokenID: "tok_assistant", Profile: string(ProfileOperator), AdminID: "adm_1", AdminName: "Admin"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	res, err := session.CallTool(context.Background(), "decoy_site_draft_save", []byte(`{"name":"Cafe","note":"a cafe","files":[{"path":"index.html","content":"<p>y</p>"}]}`))
	if err != nil || res.IsError {
		t.Fatalf("assistant draft: %+v %v", res, err)
	}
	if n := len(e.w.lib.creates); n != 1 || e.w.lib.creates[0].Source != "assistant" || e.w.lib.creates[0].State != "draft" {
		t.Fatalf("creates = %+v", e.w.lib.creates)
	}
	if len(e.plans.byID) != 0 {
		t.Error("the assistant's draft made a plan")
	}
}
