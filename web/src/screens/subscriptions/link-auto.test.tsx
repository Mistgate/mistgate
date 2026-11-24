import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { LinkAutoCard, LinkAutoIntervalRow } from "./link-auto";
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

const settings = (linkAutoSelect: boolean | undefined, linkAutoInterval?: number) => ({ title: "", rules: [], apps: [], linkAutoSelect, linkAutoInterval }) as unknown as Settings;

async function mount(linkAutoSelect: boolean | undefined, interval?: number) {
  getSubscriptionSettings.mockResolvedValue({ settings: settings(linkAutoSelect, interval), effectiveTitle: "" });
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
          <LinkAutoIntervalRow settings={settings(linkAutoSelect, interval)} />
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
    expect(document.body.textContent).toContain("apps on Xray JSON get “Auto” first");
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

describe("Interval of the Auto checks", () => {
  const radio = (label: string) => [...document.querySelectorAll("[role=radio]")].find((e) => e.textContent === label)!;
  const savedInterval = () => (updateSubscriptionSettings.mock.calls.at(-1)?.[0] as { settings: Settings } | undefined)?.settings.linkAutoInterval;
  it("reads 5 minutes when the settings predate the choice, and saves the picked one at once", async () => {
    await mount(true);
    expect(radio("5 min").getAttribute("aria-checked")).toBe("true");
    await click(radio("1 min"));
    await settle();
    expect(savedInterval()).toBe(60);
  });

  it("shows the saved choice", async () => {
    await mount(true, 600);
    expect(radio("10 min").getAttribute("aria-checked")).toBe("true");
    expect(document.body.textContent).toContain("Check the servers every");
  });
});
