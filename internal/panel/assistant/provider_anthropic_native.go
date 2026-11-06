//go:build !js

package assistant

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

func (a *anthropicAdapter) Stream(ctx context.Context, key string, req modelRequest, onText func(string) error) (modelResponse, error) {
	wire, err := anthropicWireRequest(req)
	if err != nil {
		return modelResponse{}, apiError("provider_unreachable")
	}
	var params anthropic.BetaMessageNewParams
	if err := json.Unmarshal(wire, &params); err != nil {
		return modelResponse{}, apiError("provider_unreachable")
	}
	opts := []option.RequestOption{
		option.WithoutEnvironmentDefaults(),
		option.WithHeader("x-api-key", key),
		option.WithBaseURL(a.http.base.String()),
		option.WithHTTPClient(a.http.client),
		option.WithMaxRetries(0),
	}
	if req.Model == "claude-opus-5-5" {
		params.Betas = append(params.Betas, anthropic.AnthropicBeta("server-side-fallback-2026-07-01"))
	}
	client := anthropic.NewClient(opts...)
	stream := client.Beta.Messages.NewStreaming(ctx, params)
	defer stream.Close()
	var accumulated anthropic.BetaMessage
	for stream.Next() {
		event := stream.Current()
		if err := accumulated.Accumulate(event); err != nil {
			return modelResponse{}, apiError("provider_unreachable")
		}
		if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" && event.Delta.Text != "" && onText != nil {
			if err := onText(event.Delta.Text); err != nil {
				return modelResponse{}, err
			}
		}
	}
	if err := stream.Err(); err != nil {
		return modelResponse{}, anthropicFailure(err)
	}
	var wireResponse struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
		Text    string            `json:"-"`
		Stop    string            `json:"stop_reason"`
		Usage   *struct {
			Input  int64 `json:"input_tokens"`
			Output int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(accumulated.RawJSON()), &wireResponse); err != nil {
		return modelResponse{}, apiError("provider_unreachable")
	}
	assistant, err := json.Marshal(map[string]any{"role": "assistant", "content": wireResponse.Content})
	if err != nil {
		return modelResponse{}, apiError("provider_unreachable")
	}
	result := modelResponse{Transcript: []jsonRaw{assistant}, Stop: wireResponse.Stop, UsageReported: wireResponse.Usage != nil}
	if wireResponse.Usage != nil {
		result.InputTokens, result.OutputTokens = wireResponse.Usage.Input, wireResponse.Usage.Output
	}
	for _, raw := range wireResponse.Content {
		var block struct {
			Type  string          `json:"type"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(raw, &block) != nil || block.Type != "tool_use" {
			var textBlock struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(raw, &textBlock) == nil && textBlock.Type == "text" {
				result.Text += textBlock.Text
			}
			continue
		}
		if len(block.Input) == 0 {
			block.Input = json.RawMessage(`{}`)
		}
		result.Tools = append(result.Tools, modelToolCall{id: block.ID, name: block.Name, input: append([]byte(nil), block.Input...)})
	}
	if result.Stop == "refusal" {
		return result, nil
	}
	return result, nil
}

func anthropicFailure(err error) error {
	var api *anthropic.Error
	if errors.As(err, &api) {
		switch api.StatusCode {
		case 401, 403:
			return apiError("bad_key")
		case 429:
			return apiError("rate_limited")
		case 503, 529:
			return apiError("provider_overloaded")
		case 402:
			return apiError("no_balance")
		default:
			return apiError(mapStatus(api.StatusCode))
		}
	}
	return apiError("provider_unreachable")
}
