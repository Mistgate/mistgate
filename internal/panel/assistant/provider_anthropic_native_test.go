//go:build !js

package assistant

import (
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/mcp"
)

func TestAnthropicNativeStreamingPreservesThinkingAndWireSettings(t *testing.T) {
	var requestBody string
	var betaHeader string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "anthropic-test-key" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected Anthropic request: path=%q x-api-key=%q authorization=%q", r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("Authorization"))
		}
		betaHeader = r.Header.Get("anthropic-beta")
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
		}
		requestBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7}}}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"private reasoning\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"thinking-signature\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"I cannot help with that.\"}}\n\n")
		fmt.Fprint(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\"},\"usage\":{\"output_tokens\":4}}\n\n")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	adapter := &anthropicAdapter{http: testHTTP(t, server.URL, roots)}
	tool := mcp.AssistantTool{Name: "read_node", Description: "Read a node", InputSchema: []byte(`{"type":"object"}`)}
	response, err := adapter.Stream(context.Background(), "anthropic-test-key", modelRequest{
		Model: "claude-opus-5-5", Stable: "fixed rules", System: "admin memory", Effort: adminv1.AssistantEffort_ASSISTANT_EFFORT_QUICK,
		Tools: []mcp.AssistantTool{tool},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Stop != "refusal" || response.InputTokens != 7 || response.OutputTokens != 4 || response.Text != "I cannot help with that." {
		t.Fatalf("unexpected Anthropic stream result: %+v", response)
	}
	if !strings.Contains(betaHeader, "server-side-fallback-2026-07-01") {
		t.Fatalf("Opus fallback beta was not requested: %q", betaHeader)
	}
	for _, fragment := range []string{`"thinking":"private reasoning"`, `"signature":"thinking-signature"`} {
		if !strings.Contains(string(response.Transcript[0]), fragment) {
			t.Fatalf("transcript did not preserve thinking data %s: %s", fragment, response.Transcript[0])
		}
	}
	for _, forbidden := range []string{`"budget_tokens"`, `"type":"disabled"`} {
		if strings.Contains(requestBody, forbidden) {
			t.Fatalf("Anthropic request included forbidden thinking setting %s: %s", forbidden, requestBody)
		}
	}
	for _, required := range []string{`"type":"adaptive"`, `"effort":"low"`, `"type":"auto"`, `"fallbacks":"default"`, `"eager_input_streaming":true`, `"cache_control":{"type":"ephemeral"}`} {
		if !strings.Contains(requestBody, required) {
			t.Fatalf("Anthropic request omitted %s: %s", required, requestBody)
		}
	}
}
