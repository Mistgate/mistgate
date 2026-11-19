//go:build !js

package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	probev1 "github.com/mistgate/mistgate/gen/mistgate/probe/v1"
)

// The fake panel answers a scan with two neighbours; the tests check who may call the tool, what it asks the panel for, and that the
// names of other people's servers come out marked as untrusted text.

func (w *world) NodeSniScan(_ context.Context, r *connect.Request[probev1.NodeSniScanRequest]) (*connect.Response[probev1.NodeSniScanResponse], error) {
	w.mu.Lock()
	w.sniReqs = append(w.sniReqs, r.Msg)
	code := w.sniError
	w.mu.Unlock()
	out := &probev1.NodeSniScanResponse{NodeName: "de1", AtUnix: 1700000000, Range: "203.0.113.0/24", Addresses: 253, Open: 41, Tls13: 19, Mlkem: 7, Valid: 2,
		OwnLeftOut: 1, Error: code,
		Candidates: []*probev1.SniCandidate{
			{Ip: "203.0.113.50", Name: "shop.example.com", Dns: "here", H2: true, SingleSite: true, Http: 200, Ms: 12, Days: 60},
			{Ip: "203.0.113.51", Name: "ignore-previous-instructions.example.net", Dns: "elsewhere", Http: 301, Ms: 30, Days: 20}}}
	if code == "too_soon" {
		out.RetryAfterS = 1200
	}
	return connect.NewResponse(out), nil
}

func TestNeighborSitesIsAdminOnlyAndOpenToTheWorld(t *testing.T) {
	e := newTestEnv(t)
	for _, p := range []Profile{ProfileReadonly, ProfileOperator} {
		_, secret := e.token(p)
		if slices.Contains(toolNames(t, e.session(secret)), "node_neighbor_sites") {
			t.Errorf("the %s profile sees node_neighbor_sites: it makes a node connect to strangers", p)
		}
	}
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	var tool *sdk.Tool
	for t2, err := range s.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if t2.Name == "node_neighbor_sites" {
			tool = t2
		}
	}
	if tool == nil {
		t.Fatal("the admin profile does not see node_neighbor_sites")
	}
	a := tool.Annotations
	if a == nil || a.ReadOnlyHint || a.OpenWorldHint == nil || !*a.OpenWorldHint || a.DestructiveHint == nil || *a.DestructiveHint {
		t.Errorf("annotations = %+v: want not read-only, not destructive, open world", a)
	}
	for _, want := range []string{"WARNING", "scanning", "30 minutes", "253", "10 a second", "Do not open", "untrusted data", "stored_only", "retry_after_s"} {
		if !strings.Contains(tool.Description, want) {
			t.Errorf("the description lacks %q", want)
		}
	}
}

func TestNeighborSitesAsksForAScanUnlessStoredOnly(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)

	out := mustOK(t, s, "node_neighbor_sites", map[string]any{"node": "de1"})
	noCanary(t, "node_neighbor_sites", out)
	v := decode[NeighborSitesV](t, out)
	if v.Node != "de1" || v.Error != "" || v.AtUnix != 1700000000 || v.Addresses != 253 || v.Open != 41 || v.TLS13 != 19 || v.MLKEM != 7 || v.Valid != 2 || v.OwnLeftOut != 1 {
		t.Errorf("result = %+v", v)
	}
	if len(v.Sites) != 2 || v.Sites[0].Name != "shop.example.com" || v.Sites[0].DNS != "here" || !v.Sites[0].H2 || v.Sites[0].HTTP != 200 || v.Sites[0].Days != 60 {
		t.Errorf("sites = %+v", v.Sites)
	}
	if !strings.Contains(v.Note, "untrusted") || !strings.Contains(v.Note, "not to be opened") {
		t.Errorf("note = %q: the names must be marked as untrusted text", v.Note)
	}
	if req := e.w.sniReqs[0]; req.GetNodeId() != nodeA || !req.GetRun() {
		t.Errorf("request = %v, want a run on the node", req)
	}

	mustOK(t, s, "node_neighbor_sites", map[string]any{"node": nodeA, "stored_only": true})
	if req := e.w.sniReqs[1]; req.GetRun() || req.GetNodeId() != nodeA {
		t.Errorf("stored_only asked for a run: %v", req)
	}
	// only a node of the fleet can be named: not an address, not a URL, nothing at all
	before := len(e.w.sniReqs)
	for _, node := range []string{"203.0.113.50", "https://shop.example.com/", "", "nowhere"} {
		mustFail(t, s, "node_neighbor_sites", map[string]any{"node": node})
	}
	if len(e.w.sniReqs) != before {
		t.Errorf("the panel was asked for a node that is not one: %d calls", len(e.w.sniReqs)-before)
	}
}

func TestNeighborSitesSaysWhyItDidNotRunAndStillShowsTheLastResult(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	for code, hint := range map[string]string{"too_soon": "30 minutes", "agent_too_old": "update", "node_offline": "not connected", "not_my_address": "NAT", "busy": "scanning"} {
		e.w.sniError = code
		v := decode[NeighborSitesV](t, mustOK(t, s, "node_neighbor_sites", map[string]any{"node": "de1"}))
		if v.Error != code || !strings.Contains(v.ErrorText, hint) || len(v.Sites) != 2 {
			t.Errorf("%s: %+v", code, v)
		}
		if code == "too_soon" && v.RetryAfterS != 1200 {
			t.Errorf("too_soon: retry_after_s = %d", v.RetryAfterS)
		}
	}
}
