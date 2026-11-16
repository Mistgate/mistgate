//go:build !js

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/mcp"
	"google.golang.org/protobuf/encoding/protojson"
)

type decoyNodeService struct {
	adminv1connect.UnimplementedNodeServiceHandler
}

func (decoyNodeService) ListNodes(context.Context, *connect.Request[adminv1.ListNodesRequest]) (*connect.Response[adminv1.ListNodesResponse], error) {
	return connect.NewResponse(&adminv1.ListNodesResponse{Nodes: []*adminv1.Node{{Id: "node_a", Name: "alpha", DecoyOwnAvailable: true}}}), nil
}

func (decoyNodeService) GetNode(context.Context, *connect.Request[adminv1.GetNodeRequest]) (*connect.Response[adminv1.GetNodeResponse], error) {
	return connect.NewResponse(&adminv1.GetNodeResponse{Node: &adminv1.Node{Id: "node_a", Name: "alpha", DecoyPanelAvailable: true, DecoyOwnAvailable: true}}), nil
}

const (
	siteMarkerHTML = "<h1>Marker-HOME-4471</h1>"
	siteMarkerCSS  = "body{color:#123} /* Marker-CSS-9920 */"
)

// A model writes a site into node_decoy_plan: the call passes the real tool's schema, the card lists the files without their
// contents, and neither the chat's tool line nor the saved history keeps the text; the turn itself still has it.
func TestAssistantDecoySiteFromFilesKeepsContentsOutOfCardAndHistory(t *testing.T) {
	arguments, err := json.Marshal(map[string]any{
		"node": "alpha", "mode": "own", "reason": "a quiet site for this node",
		"files": []map[string]any{
			{"path": "index.html", "content": siteMarkerHTML},
			{"path": "style.css", "content": siteMarkerCSS},
			{"path": "img/dot.png", "content_base64": "iVBORw0KGgo="},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := newAssistantFixture(t,
		modelResponse{Tools: []modelToolCall{scriptedTool("node_decoy_plan", "call_decoy", string(arguments))}},
		modelResponse{Text: "The site is ready for you to review."},
		modelResponse{Text: `{"title":"Decoy site","summary":"Prepared a decoy site."}`},
	)
	authService, err := auth.New(f.st, auth.Config{RPID: "example.com", Origins: []string{"https://example.com"}, Vault: f.vault}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.service.auth = authService
	api := http.NewServeMux()
	path, handler := adminv1connect.NewNodeServiceHandler(decoyNodeService{})
	api.Handle(path, authService.RequireSession(handler))
	runtime, err := mcp.NewAssistant(mcp.Config{Plans: assistantMCPPlanStore{st: f.st}, Auth: assistantTestMCPAuth{auth: authService}, API: api})
	if err != nil {
		t.Fatal(err)
	}
	f.service.SetToolRuntime(runtime)

	stream, err := f.client.Chat(context.Background(), connect.NewRequest(&adminv1.ChatRequest{Text: "Make a decoy site for the node alpha"}))
	if err != nil {
		t.Fatal(err)
	}
	var conversationID string
	var card *adminv1.PlanCard
	var wire strings.Builder
	for _, event := range drainChat(t, stream) {
		if started := event.GetStarted(); started != nil {
			conversationID = started.ConversationId
		}
		if plan := event.GetPlan(); plan != nil {
			card = plan
		}
		b, _ := protojson.Marshal(event)
		wire.Write(b)
	}
	if card == nil || card.Tool != "node_decoy" || card.State != adminv1.PlanState_PLAN_STATE_PROPOSED {
		t.Fatalf("no plan card for the site: %+v\n%s", card, wire.String())
	}
	var list, hash string
	for _, fact := range card.Facts {
		switch fact.Key {
		case "file_list":
			list += fact.Value
		case "bundle":
			hash = fact.Value
		}
	}
	for _, line := range []string{"index.html: " + itoa(len(siteMarkerHTML)) + " bytes", "style.css: " + itoa(len(siteMarkerCSS)) + " bytes", "img/dot.png: 8 bytes"} {
		if !strings.Contains(list, line) {
			t.Errorf("the card does not list %q: %q", line, list)
		}
	}
	if len(strings.ReplaceAll(hash, " ", "")) != 64 {
		t.Errorf("the card shows no SHA-256: %q", hash)
	}

	// what went to the chat and what is saved: no contents
	history, err := f.service.activeHistory(context.Background(), f.admin.ID, conversationID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var saved strings.Builder
	for _, raw := range history {
		saved.Write(raw)
	}
	for name, text := range map[string]string{"events": wire.String(), "saved history": saved.String()} {
		for _, marker := range []string{"Marker-HOME-4471", "Marker-CSS-9920", "iVBORw0KGgo"} {
			if strings.Contains(text, marker) {
				t.Errorf("%s keep the file contents (%s)", name, marker)
			}
		}
	}
	if !strings.Contains(saved.String(), "omitted from history") || !strings.Contains(saved.String(), "index.html") {
		t.Errorf("the saved call lost its file names: %s", saved.String())
	}
	// the turn itself still sees what the model wrote, so it can go on with it
	requests := f.adapter.requests()
	if len(requests) < 2 || !strings.Contains(string(joinHistory(requests[1].History)), "Marker-HOME-4471") {
		t.Fatal("the model lost its own call inside the turn")
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func joinHistory(h []json.RawMessage) []byte {
	var out []byte
	for _, raw := range h {
		out = append(out, raw...)
	}
	return out
}

func TestStoredTranscriptCompactsBothProviderShapes(t *testing.T) {
	args := `{"node":"alpha","mode":"own","files":[{"path":"index.html","content":"TEXT-ONE"},{"path":"a.png","content_base64":"QUJD"}],"bundle_base64":"UEsDBAoAAAAA"}`
	anthropic := `{"role":"assistant","content":[{"type":"text","text":"Writing it."},{"type":"tool_use","id":"t1","name":"node_decoy_plan","input":` + args + `}]}`
	escaped, _ := json.Marshal(args)
	openai := `{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"node_decoy_plan","arguments":` + string(escaped) + `}}]}`
	for name, raw := range map[string]string{"anthropic": anthropic, "openai": openai} {
		out := string(storedTranscript(json.RawMessage(raw)))
		for _, content := range []string{"TEXT-ONE", "QUJD", "UEsDBAoAAAAA"} {
			if strings.Contains(out, content) {
				t.Errorf("%s: %s survived: %s", name, content, out)
			}
		}
		if !json.Valid([]byte(out)) || !strings.Contains(out, "index.html") || !strings.Contains(out, "a.png") || !strings.Contains(out, "alpha") {
			t.Errorf("%s: the call lost its shape: %s", name, out)
		}
	}
	// other calls, plain messages and broken JSON pass through untouched
	for _, raw := range []string{
		`{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"user_update_plan","input":{"files":[{"content":"KEEP"}]}}]}`,
		`{"role":"user","content":"node_decoy_plan please"}`, `not json node_decoy_plan`,
	} {
		if got := string(storedTranscript(json.RawMessage(raw))); got != raw {
			t.Errorf("changed %s into %s", raw, got)
		}
	}
	if got := string(compactBulkyArgs("node_decoy_plan", []byte(`{"files":"oops","bundle_base64":5}`))); !strings.Contains(got, "oops") {
		t.Errorf("odd shapes must pass through: %s", got)
	}
}
