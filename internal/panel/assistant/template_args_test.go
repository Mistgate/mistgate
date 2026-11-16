//go:build !js

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/mcp"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// A Mihomo template is up to 64 KiB of text. The owner's card carries its name, groups, size and hosts, so the saved dialog keeps neither
// the text the model sends to subscription_mihomo_template_set_plan nor the text it read with subscription_mihomo_template_get: the
// size and a short hash stay. The turn itself still has both.

const (
	templateReadMarker  = "Marker-TPL-READ-5521"
	templateWriteMarker = "Marker-TPL-WRITE-7730"
	templateBase        = "mixed-port: 7890\nproxy-groups:\n  - name: Proxy\n    type: select\n    include-all-proxies: true\nrules:\n  - MATCH,Proxy\n"
)

type templateSubscriptionService struct {
	adminv1connect.UnimplementedSubscriptionServiceHandler
}

func (templateSubscriptionService) GetSubscriptionSettings(context.Context, *connect.Request[adminv1.GetSubscriptionSettingsRequest]) (*connect.Response[adminv1.GetSubscriptionSettingsResponse], error) {
	settings := subsettings.Defaults()
	settings.MihomoTemplates = []*adminv1.MihomoTemplate{{Name: "ROUTER", Yaml: "# " + templateReadMarker + "\n" + templateBase, GroupIds: []string{"grp_router"}}}
	return connect.NewResponse(&adminv1.GetSubscriptionSettingsResponse{Settings: settings}), nil
}

type templateGroupService struct {
	adminv1connect.UnimplementedGroupServiceHandler
}

func (templateGroupService) ListGroups(context.Context, *connect.Request[adminv1.ListGroupsRequest]) (*connect.Response[adminv1.ListGroupsResponse], error) {
	return connect.NewResponse(&adminv1.ListGroupsResponse{Groups: []*adminv1.Group{{Id: "grp_router", Name: "ROUTER"}, {Id: "grp_pc", Name: "PC"}}}), nil
}

func TestAssistantTemplateTextStaysOutOfTheSavedHistory(t *testing.T) {
	setArgs, err := json.Marshal(map[string]any{
		"name": "PC", "yaml": "# " + templateWriteMarker + "\n" + templateBase, "group_ids": []string{"grp_pc"}, "reason": "a template for the PCs",
	})
	if err != nil {
		t.Fatal(err)
	}
	f := newAssistantFixture(t,
		modelResponse{Tools: []modelToolCall{
			scriptedTool("subscription_mihomo_template_get", "call_read", `{"name":"ROUTER"}`),
			scriptedTool("subscription_mihomo_template_set_plan", "call_write", string(setArgs)),
		}},
		modelResponse{Text: "The template is ready for you to review."},
		modelResponse{Text: `{"title":"PC template","summary":"Prepared a template."}`},
	)
	authService, err := auth.New(f.st, auth.Config{RPID: "example.com", Origins: []string{"https://example.com"}, Vault: f.vault}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.service.auth = authService
	api := http.NewServeMux()
	for _, h := range []func() (string, http.Handler){
		func() (string, http.Handler) {
			return adminv1connect.NewSubscriptionServiceHandler(templateSubscriptionService{})
		},
		func() (string, http.Handler) { return adminv1connect.NewGroupServiceHandler(templateGroupService{}) },
	} {
		path, handler := h()
		api.Handle(path, authService.RequireSession(handler))
	}
	runtime, err := mcp.NewAssistant(mcp.Config{Plans: assistantMCPPlanStore{st: f.st}, Auth: assistantTestMCPAuth{auth: authService}, API: api})
	if err != nil {
		t.Fatal(err)
	}
	f.service.SetToolRuntime(runtime)

	stream, err := f.client.Chat(context.Background(), connect.NewRequest(&adminv1.ChatRequest{Text: "Make a Mihomo template for the PC group"}))
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
	if card == nil || card.Tool != "subscription_mihomo_template_set" || card.State != adminv1.PlanState_PLAN_STATE_PROPOSED {
		t.Fatalf("no plan card for the template: %+v\n%s", card, wire.String())
	}
	cardJSON, _ := protojson.Marshal(card)
	if strings.Contains(string(cardJSON), templateWriteMarker) || strings.Contains(string(cardJSON), "include-all-proxies") {
		t.Errorf("the card carries the text: %s", cardJSON)
	}

	history, err := f.service.activeHistory(context.Background(), f.admin.ID, conversationID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var saved strings.Builder
	for _, raw := range history {
		saved.Write(raw)
	}
	for name, text := range map[string]string{"events": wire.String(), "saved history": saved.String()} {
		for _, marker := range []string{templateWriteMarker, templateReadMarker, "include-all-proxies"} {
			if strings.Contains(text, marker) {
				t.Errorf("%s keep the template text (%s)", name, marker)
			}
		}
	}
	if n := strings.Count(saved.String(), "omitted from history"); n != 2 {
		t.Errorf("the saved history marks %d omissions, want 2 (the read and the write): %s", n, saved.String())
	}
	if !strings.Contains(saved.String(), "subscription_mihomo_template_set_plan") || !strings.Contains(saved.String(), "grp_pc") {
		t.Errorf("the saved call lost its name or groups: %s", saved.String())
	}
	// the turn itself still sees both, so the model can go on
	requests := f.adapter.requests()
	if len(requests) < 2 {
		t.Fatalf("%d model requests", len(requests))
	}
	live := string(joinHistory(requests[1].History))
	if !strings.Contains(live, templateWriteMarker) || !strings.Contains(live, templateReadMarker) {
		t.Fatal("the model lost the text inside the turn")
	}
}

func TestStoredTranscriptCompactsTheTemplateInBothProviderShapes(t *testing.T) {
	args := `{"name":"PC","yaml":"TEXT-ONE\nTEXT-TWO","group_ids":["grp_pc"]}`
	anthropic := `{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"subscription_mihomo_template_set_plan","input":` + args + `}]}`
	escaped, _ := json.Marshal(args)
	openai := `{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"subscription_mihomo_template_set_plan","arguments":` + string(escaped) + `}}]}`
	for name, raw := range map[string]string{"anthropic": anthropic, "openai": openai} {
		out := string(storedTranscript(json.RawMessage(raw)))
		if strings.Contains(out, "TEXT-ONE") || !json.Valid([]byte(out)) || !strings.Contains(out, "grp_pc") || !strings.Contains(out, "omitted from history") {
			t.Errorf("%s: %s", name, out)
		}
	}
	// the arguments of the chat's tool line
	if got := string(compactBulkyArgs("subscription_mihomo_template_set_plan", []byte(args))); strings.Contains(got, "TEXT-ONE") || !strings.Contains(got, "PC") {
		t.Errorf("tool line: %s", got)
	}
	// a plan without a text (groups only) and other tools pass unchanged
	for _, c := range []struct{ tool, args string }{
		{"subscription_mihomo_template_set_plan", `{"name":"PC","group_ids":[]}`},
		{"subscription_texts_set_plan", `{"yaml":"KEEP"}`},
	} {
		if got := string(compactBulkyArgs(c.tool, []byte(c.args))); got != c.args {
			t.Errorf("%s changed: %s", c.tool, got)
		}
	}
}

func TestToolResultMessagesCutOnlyTheTemplateRead(t *testing.T) {
	calls := []modelToolCall{{id: "a", name: "subscription_mihomo_template_get"}, {id: "b", name: "user_get"}, {id: "c", name: "subscription_mihomo_template_get"}}
	outputs := []toolOutput{{text: "TEMPLATE-TEXT"}, {text: "a user"}, {text: "no such template", err: true}}
	live, stored := toolResultMessages(&scriptedAdapter{}, calls, outputs)
	joined := func(l []json.RawMessage) string { return string(joinHistory(l)) }
	if !strings.Contains(joined(live), "TEMPLATE-TEXT") {
		t.Errorf("the turn lost the text: %s", joined(live))
	}
	if s := joined(stored); strings.Contains(s, "TEMPLATE-TEXT") || !strings.Contains(s, "omitted from history") || !strings.Contains(s, "a user") || !strings.Contains(s, "no such template") {
		t.Errorf("stored: %s", s)
	}
	// nothing to cut: the same messages
	l, s := toolResultMessages(&scriptedAdapter{}, calls[1:2], outputs[1:2])
	if joined(l) != joined(s) {
		t.Errorf("a round without a template read changed: %s / %s", joined(l), joined(s))
	}
}
