//go:build !js

package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

type anthropicAdapter struct{ http providerHTTP }

func (a *anthropicAdapter) Provider() adminv1.AssistantProvider {
	return adminv1.AssistantProvider_ASSISTANT_PROVIDER_ANTHROPIC
}

func (a *anthropicAdapter) Check(ctx context.Context, key string) ([]modelInfo, error) {
	var all []modelInfo
	page := ""
	for pages := 0; pages < 20; pages++ {
		u := *a.http.base
		u.Path = strings.TrimRight(u.Path, "/") + "/v1/models"
		q := u.Query()
		q.Set("limit", "1000")
		if page != "" {
			q.Set("after_id", page)
		}
		u.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, apiError("provider_unreachable")
		}
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("Accept", "application/json")
		resp, err := a.http.do(req)
		if err != nil {
			return nil, err
		}
		var payload struct {
			Data []struct {
				ID           string `json:"id"`
				DisplayName  string `json:"display_name"`
				Context      int64  `json:"context_window"`
				Capabilities struct {
					Tools    *bool    `json:"tools"`
					Features []string `json:"features"`
				} `json:"capabilities"`
				Tools *bool `json:"supports_tools"`
			} `json:"data"`
			HasMore bool   `json:"has_more"`
			LastID  string `json:"last_id"`
			FirstID string `json:"first_id"`
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			err := statusError(resp.StatusCode, resp.Body)
			resp.Body.Close()
			return nil, err
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			return nil, apiError("provider_unreachable")
		}
		resp.Body.Close()
		for _, item := range payload.Data {
			id := strings.TrimSpace(item.ID)
			if id == "" || !anthropicToolsSupported(id, item.Capabilities.Tools, item.Capabilities.Features, item.Tools) {
				continue
			}
			name := item.DisplayName
			if name == "" {
				name = id
			}
			p := priceFor(a.Provider(), id)
			all = append(all, modelInfo{id: id, name: name, context: item.Context, inputPrice: p.input, outputPrice: p.output, recommended: id == defaultModel(a.Provider())})
		}
		if !payload.HasMore {
			break
		}
		page = payload.LastID
		if page == "" {
			page = payload.FirstID
		}
		if page == "" {
			break
		}
	}
	if len(all) == 0 {
		return nil, apiError("provider_unreachable")
	}
	return all, nil
}

func anthropicToolsSupported(id string, capability *bool, features []string, supports *bool) bool {
	if supports != nil {
		return *supports
	}
	if capability != nil {
		return *capability
	}
	if contains(features, "tools") || contains(features, "tool_use") {
		return true
	}
	// The public model list has historically omitted the capability object. These are the three named picker models,
	// all of which are defined here as tool-capable; other models require an explicit provider capability.
	return id == "claude-opus-5-5" || id == "claude-sonnet-5-5" || id == "claude-haiku-5-5"
}

func (a *anthropicAdapter) ToolResults(calls []modelToolCall, results []toolOutput) []jsonRaw {
	blocks := make([]any, 0, len(calls))
	for i, call := range calls {
		result := toolOutput{err: true, text: "tool failed"}
		if i < len(results) {
			result = results[i]
		}
		blocks = append(blocks, map[string]any{"type": "tool_result", "tool_use_id": call.id, "content": result.text, "is_error": result.err})
	}
	if len(blocks) == 0 {
		return nil
	}
	b, _ := json.Marshal(map[string]any{"role": "user", "content": blocks})
	return []jsonRaw{b}
}

func anthropicWireRequest(req modelRequest) ([]byte, error) {
	tools := make([]map[string]any, 0, len(req.Tools))
	for i, tool := range req.Tools {
		var schema any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			return nil, err
		}
		row := map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": schema, "eager_input_streaming": true}
		if i == len(req.Tools)-1 {
			row["cache_control"] = map[string]string{"type": "ephemeral"}
		}
		tools = append(tools, row)
	}
	messages := make([]json.RawMessage, 0, len(req.History))
	for _, raw := range req.History {
		messages = append(messages, json.RawMessage(raw))
	}
	system := []any{map[string]any{"type": "text", "text": req.Stable, "cache_control": map[string]string{"type": "ephemeral"}}}
	if req.System != "" {
		system = append(system, map[string]any{"type": "text", "text": req.System})
	}
	body := map[string]any{
		"model": req.Model, "max_tokens": 8192, "messages": messages, "system": system,
		"tools": tools, "tool_choice": map[string]string{"type": "auto"}, "thinking": map[string]string{"type": "adaptive"},
		"output_config": map[string]string{"effort": effortString(req.Effort)},
	}
	if req.Model == "claude-opus-5-5" {
		body["fallbacks"] = "default"
	}
	if len(tools) == 0 {
		delete(body, "tools")
		delete(body, "tool_choice")
	}
	return json.Marshal(body)
}
