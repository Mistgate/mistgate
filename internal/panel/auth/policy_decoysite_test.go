//go:build !js

package auth

import (
	"context"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	decoysitev1 "github.com/mistgate/mistgate/gen/mistgate/decoysite/v1"
	"github.com/mistgate/mistgate/gen/mistgate/decoysite/v1/decoysitev1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	pDecoyList     = decoysitev1connect.DecoySiteServiceListDecoySitesProcedure
	pDecoyGet      = decoysitev1connect.DecoySiteServiceGetDecoySiteProcedure
	pDecoyFile     = decoysitev1connect.DecoySiteServiceGetDecoyFileProcedure
	pDecoyCreate   = decoysitev1connect.DecoySiteServiceCreateDecoySiteProcedure
	pDecoyDownload = decoysitev1connect.DecoySiteServiceDownloadDecoySiteProcedure
	pDecoyVersion  = decoysitev1connect.DecoySiteServiceAddDecoySiteVersionProcedure
	pDecoyAssign   = decoysitev1connect.DecoySiteServiceAssignDecoySiteProcedure
)

// A signed-in admin reaches the whole decoy library as the owner only.
func TestDecoySiteServiceIsOwnerOnlyForAdmins(t *testing.T) {
	var n int
	for path, l := range procedureLevels {
		if len(path) < len(decoysitev1connect.DecoySiteServiceName)+2 || path[1:len(decoysitev1connect.DecoySiteServiceName)+1] != decoysitev1connect.DecoySiteServiceName {
			continue
		}
		n++
		if l != levelOwner {
			t.Errorf("%s level = %v, want owner", path, l)
		}
		if roleAllows(store.RoleHelper, l) || roleAllows(store.RoleReadonly, l) {
			t.Errorf("%s reaches a helper or a read-only admin", path)
		}
	}
	if n != 22 {
		t.Fatalf("decoy library procedures with a level = %d, want 22", n)
	}
}

// The exact list of what tokens and the MCP reach: the reads, the draft calls (inside the MCP only) and the five changes that
// need the owner's approval. The rest of the service stays closed.
func TestDecoySiteTokenPolicyIsExactlyTheToolsNeeds(t *testing.T) {
	want := map[string]struct {
		access int
		role   string
	}{
		pDecoyList:     {TokenAccessDirect, store.RoleReadonly},
		pDecoyGet:      {TokenAccessDirect, store.RoleReadonly},
		pDecoyFile:     {TokenAccessDirect, store.RoleReadonly},
		pDecoyCreate:   {TokenAccessPlanning, store.RoleHelper},
		pDecoyDownload: {TokenAccessPlanning, store.RoleHelper},
		pDecoyVersion:  {TokenAccessDraft, store.RoleHelper},
		decoysitev1connect.DecoySiteServiceSetDecoySiteReadyProcedure: {TokenAccessApproved, store.RoleOwner},
		pDecoyAssign: {TokenAccessApproved, store.RoleOwner},
		decoysitev1connect.DecoySiteServiceUnassignDecoySiteProcedure: {TokenAccessApproved, store.RoleOwner},
		decoysitev1connect.DecoySiteServiceRevertDecoySiteProcedure:   {TokenAccessApproved, store.RoleOwner},
		decoysitev1connect.DecoySiteServiceDeleteDecoySiteProcedure:   {TokenAccessApproved, store.RoleOwner},
	}
	for path := range procedureLevels {
		if len(path) < len(decoysitev1connect.DecoySiteServiceName)+2 || path[1:len(decoysitev1connect.DecoySiteServiceName)+1] != decoysitev1connect.DecoySiteServiceName {
			continue
		}
		access, listed := TokenAccess(path)
		w, open := want[path]
		if listed != open {
			t.Errorf("%s: on the token list = %v, want %v", path, listed, open)
			continue
		}
		if !open {
			continue
		}
		if access != w.access || TokenProcedureRole(path) != w.role {
			t.Errorf("%s: access %d role %s, want %d %s", path, access, TokenProcedureRole(path), w.access, w.role)
		}
		if NeedsStepUp(path) {
			t.Errorf("%s needs a step-up", path)
		}
	}
	for tool, procs := range map[string][]string{
		"decoy_site_ready":    {decoysitev1connect.DecoySiteServiceSetDecoySiteReadyProcedure},
		"decoy_site_assign":   {decoysitev1connect.DecoySiteServiceSetDecoySiteReadyProcedure, pDecoyAssign},
		"decoy_site_unassign": {decoysitev1connect.DecoySiteServiceUnassignDecoySiteProcedure},
		"decoy_site_version":  {pDecoyVersion},
		"decoy_site_revert":   {decoysitev1connect.DecoySiteServiceRevertDecoySiteProcedure},
		"decoy_site_delete":   {decoysitev1connect.DecoySiteServiceDeleteDecoySiteProcedure},
	} {
		if got := grantProcedures[tool]; len(got) != len(procs) || got[0] != procs[0] || got[len(got)-1] != procs[len(procs)-1] {
			t.Errorf("%s grant = %v, want %v", tool, got, procs)
		}
	}
}

func decoyAssignBody(t *testing.T, dry bool) []byte {
	t.Helper()
	b, err := protojson.Marshal(&decoysitev1.AssignDecoySiteRequest{SiteId: "dsi_a", NodeIds: []string{"nod_x"}, DryRun: dry})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// What a token can do with the library over /api, inside the MCP while it plans, and inside an approved plan.
func TestDecoySiteTokenCalls(t *testing.T) {
	e := newTokenEnv(t)
	_, roSecret := e.mkToken("ro", store.ProfileReadonly, 600)
	op, opSecret := e.mkToken("op", store.ProfileOperator, 600)
	ad, adSecret := e.mkToken("ad", store.ProfileAdmin, 600)
	planning := WithPlanning(WithChannel(context.Background(), ChannelMCP))
	granted := func(planID string) context.Context {
		return e.s.WithApprovedStepUp(WithChannel(context.Background(), ChannelMCP), planID)
	}

	// reads: every profile, directly
	for _, s := range []string{roSecret, opSecret, adSecret} {
		for _, p := range []string{pDecoyList, pDecoyGet, pDecoyFile} {
			if got := e.code(p, s); got != 200 {
				t.Errorf("%s with a token: %d", p, got)
			}
		}
	}
	// draft calls: operator and above, inside the MCP while it plans; never over /api, never for a read-only token
	for _, p := range []string{pDecoyCreate, pDecoyDownload, pDecoyVersion} {
		for _, s := range []string{opSecret, adSecret} {
			if got := e.code(p, s); got != 403 {
				t.Errorf("%s over /api: %d", p, got)
			}
			if got := e.code(p, s, callOpts{ctx: WithChannel(context.Background(), ChannelMCP)}); got != 403 {
				t.Errorf("%s over MCP outside a plan: %d", p, got)
			}
			if got := e.code(p, s, callOpts{ctx: planning}); got != 200 {
				t.Errorf("%s while planning: %d", p, got)
			}
		}
		if got := e.code(p, roSecret, callOpts{ctx: planning}); got != 403 {
			t.Errorf("%s while planning with a read-only token: %d", p, got)
		}
	}
	// a draft call made while planning is not an approved plan's call: the handler sees no plan tool and keeps to drafts
	e.seen.approved = "x"
	if got := e.code(pDecoyVersion, opSecret, callOpts{ctx: planning}); got != 200 || e.seen.approved != "" {
		t.Errorf("AddDecoySiteVersion while planning: %d, approved tool %q", got, e.seen.approved)
	}
	// the new version of a site that stands somewhere is the owner's approval: the plan of decoy_site_version opens it
	if got := e.code(pDecoyVersion, opSecret, callOpts{ctx: granted(e.planFor(op, "decoy_site_version", true, "applying").ID)}); got != 200 || e.seen.approved != "decoy_site_version" {
		t.Errorf("AddDecoySiteVersion in an approved plan: %d, approved tool %q", got, e.seen.approved)
	}
	if got := e.code(pDecoyVersion, opSecret, callOpts{ctx: granted(e.planFor(op, "decoy_site_version", true, "awaiting").ID)}); got != 403 {
		t.Errorf("AddDecoySiteVersion in a plan not yet approved: %d", got)
	}
	if got := e.code(pDecoyVersion, opSecret, callOpts{ctx: granted(e.planFor(op, "decoy_site_delete", true, "applying").ID)}); got != 403 {
		t.Errorf("AddDecoySiteVersion in the approved plan of another tool: %d", got)
	}

	// the changes that put a site on a node: the owner's approval, an owner-level token and the tool's own grant
	for _, p := range []string{
		decoysitev1connect.DecoySiteServiceSetDecoySiteReadyProcedure, decoysitev1connect.DecoySiteServiceUnassignDecoySiteProcedure,
		decoysitev1connect.DecoySiteServiceRevertDecoySiteProcedure, decoysitev1connect.DecoySiteServiceDeleteDecoySiteProcedure, pDecoyAssign,
	} {
		if got := e.code(p, adSecret); got != 403 {
			t.Errorf("%s over /api: %d", p, got)
		}
		if got := e.code(p, adSecret, callOpts{ctx: planning, body: []byte(`{}`)}); got != 403 {
			t.Errorf("%s while planning: %d", p, got)
		}
	}
	if got := e.code(pDecoyAssign, adSecret, callOpts{ctx: granted(e.planFor(ad, "decoy_site_assign", true, "applying").ID)}); got != 200 {
		t.Errorf("AssignDecoySite in the approved plan of decoy_site_assign: %d", got)
	}
	if got := e.code(pDecoyAssign, adSecret, callOpts{ctx: granted(e.planFor(ad, "decoy_site_ready", true, "applying").ID)}); got != 403 {
		t.Errorf("AssignDecoySite in the approved plan of decoy_site_ready: %d", got)
	}
	if got := e.code(pDecoyAssign, opSecret, callOpts{ctx: granted(e.planFor(op, "decoy_site_assign", true, "applying").ID)}); got != 403 {
		t.Errorf("AssignDecoySite with an operator token: %d", got)
	}
	// the plan of an assignment asks what it would replace: a dry run passes while planning, a real one does not
	if got := e.code(pDecoyAssign, adSecret, callOpts{ctx: planning, body: decoyAssignBody(t, true), contentType: "application/json"}); got != 200 {
		t.Errorf("AssignDecoySite dry run while planning: %d", got)
	}
	if got := e.code(pDecoyAssign, adSecret, callOpts{ctx: planning, body: decoyAssignBody(t, false), contentType: "application/json"}); got != 403 {
		t.Errorf("AssignDecoySite for real while planning: %d", got)
	}
	bin, err := proto.Marshal(&decoysitev1.AssignDecoySiteRequest{SiteId: "dsi_a", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.code(pDecoyAssign, adSecret, callOpts{ctx: planning, body: bin, contentType: "application/proto"}); got != 200 {
		t.Errorf("AssignDecoySite dry run as proto while planning: %d", got)
	}
	// not an API call: over /api even a dry run is closed
	if got := e.code(pDecoyAssign, adSecret, callOpts{body: decoyAssignBody(t, true), contentType: "application/json"}); got != 403 {
		t.Errorf("AssignDecoySite dry run over /api: %d", got)
	}
}

// The built-in entries ("builtin", "panel_dir") are read by the three calls that read a site, under their ids, so they follow the
// rights of those calls and nothing else: the file read is the read-only tools' (decoy_site_file), the pages and the preview are the
// owner's own.
func TestDecoyBuiltinEntriesFollowTheRightsOfTheReadsOfASite(t *testing.T) {
	if access, ok := TokenAccess(pDecoyFile); !ok || access != TokenAccessDirect || TokenProcedureRole(pDecoyFile) != store.RoleReadonly {
		t.Errorf("GetDecoyFile: access %d on the token list %v, role %s", access, ok, TokenProcedureRole(pDecoyFile))
	}
	for _, p := range []string{decoysitev1connect.DecoySiteServiceListDecoyPagesProcedure, decoysitev1connect.DecoySiteServiceGetDecoyPagePreviewProcedure} {
		if _, ok := TokenAccess(p); ok {
			t.Errorf("%s is on the token list", p)
		}
		if procedureLevels[p] != levelOwner {
			t.Errorf("%s level = %v, want owner", p, procedureLevels[p])
		}
	}
}
