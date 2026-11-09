import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { LinkAutoCard } from "./link-auto";
import type { Settings } from "./model";

const getSubscriptionSettings = vi.fn();
const updateSubscriptionSettings = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  subscriptions: { getSubscriptionSettings: (...a: unknown[]) => getSubscriptionSettings(...a), updateSubscriptionSettings: (...a: unknown[]) => updateSubscriptionSettings(...a) },
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [getSubscriptionSettings, updateSubscriptionSettings]) m.mockReset();
});

const settings = (linkAutoSelect: boolean | undefined) => ({ title: "", rules: [], apps: [], linkAutoSelect }) as unknown as Settings;

async function mount(linkAutoSelect: boolean | undefined) {
  getSubscriptionSettings.mockResolvedValue({ settings: settings(linkAutoSelect), effectiveTitle: "" });
  updateSubscriptionSettings.mockImplementation(async (r: { settings: Settings }) => ({ settings: r.settings }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <LinkAutoCard settings={settings(linkAutoSelect)} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const toggle = () => document.querySelector("[role=switch]")!;
const click = (b: Element) => act(async () => void b.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const saved = () => (updateSubscriptionSettings.mock.calls.at(-1)?.[0] as { settings: Settings } | undefined)?.settings.linkAutoSelect;

describe("Automatic server choice", () => {
  it("is on when the settings predate the switch, and turns off at once", async () => {
    await mount(undefined);
    expect(toggle().getAttribute("aria-checked")).toBe("true");
    expect(document.body.textContent).toContain("an “Auto” group first");
    await click(toggle());
    await settle();
    expect(saved()).toBe(false);
  });

  it("says it is off while off, and turns back on", async () => {
    await mount(false);
    expect(toggle().getAttribute("aria-checked")).toBe("false");
    expect(document.body.textContent).toContain("Off: the apps get the plain list of servers");
    await click(toggle());
    await settle();
    expect(saved()).toBe(true);
  });
});
