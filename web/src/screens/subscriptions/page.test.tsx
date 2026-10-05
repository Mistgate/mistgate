import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { Platform } from "@/gen/mistgate/admin/v1/subscription_pb";
import { appOf, knownApps } from "./known-apps";
import { validDownload, type Settings } from "./model";
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

const tick = (ms = 10) => act(async () => void (await new Promise((r) => setTimeout(r, ms))));
/** Base UI opens a Select on the press and takes an option on the release. */
const press = (el: Element) =>
  act(async () => {
    for (const ev of ["pointerdown", "mousedown", "pointerup", "mouseup", "click"]) el.dispatchEvent(new MouseEvent(ev, { bubbles: true, button: 0 }));
  });
// a closed list may linger in the document: the options of one Select are the ones in its own listbox (id = trigger id + "-list")
const options = (label: string) => [...(document.getElementById(`${labelled(label)!.id}-list`)?.querySelectorAll("[role=option]") ?? [])];
async function open(label: string) {
  await press(labelled(label)!);
  for (let i = 0; i < 100 && options(label).length === 0; i++) await tick(10); // the popup opens asynchronously
}
/** Opens a Select by its label and takes the option with this text. */
async function choose(label: string, optionText: string) {
  await open(label);
  const option = options(label).find((o) => o.textContent?.trim() === optionText);
  expect(option, `option ${optionText}`).toBeDefined();
  await press(option!);
  for (let i = 0; i < 100 && labelled(label)?.textContent?.trim() !== optionText; i++) await tick(10); // the value lands
}
/** What a Select offers, in order (the first option is taken afterwards, which closes the list: the blank choice or the first platform). */
async function optionTexts(label: string) {
  await open(label);
  const out = options(label).map((o) => o.textContent?.trim() ?? "");
  await choose(label, out[0]!);
  return out;
}

describe("the known-apps picker", () => {
  it("fills a new card from a known app for the chosen platform, and the card stays editable", async () => {
    await mount(settingsOf({ apps: [] }));
    await choose("Platform", "Windows");
    await choose("Known app", "kl!ck");
    expect(labelled("Takes")).toBeNull(); // a known app brings its own kind
    await click(button("Add app"));
    const inputs = [...document.querySelectorAll<HTMLInputElement>("input")].map((i) => i.value);
    expect(inputs).toContain("kl!ck");
    expect(inputs).toContain("https://github.com/vbu00/klick/releases/latest");
    expect(inputs).toContain("klick://add?url={url_enc}&name={name_enc}");
    expect(text()).toContain("By subscription link");
    await type(document.querySelector<HTMLInputElement>("input[maxlength='60']")!, "kl!ck 0.5");
    await click(button("Save"));
    expect(saved().apps).toEqual([{ platform: Platform.WINDOWS, kind: App.HAPP, name: "kl!ck 0.5", downloadUrl: "https://github.com/vbu00/klick/releases/latest", addLinkTemplate: "klick://add?url={url_enc}&name={name_enc}", description: "", recommended: false }]);
  });

  it("offers the apps that exist on the platform: kl!ck on a computer, not on a phone", async () => {
    await mount(settingsOf({ apps: [] }));
    expect(await optionTexts("Known app")).toEqual(["Custom app, fill in by hand", "Happ", "AmneziaVPN", "Hiddify"]); // iOS, the platform it opens on
    await choose("Platform", "macOS");
    expect(await optionTexts("Known app")).toEqual(["Custom app, fill in by hand", "kl!ck", "Happ", "AmneziaVPN", "Clash Verge Rev", "FlClash", "Hiddify"]);
  });

  it("an AmneziaVPN pick is the key kind and has no add link; a change of platform takes the pick back to a blank card", async () => {
    await mount(settingsOf({ apps: [] }));
    await choose("Platform", "Android");
    await choose("Known app", "AmneziaVPN");
    await click(button("Add app"));
    await choose("Platform", "Windows"); // the same pick has a build here too, so it stays
    await click(button("Add app"));
    await choose("Known app", "kl!ck");
    await choose("Platform", "iOS"); // kl!ck has no iOS build: back to the blank card and its kind choice
    expect(labelled("Takes")).not.toBeNull();
    await click(button("Add app"));
    await type(document.querySelectorAll<HTMLInputElement>("input[maxlength='60']")[2]!, "Mine"); // a blank card cannot be saved
    await click(button("Save"));
    expect(saved().apps).toMatchObject([
      { platform: Platform.ANDROID, kind: App.AMNEZIA, name: "AmneziaVPN", downloadUrl: "https://play.google.com/store/apps/details?id=org.amnezia.vpn", addLinkTemplate: "" },
      { platform: Platform.WINDOWS, kind: App.AMNEZIA, name: "AmneziaVPN", downloadUrl: "https://amnezia.org/downloads", addLinkTemplate: "" },
      { platform: Platform.IOS, kind: App.HAPP, name: "Mine", downloadUrl: "", addLinkTemplate: "" },
    ]);
  });
});

describe("the known-apps catalog", () => {
  it("only holds links the panel accepts, and no unconfirmed schemes", () => {
    const placeholders = new Set(["{url}", "{url_enc}", "{name_enc}"]);
    for (const k of knownApps) {
      expect(Object.keys(k.downloads).length, k.id).toBeGreaterThan(0);
      for (const u of Object.values(k.downloads)) expect(validDownload(u) && u.startsWith("https://"), `${k.id} ${u}`).toBe(true);
      if (k.addLinkTemplate) {
        expect(k.addLinkTemplate, k.id).toMatch(/^[a-z][a-z0-9+.-]{1,31}:\/\/\S+$/);
        for (const m of k.addLinkTemplate.match(/\{[^{}]*\}/g) ?? []) expect(placeholders.has(m), `${k.id} ${m}`).toBe(true);
      }
    }
    expect(knownApps.map((k) => k.id)).not.toEqual(expect.arrayContaining(["streisand"]));
    expect(knownApps.some((k) => /^(streisand|sub|sing-box):/.test(k.addLinkTemplate))).toBe(false);
  });

  it("kl!ck is on Windows and macOS only; the defaults' Happ and AmneziaVPN links are the catalog's", () => {
    expect(Object.keys(knownApps.find((k) => k.id === "klick")!.downloads).map(Number).sort()).toEqual([Platform.WINDOWS, Platform.MACOS].sort());
    expect(appOf("happ", Platform.WINDOWS)).toMatchObject({ downloadUrl: "https://github.com/Happ-proxy/happ-desktop/releases/latest/download/setup-Happ.x64.exe", addLinkTemplate: "happ://add/{url}" });
    expect(appOf("amnezia", Platform.LINUX)).toMatchObject({ kind: App.AMNEZIA, downloadUrl: "https://amnezia.org/downloads" });
    expect(appOf("klick", Platform.IOS)).toBeNull();
  });
});