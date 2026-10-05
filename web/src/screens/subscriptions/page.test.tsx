import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { Platform } from "@/gen/mistgate/admin/v1/subscription_pb";
import type { Settings } from "./model";
import { PageTab } from "./page";

const updateSubscriptionSettings = vi.fn();
const getSubscriptionSettings = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  subscriptions: {
    getSubscriptionSettings: (...a: unknown[]) => getSubscriptionSettings(...a),
    updateSubscriptionSettings: (...a: unknown[]) => updateSubscriptionSettings(...a),
  },
  users: { listUsers: () => Promise.resolve({ users: [] }) },
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  window.matchMedia = ((q: string) => ({ matches: true, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, onchange: null, dispatchEvent: () => false })) as never;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  updateSubscriptionSettings.mockReset();
  getSubscriptionSettings.mockReset();
});

const app = (platform: Platform, kind: App, name: string, extra: Partial<Settings["apps"][number]> = {}) => ({ platform, kind, name, downloadUrl: "https://example.com/" + name, addLinkTemplate: "", description: "", recommended: false, ...extra });

const settingsOf = (over: Partial<Settings> = {}): Settings =>
  ({
    title: "",
    announcement: "",
    supportUrl: "",
    updateIntervalHours: 12,
    serverNameTemplate: "{flag} {node}",
    rules: [],
    defaultDnsPresetId: "",
    apps: [app(Platform.WINDOWS, App.HAPP, "First"), app(Platform.IOS, App.HAPP, "Phone"), app(Platform.WINDOWS, App.HAPP, "Second"), app(Platform.WINDOWS, App.AMNEZIA, "Third")],
    userPage: { showAnnouncement: true, showSupport: true, showQr: true, allowDeviceSelfService: true },
    ...over,
  }) as Settings;

async function mount(settings: Settings) {
  getSubscriptionSettings.mockResolvedValue({ settings, effectiveTitle: "" });
  updateSubscriptionSettings.mockImplementation(async (r: { settings: unknown }) => ({ settings: r.settings }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(<QueryClientProvider client={qc}><PageTab settings={settings} /></QueryClientProvider>));
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const labelled = (aria: string) => document.querySelector<HTMLElement>(`[aria-label="${aria}"]`);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const switchOf = (label: string) => [...document.querySelectorAll("label")].find((l) => l.textContent?.includes(label))?.querySelector<HTMLElement>("[role=switch]");
const type = (el: HTMLInputElement, v: string) =>
  act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, v);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
const names = () => [...document.querySelectorAll<HTMLInputElement>("input[maxlength='60']")].map((i) => i.value);
const saved = () => (updateSubscriptionSettings.mock.calls[0]![0] as { settings: Settings }).settings;

describe("the password setting", () => {
  it("is on for a document that has no such key (the upgrade) and saves an explicit off", async () => {
    await mount(settingsOf());
    const sw = switchOf("Password on the page");
    expect(sw?.getAttribute("aria-checked")).toBe("true");
    expect(text()).toContain("Apps still fetch the subscription without one");
    await click(sw);
    await click(button("Save"));
    expect(saved().userPage?.requirePagePassword).toBe(false);
    expect(saved().userPage?.allowDeviceSelfService).toBe(true); // the other options go along unchanged
  });

  it("an explicit off reads as off", async () => {
    await mount(settingsOf({ userPage: { showAnnouncement: true, showSupport: true, showQr: true, allowDeviceSelfService: true, requirePagePassword: false } as Settings["userPage"] }));
    expect(switchOf("Password on the page")?.getAttribute("aria-checked")).toBe("false");
  });
});

describe("the DNS choice setting", () => {
  it("is off for a document that has no such key (the upgrade) and saves an explicit on without losing the others", async () => {
    await mount(settingsOf());
    const sw = switchOf("DNS choice on the page");
    expect(sw?.getAttribute("aria-checked")).toBe("false");
    expect(text()).toContain("keeps the choices already made");
    await click(sw);
    await click(button("Save"));
    expect(saved().userPage?.allowDnsChoice).toBe(true);
    expect(saved().userPage?.allowDeviceSelfService).toBe(true);
    expect(saved().userPage?.requirePagePassword).toBe(true);
  });

  it("an explicit on reads as on, an unchanged form has nothing to save, and off is saved as off", async () => {
    await mount(settingsOf({ userPage: { showAnnouncement: true, showSupport: true, showQr: true, allowDeviceSelfService: true, allowDnsChoice: true } as Settings["userPage"] }));
    const sw = switchOf("DNS choice on the page");
    expect(sw?.getAttribute("aria-checked")).toBe("true");
    expect(button("Save")).toBeUndefined();
    await click(sw);
    await click(button("Save"));
    expect(saved().userPage?.allowDnsChoice).toBe(false);
  });
});

describe("the apps list", () => {
  it("names the kinds by what they do, not by an app", async () => {
    await mount(settingsOf());
    expect(text()).toContain("By subscription link");
    expect(text()).toContain("AmneziaWG key");
  });

  it("a description and the recommended mark are saved, the description trimmed", async () => {
    await mount(settingsOf());
    const desc = document.querySelectorAll<HTMLInputElement>("input[maxlength='80']");
    expect(desc).toHaveLength(4);
    expect(desc[0]!.maxLength).toBe(80);
    await type(desc[0]!, "  Two protocols in one app  ");
    expect(text()).toContain("28/80");
    await click([...document.querySelectorAll("[role=switch]")].find((s) => s.closest("label")?.textContent?.includes("Recommended")));
    await click(button("Save"));
    const a = saved().apps[0]!;
    expect(a).toMatchObject({ name: "First", description: "Two protocols in one app", recommended: true });
    expect(saved().apps[2]).toMatchObject({ name: "Second", description: "", recommended: false });
  });

  it("an app moves among the apps of its own platform only", async () => {
    await mount(settingsOf());
    expect(names()).toEqual(["First", "Phone", "Second", "Third"]);
    expect(labelled("Move First up")?.hasAttribute("disabled")).toBe(true); // first of its platform
    expect(labelled("Move Phone down")?.hasAttribute("disabled")).toBe(true); // alone on its platform
    await click(labelled("Move First down")); // past the phone app, to the place of the next Windows one
    expect(names()).toEqual(["Second", "Phone", "First", "Third"]);
    await click(labelled("Move Third up"));
    expect(names()).toEqual(["Second", "Phone", "Third", "First"]);
    await click(button("Save"));
    expect(saved().apps.map((x) => x.name)).toEqual(["Second", "Phone", "Third", "First"]);
  });

  it("several apps of one kind on one platform are fine, and a new app starts without description or badge", async () => {
    await mount(settingsOf({ apps: [] }));
    await click(button("Add app"));
    await click(button("Add app"));
    expect(document.querySelectorAll("input[maxlength='60']")).toHaveLength(2);
    expect(text()).toContain("0/80");
  });
});
