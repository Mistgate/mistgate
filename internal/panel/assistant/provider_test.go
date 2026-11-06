//go:build !js

package assistant

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/mcp"
)

func testHTTP(t *testing.T, base string, roots *x509.CertPool) providerHTTP {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
	httpClient, err := newProviderHTTP(base, client)
	if err != nil {
		t.Fatal(err)
	}
	return httpClient
}

func TestOpenAIStreamToolFragmentsReasoningAndHistory(t *testing.T) {
	var calls atomic.Int32
	var secondMessages []map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer secret-key" {
			t.Errorf("unexpected request: %s %s authorization=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if calls.Add(1) == 1 {
			if body["tool_choice"] != "auto" || body["reasoning_effort"] != "low" {
				t.Errorf("provider options missing: %#v", body)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, ": keep-alive\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think \"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"carefully\",\"content\":\"Reading\"}}]}\n\n")
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_","arguments":"{\"node\":"}}]}}]}`+"\n\n")
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"node","arguments":"\"n1\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
			fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":17,\"completion_tokens\":5}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		messages, _ := body["messages"].([]any)
		for _, raw := range messages {
			if message, ok := raw.(map[string]any); ok {
				secondMessages = append(secondMessages, message)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := &openAIAdapter{kind: adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK, http: testHTTP(t, server.URL, server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs), defaultModel: "deepseek-flash"}
	tool := mcp.AssistantTool{Name: "read_node", Description: "Read a node", InputSchema: json.RawMessage(`{"type":"object","properties":{"node":{"type":"string"}},"required":["node"]}`)}
	first, err := adapter.Stream(context.Background(), "secret-key", modelRequest{Model: "deepseek-flash", Stable: "stable", System: "per admin", Effort: adminv1.AssistantEffort_ASSISTANT_EFFORT_QUICK, Tools: []mcp.AssistantTool{tool}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Text != "Reading" || first.InputTokens != 17 || first.OutputTokens != 5 || len(first.Tools) != 1 {
		t.Fatalf("unexpected first result: %+v", first)
	}
	if first.Tools[0].name != "read_node" || first.Tools[0].id != "call_1" || string(first.Tools[0].input) != `{"node":"n1"}` {
		t.Fatalf("tool fragments were not assembled: %+v", first.Tools[0])
	}
	var assistantMessage map[string]any
	if err = json.Unmarshal(first.Transcript[0], &assistantMessage); err != nil {
		t.Fatal(err)
	}
	if assistantMessage["reasoning_content"] != "think carefully" {
		t.Fatalf("reasoning content was not preserved: %#v", assistantMessage)
	}
	results := adapter.ToolResults(first.Tools, []toolOutput{{text: "node result"}})
	second, err := adapter.Stream(context.Background(), "secret-key", modelRequest{Model: "deepseek-flash", History: append(first.Transcript, results...)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Text != "done" || len(secondMessages) < 2 {
		t.Fatalf("second response or request history missing: %+v %#v", second, secondMessages)
	}
	var foundReasoning, foundToolResult bool
	for _, message := range secondMessages {
		if message["role"] == "assistant" && message["reasoning_content"] == "think carefully" {
			foundReasoning = true
		}
		if message["role"] == "tool" && message["tool_call_id"] == "call_1" {
			foundToolResult = true
		}
	}
	if !foundReasoning || !foundToolResult {
		t.Fatalf("DeepSeek history did not echo assistant reasoning and tool result: %#v", secondMessages)
	}
}

func TestOpenAIAdapterOllamaOmitsToolChoiceAndCreatesMissingID(t *testing.T) {
	var request map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":2,\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	adapter := &openAIAdapter{kind: adminv1.AssistantProvider_ASSISTANT_PROVIDER_OLLAMA_CLOUD, http: testHTTP(t, server.URL, server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs)}
	resp, err := adapter.Stream(context.Background(), "key", modelRequest{Model: "m", Tools: []mcp.AssistantTool{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := request["tool_choice"]; exists {
		t.Fatalf("Ollama request has tool_choice: %#v", request)
	}
	streamOptions, _ := request["stream_options"].(map[string]any)
	if streamOptions["include_usage"] != true {
		t.Fatalf("Ollama request did not request usage: %#v", request)
	}
	if request["max_tokens"] != float64(defaultMaxOutputTokens) {
		t.Fatalf("Ollama request did not bound output tokens: %#v", request)
	}
	if resp.UsageReported {
		t.Fatalf("stream without a usage chunk was treated as reported usage: %+v", resp)
	}
	if len(resp.Tools) != 1 || resp.Tools[0].id == "" || !strings.HasPrefix(resp.Tools[0].id, "call_") {
		t.Fatalf("missing tool call id was not generated: %+v", resp.Tools)
	}
}

func TestZAIErrorMappingAndAnthropicWireContract(t *testing.T) {
	for code, want := range map[string]string{"1000": "bad_key", "1113": "no_balance", "1302": "rate_limited", "1305": "provider_overloaded"} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request map[string]any
			_ = json.NewDecoder(r.Body).Decode(&request)
			thinking, _ := request["thinking"].(map[string]any)
			if request["reasoning_effort"] != "high" || thinking["type"] != "enabled" || thinking["clear_thinking"] != false {
				t.Errorf("Z.ai reasoning options missing: %#v", request)
			}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":{"code":%s}}`, code)
		}))
		adapter := &openAIAdapter{kind: adminv1.AssistantProvider_ASSISTANT_PROVIDER_ZAI, http: testHTTP(t, server.URL, server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs)}
		_, err := adapter.Stream(context.Background(), "key", modelRequest{Model: "glm", System: "notes"}, nil)
		server.Close()
		if got := providerCode(err); got != want {
			t.Errorf("z.ai code %s mapped to %s, want %s", code, got, want)
		}
	}

	tool := mcp.AssistantTool{Name: "read_node", Description: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}
	wire, err := anthropicWireRequest(modelRequest{Model: "claude-opus-5-5", Stable: "fixed rules", System: "per admin", Effort: adminv1.AssistantEffort_ASSISTANT_EFFORT_QUICK, Tools: []mcp.AssistantTool{tool}})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err = json.Unmarshal(wire, &body); err != nil {
		t.Fatal(err)
	}
	if body["tool_choice"].(map[string]any)["type"] != "auto" || body["fallbacks"] != "default" {
		t.Fatalf("unexpected Anthropic tool/fallback settings: %#v", body)
	}
	if _, exists := body["budget_tokens"]; exists || strings.Contains(strings.ToLower(string(wire)), "disabled") {
		t.Fatalf("Anthropic request disabled thinking: %s", wire)
	}
	if body["output_config"].(map[string]any)["effort"] != "low" || body["thinking"].(map[string]any)["type"] != "adaptive" {
		t.Fatalf("Anthropic effort/thinking missing: %#v", body)
	}
	if body["system"].([]any)[0].(map[string]any)["cache_control"] == nil {
		t.Fatalf("fixed system prefix is not cache-marked: %#v", body["system"])
	}
	toolRow := body["tools"].([]any)[0].(map[string]any)
	if toolRow["eager_input_streaming"] != true || toolRow["cache_control"] == nil {
		t.Fatalf("Anthropic tool flags missing: %#v", toolRow)
	}
}

func TestProviderRedirectCannotChangeHostOrScheme(t *testing.T) {
	var secondHits atomic.Int32
	second := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { secondHits.Add(1) }))
	defer second.Close()
	roots := x509.NewCertPool()
	roots.AddCert(second.Certificate())
	first := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+"/models", http.StatusFound)
	}))
	defer first.Close()
	roots.AddCert(first.Certificate())
	p := testHTTP(t, first.URL, roots)
	resp, err := p.request(context.Background(), http.MethodGet, "models", "key", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || secondHits.Load() != 0 {
		t.Fatalf("cross-host redirect was followed: status=%d destination hits=%d", resp.StatusCode, secondHits.Load())
	}
}

func TestProviderModelsAndResponseSizeLimit(t *testing.T) {
	var anthropicKey, deepseekKey, ollamaKey atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			if r.Header.Get("x-api-key") != "" {
				if r.Header.Get("x-api-key") != "anthropic-key" {
					anthropicKey.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"data":[{"id":"claude-opus-5-5","display_name":"Claude Opus","context_window":200000},{"id":"claude-unknown","capabilities":{"tools":false}},{"id":"claude-tool-model","capabilities":{"features":["tools"]}}]}`)
				return
			}
			if r.Header.Get("Authorization") != "Bearer ollama-key" {
				ollamaKey.Add(1)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"tool-model","supports_tools":true},{"id":"plain-model","supports_tools":false},{"id":"capability-model","capabilities":["tools"]},{"id":"deepseek-v4.1-flash"},{"id":"unknown-model"}]}`)
		case "/models":
			if r.Header.Get("Authorization") != "Bearer deepseek-key" {
				deepseekKey.Add(1)
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"deepseek-flash","context_window":1000000,"max_output_tokens":8192},{"id":"deepseek-other","context_window":64000,"max_output_tokens":4096}]}`)
		case "/large":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, strings.Repeat("x", maxProviderResponse+1))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	baseHTTP := testHTTP(t, server.URL, roots)
	anthropic := &anthropicAdapter{http: baseHTTP}
	models, err := anthropic.Check(context.Background(), "anthropic-key")
	if err != nil || len(models) != 2 || models[0].id != "claude-opus-5-5" || models[1].id != "claude-tool-model" || anthropicKey.Load() != 0 {
		t.Fatalf("Anthropic tool model list is wrong: %+v err=%v bad key headers=%d", models, err, anthropicKey.Load())
	}
	deepseek := &openAIAdapter{kind: adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK, http: baseHTTP, defaultModel: "deepseek-flash"}
	models, err = deepseek.Check(context.Background(), "deepseek-key")
	if err != nil || len(models) != 2 || models[0].context != 1_000_000 || deepseekKey.Load() != 0 {
		t.Fatalf("DeepSeek model list is wrong: %+v err=%v bad key headers=%d", models, err, deepseekKey.Load())
	}
	ollamaSpec, _ := specFor(adminv1.AssistantProvider_ASSISTANT_PROVIDER_OLLAMA_CLOUD)
	ollama := &openAIAdapter{kind: adminv1.AssistantProvider_ASSISTANT_PROVIDER_OLLAMA_CLOUD, http: baseHTTP, models: ollamaSpec.curated}
	// The adapter's fixed /models endpoint is redirected only for this fixture; use a path-prefixed base to model /v1/models.
	ollama.http.base.Path = "/v1"
	models, err = ollama.Check(context.Background(), "ollama-key")
	if err != nil || len(models) != 3 || ollamaKey.Load() != 0 {
		t.Fatalf("Ollama tool model filter is wrong: %+v err=%v bad key headers=%d", models, err, ollamaKey.Load())
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/large", nil)
	response, err := baseHTTP.do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err == nil {
		t.Fatal("provider response larger than 4 MiB was accepted")
	}
}
