import { describe, expect, it } from "vitest";
import { AssistantProvider } from "@/gen/mistgate/admin/v1/assistant_pb";
import { customProviderBrand, modelBrand, providerBrand } from "./brand-icons";

describe("brand marks", () => {
  it("names the maker of the models providers list", () => {
    expect(modelBrand("deepseek-v4.1-flash")).toBe("deepseek");
    expect(modelBrand("glm-5.3-flash")).toBe("zai");
    expect(modelBrand("kimi-k3.1-pro")).toBe("kimi");
    expect(modelBrand("minimax-m3.1-flash")).toBe("minimax");
    expect(modelBrand("gemma4")).toBe("gemma");
    expect(modelBrand("qwen3-coder:480b")).toBe("qwen");
    expect(modelBrand("gpt-oss:120b")).toBe("openai");
    expect(modelBrand("meta-llama/llama-4-maverick")).toBe("meta");
    expect(modelBrand("Mistral-Large")).toBe("mistral");
    expect(modelBrand("claude-opus-5-5")).toBe("claude");
  });
  it("has no mark for a model it does not know, and one for every built-in provider", () => {
    expect(modelBrand("some-new-model")).toBeUndefined();
    expect(modelBrand("")).toBeUndefined();
    for (const p of [AssistantProvider.ANTHROPIC, AssistantProvider.DEEPSEEK, AssistantProvider.ZAI, AssistantProvider.OLLAMA_CLOUD]) expect(providerBrand[p]).toBeTruthy();
  });
  it("recognizes custom provider addresses without trusting a hostname suffix", () => {
    expect(customProviderBrand("https://openrouter.ai/api/v1")).toBe("openrouter");
    expect(customProviderBrand("https://api.groq.com/openai/v1")).toBe("groq");
    expect(customProviderBrand("http://127.0.0.1:11434/v1")).toBe("ollama");
    expect(customProviderBrand("https://groq.com.example.com")).toBeUndefined();
    expect(customProviderBrand("https://example.com:11434")).toBeUndefined();
  });
});
