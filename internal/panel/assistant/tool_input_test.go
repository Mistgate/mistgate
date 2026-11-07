//go:build !js

package assistant

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/mcp"
)

type journalNodeService struct {
	adminv1connect.UnimplementedNodeServiceHandler
	mu  sync.Mutex
	got *adminv1.GetNodeJournalRequest
}

func (s *journalNodeService) GetNodeJournal(_ context.Context, req *connect.Request[adminv1.GetNodeJournalRequest]) (*connect.Response[adminv1.GetNodeJournalResponse], error) {
	s.mu.Lock()
	s.got = req.Msg
	s.mu.Unlock()
	return connect.NewResponse(&adminv1.GetNodeJournalResponse{}), nil
}

type alertsHealthService struct {
	adminv1connect.UnimplementedHealthServiceHandler
	mu  sync.Mutex
	got *adminv1.ListAlertsRequest
}

func (s *alertsHealthService) ListAlerts(_ context.Context, req *connect.Request[adminv1.ListAlertsRequest]) (*connect.Response[adminv1.ListAlertsResponse], error) {
	s.mu.Lock()
	s.got = req.Msg
	s.mu.Unlock()
	return connect.NewResponse(&adminv1.ListAlertsResponse{}), nil
}

// What an OpenAI-compatible model sends (DeepSeek through Ollama Cloud on a real panel): whole numbers, and null for the
// optional arguments it does not use. The panel's own tools must run with them; v0.1.81-dev.2 rejected every call that
// had a number in it.
func TestAssistantRealMCPReadToolsTakeNumbersAndNulls(t *testing.T) {
	f := newAssistantFixture(t,
		modelResponse{Tools: []modelToolCall{
			scriptedTool("node_journal", "call_journal", `{"node":null,"hours":24,"min_level":"warning","limit":50}`),
			scriptedTool("alerts_list", "call_alerts", `{"include_history":true,"window_s":86400,"node":null}`),
		}},
		modelResponse{Text: "No node had problems today."},
		modelResponse{Text: `{"title":"Node problems","summary":"Checked the journal and alerts."}`},
	)
	authService, err := auth.New(f.st, auth.Config{RPID: "example.com", Origins: []string{"https://example.com"}, Vault: f.vault}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.service.auth = authService
	nodes, health := &journalNodeService{}, &alertsHealthService{}
	api := http.NewServeMux()
	nodePath, nodeHandler := adminv1connect.NewNodeServiceHandler(nodes)
	healthPath, healthHandler := adminv1connect.NewHealthServiceHandler(health)
	api.Handle(nodePath, authService.RequireSession(nodeHandler))
	api.Handle(healthPath, authService.RequireSession(healthHandler))
	runtime, err := mcp.NewAssistant(mcp.Config{Plans: assistantMCPPlanStore{st: f.st}, Auth: assistantTestMCPAuth{auth: authService}, API: api})
	if err != nil {
		t.Fatal(err)
	}
	f.service.SetToolRuntime(runtime)

	stream, err := f.client.Chat(context.Background(), connect.NewRequest(&adminv1.ChatRequest{Text: "Which nodes had problems today?"}))
	if err != nil {
		t.Fatal(err)
	}
	finished := map[string]*adminv1.ToolCall{}
	for _, event := range drainChat(t, stream) {
		if tool := event.GetToolFinished(); tool != nil {
			finished[tool.Tool] = tool
		}
	}
	for _, name := range []string{"node_journal", "alerts_list"} {
		if tool := finished[name]; tool == nil || !tool.Ok {
			t.Fatalf("%s did not run: %+v", name, tool)
		}
	}
	nodes.mu.Lock()
	journal := nodes.got
	nodes.mu.Unlock()
	health.mu.Lock()
	alerts := health.got
	health.mu.Unlock()
	if journal == nil || journal.Hours != 24 || journal.Limit != 50 || journal.NodeId != "" || journal.MinLevel != adminv1.EventSeverity_EVENT_SEVERITY_WARNING {
		t.Fatalf("node_journal reached the API as %+v", journal)
	}
	if alerts == nil || alerts.HistoryWindowS != 86400 || alerts.NodeId != "" {
		t.Fatalf("alerts_list reached the API as %+v", alerts)
	}
}

func TestToolInputRejectionTellsTheModelWhatToFix(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"hours":{"type":"integer"},"min_level":{"type":"string","enum":["warning","error"]},"node":{"type":"string"},"user_id":{"type":"string"}},"required":["user_id"],"additionalProperties":false}`)
	ok := map[string]string{
		`{"user_id":"usr_1","hours":24}`:               `{"hours":24,"user_id":"usr_1"}`,
		`{"user_id":"usr_1","hours":24.0}`:             `{"hours":24,"user_id":"usr_1"}`,
		`{"user_id":"usr_1","hours":2.4e1}`:            `{"hours":24,"user_id":"usr_1"}`,
		`{"user_id":"usr_1","node":null,"hours":null}`: `{"user_id":"usr_1"}`,
		` {"user_id":"usr_1","min_level":"error"} `:    `{"min_level":"error","user_id":"usr_1"}`,
		`{"user_id":"usr_1","hours":9007199254740993}`: `{"hours":9007199254740993,"user_id":"usr_1"}`,
	}
	for in, want := range ok {
		got, err := validateToolInput(schema, []byte(in))
		if err != nil || string(got) != want {
			t.Errorf("%s: got %s, %v; want %s", in, got, err, want)
		}
	}
	bad := map[string][]string{
		`{"hours":"24","user_id":"usr_1"}`:                     {"hours must be a whole number"},
		`{"user_id":"usr_1","hours":1.5}`:                      {"hours must be a whole number"},
		`{"user_id":"usr_1","min_level":"fatal"}`:              {"min_level must be one of warning, error"},
		`{"user_id":"usr_1","since":"24h"}`:                    {"since is not an argument of this tool"},
		`{"hours":24}`:                                         {"user_id is required"},
		`{"user_id":"usr_1","password: hunter2":"x"}`:          {"an argument is not one of this tool's"},
		`{"user_id":"usr_1","node":{"secret":"s3cr3t-value"}}`: {"node must be a string"},
	}
	for in, wants := range bad {
		_, err := validateToolInput(schema, []byte(in))
		if err == nil {
			t.Errorf("%s accepted", in)
			continue
		}
		for _, want := range wants {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: %q does not say %q", in, err, want)
			}
		}
		for _, secret := range []string{"hunter2", "s3cr3t", "fatal", "24h"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: the rejection repeats a value: %q", in, err)
			}
		}
	}
	for _, in := range []string{`[]`, `"x"`, `{"user_id":"usr_1"} {}`, `{"user_id":`} {
		if _, err := validateToolInput(schema, []byte(in)); err == nil {
			t.Errorf("%s accepted", in)
		}
	}
}
