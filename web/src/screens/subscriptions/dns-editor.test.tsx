import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { DnsCategory, DnsServerKind, DnsTransport } from "@/gen/mistgate/admin/v1/dns_pb";
import { useTx } from "@/screens/users/t";
import { PresetEditor } from "./dns-editor";
import type { DnsData, Preset } from "./model";

const updateDnsPreset = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  dns: { updateDnsPreset: (...a: unknown[]) => updateDnsPreset(...a), listDnsPresets: () => Promise.resolve({ presets: [], providers: [], clientSupport: [] }) },
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
  updateDnsPreset.mockReset();
});

const data: DnsData = { presets: [], providers: [], clientSupport: [] };
const server = { kind: DnsServerKind.PLAIN, address: "223.5.5.5", providerVariant: "" };
const presetWith = (split: Preset["split"]): Preset => ({
  id: "dns_custom_cn", name: "China split", description: "", builtin: false, servers: [{ ...server, address: "1.1.1.1" }], split,
  ipv4Only: false, userCount: 0, isDefault: false, splitDirect: true, preferredTransport: DnsTransport.PLAIN, category: DnsCategory.REGIONAL, country: "CN",
});

function Editor({ preset }: { preset: Preset }) {
  const t = useTx();
  return <PresetEditor open preset={preset} data={data} t={t} fallback="Default" onClose={() => {}} />;
}

async function mount(preset: Preset) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(<QueryClientProvider client={qc}><Editor preset={preset} /></QueryClientProvider>));
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
