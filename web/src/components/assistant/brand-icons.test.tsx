import { describe, expect, it } from "vitest";
import { AssistantProvider } from "@/gen/mistgate/admin/v1/assistant_pb";
import { customProviderBrand, providerBrand } from "./brand-icons";

describe("brand marks", () => {
  it("has a mark for every built-in provider", () => {
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
