//go:build js

package assistant

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func (a *anthropicAdapter) Stream(ctx context.Context, key string, req modelRequest, onText func(string) error) (modelResponse, error) {
	wire, err := anthropicWireRequest(req)
	if err != nil {
		return modelResponse{}, apiError("provider_unreachable")
	}
	var body map[string]any
	if err := json.Unmarshal(wire, &body); err != nil {
		return modelResponse{}, apiError("provider_unreachable")
	}
	body["stream"] = true
	wire, err = json.Marshal(body)
	if err != nil {
		return modelResponse{}, apiError("provider_unreachable")
	}
	u := *a.http.base
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(wire))
	if err != nil {
		return modelResponse{}, apiError("provider_unreachable")
	}
	httpReq.Header.Set("x-api-key", key)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if req.Model == "claude-opus-5-5" {
		httpReq.Header.Set("anthropic-beta", "server-side-fallback-2026-07-01")
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")
	resp, err := a.http.do(httpReq)
	if err != nil {
		return modelResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return modelResponse{}, anthropicStatus(resp.StatusCode, resp.Body)
	}
	blocks := make([]map[string]any, 0, 8)
	var inputTokens, outputTokens int64
	stop := ""
	done := false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), maxProviderResponse)
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		line := data.String()
		data.Reset()
		if line == "[DONE]" {
			done = true
			return nil
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return apiError("provider_unreachable")
		}
		typeName, _ := event["type"].(string)
		switch typeName {
		case "message_start":
			if msg, ok := event["message"].(map[string]any); ok {
				if usage, ok := msg["usage"].(map[string]any); ok {
					inputTokens = number(usage["input_tokens"])
				}
			}
		case "content_block_start":
			block, _ := event["content_block"].(map[string]any)
			blocks = append(blocks, block)
		case "content_block_delta":
			index := int(number(event["index"]))
			if index < 0 || index >= len(blocks) {
				return apiError("provider_unreachable")
			}
			delta, _ := event["delta"].(map[string]any)
			deltaType, _ := delta["type"].(string)
			switch deltaType {
			case "text_delta":
				text, _ := delta["text"].(string)
				blocks[index]["text"] = appendString(blocks[index]["text"], text)
				if text != "" && onText != nil {
					if err := onText(text); err != nil {
						return err
					}
				}
			case "input_json_delta":
				partial, _ := delta["partial_json"].(string)
				blocks[index]["_partial_json"] = appendString(blocks[index]["_partial_json"], partial)
			case "thinking_delta":
				thinking, _ := delta["thinking"].(string)
				blocks[index]["thinking"] = appendString(blocks[index]["thinking"], thinking)
			case "signature_delta":
				signature, _ := delta["signature"].(string)
				blocks[index]["signature"] = appendString(blocks[index]["signature"], signature)
			}
		case "message_delta":
			if delta, ok := event["delta"].(map[string]any); ok {
				stop, _ = delta["stop_reason"].(string)
			}
			if usage, ok := event["usage"].(map[string]any); ok {
				outputTokens = number(usage["output_tokens"])
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return modelResponse{}, err
			}
			if done {
				break
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(value))
		}
	}
	if scanner.Err() != nil {
		return modelResponse{}, apiError("provider_unreachable")
	}
	if err := flush(); err != nil {
		return modelResponse{}, err
	}
	cleanBlocks := make([]json.RawMessage, 0, len(blocks))
	response := modelResponse{Stop: stop, InputTokens: inputTokens, OutputTokens: outputTokens}
	for _, block := range blocks {
		partial, _ := block["_partial_json"].(string)
		delete(block, "_partial_json")
		if partial != "" {
			var input any
			if err := json.Unmarshal([]byte(partial), &input); err != nil {
				block["input"] = partial
			} else {
				block["input"] = input
			}
		}
		b, err := json.Marshal(block)
		if err != nil {
			return modelResponse{}, apiError("provider_unreachable")
		}
		cleanBlocks = append(cleanBlocks, b)
		if block["type"] == "tool_use" {
			id, _ := block["id"].(string)
			name, _ := block["name"].(string)
			input, _ := json.Marshal(block["input"])
			response.Tools = append(response.Tools, modelToolCall{id: id, name: name, input: input})
		} else if block["type"] == "text" {
			if text, ok := block["text"].(string); ok {
				response.Text += text
			}
		}
	}
	transcript, err := json.Marshal(map[string]any{"role": "assistant", "content": cleanBlocks})
	if err != nil {
		return modelResponse{}, apiError("provider_unreachable")
	}
	response.Transcript = []jsonRaw{transcript}
	return response, nil
}

func anthropicStatus(status int, body io.Reader) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return apiError("bad_key")
	}
	if status == http.StatusTooManyRequests {
		return apiError("rate_limited")
	}
	if status == http.StatusServiceUnavailable || status == 529 {
		return apiError("provider_overloaded")
	}
	return apiError(mapStatus(status))
}

func number(value any) int64 {
	switch n := value.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	default:
		return 0
	}
}
func appendString(old any, next string) string { value, _ := old.(string); return value + next }
