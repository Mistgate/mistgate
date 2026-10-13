package mcp

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	adminv1connect "github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/decoy"
)

func TestNodeDecoyPlanApplyRequiresOwnerApprovalAndUploadsBundle(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	bundle, err := decoy.FromFiles([]decoy.File{{Path: "index.html", Content: []byte("one node's site")}})
	if err != nil {
		t.Fatal(err)
	}
	p := planOf(t, s, "node_decoy", map[string]any{
		"node": nodeA, "mode": decoy.ModeOwn, "bundle_base64": base64.StdEncoding.EncodeToString(bundle.Archive()),
	})
	if !p.NeedsApproval || len(p.Danger) == 0 || len(e.w.nodeDecoyReqs) != 0 {
		t.Fatalf("plan did not wait for owner approval or applied early: %+v requests=%d", p, len(e.w.nodeDecoyReqs))
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	out := applyOf(t, s, "node_decoy", p.ConfirmToken)
	if out.Status != "applied" || !strings.Contains(out.Result, "decoy") {
		t.Fatalf("apply = %+v", out)
	}
	if len(e.w.nodeDecoyReqs) != 1 {
		t.Fatalf("SetNodeDecoyMode called %d times", len(e.w.nodeDecoyReqs))
	}
	request := e.w.nodeDecoyReqs[0]
	if request.GetNodeId() != nodeA || request.GetMode() != decoy.ModeOwn {
		t.Fatalf("request = %+v", request)
	}
	stored, err := decoy.ParseArchive(request.GetBundleArchive())
	if err != nil || stored.Hash() != bundle.Hash() {
		t.Fatalf("uploaded bundle hash = %v, err=%v; want %s", stored, err, bundle.Hash())
	}
	const procedure = adminv1connect.NodeServiceSetNodeDecoyModeProcedure
	calls := e.w.calls(procedure)
	if len(calls) != 1 || calls[0].Approved != p.PlanID || calls[0].Token == "" {
		t.Fatalf("approved API calls = %+v", calls)
	}
	if len(e.audits("mcp_plan")) != 1 || len(e.audits("mcp_apply")) != 1 {
		t.Fatal("MCP plan/apply audit rows were not recorded")
	}
}

func TestNodeDecoyToolDescriptionAvoidsRepeatingOtherSites(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	found := false
	for tool, err := range s.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name == "node_decoy_plan" {
			found = true
			if !strings.Contains(tool.Description, "never repeat the subject or look") {
				t.Fatalf("node_decoy_plan description omits the site uniqueness rule: %q", tool.Description)
			}
		}
	}
	if !found {
		t.Fatal("node_decoy_plan is missing")
	}
}

// A refused bundle comes back as a coded reason an agent can act on, and nothing is planned or sent.
func TestNodeDecoyPlanRefusesBadBundlesWithAReason(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	noIndex := zipOf(t, map[string]string{"about.html": "x"})
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"not base64":         {map[string]any{"node": nodeA, "mode": "own", "bundle_base64": "!!!"}, "bundle_base64"},
		"missing bundle":     {map[string]any{"node": nodeA, "mode": "own"}, "required"},
		"no index.html":      {map[string]any{"node": nodeA, "mode": "own", "bundle_base64": base64.StdEncoding.EncodeToString(noIndex)}, "decoy_bundle_no_index"},
		"bundle with panel":  {map[string]any{"node": nodeA, "mode": "panel", "bundle_base64": base64.StdEncoding.EncodeToString(noIndex)}, "only allowed with own"},
		"unknown mode":       {map[string]any{"node": nodeA, "mode": "proxy"}, "mode must be"},
		"archive over 2 MiB": {map[string]any{"node": nodeA, "mode": "own", "bundle_base64": base64.StdEncoding.EncodeToString(make([]byte, decoy.MaxBundleBytes+1))}, "decoy_bundle_too_large"},
	} {
		out, isErr := callTool(t, s, "node_decoy_plan", tc.args)
		if !isErr || !strings.Contains(out, tc.want) {
			t.Errorf("%s: isError=%v out=%q, want it to contain %q", name, isErr, out, tc.want)
		}
	}
	if len(e.w.nodeDecoyReqs) != 0 {
		t.Fatal("a refused plan reached the admin API")
	}
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
