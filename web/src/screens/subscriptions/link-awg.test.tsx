import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { setLang } from "@/i18n";
import { LinkAwgCard } from "./link-awg";
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
  setLang("en");
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [getSubscriptionSettings, updateSubscriptionSettings]) m.mockReset();
});

const settings = (linkAwg: boolean | undefined) => ({ title: "", rules: [], apps: [], linkAwg }) as unknown as Settings;

/** The card with the stored switch (undefined: settings saved before it existed) and `keys` AmneziaWG keys held by the link apps. */
async function mount(linkAwg: boolean | undefined, keys = 0) {
  getSubscriptionSettings.mockResolvedValue({ settings: settings(linkAwg), effectiveTitle: "", linkAwgKeys: keys });
  updateSubscriptionSettings.mockImplementation(async (r: { settings: Settings }) => ({ settings: r.settings }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <LinkAwgCard settings={settings(linkAwg)} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const text = () => document.body.textContent ?? "";
const toggle = () => document.querySelector("[role=switch]")!;
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
// the confirmation, not a toast (a toast is a dialog too)
const dialog = () => [...document.querySelectorAll("[role=dialog]")].find((d) => [...d.querySelectorAll("button")].some((b) => b.textContent === "Cancel")) ?? null;
const inDialog = (label: string) => click([...dialog()!.querySelectorAll("button")].find((b) => b.textContent?.trim() === label));
const saved = () => (updateSubscriptionSettings.mock.calls.at(-1)?.[0] as { settings: Settings } | undefined)?.settings.linkAwg;

describe("AmneziaWG in apps on the link", () => {
  it("is on when the settings predate the switch, and says what the Mihomo apps get", async () => {
    await mount(undefined);
    expect(toggle().getAttribute("aria-checked")).toBe("true");
    expect(text()).toContain("kl!ck and the other Mihomo apps get the AmneziaWG servers");
    expect(text()).toContain("The Mistgate app is not affected by this switch");
  });

  it("asks before taking it out while the link apps hold keys, with how many, and saves only on confirm", async () => {
    await mount(true, 14);
    await click(toggle());
    await settle();
    expect(dialog()?.textContent).toContain("Apps on the link hold 14 AmneziaWG keys now");
    expect(dialog()?.textContent).toContain("AmneziaVPN keys are not affected");
    expect(updateSubscriptionSettings).not.toHaveBeenCalled();
    await inDialog("Take it out");
    await settle();
    expect(saved()).toBe(false);
  });

  it("takes it out at once when no app holds a key", async () => {
    await mount(true, 0);
    await click(toggle());
    await settle();
    expect(dialog()).toBeNull();
    expect(saved()).toBe(false);
  });

  it("turns back on at once and says it is off while off", async () => {
    await mount(false);
    expect(toggle().getAttribute("aria-checked")).toBe("false");
    expect(text()).toContain("Off: the apps on the link get no AmneziaWG server");
    expect(text()).toContain("The Mistgate app is not affected by this switch"); // also while it is off
    await click(toggle());
    await settle();
    expect(dialog()).toBeNull();
    expect(saved()).toBe(true);
  });
});
