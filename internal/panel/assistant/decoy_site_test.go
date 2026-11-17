//go:build !js

package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	decoysitev1 "github.com/mistgate/mistgate/gen/mistgate/decoysite/v1"
	"github.com/mistgate/mistgate/gen/mistgate/decoysite/v1/decoysitev1connect"
	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/mcp"
)

// The library of decoy sites for the assistant: the real token policy and the real MCP tools over a small fake library.

type libSite struct {
	id, name, note, source, state string
	versions                      []*decoy.Bundle
	nodes                         []string
}

type libraryService struct {
	decoysitev1connect.UnimplementedDecoySiteServiceHandler
	mu      sync.Mutex
	sites   []*libSite
	creates []*decoysitev1.CreateDecoySiteRequest
	adds    []*decoysitev1.AddDecoySiteVersionRequest
	assigns int
}

func (l *libraryService) site(id string) *libSite {
	for _, s := range l.sites {
		if s.id == id {
			return s
		}
	}
	return nil
}

func (s *libSite) msg() *decoysitev1.DecoySite {
	cur := s.versions[len(s.versions)-1]
	return &decoysitev1.DecoySite{Id: s.id, Name: s.name, Note: s.note, Source: s.source, State: s.state, BundleSha256: cur.Hash(), Version: uint32(len(s.versions)),
		FileCount: uint32(len(cur.Files())), SizeBytes: uint64(cur.Size()), NodeCount: uint32(len(s.nodes)), Title: s.name,
		Checks: []*decoysitev1.DecoyCheck{{Code: "index_html", Level: "required", Ok: true}}}
}

func (l *libraryService) ListDecoySites(context.Context, *connect.Request[decoysitev1.ListDecoySitesRequest]) (*connect.Response[decoysitev1.ListDecoySitesResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := &decoysitev1.ListDecoySitesResponse{PanelSource: "builtin", SiteLimit: 100, VersionLimit: 5}
	for _, s := range l.sites {
		out.Sites = append(out.Sites, s.msg())
	}
	return connect.NewResponse(out), nil
}

func (l *libraryService) GetDecoySite(_ context.Context, r *connect.Request[decoysitev1.GetDecoySiteRequest]) (*connect.Response[decoysitev1.GetDecoySiteResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.site(r.Msg.SiteId)
	if s == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("decoy_site_not_found"))
	}
	out := &decoysitev1.GetDecoySiteResponse{Site: s.msg()}
	for i, v := range s.versions {
		out.Versions = append(out.Versions, &decoysitev1.DecoyVersion{Seq: uint32(i + 1), Current: i == len(s.versions)-1, FileCount: uint32(len(v.Files()))})
	}
	for _, f := range s.versions[len(s.versions)-1].Infos() {
		out.Files = append(out.Files, &decoysitev1.DecoyFileInfo{Path: f.Path, Size: uint64(f.Size)})
	}
	for _, n := range s.nodes {
		out.Nodes = append(out.Nodes, &decoysitev1.DecoySiteNode{NodeId: n, NodeName: "alpha", Supported: true})
	}
	return connect.NewResponse(out), nil
}

func (l *libraryService) GetDecoyFile(_ context.Context, r *connect.Request[decoysitev1.GetDecoyFileRequest]) (*connect.Response[decoysitev1.GetDecoyFileResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.site(r.Msg.SiteId)
	if s == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("decoy_site_not_found"))
	}
	c, ok := s.versions[len(s.versions)-1].File(r.Msg.Path)
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("decoy_file_not_found"))
	}
	return connect.NewResponse(&decoysitev1.GetDecoyFileResponse{Text: string(c), Size: uint64(len(c))}), nil
}

func (l *libraryService) DownloadDecoySite(_ context.Context, r *connect.Request[decoysitev1.DownloadDecoySiteRequest]) (*connect.Response[decoysitev1.DownloadDecoySiteResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.site(r.Msg.SiteId)
	if s == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("decoy_site_not_found"))
	}
	return connect.NewResponse(&decoysitev1.DownloadDecoySiteResponse{Archive: s.versions[len(s.versions)-1].Archive()}), nil
}

// draftOnly is the panel's rule: a token or the assistant, with no approved plan, saves drafts only.
func draftOnly(ctx context.Context) bool {
	return auth.PrincipalFrom(ctx).Token && auth.ApprovedPlanTool(ctx) == ""
}

func (l *libraryService) CreateDecoySite(ctx context.Context, r *connect.Request[decoysitev1.CreateDecoySiteRequest]) (*connect.Response[decoysitev1.CreateDecoySiteResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r.Msg.State != "draft" && draftOnly(ctx) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("decoy_site_not_draft"))
	}
	b, err := decoy.ParseArchive(r.Msg.BundleArchive)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	s := &libSite{id: "dsi_new" + string(rune('a'+len(l.sites))), name: r.Msg.Name, note: r.Msg.Note, source: r.Msg.Source, state: r.Msg.State, versions: []*decoy.Bundle{b}}
	l.sites = append(l.sites, s)
	l.creates = append(l.creates, r.Msg)
	return connect.NewResponse(&decoysitev1.CreateDecoySiteResponse{Site: s.msg()}), nil
}

func (l *libraryService) AddDecoySiteVersion(ctx context.Context, r *connect.Request[decoysitev1.AddDecoySiteVersionRequest]) (*connect.Response[decoysitev1.AddDecoySiteVersionResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.site(r.Msg.SiteId)
	if s == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("decoy_site_not_found"))
	}
	if s.state != "draft" && draftOnly(ctx) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("decoy_site_not_draft"))
	}
	b, err := decoy.ParseArchive(r.Msg.BundleArchive)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	s.versions = append(s.versions, b)
	l.adds = append(l.adds, r.Msg)
	return connect.NewResponse(&decoysitev1.AddDecoySiteVersionResponse{Site: s.msg(), UpdatedNodeIds: slices.Clone(s.nodes)}), nil
}

func (l *libraryService) AssignDecoySite(_ context.Context, r *connect.Request[decoysitev1.AssignDecoySiteRequest]) (*connect.Response[decoysitev1.AssignDecoySiteResponse], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.site(r.Msg.SiteId)
	if s == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("decoy_site_not_found"))
	}
	if !r.Msg.DryRun {
		l.assigns++
	}
	out := &decoysitev1.AssignDecoySiteResponse{}
	for _, n := range r.Msg.NodeIds {
		out.Replaced = append(out.Replaced, &decoysitev1.DecoyReplaced{Target: n, Previous: "builtin"})
	}
	return connect.NewResponse(out), nil
}

type decoyFixture struct {
	*assistantFixture
	lib *libraryService
}

// newDecoyFixture is the assistant over the real MCP tools and the real token policy, with one node, alpha, and the fake library.
func newDecoyFixture(t *testing.T, responses ...modelResponse) *decoyFixture {
	t.Helper()
	f := newAssistantFixture(t, responses...)
	authService, err := auth.New(f.st, auth.Config{RPID: "example.com", Origins: []string{"https://example.com"}, Vault: f.vault}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.service.auth = authService
	lib := &libraryService{}
	api := http.NewServeMux()
	path, handler := adminv1connect.NewNodeServiceHandler(decoyNodeService{})
	api.Handle(path, authService.RequireSession(handler))
	path, handler = decoysitev1connect.NewDecoySiteServiceHandler(lib)
	api.Handle(path, authService.RequireSession(handler))
	runtime, err := mcp.NewAssistant(mcp.Config{Plans: assistantMCPPlanStore{st: f.st}, Auth: assistantTestMCPAuth{auth: authService}, API: api})
	if err != nil {
		t.Fatal(err)
	}
	f.service.SetToolRuntime(runtime)
	return &decoyFixture{assistantFixture: f, lib: lib}
}

type chatRun struct {
	conversationID string
	plans          []*adminv1.PlanCard
	wire, saved    string
	toolErrors     []string
}

func (f *decoyFixture) chat(t *testing.T, text string) chatRun {
	t.Helper()
	stream, err := f.client.Chat(context.Background(), connect.NewRequest(&adminv1.ChatRequest{Text: text}))
	if err != nil {
		t.Fatal(err)
	}
	var run chatRun
	var wire strings.Builder
	for _, event := range drainChat(t, stream) {
		if started := event.GetStarted(); started != nil {
			run.conversationID = started.ConversationId
		}
		if plan := event.GetPlan(); plan != nil {
			run.plans = append(run.plans, plan)
		}
		if done := event.GetToolFinished(); done != nil && !done.Ok {
			run.toolErrors = append(run.toolErrors, done.Tool+": "+done.Error)
		}
		b, _ := protojson.Marshal(event)
		wire.Write(b)
	}
	run.wire = wire.String()
	history, err := f.service.activeHistory(context.Background(), f.admin.ID, run.conversationID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var saved strings.Builder
	for _, raw := range history {
		saved.Write(raw)
	}
	run.saved = saved.String()
	return run
}

func jsonArgs(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func factValues(card *adminv1.PlanCard, key string) []string {
	var out []string
	for _, f := range card.Facts {
		if f.Key == key {
			out = append(out, f.Value)
		}
	}
	return out
}

func (f *decoyFixture) seed(t *testing.T, name, note, state string, files ...decoy.File) *libSite {
	t.Helper()
	b, err := decoy.FromFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	s := &libSite{id: "dsi_seed" + string(rune('a'+len(f.lib.sites))), name: name, note: note, source: "upload", state: state, versions: []*decoy.Bundle{b}}
	f.lib.sites = append(f.lib.sites, s)
	return s
}

const (
	teaMarker    = "Marker-TEA-6151"
	bakeryMarker = "Marker-BAKERY-2208"
	priceMarker  = "Marker-PRICES-8830"
)

// "Make a decoy site for alpha": the assistant looks at the library, saves a DRAFT (no card, nothing on a node, marked as the
// assistant's), and offers one card that marks it ready and puts it on alpha. The site's text is in no chat event and no saved history.
func TestAssistantDecoySiteRequestSavesADraftAndOffersTheAssignment(t *testing.T) {
	save := jsonArgs(t, map[string]any{
		"name": "Tea shop", "note": "a small tea shop with a short menu",
		"files": []map[string]any{
			{"path": "index.html", "content": "<title>Tea shop</title><h1>" + teaMarker + "</h1>"},
			{"path": "404.html", "content": "<p>Not found"},
			{"path": "style.css", "content": "body{margin:0}"},
		},
	})
	turns := []modelResponse{
		{Tools: []modelToolCall{scriptedTool("decoy_sites_list", "call_list", `{}`)}},
		{Tools: []modelToolCall{scriptedTool("decoy_site_draft_save", "call_save", save)}},
		{Tools: []modelToolCall{scriptedTool("decoy_site_assign_plan", "call_assign", `{"site_id":"dsi_newb","nodes":["alpha"],"mark_ready":true,"reason":"a decoy site for alpha"}`)}},
		{Text: "The draft is saved. Approve the card to put it on alpha."},
		{Text: `{"title":"Decoy site","summary":"Made a draft."}`},
	}
	f := newDecoyFixture(t, turns...)
	f.seed(t, "Bakery", "a neighbourhood bakery", "ready", decoy.File{Path: "index.html", Content: []byte("<p>" + bakeryMarker)})

	run := f.chat(t, "Make a decoy site for the node alpha")
	if len(run.toolErrors) != 0 {
		t.Fatalf("tools failed: %v\n%s", run.toolErrors, run.wire)
	}
	// the draft: saved by the assistant, a draft, no card for it
	if len(f.lib.creates) != 1 {
		t.Fatalf("creates = %d\n%s", len(f.lib.creates), run.wire)
	}
	c := f.lib.creates[0]
	if c.Source != "assistant" || c.State != "draft" || c.Name != "Tea shop" || c.Note == "" {
		t.Fatalf("created = %+v", c)
	}
	// the model saw the other sites before it wrote: the list went into its next request
	requests := f.adapter.requests()
	if len(requests) < 2 || !strings.Contains(string(joinHistory(requests[1].History)), "a neighbourhood bakery") {
		t.Error("the model did not see the note of the other site")
	}
	// exactly one card: the assignment
	if len(run.plans) != 1 {
		t.Fatalf("%d cards, want one: %+v", len(run.plans), run.plans)
	}
	card := run.plans[0]
	if card.Tool != "decoy_site_assign" || card.State != adminv1.PlanState_PLAN_STATE_PROPOSED || !card.NeedsApproval {
		t.Fatalf("card = %+v", card)
	}
	if got := factValues(card, "marks_ready"); len(got) != 1 || got[0] != "true" {
		t.Errorf("marks_ready = %v", got)
	}
	if got := factValues(card, "nodes"); len(got) != 1 || got[0] != "alpha" {
		t.Errorf("nodes = %v", got)
	}
	if got := factValues(card, "replaces"); len(got) != 1 || !strings.HasPrefix(got[0], "alpha: builtin") {
		t.Errorf("replaces = %v", got)
	}
	if f.lib.assigns != 0 || f.lib.sites[1].state != "draft" {
		t.Fatalf("the card changed something before the owner: assigns %d, state %s", f.lib.assigns, f.lib.sites[1].state)
	}
	// the text of the site is nowhere the person or the history keeps it; the turn itself had it
	for name, text := range map[string]string{"events": run.wire, "saved history": run.saved} {
		if strings.Contains(text, teaMarker) {
			t.Errorf("%s keep the site's text", name)
		}
	}
	if !strings.Contains(run.saved, "index.html") || !strings.Contains(run.saved, "omitted from history") {
		t.Errorf("the saved call lost its file names: %s", run.saved)
	}
	if !strings.Contains(string(joinHistory(requests[2].History)), teaMarker) {
		t.Error("the model lost its own call inside the turn")
	}
}

// "Add a prices page" to a site that stands on alpha: the assistant reads the site, and the change to a site that is serving is a
// card that lists the files added, changed and deleted, with sizes and the number of nodes, and no text. The text it read is not kept.
func TestAssistantEditOfASiteInUseIsACardWithTheFileChanges(t *testing.T) {
	change := jsonArgs(t, map[string]any{
		"site_id": "dsi_seeda", "reason": "a prices page",
		"change_files": []map[string]any{
			{"path": "prices.html", "content": "<p>" + priceMarker},
			{"path": "index.html", "content": "<title>Bakery</title><a href=\"/prices.html\">Prices</a>"},
		},
		"remove_files": []string{"old.html"},
	})
	f := newDecoyFixture(t,
		modelResponse{Tools: []modelToolCall{scriptedTool("decoy_site_get", "call_get", `{"site_id":"dsi_seeda"}`)}},
		modelResponse{Tools: []modelToolCall{scriptedTool("decoy_site_file", "call_file", `{"site_id":"dsi_seeda","path":"index.html"}`)}},
		modelResponse{Tools: []modelToolCall{scriptedTool("decoy_site_version_plan", "call_version", change)}},
		modelResponse{Text: "Approve the card to send the change to alpha."},
		modelResponse{Text: `{"title":"Prices page","summary":"Prepared a new version."}`},
	)
	site := f.seed(t, "Bakery", "a neighbourhood bakery", "ready",
		decoy.File{Path: "index.html", Content: []byte("<title>Bakery</title><p>" + bakeryMarker)},
		decoy.File{Path: "404.html", Content: []byte("<p>gone")}, decoy.File{Path: "old.html", Content: []byte("<p>old")})
	site.nodes = []string{"node_a"}

	run := f.chat(t, "Add a prices page to the bakery site")
	if len(run.toolErrors) != 0 {
		t.Fatalf("tools failed: %v\n%s", run.toolErrors, run.wire)
	}
	if len(run.plans) != 1 || run.plans[0].Tool != "decoy_site_version" || !run.plans[0].NeedsApproval {
		t.Fatalf("cards = %+v", run.plans)
	}
	card := run.plans[0]
	if got := factValues(card, "files_added"); len(got) != 1 || got[0] != "1" {
		t.Errorf("files_added = %v", got)
	}
	if got := factValues(card, "files_changed"); len(got) != 1 || got[0] != "1" {
		t.Errorf("files_changed = %v", got)
	}
	if got := factValues(card, "files_deleted"); len(got) != 1 || got[0] != "1" {
		t.Errorf("files_deleted = %v", got)
	}
	if got := factValues(card, "standing_on_nodes"); len(got) != 1 || got[0] != "1" || !strings.Contains(card.Summary, "1 node(s)") {
		t.Errorf("nodes = %v / %s", got, card.Summary)
	}
	list := strings.Join(factValues(card, "file_changes"), "\n")
	for _, want := range []string{"added prices.html: " + itoa(len("<p>"+priceMarker)) + " bytes", "changed index.html:", "deleted old.html: 6 bytes"} {
		if !strings.Contains(list, want) {
			t.Errorf("the card does not list %q: %q", want, list)
		}
	}
	if len(f.lib.adds) != 0 || len(site.versions) != 1 {
		t.Fatalf("the card saved a version before the owner")
	}
	// what it read and what it wrote stay out of the history and the events; the turn itself had them
	for name, text := range map[string]string{"events": run.wire, "saved history": run.saved} {
		for _, marker := range []string{bakeryMarker, priceMarker} {
			if strings.Contains(text, marker) {
				t.Errorf("%s keep %s", name, marker)
			}
		}
	}
	requests := f.adapter.requests()
	if len(requests) < 3 || !strings.Contains(string(joinHistory(requests[2].History)), bakeryMarker) {
		t.Error("the model did not get the text of the file it read")
	}
}

// The same edit of a DRAFT is saved at once, with no card.
func TestAssistantEditOfADraftIsSavedWithoutACard(t *testing.T) {
	change := jsonArgs(t, map[string]any{
		"site_id": "dsi_seeda", "change_files": []map[string]any{{"path": "prices.html", "content": "<p>" + priceMarker}},
	})
	f := newDecoyFixture(t,
		modelResponse{Tools: []modelToolCall{scriptedTool("decoy_site_draft_save", "call_save", change)}},
		modelResponse{Text: "Saved as a new version of the draft."},
		modelResponse{Text: `{"title":"Prices page","summary":"Saved."}`},
	)
	site := f.seed(t, "Bakery", "a neighbourhood bakery", "draft", decoy.File{Path: "index.html", Content: []byte("<p>" + bakeryMarker)})
	run := f.chat(t, "Continue the bakery draft: add prices")
	if len(run.toolErrors) != 0 || len(run.plans) != 0 {
		t.Fatalf("errors %v, cards %+v\n%s", run.toolErrors, run.plans, run.wire)
	}
	if len(site.versions) != 2 || len(f.lib.adds) != 1 {
		t.Fatalf("versions = %d", len(site.versions))
	}
	if _, ok := site.versions[1].File("index.html"); !ok {
		t.Error("the new version dropped the page it did not change")
	}
	if strings.Contains(run.saved, priceMarker) || strings.Contains(run.wire, priceMarker) {
		t.Error("the page's text is in the history or the events")
	}
}

func TestStoredTranscriptCompactsTheLibraryToolsToo(t *testing.T) {
	for tool, args := range map[string]string{
		"decoy_site_draft_save":   `{"name":"N","note":"n","files":[{"path":"index.html","content":"TEXT-ONE"}]}`,
		"decoy_site_version_plan": `{"site_id":"dsi_a","change_files":[{"path":"a.html","content":"TEXT-ONE"},{"path":"a.png","content_base64":"QUJD"}],"remove_files":["b.html"]}`,
	} {
		raw := `{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"` + tool + `","input":` + args + `}]}`
		out := string(storedTranscript(json.RawMessage(raw)))
		for _, content := range []string{"TEXT-ONE", "QUJD"} {
			if strings.Contains(out, content) {
				t.Errorf("%s: %s survived: %s", tool, content, out)
			}
		}
		if !strings.Contains(out, "omitted from history") || !json.Valid([]byte(out)) {
			t.Errorf("%s: %s", tool, out)
		}
	}
	// the text of a site's file that was read is kept out of the saved results, not out of the live ones
	calls := []modelToolCall{{id: "c1", name: "decoy_site_file"}, {id: "c2", name: "decoy_site_get"}}
	outputs := []toolOutput{{text: "SECRET-PAGE-TEXT"}, {text: "files: index.html"}}
	live, stored := toolResultMessages(&scriptedAdapter{}, calls, outputs)
	if !strings.Contains(joinRaw(live), "SECRET-PAGE-TEXT") || strings.Contains(joinRaw(stored), "SECRET-PAGE-TEXT") || !strings.Contains(joinRaw(stored), "files: index.html") {
		t.Errorf("live %s\nstored %s", joinRaw(live), joinRaw(stored))
	}
}

func joinRaw(raws []jsonRaw) string {
	var b strings.Builder
	for _, r := range raws {
		b.Write(r)
	}
	return b.String()
}
