//go:build !js

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func TestCustomProviderSaveAndToolProbe(t *testing.T) {
	f := newAssistantFixture(t)
	var authorize string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorize = r.Header.Get("Authorization")
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"data":[{"id":"another-model"}]}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"mistgate_test"}}]}}]}`)
	}))
	defer server.Close()
	p := adminv1.AssistantProvider_ASSISTANT_PROVIDER_CUSTOM_1
	resp, err := f.service.setCustomProvider(context.Background(), f.admin, &adminv1.SetProviderKeyRequest{Provider: p, CustomName: "Local", CustomUrl: server.URL + "/v1", CustomModel: "manual-model", AllowLocal: true, InputUsdPerMtok: 2, OutputUsdPerMtok: 4})
	if err != nil || !resp.Msg.Status.Connected || len(resp.Msg.Models) != 2 {
		t.Fatalf("save: %+v %v", resp, err)
	}
	if authorize != "" {
		t.Fatalf("keyless provider received authorization: %q", authorize)
	}
	row, err := f.service.readProviderRow(context.Background(), p)
	if err != nil || row.customName != "Local" || strings.Contains(string(row.sealed), "manual-model") {
		t.Fatalf("stored row: %+v %v", row, err)
	}
	if got := f.service.providerEstimatePrice(context.Background(), p, "manual-model", 1_000_000, 1_000_000); got != 6 {
		t.Fatalf("price = %v", got)
	}
	reservation, err := f.service.reserveProviderCall(context.Background(), f.admin.ID, p, "manual-model", 20, modelRequest{MaxOutputTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.W.Exec(`UPDATE assistant_provider SET input_usd_per_mtok=99,output_usd_per_mtok=99 WHERE provider=?`, int32(p)); err != nil {
		t.Fatal(err)
	}
	if got := f.service.reconcileProviderCall(context.Background(), reservation, f.admin.ID, p, "manual-model", 1_000_000, 1_000_000, true, true); got != 6 {
		t.Fatalf("price changed during call: %v", got)
	}
	if ProviderKeyAAD(p) == ProviderKeyAAD(p+1) {
		t.Fatal("custom slots share vault AAD")
	}
	short, err := f.service.setCustomProvider(context.Background(), f.admin, &adminv1.SetProviderKeyRequest{Provider: p + 1, CustomName: "Short key", CustomUrl: server.URL, CustomModel: "manual-model", ApiKey: "abc", AllowLocal: true})
	if err != nil || strings.Contains(short.Msg.Status.KeyHint, "abc") {
		t.Fatalf("short key exposed: %+v %v", short, err)
	}
}

func TestCustomProviderChatAndAccess(t *testing.T) {
	f := newAssistantFixture(t)
	var rejectedStreamOptions atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"data":[{"id":"model"}]}`)
			return
		}
		var body struct {
			Stream        bool            `json:"stream"`
			StreamOptions json.RawMessage `json:"stream_options"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Stream {
			fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"mistgate_test"}}]}}]}`)
			return
		}
		if len(body.StreamOptions) != 0 {
			rejectedStreamOptions.Store(true)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello from custom\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	p := adminv1.AssistantProvider_ASSISTANT_PROVIDER_CUSTOM_1
	_, err := f.service.setCustomProvider(context.Background(), f.admin, &adminv1.SetProviderKeyRequest{Provider: p, CustomName: "My model", CustomUrl: server.URL + "/v1", CustomModel: "model", AllowLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := f.client.UpdateAssistantSettings(context.Background(), connect.NewRequest(&adminv1.UpdateAssistantSettingsRequest{Settings: &adminv1.AssistantSettings{Enabled: true, Provider: adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK, FallbackProvider: p, Effort: adminv1.AssistantEffort_ASSISTANT_EFFORT_NORMAL, Audience: adminv1.AssistantAudience_ASSISTANT_AUDIENCE_EVERYONE}}))
	if err != nil || updated.Msg.Settings.FallbackProvider != p {
		t.Fatalf("custom fallback: %+v %v", updated, err)
	}
	if _, err = f.st.W.Exec(`UPDATE assistant_settings SET provider=? WHERE id=1`, int32(p)); err != nil {
		t.Fatal(err)
	}
	access, err := f.client.GetAssistantAccess(context.Background(), connect.NewRequest(&adminv1.GetAssistantAccessRequest{}))
	if err != nil || !access.Msg.Available || access.Msg.Choices[0].CustomName != "My model" {
		t.Fatalf("access: %+v %v", access, err)
	}
	stream, err := f.client.Chat(context.Background(), connect.NewRequest(&adminv1.ChatRequest{Text: "Hello"}))
	if err != nil {
		t.Fatal(err)
	}
	events := drainChat(t, stream)
	found := false
	for _, event := range events {
		if strings.Contains(event.GetTextDelta(), "Hello from custom") {
			found = true
		}
	}
	if !found || !rejectedStreamOptions.Load() {
		t.Fatalf("custom response missing: %+v", events)
	}
}

func TestCustomProviderRejectsMissingToolCall(t *testing.T) {
	f := newAssistantFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"hello"}}]}`)
	}))
	defer server.Close()
	p := adminv1.AssistantProvider_ASSISTANT_PROVIDER_CUSTOM_1
	_, err := f.service.setCustomProvider(context.Background(), f.admin, &adminv1.SetProviderKeyRequest{Provider: p, CustomName: "Local", CustomUrl: server.URL, CustomModel: "test", AllowLocal: true})
	if err == nil {
		t.Fatal("accepted a model that did not call the tool")
	}
	row, err := f.service.readProviderRow(context.Background(), p)
	if err != nil || len(row.sealed) != 0 {
		t.Fatalf("failed check was stored: %+v %v", row, err)
	}
}

func TestCustomProviderUsesKeyAndEndpointFromSameSnapshot(t *testing.T) {
	f := newAssistantFixture(t)
	var first, second int
	one := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first++
		fmt.Fprint(w, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"mistgate_test"}}]}}]}`)
	}))
	defer one.Close()
	two := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { second++ }))
	defer two.Close()
	p := adminv1.AssistantProvider_ASSISTANT_PROVIDER_CUSTOM_1
	req := &adminv1.SetProviderKeyRequest{Provider: p, CustomName: "Local", CustomUrl: one.URL, CustomModel: "test", ApiKey: "old-key", AllowLocal: true}
	if _, err := f.service.setCustomProvider(context.Background(), f.admin, req); err != nil {
		t.Fatal(err)
	}
	row, err := f.service.readProviderRow(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.W.Exec(`UPDATE assistant_provider SET custom_url=? WHERE provider=?`, two.URL, int32(p)); err != nil {
		t.Fatal(err)
	}
	a := f.service.adapterForRow(p, row)
	if _, err = a.Check(context.Background(), "old-key"); err != nil {
		t.Fatal(err)
	}
	if first < 2 || second != 0 {
		t.Fatalf("request went to changed endpoint: first=%d second=%d", first, second)
	}
}
