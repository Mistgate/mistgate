import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { AssistantAudience, AssistantEffort, AssistantProvider } from "@/gen/mistgate/admin/v1/assistant_pb";
import { setLang } from "@/i18n";
import { AssistantTab } from "./settings";

const getAssistantSettings = vi.fn();
const updateAssistantSettings = vi.fn();
const setProviderKey = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  assistant: {
    getAssistantSettings: (...a: unknown[]) => getAssistantSettings(...a),
    updateAssistantSettings: (...a: unknown[]) => updateAssistantSettings(...a),
    setProviderKey: (...a: unknown[]) => setProviderKey(...a),
    listProviderModels: async () => ({ models: [{ id: "deepseek-v4.1-flash", name: "deepseek-v4.1-flash", contextTokens: 0n, inputUsdPerMtok: 0.3, outputUsdPerMtok: 1.2, recommended: true }] }),
  },
}));
vi.mock("@/components/step-up", () => ({
  useStepUp: () => async <T,>(call: () => Promise<T>) => call(),
  isStepUpCancelled: () => false,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  setLang("en");
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  getAssistantSettings.mockReset();
  updateAssistantSettings.mockReset();
  setProviderKey.mockReset();
});

const status = (provider: AssistantProvider, connected: boolean) => ({
  provider,
  connected,
  keyHint: connected ? "JGpA" : "",
  addedUnix: 0n,
  choice: { provider, model: "", budgetUsdMonth: 20 },
  spentUsdMonth: 0,
  lastErrorCode: "",
  lastErrorUnix: 0n,
});

async function mount(includeCustom = false) {
  // what a panel answers before the owner ever picked a provider: none stored, only Ollama Cloud connected
  getAssistantSettings.mockResolvedValue({
    settings: { enabled: true, provider: AssistantProvider.UNSPECIFIED, effort: AssistantEffort.UNSPECIFIED, audience: AssistantAudience.UNSPECIFIED, panelNotes: "", retentionDays: 90 },
    providers: [status(AssistantProvider.ANTHROPIC, false), status(AssistantProvider.DEEPSEEK, false), status(AssistantProvider.ZAI, false), status(AssistantProvider.OLLAMA_CLOUD, true), ...(includeCustom ? [status(AssistantProvider.CUSTOM_1, false)] : [])],
    usage: [],
  });
  updateAssistantSettings.mockResolvedValue({ settings: {}, providers: [] });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <AssistantTab />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));

describe("the assistant's settings", () => {
  const saveButton = () => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Save");

  it("offers Save only once something changed, then saves the provider the select shows and only the connected providers' choices", async () => {
    await mount();
    expect(saveButton()).toBeUndefined();
    const thorough = [...document.querySelectorAll("[role=radio]")].find((r) => r.textContent?.trim() === "Thorough");
    await click(thorough);
    expect(saveButton()).toBeDefined();
    await click(saveButton());
    for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
    expect(updateAssistantSettings).toHaveBeenCalledTimes(1);
    const req = updateAssistantSettings.mock.calls[0]![0] as { settings: { provider: number; effort: number; audience: number }; choices: { provider: number }[] };
    expect(req.settings.provider).toBe(AssistantProvider.OLLAMA_CLOUD);
    expect(req.settings.effort).toBe(AssistantEffort.THOROUGH);
    expect(req.settings.audience).toBe(AssistantAudience.OWNER);
    expect(req.choices.map((c) => c.provider)).toEqual([AssistantProvider.OLLAMA_CLOUD]);
  });

  it("discards changes and lists the provider in use first", async () => {
    await mount();
    const names = [...document.querySelectorAll("section b")].map((b) => b.textContent);
    expect(names[0]).toContain("Ollama Cloud");
    await click([...document.querySelectorAll("[role=radio]")].find((r) => r.textContent?.trim() === "Quick"));
    expect(saveButton()).toBeDefined();
    await click([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Discard"));
    expect(saveButton()).toBeUndefined();
  });

  it("lets the owner configure a local custom provider without a key", async () => {
    await mount(true);
    const card = [...document.querySelectorAll("section b")].find((b) => b.textContent?.includes("Custom provider 1"))?.closest("div.flex.flex-col.gap-3");
    expect(card).toBeTruthy();
    await click([...card!.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Connect"));
    const fill = async (label: string, value: string) => {
      const input = [...card!.querySelectorAll("label")].find((el) => el.textContent?.includes(label))?.parentElement?.querySelector("input") as HTMLInputElement;
      expect(input).toBeTruthy();
      await act(async () => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
        input.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    await fill("Name", "Local model");
    await fill("API address", "http://127.0.0.1:11434/v1");
    await fill("Model ID", "model-test");
    await click(card!.querySelector('input[type="checkbox"]'));
    setProviderKey.mockResolvedValue({});
    await click([...card!.querySelectorAll("button")].find((b) => b.textContent?.includes("Check and save")));
    expect(setProviderKey).toHaveBeenCalledWith(expect.objectContaining({ provider: AssistantProvider.CUSTOM_1, customName: "Local model", customModel: "model-test", allowLocal: true, apiKey: "" }));
  });
});
