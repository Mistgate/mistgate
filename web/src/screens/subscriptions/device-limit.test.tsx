import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { LinkDeviceLimit } from "@/gen/mistgate/admin/v1/subscription_pb";
import { setLang } from "@/i18n";
import { DeviceLimitCard } from "./device-limit";
import type { Settings } from "./model";

const getSubscriptionSettings = vi.fn();
const updateSubscriptionSettings = vi.fn();
const listUsers = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  subscriptions: { getSubscriptionSettings: (...a: unknown[]) => getSubscriptionSettings(...a), updateSubscriptionSettings: (...a: unknown[]) => updateSubscriptionSettings(...a) },
  users: { listUsers: (...a: unknown[]) => listUsers(...a) },
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
  for (const m of [getSubscriptionSettings, updateSubscriptionSettings, listUsers]) m.mockReset();
});

const settings = (mode: LinkDeviceLimit) => ({ title: "", rules: [], apps: [], linkDeviceLimit: mode }) as unknown as Settings;

/** The card with the saved mode, `shared` people on the shared place and `over` people over their limit. */
async function mount(mode: LinkDeviceLimit, { shared = 0, over = 0 } = {}) {
  getSubscriptionSettings.mockResolvedValue({ settings: settings(mode), effectiveTitle: "", sharedLinkUsers: shared });
  updateSubscriptionSettings.mockImplementation(async (r: { settings: Settings }) => ({ settings: r.settings }));
  listUsers.mockResolvedValue({ users: [], nextPageToken: "", counts: { all: 9, overDeviceLimit: over } });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <DeviceLimitCard settings={settings(mode)} />
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
const radio = (label: string) => [...document.querySelectorAll("[role=radio]")].find((r) => r.textContent?.startsWith(label))!;
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
// the confirmation, not a toast (a toast is a dialog too)
const dialog = () => [...document.querySelectorAll("[role=dialog]")].find((d) => [...d.querySelectorAll("button")].some((b) => b.textContent === "Cancel" || b.textContent === "Отмена")) ?? null;
const inDialog = (label: string) => click([...dialog()!.querySelectorAll("button")].find((b) => b.textContent?.trim() === label));
const saved = () => (updateSubscriptionSettings.mock.calls.at(-1)?.[0] as { settings: Settings } | undefined)?.settings.linkDeviceLimit;

describe("the device limit mode", () => {
  it("offers the three modes with one short line each, the saved one checked", async () => {
    await mount(LinkDeviceLimit.TRACK);
    expect([...document.querySelectorAll("[role=radio]")].map((r) => r.textContent)).toEqual([
      "Only countNobody is refused; the users list shows who is over.",
      "LimitA new device over the limit cannot connect.",
      "StrictAs “Limit”, and apps without a device ID cannot connect.",
    ]);
    expect(radio("Only count").getAttribute("aria-checked")).toBe("true");
  });

  it("saves “Only count” at once", async () => {
    await mount(LinkDeviceLimit.ENFORCE);
    await click(radio("Only count"));
    await settle();
    expect(dialog()).toBeNull();
    expect(saved()).toBe(LinkDeviceLimit.TRACK);
  });

  it("asks before “Strict” with how many people use apps without a device ID, and saves only on the confirmation", async () => {
    await mount(LinkDeviceLimit.ENFORCE, { shared: 3 });
    await click(radio("Strict"));
    await settle();
    expect(dialog()!.textContent).toContain("Switch to strict?");
    expect(dialog()!.textContent).toContain("3 people use apps without a device ID right now — they will stop connecting.");
    expect(updateSubscriptionSettings).not.toHaveBeenCalled();
    expect(radio("Limit").getAttribute("aria-checked")).toBe("true"); // nothing moved yet
    await inDialog("Cancel");
    await settle();
    expect(updateSubscriptionSettings).not.toHaveBeenCalled();

    await click(radio("Strict"));
    await settle();
    await inDialog("Switch to strict");
    await settle();
    expect(saved()).toBe(LinkDeviceLimit.STRICT);
  });

  it("says so when nobody uses apps without a device ID", async () => {
    await mount(LinkDeviceLimit.TRACK);
    await click(radio("Strict"));
    await settle();
    expect(dialog()!.textContent).toContain("Nobody uses apps without a device ID right now.");
  });

  it("asks before “Limit” while people are over their limit: they keep their devices, a new one is refused", async () => {
    await mount(LinkDeviceLimit.TRACK, { over: 2 });
    await click(radio("Limit"));
    await settle();
    expect(dialog()!.textContent).toContain("2 people are over the device limit now. They keep their current devices; a new device is refused");
    await inDialog("Turn on");
    await settle();
    expect(saved()).toBe(LinkDeviceLimit.ENFORCE);
  });

  it("saves “Limit” at once while nobody is over the limit", async () => {
    await mount(LinkDeviceLimit.TRACK);
    await click(radio("Limit"));
    await settle();
    expect(dialog()).toBeNull();
    expect(saved()).toBe(LinkDeviceLimit.ENFORCE);
    expect(text()).toContain("Saved");
  });

  it("speaks Russian with its forms", async () => {
    setLang("ru");
    await mount(LinkDeviceLimit.ENFORCE, { shared: 1 });
    await click(radio("Строго"));
    await settle();
    expect(dialog()!.textContent).toContain("1 человек сейчас пользуется приложениями без ID устройства — он перестанет подключаться.");
  });
});
