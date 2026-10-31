import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { DnsCategory, DnsServerKind, DnsTransport } from "@/gen/mistgate/admin/v1/dns_pb";
import { ToastProvider } from "@/components/ui/toast";
import { useTx } from "@/screens/users/t";
import { DnsTab } from "./dns";
import { PresetEditor } from "./dns-editor";
import type { DnsData, Preset } from "./model";

const updateDnsPreset = vi.fn();
const createDnsPreset = vi.fn();
const listDnsPresets = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  dns: {
    updateDnsPreset: (...a: unknown[]) => updateDnsPreset(...a),
    createDnsPreset: (...a: unknown[]) => createDnsPreset(...a),
    listDnsPresets: (...a: unknown[]) => listDnsPresets(...a),
  },
  subscriptions: { getSubscriptionSettings: () => Promise.resolve({ settings: { apps: [] }, effectiveTitle: "" }) },
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
  for (const m of [updateDnsPreset, createDnsPreset, listDnsPresets]) m.mockReset();
});

const data: DnsData = { presets: [], providers: [], clientSupport: [] };
const server = { kind: DnsServerKind.PLAIN, address: "223.5.5.5", providerVariant: "" };
const presetWith = (split: Preset["split"]): Preset => ({
  id: "dns_custom_cn", name: "China split", description: "", builtin: false, servers: [{ ...server, address: "1.1.1.1" }], split,
  ipv4Only: false, userCount: 0, isDefault: false, splitDirect: true, preferredTransport: DnsTransport.PLAIN, category: DnsCategory.REGIONAL, country: "CN", blockAds: false,
});

function Editor({ preset }: { preset?: Preset }) {
  const t = useTx();
  return <PresetEditor open preset={preset} data={data} t={t} fallback="Default" onClose={() => {}} />;
}

async function mount(preset?: Preset, screen = <Editor preset={preset} />, list: Preset[] = []) {
  act(() => root?.unmount()); // a second mount in one test replaces the first
  host?.remove();
  listDnsPresets.mockResolvedValue({ presets: list, providers: [], clientSupport: [] });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(<QueryClientProvider client={qc}><ToastProvider>{screen}</ToastProvider></QueryClientProvider>));
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const china = () => document.querySelector<HTMLButtonElement>('[role=group][aria-label="Rule 1 · All sites of a country"] button');
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const alertText = () => document.querySelector("[role=alert]")?.textContent ?? "";

describe("the country lists of a split rule", () => {
  it("shows a rule of China alone with the toggle on and no error for its empty suffixes", async () => {
    await mount(presetWith([{ suffixes: [], geo: ["cn"], servers: [server] }]));
    expect(china()?.textContent).toContain("China");
    expect(china()?.getAttribute("aria-pressed")).toBe("true");
    expect(document.querySelector("[aria-invalid=true]")).toBeNull();
    expect(alertText()).toBe("");

    // off: the rule has neither, the bar says so; on again: back where it started, nothing to save
    await click(china());
    expect(china()?.getAttribute("aria-pressed")).toBe("false");
    expect(alertText()).toBe("A split rule needs a suffix or a country, and a server.");
    await click(china());
    expect(china()?.getAttribute("aria-pressed")).toBe("true");
    expect(alertText()).toBe("");
    expect(button("Save")?.disabled).toBe(true);
  });

  it("saves the country next to the suffixes", async () => {
    updateDnsPreset.mockResolvedValue({});
    await mount(presetWith([{ suffixes: [".cn"], geo: [], servers: [server] }]));
    expect(china()?.getAttribute("aria-pressed")).toBe("false");
    await click(china());
    await click(button("Save"));
    expect(updateDnsPreset).toHaveBeenCalledTimes(1);
    const sent = updateDnsPreset.mock.calls[0]![0] as { split: { suffixes: string[]; geo: string[] }[] };
    expect(sent.split).toEqual([{ suffixes: [".cn"], geo: ["cn"], servers: [{ kind: DnsServerKind.PLAIN, address: "223.5.5.5" }] }]);
  });

  it("keeps a country set through the API that the editor does not offer, and can turn it off", async () => {
    await mount(presetWith([{ suffixes: [], geo: ["ir"], servers: [server] }]));
    const pills = [...document.querySelectorAll('[role=group][aria-label="Rule 1 · All sites of a country"] button')];
    expect(pills.map((b) => [b.textContent, b.getAttribute("aria-pressed")])).toEqual([
      ["China", "false"],
      ["Iran", "true"],
    ]);
  });
});

describe("blocking ads in the apps", () => {
  const ads = () => [...document.querySelectorAll("label")].find((l) => l.textContent?.includes("Block ads and trackers in the apps"))!.querySelector<HTMLElement>("[role=switch]")!;
  const sent = () => updateDnsPreset.mock.calls[0]![0] as { blockAds?: boolean; ipv4Only: boolean };
  const type = (selector: string, value: string) =>
    act(async () => {
      const el = document.querySelector<HTMLInputElement>(selector)!;
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });

  it("shows the switch with its hint, off for a preset without the flag, and saves it turned on", async () => {
    updateDnsPreset.mockResolvedValue({});
    await mount(presetWith([]));
    expect(document.body.textContent).toContain("The apps drop the domains of the community list category-ads-all themselves");
    expect(ads().getAttribute("aria-checked")).toBe("false");
    await click(ads());
    expect(ads().getAttribute("aria-checked")).toBe("true");
    await click(button("Save"));
    expect(sent().blockAds).toBe(true);
  });

  it("turns it off on a preset that has it", async () => {
    updateDnsPreset.mockResolvedValue({});
    await mount({ ...presetWith([]), blockAds: true });
    expect(ads().getAttribute("aria-checked")).toBe("true");
    await click(ads());
    await click(button("Save"));
    expect(sent().blockAds).toBe(false);
  });

  it("leaves it out of an update that did not touch it, so the panel keeps what is stored", async () => {
    updateDnsPreset.mockResolvedValue({});
    await mount({ ...presetWith([]), blockAds: true });
    await click([...document.querySelectorAll("label")].find((l) => l.textContent?.includes("IPv4 only"))!.querySelector("[role=switch]"));
    await click(button("Save"));
    expect(sent().ipv4Only).toBe(true);
    expect(sent().blockAds).toBeUndefined();
    // on and off again is no change at all
    updateDnsPreset.mockClear();
    await mount(presetWith([]));
    await click(ads());
    await click(ads());
    expect(button("Save")?.disabled).toBe(true);
  });

  it("sends it with a new preset, on or off", async () => {
    createDnsPreset.mockResolvedValue({});
    await mount();
    await type("#dns-name", "Office");
    await click(button("+ Custom server"));
    await type('input[aria-label="Address of server 1"]', "9.9.9.9");
    await click(button("Create"));
    expect(createDnsPreset.mock.calls[0]![0]).toMatchObject({ name: "Office", blockAds: false });
    await mount();
    await type("#dns-name", "No ads");
    await click(button("+ Custom server"));
    await type('input[aria-label="Address of server 1"]', "9.9.9.9");
    await click(ads());
    await click(button("Create"));
    expect(createDnsPreset.mock.calls[1]![0]).toMatchObject({ name: "No ads", blockAds: true });
  });

  it("marks the presets that block ads in the list", async () => {
    await mount(undefined, <DnsTab />, [
      { ...presetWith([]), id: "dns_builtin_ads_device", name: "Без рекламы на устройстве", builtin: true, blockAds: true, category: DnsCategory.NO_ADS, country: "" },
      { ...presetWith([]), id: "dns_plain", name: "Plain", blockAds: false, category: DnsCategory.REGULAR, country: "" },
    ]);
    const pills = (name: string) => {
      const card = [...document.querySelectorAll("button")].find((b) => b.querySelector("b")?.textContent === name);
      expect(card).toBeDefined();
      return [...card!.querySelectorAll("span")].map((s) => s.textContent);
    };
    expect(pills("No ads on the device")).toContain("no ads");
    expect(pills("Plain")).not.toContain("no ads");
  });
});
