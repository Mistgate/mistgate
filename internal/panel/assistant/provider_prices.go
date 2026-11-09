//go:build !js

package assistant

import (
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

type price struct{ input, output float64 }

// Provider names, endpoint paths, defaults and model prices stay together so the provider facts that change most often
// can be corrected without touching the turn loop. Prices are USD per million tokens; DeepSeek uses peak pricing.
type providerSpec struct {
	provider     adminv1.AssistantProvider
	base         string
	defaultModel string
	curated      []modelInfo
}

var providerSpecs = []providerSpec{
	{provider: adminv1.AssistantProvider_ASSISTANT_PROVIDER_ANTHROPIC,
		base: "https://api.anthropic.com", defaultModel: "claude-opus-5-5",
		curated: []modelInfo{{id: "claude-opus-5-5", name: "Claude Opus 5.5", context: 200000, inputPrice: 4, outputPrice: 20, recommended: true},
			{id: "claude-sonnet-5-5", name: "Claude Sonnet 5.5", context: 200000, inputPrice: 2, outputPrice: 10},
			{id: "claude-haiku-5-5", name: "Claude Haiku 5.5", context: 200000, inputPrice: .10, outputPrice: .50}}},
	{provider: adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK,
		base: "https://api.deepseek.com", defaultModel: "deepseek-flash",
		curated: []modelInfo{{id: "deepseek-flash", name: "DeepSeek V4.1 Flash", context: 1000000, inputPrice: .30, outputPrice: 1.20, recommended: true},
			{id: "deepseek-v4-pro", name: "DeepSeek V4 Pro", context: 1000000, inputPrice: 1.32, outputPrice: 3.96}}},
	{provider: adminv1.AssistantProvider_ASSISTANT_PROVIDER_ZAI,
		base: "https://api.z.ai/api/paas/v4", defaultModel: "glm-5.3-flash",
		curated: []modelInfo{{id: "glm-5.3", name: "GLM-5.3", context: 1000000, inputPrice: 1.40, outputPrice: 4.40},
			{id: "glm-5.3-flash", name: "GLM-5.3 Flash", context: 1000000, inputPrice: .15, outputPrice: .50, recommended: true}}},
	{provider: adminv1.AssistantProvider_ASSISTANT_PROVIDER_OLLAMA_CLOUD,
		base: "https://ollama.com/v1", defaultModel: "deepseek-v4.1-flash",
		curated: []modelInfo{{id: "deepseek-v4.1-flash", name: "DeepSeek V4.1 Flash", context: 1000000, inputPrice: .30, outputPrice: 1.20, recommended: true},
			{id: "deepseek-v4-pro", name: "DeepSeek V4 Pro", context: 1000000, inputPrice: 1.32, outputPrice: 3.96},
			{id: "glm-5.3", name: "GLM-5.3", context: 1000000, inputPrice: 1.40, outputPrice: 4.40},
			{id: "glm-5.3-flash", name: "GLM-5.3 Flash", context: 1000000, inputPrice: .15, outputPrice: .50},
			{id: "kimi-k3.1-pro", name: "Kimi K3.1 Pro", context: 1000000, inputPrice: 2, outputPrice: 10},
			{id: "minimax-m3.1-flash", name: "MiniMax M3.1 Flash", context: 976000, inputPrice: .15, outputPrice: .60},
			{id: "gemma4", name: "Gemma 4", context: 256000, inputPrice: .14, outputPrice: .40}}},
}

func init() {
	for p := adminv1.AssistantProvider_ASSISTANT_PROVIDER_CUSTOM_1; p <= adminv1.AssistantProvider_ASSISTANT_PROVIDER_CUSTOM_5; p++ {
		providerSpecs = append(providerSpecs, providerSpec{provider: p})
	}
}

func isCustomProvider(p adminv1.AssistantProvider) bool {
	return p >= adminv1.AssistantProvider_ASSISTANT_PROVIDER_CUSTOM_1 && p <= adminv1.AssistantProvider_ASSISTANT_PROVIDER_CUSTOM_5
}

func specFor(provider adminv1.AssistantProvider) (providerSpec, bool) {
	for _, spec := range providerSpecs {
		if spec.provider == provider {
			return spec, true
		}
	}
	return providerSpec{}, false
}

func priceFor(provider adminv1.AssistantProvider, model string) price {
	spec, ok := specFor(provider)
	if !ok {
		return price{}
	}
	var highest price
	for _, known := range spec.curated {
		p := price{known.inputPrice, known.outputPrice}
		if p.input+p.output > highest.input+highest.output {
			highest = p
		}
		if known.id == model {
			return p
		}
	}
	return highest
}

func defaultModel(provider adminv1.AssistantProvider) string {
	if spec, ok := specFor(provider); ok {
		return spec.defaultModel
	}
	return ""
}
