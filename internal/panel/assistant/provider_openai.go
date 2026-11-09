//go:build !js

package assistant

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

type openAIAdapter struct {
	kind         adminv1.AssistantProvider
	http         providerHTTP
	models       []modelInfo
	defaultModel string
}

func (a *openAIAdapter) Provider() adminv1.AssistantProvider { return a.kind }

func (a *openAIAdapter) Check(ctx context.Context, key string) ([]modelInfo, error) {
	if isCustomProvider(a.kind) {
		return a.checkCustom(ctx, key)
	}
	if a.kind == adminv1.AssistantProvider_ASSISTANT_PROVIDER_ZAI {
		body, _ := json.Marshal(map[string]any{
			"model": a.defaultModel, "messages": []any{map[string]any{"role": "user", "content": "ok"}},
			"max_tokens": 1,
		})
		resp, err := a.http.request(ctx, http.MethodPost, "chat/completions", key, "application/json", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, zaiStatus(resp.StatusCode, resp.Body)
		}
		if _, err = io.Copy(io.Discard, resp.Body); err != nil {
			return nil, apiError("provider_unreachable")
		}
		return append([]modelInfo(nil), a.models...), nil
	}
	resp, err := a.http.request(ctx, http.MethodGet, "models", key, "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(resp.StatusCode, resp.Body)
	}
	var payload struct {
		Data []struct {
			ID           string   `json:"id"`
			Context      int64    `json:"context_window"`
			MaxOutput    int64    `json:"max_output_tokens"`
			Capabilities []string `json:"capabilities"`
			Tools        *bool    `json:"supports_tools"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, apiError("provider_unreachable")
	}
	var models []modelInfo
	for _, row := range payload.Data {
		id := strings.TrimSpace(row.ID)
		if id == "" {
			continue
		}
		info := modelInfo{id: id, name: id, context: row.Context}
		price := priceFor(a.kind, id)
		info.inputPrice, info.outputPrice = price.input, price.output
		info.recommended = id == a.defaultModel
		if a.kind == adminv1.AssistantProvider_ASSISTANT_PROVIDER_OLLAMA_CLOUD && !ollamaModelSupportsTools(id, row.Tools, row.Capabilities, a.models) {
			continue
		}
		if a.kind == adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK {
			info.context = row.Context
		}
		models = append(models, info)
	}
	if a.kind == adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK && len(models) == 0 {
		return nil, apiError("provider_unreachable")
	}
	return models, nil
}

// checkCustom makes the model actually call a harmless tool; a successful HTTP
// response alone does not establish that a compatible endpoint supports tools.
func (a *openAIAdapter) checkCustom(ctx context.Context, key string) ([]modelInfo, error) {
	if a.defaultModel == "" {
		return nil, apiError("provider_unreachable")
	}
	body, _ := json.Marshal(map[string]any{
		"model": a.defaultModel, "messages": []any{map[string]any{"role": "user", "content": "Call the test tool."}},
		"tools":       []any{map[string]any{"type": "function", "function": map[string]any{"name": "mistgate_test", "description": "Test tool", "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}}},
		"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "mistgate_test"}},
		"max_tokens":  128,
	})
	resp, err := a.http.request(ctx, http.MethodPost, "chat/completions", key, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, statusError(resp.StatusCode, resp.Body)
	}
	var result struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil {
		return nil, apiError("provider_unreachable")
	}
	for _, choice := range result.Choices {
		for _, call := range choice.Message.ToolCalls {
			if call.Function.Name == "mistgate_test" {
				return a.customModels(ctx, key), nil
			}
		}
	}
	return nil, apiError("provider_unreachable")
}

// Discovery is optional: several compatible servers do not implement /models.
// A manually supplied model remains usable after the tool probe above.
func (a *openAIAdapter) customModels(ctx context.Context, key string) []modelInfo {
	models := []modelInfo{{id: a.defaultModel, name: a.defaultModel}}
	resp, err := a.http.request(ctx, http.MethodGet, "models", key, "", nil)
	if err != nil {
		return models
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return models
	}
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&result) != nil {
		return models
	}
	seen := map[string]bool{a.defaultModel: true}
	for _, item := range result.Data {
		id := strings.TrimSpace(item.ID)
		if id != "" && len(id) <= 256 && !seen[id] && len(models) < 500 {
			seen[id] = true
			models = append(models, modelInfo{id: id, name: id})
		}
	}
	return models
}

func ollamaModelSupportsTools(id string, supports *bool, capabilities []string, curated []modelInfo) bool {
	if supports != nil {
		return *supports
	}
	if contains(capabilities, "tools") {
		return true
	}
	for _, model := range curated {
		if id == model.id || id == model.id+":cloud" {
			return true
		}
	}
	return false
}

func (a *openAIAdapter) Stream(ctx context.Context, key string, req modelRequest, onText func(string) error) (modelResponse, error) {
	msgs := make([]json.RawMessage, 0, len(req.History))
	if req.System != "" || req.Stable != "" {
		content := strings.TrimSpace(req.Stable + "\n\n" + req.System)
		system, _ := json.Marshal(map[string]string{"role": "system", "content": content})
		msgs = append(msgs, system)
	}
	for _, raw := range req.History {
		msgs = append(msgs, json.RawMessage(raw))
	}
	tools := make([]any, 0, len(req.Tools))
	for _, tool := range req.Tools {
		var schema any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			return modelResponse{}, apiError("provider_unreachable")
		}
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
			"name": tool.Name, "description": tool.Description, "parameters": schema,
		}})
	}
	maxOutput := req.MaxOutputTokens
	if maxOutput <= 0 {
		maxOutput = defaultMaxOutputTokens
	}
	bodyMap := map[string]any{"model": req.Model, "messages": msgs, "stream": true, "max_tokens": maxOutput}
	if len(tools) > 0 {
		bodyMap["tools"] = tools
	}
	if len(tools) > 0 && a.kind != adminv1.AssistantProvider_ASSISTANT_PROVIDER_OLLAMA_CLOUD {
		bodyMap["tool_choice"] = "auto"
	}
	if a.kind == adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK || a.kind == adminv1.AssistantProvider_ASSISTANT_PROVIDER_ZAI {
		bodyMap["reasoning_effort"] = openAIReasoningEffort(a.kind, req.Effort)
	}
	if a.kind == adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK || a.kind == adminv1.AssistantProvider_ASSISTANT_PROVIDER_ZAI || a.kind == adminv1.AssistantProvider_ASSISTANT_PROVIDER_OLLAMA_CLOUD || isCustomProvider(a.kind) {
		bodyMap["stream_options"] = map[string]any{"include_usage": true}
	}
	if a.kind == adminv1.AssistantProvider_ASSISTANT_PROVIDER_ZAI {
		bodyMap["thinking"] = map[string]any{"type": "enabled", "clear_thinking": false}
	}
	body, err := json.Marshal(bodyMap)
	if err != nil {
		return modelResponse{}, apiError("provider_unreachable")
	}
	resp, err := a.http.request(ctx, http.MethodPost, "chat/completions", key, "application/json", bytes.NewReader(body))
	if err != nil {
		return modelResponse{}, err
	}
	// Some compatible servers reject stream_options. Retry once without it;
	// when usage is absent the reservation remains the conservative estimate.
	if isCustomProvider(a.kind) && resp.StatusCode == http.StatusBadRequest {
		resp.Body.Close()
		delete(bodyMap, "stream_options")
		body, _ = json.Marshal(bodyMap)
		resp, err = a.http.request(ctx, http.MethodPost, "chat/completions", key, "application/json", bytes.NewReader(body))
		if err != nil {
			return modelResponse{}, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if a.kind == adminv1.AssistantProvider_ASSISTANT_PROVIDER_ZAI {
			return modelResponse{}, zaiStatus(resp.StatusCode, resp.Body)
		}
		return modelResponse{}, statusError(resp.StatusCode, resp.Body)
	}

	var text strings.Builder
	var reasoning strings.Builder
	toolCalls := map[int]*modelToolCall{}
	toolArguments := map[int]*strings.Builder{}
	var inputTokens, outputTokens int64
	usageReported := false
	finish := ""
	done := false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), maxProviderResponse)
	var data strings.Builder
	processEvent := func(eventData string) error {
		if eventData == "" {
			return nil
		}
		if eventData == "[DONE]" {
			done = true
			return nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   any    `json:"content"`
					Reasoning string `json:"reasoning_content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				Finish *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				Prompt     int64 `json:"prompt_tokens"`
				Completion int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(eventData), &chunk); err != nil {
			return apiError("provider_unreachable")
		}
		if chunk.Usage != nil {
			inputTokens, outputTokens = chunk.Usage.Prompt, chunk.Usage.Completion
			usageReported = true
		}
		for _, choice := range chunk.Choices {
			if choice.Finish != nil {
				finish = *choice.Finish
			}
			if choice.Delta.Reasoning != "" {
				reasoning.WriteString(choice.Delta.Reasoning)
			}
			if choice.Delta.Content != nil {
				if value, ok := choice.Delta.Content.(string); ok {
					text.WriteString(value)
					if onText != nil {
						if err := onText(value); err != nil {
							return err
						}
					}
				}
			}
			for _, delta := range choice.Delta.ToolCalls {
				call := toolCalls[delta.Index]
				if call == nil {
					call = &modelToolCall{}
					toolCalls[delta.Index] = call
					toolArguments[delta.Index] = &strings.Builder{}
				}
				if delta.ID != "" {
					call.id = delta.ID
				}
				if delta.Function.Name != "" {
					call.name += delta.Function.Name
				}
				toolArguments[delta.Index].WriteString(delta.Function.Arguments)
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if data.Len() > 0 {
				if err := processEvent(data.String()); err != nil {
					return modelResponse{}, err
				}
				data.Reset()
				if done {
					break
				}
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		value, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		if data.Len() > 0 {
			data.WriteByte('\n')
		}
		data.WriteString(strings.TrimSpace(value))
	}
	if err := scanner.Err(); err != nil {
		return modelResponse{}, apiError("provider_unreachable")
	}
	if data.Len() > 0 {
		if err := processEvent(data.String()); err != nil {
			return modelResponse{}, err
		}
	}
	indices := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	calls := make([]modelToolCall, 0, len(indices))
	for _, index := range indices {
		call := toolCalls[index]
		call.input = []byte(toolArguments[index].String())
		if call.id == "" {
			call.id = store.NewID("call_")
		}
		calls = append(calls, *call)
	}
	assistant := map[string]any{"role": "assistant", "content": nil}
	if text.Len() > 0 {
		assistant["content"] = text.String()
	}
	if reasoning.Len() > 0 {
		assistant["reasoning_content"] = reasoning.String()
	}
	if len(calls) > 0 {
		wireCalls := make([]any, 0, len(calls))
		for _, call := range calls {
			wireCalls = append(wireCalls, map[string]any{"id": call.id, "type": "function", "function": map[string]string{"name": call.name, "arguments": string(call.input)}})
		}
		assistant["tool_calls"] = wireCalls
	}
	raw, err := json.Marshal(assistant)
	if err != nil {
		return modelResponse{}, apiError("provider_unreachable")
	}
	return modelResponse{Transcript: []jsonRaw{raw}, Tools: calls, Text: text.String(), Stop: finish, InputTokens: inputTokens, OutputTokens: outputTokens, UsageReported: usageReported}, nil
}

func (a *openAIAdapter) ToolResults(calls []modelToolCall, results []toolOutput) []jsonRaw {
	out := make([]jsonRaw, 0, len(calls))
	for i, call := range calls {
		result := toolOutput{err: true, text: "tool failed"}
		if i < len(results) {
			result = results[i]
		}
		b, _ := json.Marshal(map[string]any{"role": "tool", "tool_call_id": call.id, "content": result.text})
		out = append(out, b)
	}
	return out
}

func statusError(status int, body io.Reader) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return apiError("bad_key")
	}
	if status == http.StatusPaymentRequired {
		return apiError("no_balance")
	}
	return apiError(mapStatus(status))
}

func zaiStatus(status int, body io.Reader) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return apiError("bad_key")
	}
	var payload struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	b, _ := io.ReadAll(io.LimitReader(body, maxProviderResponse))
	_ = json.Unmarshal(b, &payload)
	switch payload.Error.Code {
	case 1000, 1001, 1003:
		return apiError("bad_key")
	case 1113:
		return apiError("no_balance")
	case 1302:
		return apiError("rate_limited")
	case 1305:
		return apiError("provider_overloaded")
	}
	return apiError(mapStatus(status))
}

func effortString(e adminv1.AssistantEffort) string {
	switch e {
	case adminv1.AssistantEffort_ASSISTANT_EFFORT_QUICK:
		return "low"
	case adminv1.AssistantEffort_ASSISTANT_EFFORT_THOROUGH:
		return "high"
	default:
		return "medium"
	}
}

func openAIReasoningEffort(provider adminv1.AssistantProvider, e adminv1.AssistantEffort) string {
	if provider == adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK || provider == adminv1.AssistantProvider_ASSISTANT_PROVIDER_ZAI {
		switch e {
		case adminv1.AssistantEffort_ASSISTANT_EFFORT_QUICK:
			return "low"
		case adminv1.AssistantEffort_ASSISTANT_EFFORT_THOROUGH:
			return "max"
		default:
			return "high"
		}
	}
	return effortString(e)
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
