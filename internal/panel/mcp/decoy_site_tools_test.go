//go:build !js

package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	decoysitev1 "github.com/mistgate/mistgate/gen/mistgate/decoysite/v1"
	"github.com/mistgate/mistgate/gen/mistgate/decoysite/v1/decoysitev1connect"
	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/panel/edition"
)

// A small library behind the fake admin API: the semantics of DecoySiteService that the tools rely on (a draft is the only site a
// call without an approved plan may save to, an assignment's dry run says what it would replace, a site in use is not deleted),
// and a log of what reached it. The real service runs under the real auth in cmd/mistgate/mcp_wiring_test.go.

type fakeSite struct {
	id, name, note, source, state string
	by                            string
	versions                      []*decoy.Bundle
	warnings                      []string
	nodes                         []string
	panel                         bool
}

type fakeLibrary struct {
	decoysitev1connect.UnimplementedDecoySiteServiceHandler
	mu       sync.Mutex
	sites    map[string]*fakeSite
	next     int
	edge     bool     // every call answers unavailable_in_edition
	sharedAt []string // node ids: an assignment onto them (with others) warns shared_domains
	calls    []string // the procedures that reached the library, in order, by method name
	assigned []*decoysitev1.AssignDecoySiteRequest
	creates  []*decoysitev1.CreateDecoySiteRequest
	adds     []*decoysitev1.AddDecoySiteVersionRequest
	added    []string // tokens that saved: the principal of each create / add
}

func newFakeLibrary() *fakeLibrary { return &fakeLibrary{sites: map[string]*fakeSite{}} }

var nodeNamesOfFixture = map[string]string{nodeA: "de1", nodeB: "nl1"}

func (l *fakeLibrary) siteOf(node string) string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.sites {
		if slices.Contains(s.nodes, node) {
			return s.id
		}
	}
	return ""
}

func (l *fakeLibrary) gate(method string) error {
	l.calls = append(l.calls, method)
	if l.edge {
		return edition.Refuse(edition.DecoyOwn)
	}
	return nil
}

func (l *fakeLibrary) put(name, note, state string, b *decoy.Bundle, warnings ...string) *fakeSite {
	l.next++
	s := &fakeSite{id: fmt.Sprintf("dsi_site%d", l.next), name: name, note: note, source: "upload", state: state, by: "the owner", versions: []*decoy.Bundle{b}, warnings: warnings}
	l.sites[s.id] = s
	return s
}

func (s *fakeSite) current() *decoy.Bundle { return s.versions[len(s.versions)-1] }

func (l *fakeLibrary) msg(s *fakeSite) *decoysitev1.DecoySite {
	checks := []*decoysitev1.DecoyCheck{{Code: "index_html", Level: "required", Ok: true}}
	for _, w := range s.warnings {
		checks = append(checks, &decoysitev1.DecoyCheck{Code: w, Level: "warning", Paths: []string{"index.html"}})
	}
	return &decoysitev1.DecoySite{
		Id: s.id, Name: s.name, Note: s.note, Source: s.source, SourceLabel: s.by, State: s.state, BundleSha256: s.current().Hash(),
		Version: uint32(len(s.versions)), SizeBytes: uint64(s.current().Size()), FileCount: uint32(len(s.current().Files())), Title: s.name + " home",
		Checks: checks, NodeCount: uint32(len(s.nodes)), OnPanel: s.panel, UpdatedUnix: 1700000000, CreatedBy: s.by, UpdatedBy: s.by,
	}
}

func notFound() error {
	return connect.NewError(connect.CodeNotFound, errors.New("decoy_site_not_found"))
}

func refusal(code string) error {
	return connect.NewError(connect.CodeFailedPrecondition, errors.New(code))
}

// approved is true for a call made inside an owner-approved plan.
func approved(ctx context.Context) bool { a, _ := ctx.Value(keyApproved).(string); return a != "" }

func who(ctx context.Context) string {
	p, _ := ctx.Value(keyPrincipal).(Principal)
	return p.TokenID
}

func (l *fakeLibrary) ListDecoySites(_ context.Context, _ *connect.Request[decoysitev1.ListDecoySitesRequest]) (*connect.Response[decoysitev1.ListDecoySitesResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.gate("ListDecoySites"); err != nil {
		return nil, err
	}
	out := &decoysitev1.ListDecoySitesResponse{PanelSource: "builtin", SiteLimit: 100, VersionLimit: 5,
		Builtin: []*decoysitev1.DecoyBuiltinItem{{Id: "builtin", Available: true}, {Id: "panel_dir", ReasonCode: "panel_unavailable"}}}
	for _, id := range l.ids() {
		out.Sites = append(out.Sites, l.msg(l.sites[id]))
	}
	return connect.NewResponse(out), nil
}

func (l *fakeLibrary) ids() []string {
	ids := make([]string, 0, len(l.sites))
	for id := range l.sites {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (l *fakeLibrary) GetDecoySite(_ context.Context, r *connect.Request[decoysitev1.GetDecoySiteRequest]) (*connect.Response[decoysitev1.GetDecoySiteResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.gate("GetDecoySite"); err != nil {
		return nil, err
	}
	s := l.sites[r.Msg.SiteId]
	if s == nil {
		return nil, notFound()
	}
	out := &decoysitev1.GetDecoySiteResponse{Site: l.msg(s)}
	for i, v := range s.versions {
		out.Versions = append(out.Versions, &decoysitev1.DecoyVersion{Seq: uint32(i + 1), BundleSha256: v.Hash(), CreatedBy: s.by, FileCount: uint32(len(v.Files())), SizeBytes: uint64(v.Size()), Current: i == len(s.versions)-1})
	}
	for _, f := range s.current().Infos() {
		out.Files = append(out.Files, &decoysitev1.DecoyFileInfo{Path: f.Path, Size: uint64(f.Size)})
	}
	for _, n := range s.nodes {
		out.Nodes = append(out.Nodes, &decoysitev1.DecoySiteNode{NodeId: n, NodeName: nodeNamesOfFixture[n], Supported: true})
	}
	return connect.NewResponse(out), nil
}

func (l *fakeLibrary) GetDecoyFile(_ context.Context, r *connect.Request[decoysitev1.GetDecoyFileRequest]) (*connect.Response[decoysitev1.GetDecoyFileResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.gate("GetDecoyFile"); err != nil {
		return nil, err
	}
	s := l.sites[r.Msg.SiteId]
	if s == nil {
		return nil, notFound()
	}
	b := s.current()
	if r.Msg.Seq > 0 && int(r.Msg.Seq) <= len(s.versions) {
		b = s.versions[r.Msg.Seq-1]
	}
	c, ok := b.File(r.Msg.Path)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("decoy_file_not_found"))
	}
	return connect.NewResponse(&decoysitev1.GetDecoyFileResponse{Text: string(c), Size: uint64(len(c))}), nil
}

func (l *fakeLibrary) DownloadDecoySite(_ context.Context, r *connect.Request[decoysitev1.DownloadDecoySiteRequest]) (*connect.Response[decoysitev1.DownloadDecoySiteResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.gate("DownloadDecoySite"); err != nil {
		return nil, err
	}
	s := l.sites[r.Msg.SiteId]
	if s == nil {
		return nil, notFound()
	}
	b := s.current()
	if r.Msg.Seq > 0 {
		if int(r.Msg.Seq) > len(s.versions) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("decoy_version_not_found"))
		}
		b = s.versions[r.Msg.Seq-1]
	}
	return connect.NewResponse(&decoysitev1.DownloadDecoySiteResponse{Filename: "x.zip", Archive: b.Archive()}), nil
}

func (l *fakeLibrary) CreateDecoySite(ctx context.Context, r *connect.Request[decoysitev1.CreateDecoySiteRequest]) (*connect.Response[decoysitev1.CreateDecoySiteResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.gate("CreateDecoySite"); err != nil {
		return nil, err
	}
	if r.Msg.State != "draft" && !approved(ctx) {
		return nil, refusal("decoy_site_not_draft")
	}
	b, err := decoy.ParseArchive(r.Msg.BundleArchive)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(decoy.ClientMessage(err)))
	}
	for _, s := range l.sites {
		if s.name == r.Msg.Name {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("decoy_name_taken"))
		}
	}
	l.creates = append(l.creates, r.Msg)
	l.added = append(l.added, who(ctx))
	s := l.put(r.Msg.Name, r.Msg.Note, r.Msg.State, b)
	s.source, s.by = r.Msg.Source, who(ctx)
	return connect.NewResponse(&decoysitev1.CreateDecoySiteResponse{Site: l.msg(s)}), nil
}

func (l *fakeLibrary) AddDecoySiteVersion(ctx context.Context, r *connect.Request[decoysitev1.AddDecoySiteVersionRequest]) (*connect.Response[decoysitev1.AddDecoySiteVersionResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.gate("AddDecoySiteVersion"); err != nil {
		return nil, err
	}
	s := l.sites[r.Msg.SiteId]
	if s == nil {
		return nil, notFound()
	}
	if s.state != "draft" && !approved(ctx) {
		return nil, refusal("decoy_site_not_draft")
	}
	b, err := decoy.ParseArchive(r.Msg.BundleArchive)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(decoy.ClientMessage(err)))
	}
	l.adds = append(l.adds, r.Msg)
	l.added = append(l.added, who(ctx))
	s.versions = append(s.versions, b)
	return connect.NewResponse(&decoysitev1.AddDecoySiteVersionResponse{Site: l.msg(s), UpdatedNodeIds: slices.Clone(s.nodes)}), nil
}

func (l *fakeLibrary) SetDecoySiteReady(_ context.Context, r *connect.Request[decoysitev1.SetDecoySiteReadyRequest]) (*connect.Response[decoysitev1.SetDecoySiteReadyResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.gate("SetDecoySiteReady"); err != nil {
		return nil, err
	}
	s := l.sites[r.Msg.SiteId]
	if s == nil {
		return nil, notFound()
	}
	s.state = "ready"
	return connect.NewResponse(&decoysitev1.SetDecoySiteReadyResponse{Site: l.msg(s)}), nil
}

func (l *fakeLibrary) AssignDecoySite(_ context.Context, r *connect.Request[decoysitev1.AssignDecoySiteRequest]) (*connect.Response[decoysitev1.AssignDecoySiteResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.gate("AssignDecoySite"); err != nil {
		return nil, err
	}
	s := l.sites[r.Msg.SiteId]
	if s == nil {
		return nil, notFound()
	}
	if s.state != "ready" && !r.Msg.DryRun {
		return nil, refusal("decoy_site_draft")
	}
	out := &decoysitev1.AssignDecoySiteResponse{}
	for _, n := range r.Msg.NodeIds {
		rp := &decoysitev1.DecoyReplaced{Target: n, Previous: "builtin"}
		for _, o := range l.sites {
			if slices.Contains(o.nodes, n) {
				rp.Previous, rp.PreviousSiteId, rp.PreviousSiteName = "own", o.id, o.name
			}
		}
		out.Replaced = append(out.Replaced, rp)
	}
	if r.Msg.Panel {
		out.Replaced = append(out.Replaced, &decoysitev1.DecoyReplaced{Target: "panel", Previous: "builtin"})
	}
	for _, w := range s.warnings {
		out.Warnings = append(out.Warnings, &decoysitev1.DecoyCheck{Code: w, Level: "warning", Paths: []string{"index.html"}})
	}
	if len(l.sharedAt) > 0 && len(r.Msg.NodeIds) > 0 {
		out.Warnings = append(out.Warnings, &decoysitev1.DecoyCheck{Code: "shared_domains", Level: "warning", Paths: []string{"de1", "nl1"}})
	}
	if r.Msg.DryRun {
		return connect.NewResponse(out), nil
	}
	for _, o := range l.sites {
		o.nodes = slices.DeleteFunc(o.nodes, func(n string) bool { return slices.Contains(r.Msg.NodeIds, n) })
	}
	s.nodes = append(s.nodes, r.Msg.NodeIds...)
	s.panel = s.panel || r.Msg.Panel
	l.assigned = append(l.assigned, r.Msg)
	out.Applied = true
	return connect.NewResponse(out), nil
}

func (l *fakeLibrary) UnassignDecoySite(_ context.Context, r *connect.Request[decoysitev1.UnassignDecoySiteRequest]) (*connect.Response[decoysitev1.UnassignDecoySiteResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.gate("UnassignDecoySite"); err != nil {
		return nil, err
	}
	s := l.sites[r.Msg.SiteId]
	if s == nil {
		return nil, notFound()
	}
	s.nodes = slices.DeleteFunc(s.nodes, func(n string) bool { return slices.Contains(r.Msg.NodeIds, n) })
	if r.Msg.Panel {
		s.panel = false
	}
	return connect.NewResponse(&decoysitev1.UnassignDecoySiteResponse{}), nil
}

func (l *fakeLibrary) RevertDecoySite(_ context.Context, r *connect.Request[decoysitev1.RevertDecoySiteRequest]) (*connect.Response[decoysitev1.RevertDecoySiteResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.gate("RevertDecoySite"); err != nil {
		return nil, err
	}
	s := l.sites[r.Msg.SiteId]
	if s == nil {
		return nil, notFound()
	}
	if int(r.Msg.Seq) < 1 || int(r.Msg.Seq) > len(s.versions) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("decoy_version_not_found"))
	}
	s.versions = append(s.versions, s.versions[r.Msg.Seq-1])
	return connect.NewResponse(&decoysitev1.RevertDecoySiteResponse{Site: l.msg(s), UpdatedNodeIds: slices.Clone(s.nodes)}), nil
}

func (l *fakeLibrary) DeleteDecoySite(_ context.Context, r *connect.Request[decoysitev1.DeleteDecoySiteRequest]) (*connect.Response[decoysitev1.DeleteDecoySiteResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.gate("DeleteDecoySite"); err != nil {
		return nil, err
	}
	s := l.sites[r.Msg.SiteId]
	if s == nil {
		return nil, notFound()
	}
	if len(s.nodes) > 0 || s.panel {
		return nil, refusal("decoy_site_in_use")
	}
	delete(l.sites, s.id)
	return connect.NewResponse(&decoysitev1.DeleteDecoySiteResponse{}), nil
}

// --- the tests ---

const (
	markerHome  = "Marker-HOME-5513"
	markerAbout = "Marker-ABOUT-7702"
	markerNew   = "Marker-NEWPAGE-3091"
)

func bakery(t *testing.T) *decoy.Bundle {
	t.Helper()
	b, err := decoy.FromFiles([]decoy.File{
		{Path: "index.html", Content: []byte("<title>Bakery</title><p>" + markerHome)},
		{Path: "about.html", Content: []byte("<p>" + markerAbout)},
		{Path: "404.html", Content: []byte("<p>gone")},
		{Path: "style.css", Content: []byte("body{margin:0}")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fileArgs(path, content string) map[string]any {
	return map[string]any{"path": path, "content": content}
}

func listNames(t *testing.T, e *testEnv, secret string) []string {
	t.Helper()
	s := e.session(secret)
	var names []string
	for tool, err := range s.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(tool.Name, "decoy_site") {
			names = append(names, tool.Name)
		}
	}
	sort.Strings(names)
	return names
}

// Each profile sees exactly its tools: reads for everyone, the draft and the new-version plan for the operator, the rest of the changes
// for the admin profile.
func TestDecoyToolsPerProfile(t *testing.T) {
	e := newTestEnv(t)
	reads := []string{"decoy_site_file", "decoy_site_get", "decoy_sites_list"}
	_, ro := e.token(ProfileReadonly)
	_, op := e.token(ProfileOperator)
	_, ad := e.token(ProfileAdmin)
	if got := listNames(t, e, ro); !slices.Equal(got, reads) {
		t.Errorf("readonly sees %v", got)
	}
	opWant := slices.Concat(reads, []string{"decoy_site_draft_save", "decoy_site_version_apply", "decoy_site_version_plan"})
	sort.Strings(opWant)
	if got := listNames(t, e, op); !slices.Equal(got, opWant) {
		t.Errorf("operator sees %v, want %v", got, opWant)
	}
	adWant := slices.Concat(opWant, []string{"decoy_site_assign_apply", "decoy_site_assign_plan", "decoy_site_delete_apply", "decoy_site_delete_plan",
		"decoy_site_ready_apply", "decoy_site_ready_plan", "decoy_site_revert_apply", "decoy_site_revert_plan", "decoy_site_unassign_apply", "decoy_site_unassign_plan"})
	sort.Strings(adWant)
	if got := listNames(t, e, ad); !slices.Equal(got, adWant) {
		t.Errorf("admin sees %v, want %v", got, adWant)
	}
	// a tool the profile does not see cannot be called
	if _, err := e.session(op).CallTool(context.Background(), &sdk.CallToolParams{Name: "decoy_site_assign_plan", Arguments: map[string]any{}}); err == nil {
		t.Error("an operator token called decoy_site_assign_plan")
	}
	if _, err := e.session(ro).CallTool(context.Background(), &sdk.CallToolParams{Name: "decoy_site_draft_save", Arguments: map[string]any{}}); err == nil {
		t.Error("a readonly token called decoy_site_draft_save")
	}
}

func TestDecoyReads(t *testing.T) {
	e := newTestEnv(t)
	site := e.w.lib.put("Bakery", "a small neighbourhood bakery", "ready", bakery(t), "no_404")
	site.nodes = []string{nodeA}
	_, ro := e.token(ProfileReadonly)
	s := e.session(ro)

	list := decode[DecoySitesV](t, mustOK(t, s, "decoy_sites_list", map[string]any{}))
	if len(list.Sites) != 1 || list.SiteLimit != 100 || list.PanelSource != "builtin" {
		t.Fatalf("list = %+v", list)
	}
	got := list.Sites[0]
	if got.ID != site.id || got.Name != "Bakery" || got.Note != "a small neighbourhood bakery" || got.State != "ready" || got.Source != "upload" ||
		got.Files != 4 || got.NodeN != 1 || !slices.Equal(got.Nodes, []string{"de1"}) || len(got.Warnings) != 1 || got.Warnings[0].Code != "no_404" || got.Title != "Bakery home" {
		t.Fatalf("site = %+v", got)
	}
	if len(got.Bundle) != 12 {
		t.Errorf("bundle %q is not a short hash", got.Bundle)
	}

	one := decode[DecoySiteGetV](t, mustOK(t, s, "decoy_site_get", map[string]any{"site_id": site.id}))
	if len(one.Versions) != 1 || !one.Versions[0].Current || len(one.Files) != 4 || len(one.Nodes) != 1 || one.Nodes[0].Node != "de1" || one.Nodes[0].Delivery != "serving" {
		t.Fatalf("get = %+v", one)
	}
	sizes := map[string]uint64{}
	for _, f := range one.Files {
		sizes[f.Path] = f.Bytes
	}
	if sizes["style.css"] != uint64(len("body{margin:0}")) {
		t.Errorf("file sizes = %v", sizes)
	}

	file := decode[DecoyFileV](t, mustOK(t, s, "decoy_site_file", map[string]any{"site_id": site.id, "path": "about.html"}))
	if file.Text != "<p>"+markerAbout || file.Bytes != uint64(len(file.Text)) || file.Truncated {
		t.Fatalf("file = %+v", file)
	}

	for name, tc := range map[string]struct {
		tool string
		args map[string]any
		want string
	}{
		"bad id":       {"decoy_site_get", map[string]any{"site_id": "../x"}, "not a valid decoy site id"},
		"unknown site": {"decoy_site_get", map[string]any{"site_id": "dsi_nope"}, "decoy_site_not_found"},
		"no file":      {"decoy_site_file", map[string]any{"site_id": site.id, "path": "none.html"}, "decoy_file_not_found"},
		"no path":      {"decoy_site_file", map[string]any{"site_id": site.id}, "path"},
	} {
		if out := mustFail(t, s, tc.tool, tc.args); !strings.Contains(out, tc.want) {
			t.Errorf("%s: %q does not contain %q", name, out, tc.want)
		}
	}
	// the node's address never shows
	noCanary(t, "decoy reads", mustOK(t, s, "decoy_sites_list", map[string]any{}))
}

// The draft is saved at once, by the token, with no plan and nothing sent to a node; its text is in no audit row.
func TestDecoyDraftSaveNeedsNoApprovalAndReachesNoNode(t *testing.T) {
	e := newTestEnv(t)
	tokenID, op := e.token(ProfileOperator)
	s := e.session(op)

	out := mustOK(t, s, "decoy_site_draft_save", map[string]any{
		"name": "Tea shop", "note": "a quiet tea shop with a small menu",
		"files": []any{fileArgs("index.html", "<title>Tea</title>"+markerHome), fileArgs("404.html", "<p>gone"), fileArgs("style.css", "p{color:#222}")},
	})
	draft := decode[DecoyDraftV](t, out)
	if draft.State != "draft" || draft.Version != 1 || draft.Files != 3 || draft.SiteID == "" || !strings.Contains(draft.Next, "decoy_site_assign_plan") {
		t.Fatalf("draft = %+v", draft)
	}
	lib := e.w.lib
	if len(lib.creates) != 1 || lib.creates[0].Source != "mcp" || lib.creates[0].State != "draft" || lib.added[0] != tokenID {
		t.Fatalf("create = %+v by %v", lib.creates, lib.added)
	}
	if len(e.plans.byID) != 0 || len(lib.assigned) != 0 || len(e.w.nodeDecoyReqs) != 0 || slices.Contains(lib.calls, "AssignDecoySite") || slices.Contains(lib.calls, "SetDecoySiteReady") {
		t.Fatalf("a draft reached the owner's plans or a node: plans=%d calls=%v", len(e.plans.byID), lib.calls)
	}
	for _, a := range e.audit {
		if strings.Contains(fmt.Sprint(a), markerHome) {
			t.Errorf("audit row carries the site's text: %+v", a)
		}
	}

	// a new version of the draft: the whole site again
	two := decode[DecoyDraftV](t, mustOK(t, s, "decoy_site_draft_save", map[string]any{
		"site_id": draft.SiteID, "files": []any{fileArgs("index.html", "<title>Tea</title>v2"), fileArgs("404.html", "<p>gone")},
	}))
	if two.Version != 2 || two.Files != 2 {
		t.Fatalf("second version = %+v", two)
	}
	// changes on top of the current version: add a page, replace one, delete one
	three := decode[DecoyDraftV](t, mustOK(t, s, "decoy_site_draft_save", map[string]any{
		"site_id":      draft.SiteID,
		"change_files": []any{fileArgs("menu.html", "<p>"+markerNew), fileArgs("index.html", "<title>Tea</title>v3")},
		"remove_files": []any{"404.html"},
	}))
	if three.Version != 3 || three.Files != 2 {
		t.Fatalf("third version = %+v", three)
	}
	cur := lib.sites[draft.SiteID].current()
	if c, _ := cur.File("index.html"); string(c) != "<title>Tea</title>v3" {
		t.Errorf("index.html = %q", c)
	}
	if c, _ := cur.File("menu.html"); string(c) != "<p>"+markerNew {
		t.Errorf("menu.html = %q", c)
	}
	if _, ok := cur.File("404.html"); ok {
		t.Error("404.html is still there")
	}
	if len(e.plans.byID) != 0 {
		t.Error("an edit made a plan")
	}
}

func TestDecoyDraftSaveRefusals(t *testing.T) {
	e := newTestEnv(t)
	_, op := e.token(ProfileOperator)
	s := e.session(op)
	ready := e.w.lib.put("Bakery", "n", "ready", bakery(t))
	draft := e.w.lib.put("Tea", "n", "draft", bakery(t))
	index := fileArgs("index.html", "<p>x</p>")
	cases := map[string]struct {
		args map[string]any
		want string
	}{
		"no name":            {map[string]any{"note": "n", "files": []any{index}}, "needs a name"},
		"no note":            {map[string]any{"name": "N", "files": []any{index}}, "needs a note"},
		"long name":          {map[string]any{"name": strings.Repeat("n", 61), "note": "n", "files": []any{index}}, "needs a name"},
		"no site":            {map[string]any{"name": "N", "note": "n"}, "give the site"},
		"changes to nothing": {map[string]any{"name": "N", "note": "n", "change_files": []any{index}}, "give site_id"},
		"taken name":         {map[string]any{"name": "Bakery", "note": "n", "files": []any{index}}, "decoy_name_taken"},
		"files and bundle":   {map[string]any{"name": "N", "note": "n", "files": []any{index}, "bundle_base64": base64.StdEncoding.EncodeToString(bakery(t).Archive())}, "not both"},
		"whole and changes":  {map[string]any{"site_id": draft.id, "files": []any{index}, "remove_files": []any{"about.html"}}, "not both"},
		"name on a version":  {map[string]any{"site_id": draft.id, "name": "Other", "files": []any{index}}, "set when the draft is created"},
		"remove unknown":     {map[string]any{"site_id": draft.id, "remove_files": []any{"nope.html"}}, "not a file of the site"},
		"change and remove":  {map[string]any{"site_id": draft.id, "change_files": []any{index}, "remove_files": []any{"index.html"}}, "both"},
		"remove the index":   {map[string]any{"site_id": draft.id, "remove_files": []any{"index.html"}}, "no_index"},
		"bad path":           {map[string]any{"site_id": draft.id, "change_files": []any{fileArgs("../x", "x")}}, "must not contain"},
		"other case":         {map[string]any{"site_id": draft.id, "change_files": []any{fileArgs("About.html", "x")}}, "ignoring case"},
		"bad id":             {map[string]any{"site_id": "x", "files": []any{index}}, "not a valid decoy site id"},
		// the panel refuses a ready site and the tool says what to do
		"a ready site": {map[string]any{"site_id": ready.id, "files": []any{index}}, "decoy_site_version_plan"},
	}
	for name, tc := range cases {
		if out := mustFail(t, s, "decoy_site_draft_save", tc.args); !strings.Contains(out, tc.want) {
			t.Errorf("%s: %q does not contain %q", name, out, tc.want)
		}
	}
	if len(e.w.lib.creates) != 0 || len(e.w.lib.adds) != 0 || len(ready.versions) != 1 {
		t.Fatalf("a refused draft reached the library: creates=%d adds=%d ready versions=%d", len(e.w.lib.creates), len(e.w.lib.adds), len(ready.versions))
	}
}

// The plan puts a draft on a node in one step: the owner sees what is replaced and the warnings, nothing happens before the approval,
// and the apply marks the site ready and sends it to the node.
func TestDecoyAssignPlanApplyThroughTheOwner(t *testing.T) {
	e := newTestEnv(t)
	lib := e.w.lib
	old := lib.put("Old site", "n", "ready", bakery(t))
	old.nodes = []string{nodeA}
	draft := lib.put("Tea", "a tea shop", "draft", bakery(t), "external_urls")
	lib.sharedAt = []string{nodeA, nodeB}
	_, ad := e.token(ProfileAdmin)
	s := e.session(ad)

	if out := mustFail(t, s, "decoy_site_assign_plan", map[string]any{"site_id": draft.id, "nodes": []any{"de1"}}); !strings.Contains(out, "decoy_site_draft") || !strings.Contains(out, "mark it ready") {
		t.Errorf("a draft without mark_ready: %s", out)
	}
	if out := mustFail(t, s, "decoy_site_assign_plan", map[string]any{"site_id": draft.id, "mark_ready": true}); !strings.Contains(out, "at least one node") {
		t.Errorf("no target: %s", out)
	}
	if out := mustFail(t, s, "decoy_site_assign_plan", map[string]any{"site_id": draft.id, "mark_ready": true, "nodes": []any{"zz9"}}); !strings.Contains(out, "node not found") {
		t.Errorf("unknown node: %s", out)
	}
	p := planOf(t, s, "decoy_site_assign", map[string]any{"site_id": draft.id, "mark_ready": true, "nodes": []any{"de1", nodeB}, "panel": true, "reason": "a tea shop for de1"})
	if !p.NeedsApproval || !slices.Contains(p.Danger, "fleet") {
		t.Fatalf("plan = %+v", p)
	}
	facts := map[string][]string{}
	for _, f := range p.Facts {
		facts[f.Key] = append(facts[f.Key], f.Value)
	}
	if facts["site"][0] != "Tea" || facts["marks_ready"][0] != "true" || facts["nodes"][0] != "de1, nl1" || facts["panel"][0] != "true" {
		t.Errorf("facts = %v", facts)
	}
	if !slices.Contains(facts["replaces"], "de1: own Old site") || !slices.Contains(facts["replaces"], "nl1: builtin") || !slices.Contains(facts["replaces"], "the panel: builtin") {
		t.Errorf("replacements = %v", facts["replaces"])
	}
	if len(facts["warning"]) != 2 || !strings.HasPrefix(facts["warning"][0], "external_urls") || !strings.HasPrefix(facts["warning"][1], "shared_domains") {
		t.Errorf("warnings = %v", facts["warning"])
	}
	// nothing moved yet
	if len(lib.assigned) != 0 || draft.state != "draft" || slices.Contains(lib.calls, "SetDecoySiteReady") {
		t.Fatalf("the plan changed the library: calls=%v state=%s", lib.calls, draft.state)
	}
	if out := applyError(t, s, "decoy_site_assign", p.ConfirmToken); !strings.Contains(out, "waiting for the owner") || len(lib.assigned) != 0 {
		t.Fatalf("apply before the approval: %s", out)
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	if out := applyOf(t, s, "decoy_site_assign", p.ConfirmToken); out.Status != "applied" {
		t.Fatalf("apply = %+v", out)
	}
	if draft.state != "ready" || len(lib.assigned) != 1 || !slices.Equal(lib.assigned[0].NodeIds, []string{nodeA, nodeB}) || !lib.assigned[0].Panel || lib.assigned[0].DryRun {
		t.Fatalf("after the apply: state %s, assigned %+v", draft.state, lib.assigned)
	}
	if len(old.nodes) != 0 || !draft.panel || !slices.Equal(draft.nodes, []string{nodeA, nodeB}) {
		t.Errorf("standing: old %v, new %v panel %v", old.nodes, draft.nodes, draft.panel)
	}
	// the approved calls carried the grant of this plan
	for _, c := range e.w.calls("/mistgate.decoysite.v1.DecoySiteService/AssignDecoySite") {
		if !c.Planning && c.Approved != p.PlanID {
			t.Errorf("assignment call = %+v", c)
		}
	}
}

// A new version of a ready site that stands on nodes: the plan names the nodes and lists the files, with sizes and no text; the apply
// saves it only on the version the plan was made against.
func TestDecoyVersionPlanListsFilesAndNodesWithoutContents(t *testing.T) {
	e := newTestEnv(t)
	lib := e.w.lib
	site := lib.put("Bakery", "n", "ready", bakery(t))
	site.nodes = []string{nodeA, nodeB}
	_, op := e.token(ProfileOperator)
	s := e.session(op)

	if out := mustFail(t, s, "decoy_site_version_plan", map[string]any{"site_id": lib.put("Tea", "n", "draft", bakery(t)).id, "change_files": []any{fileArgs("a.html", "x")}}); !strings.Contains(out, "decoy_site_draft_save") {
		t.Errorf("a draft: %s", out)
	}
	if out := mustFail(t, s, "decoy_site_version_plan", map[string]any{"site_id": site.id, "change_files": []any{fileArgs("about.html", "<p>"+markerAbout)}}); !strings.Contains(out, "nothing changes") {
		t.Errorf("the same files: %s", out)
	}
	p := planOf(t, s, "decoy_site_version", map[string]any{
		"site_id": site.id, "reason": "add a menu",
		"change_files": []any{fileArgs("menu.html", "<p>"+markerNew), fileArgs("about.html", "<p>"+markerAbout+" more")},
		"remove_files": []any{"style.css"},
	})
	if !p.NeedsApproval || !strings.Contains(p.Summary, "2 node(s)") {
		t.Fatalf("plan = %+v", p)
	}
	byKey := map[string]string{}
	var lines strings.Builder
	for _, f := range p.Facts {
		if f.Key == "file_changes" {
			lines.WriteString(f.Value + "\n")
		} else {
			byKey[f.Key] = f.Value
		}
	}
	if byKey["files_added"] != "1" || byKey["files_changed"] != "1" || byKey["files_deleted"] != "1" || byKey["standing_on_nodes"] != "2" || byKey["standing_on"] != "de1, nl1" || byKey["standing_on_panel"] != "false" {
		t.Errorf("facts = %v", byKey)
	}
	for _, want := range []string{
		fmt.Sprintf("added menu.html: %d bytes", len("<p>"+markerNew)),
		fmt.Sprintf("changed about.html: %d -> %d bytes", len("<p>"+markerAbout), len("<p>"+markerAbout+" more")),
		fmt.Sprintf("deleted style.css: %d bytes", len("body{margin:0}")),
	} {
		if !strings.Contains(lines.String(), want) {
			t.Errorf("the card does not list %q: %q", want, lines.String())
		}
	}
	// no text anywhere the plan shows or keeps: the card, the audit rows, and what is stored as arguments is the archive only
	stored := e.plans.byID[p.PlanID]
	for name, text := range map[string]string{"plan": fmt.Sprint(p), "facts": stored.FactsJSON, "summary": stored.Summary, "audit": fmt.Sprint(e.audit), "params": stored.ParamsJSON} {
		for _, marker := range []string{markerHome, markerAbout, markerNew} {
			if strings.Contains(text, marker) {
				t.Errorf("%s carries the file text %s", name, marker)
			}
		}
	}
	if len(lib.adds) != 0 {
		t.Fatal("the plan saved a version")
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	// the site moved on after the plan: the apply refuses instead of overwriting
	site.versions = append(site.versions, withFile(t, bakery(t), "extra.html", "x"))
	if out := applyError(t, s, "decoy_site_version", p.ConfirmToken); !strings.Contains(out, "changed since the plan") || len(lib.adds) != 0 {
		t.Fatalf("apply on a moved site: %s", out)
	}
	// a new plan on the moved site, applied
	p2 := planOf(t, s, "decoy_site_version", map[string]any{"site_id": site.id, "change_files": []any{fileArgs("menu.html", "<p>"+markerNew)}})
	if err := e.plans.decide(p2.PlanID, true); err != nil {
		t.Fatal(err)
	}
	out := applyOf(t, s, "decoy_site_version", p2.ConfirmToken)
	if out.Status != "applied" || !strings.Contains(out.Result, "2 node(s)") || len(lib.adds) != 1 {
		t.Fatalf("apply = %+v, adds %d", out, len(lib.adds))
	}
	got, err := decoy.ParseArchive(lib.adds[0].BundleArchive)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := got.File("menu.html"); !ok || string(c) != "<p>"+markerNew {
		t.Errorf("the saved version lacks menu.html: %v", got.Infos())
	}
	if _, ok := got.File("extra.html"); !ok {
		t.Error("the saved version dropped extra.html of the version it was made on")
	}
	// the call carried the plan's grant, so a draft-only call would have been refused
	if c := e.w.calls("/mistgate.decoysite.v1.DecoySiteService/AddDecoySiteVersion"); len(c) != 1 || c[0].Approved != p2.PlanID {
		t.Errorf("AddDecoySiteVersion calls = %+v", c)
	}
}

func withFile(t *testing.T, b *decoy.Bundle, name, content string) *decoy.Bundle {
	t.Helper()
	nb, err := b.With(name, []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	return nb
}
