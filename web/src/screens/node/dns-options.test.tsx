import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { en } from "@/i18n/en";
import { DnsOptionsCard } from "./dns-options";

// Node → Settings → "DNS choice on the user page": what the node offers a person, in order, and the default.

const listDnsPresets = vi.fn();
const listNodeDnsOptions = vi.fn();
const setNodeDnsOptions = vi.fn();
const me = vi.fn();
vi.mock("@/lib/api", () => ({
  dns: {
    listDnsPresets: (...a: unknown[]) => listDnsPresets(...a),
    listNodeDnsOptions: (...a: unknown[]) => listNodeDnsOptions(...a),
    setNodeDnsOptions: (...a: unknown[]) => setNodeDnsOptions(...a),
  },
  auth: { me: (...a: unknown[]) => me(...a) },
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
  for (const m of [listDnsPresets, listNodeDnsOptions, setNodeDnsOptions, me]) m.mockReset();
});

const preset = (id: string, name: string) => ({ id, name, servers: [], split: [], splitDirect: false, isDefault: false });
const presets = [preset("dns_a", "Preset A"), preset("dns_b", "Preset B"), preset("dns_c", "Preset C")];

async function mount(role: number, offered: { presetIds: string[]; defaultPresetId: string } | null = { presetIds: ["dns_b"], defaultPresetId: "" }) {
  me.mockResolvedValue({ admin: { id: "adm_1", role } });
  listDnsPresets.mockResolvedValue({ presets, providers: [], clientSupport: [] });
  listNodeDnsOptions.mockResolvedValue({
    nodes: [{ nodeId: "nod_2", presetIds: ["dns_c"], defaultPresetId: "dns_c" }, ...(offered ? [{ nodeId: "nod_1", ...offered }] : [])],
  });
  setNodeDnsOptions.mockResolvedValue({});
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <DnsOptionsCard nodeId="nod_1" />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await settle();
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const input = (aria: string) => document.querySelector<HTMLInputElement>(`input[aria-label="${aria}"]`);
const labelled = (aria: string) => document.querySelector<HTMLElement>(`[aria-label="${aria}"]`);
const click = async (el: Element | null | undefined) => {
  await act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  for (let i = 0; i < 3; i++) await settle();
};
const order = () => [...document.querySelectorAll<HTMLInputElement>("input[type=checkbox]")].filter((c) => c.checked).map((c) => c.getAttribute("aria-label"));

describe("DNS choice on the user page (node)", () => {
  it("says what it is not and warns that only AmneziaWG keys get a DNS per node", async () => {
    await mount(1);
    expect(text()).toContain("DNS choice on the user page");
    expect(text()).toContain(en["subs.dnsOpt.note"]);
    expect(text()).toContain("In Happ and for Hysteria2 in Mihomo the DNS is one for all nodes — per node it works only for AmneziaWG keys.");
  });

  it("sends the offered presets in the owner's order and the default, and reads only its own node", async () => {
    await mount(1);
    expect(order()).toEqual(["Offer Preset B"]); // the other node's Preset C is not shown as offered here
    await click(input("Offer Preset A"));
    expect(order()).toEqual(["Offer Preset B", "Offer Preset A"]);
    await click(labelled("Move Preset A up"));
    expect(order()).toEqual(["Offer Preset A", "Offer Preset B"]);
    expect(labelled("Move Preset A up")?.hasAttribute("disabled")).toBe(true); // first
    await click(input("Default: Preset B"));
    await click(button("Save"));
    expect(setNodeDnsOptions).toHaveBeenCalledWith({ nodeId: "nod_1", presetIds: ["dns_a", "dns_b"], defaultPresetId: "dns_b" });
  });

  it("drops the default when its preset is no longer offered, and an empty list is a valid save", async () => {
    await mount(1, { presetIds: ["dns_b"], defaultPresetId: "dns_b" });
    expect(input("Default: Preset B")?.checked).toBe(true);
    await click(input("Offer Preset B"));
    expect(text()).toContain(en["subs.dnsOpt.nothing"]);
    await click(button("Save"));
    expect(setNodeDnsOptions).toHaveBeenCalledWith({ nodeId: "nod_1", presetIds: [], defaultPresetId: "" });
  });

  it("has nothing to save until something changes, and a node without a record offers nothing", async () => {
    await mount(1, null);
    expect(order()).toEqual([]);
    expect(button("Save")?.hasAttribute("disabled")).toBe(true);
  });

  it("is read-only for a helper: no save, no reordering, the boxes are locked", async () => {
    await mount(2);
    expect(text()).toContain(en["subs.dnsOpt.ownerOnly"]);
    expect(button("Save")).toBeUndefined();
    expect(labelled("Move Preset B up")).toBeNull();
    expect(input("Offer Preset A")?.disabled).toBe(true);
    expect(input("Default: Preset B")?.disabled).toBe(true);
    expect(text()).toContain("In Happ and for Hysteria2 in Mihomo"); // still shown
  });
});
