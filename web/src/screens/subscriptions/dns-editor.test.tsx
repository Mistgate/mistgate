import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { DnsCategory, DnsClient, DnsServerKind, DnsTransport } from "@/gen/mistgate/admin/v1/dns_pb";
import { ToastProvider } from "@/components/ui/toast";
import { meQuery } from "@/lib/session";
import { useTx } from "@/screens/users/t";
import { DnsTab } from "./dns";
import { PresetEditor } from "./dns-editor";
import type { DnsData, Preset } from "./model";
import { LeaveGuard } from "./guard";

const updateDnsPreset = vi.fn();
const createDnsPreset = vi.fn();
const deleteDnsPreset = vi.fn();
const listDnsPresets = vi.fn();
const updateSubscriptionSettings = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  dns: {
    updateDnsPreset: (...a: unknown[]) => updateDnsPreset(...a),
    createDnsPreset: (...a: unknown[]) => createDnsPreset(...a),
    deleteDnsPreset: (...a: unknown[]) => deleteDnsPreset(...a),
    listDnsPresets: (...a: unknown[]) => listDnsPresets(...a),
  },
  subscriptions: {
    getSubscriptionSettings: () => Promise.resolve({ settings: { apps: [{ kind: 1, name: "Happ" }], defaultDnsPresetId: "" }, effectiveTitle: "" }),
    updateSubscriptionSettings: (...a: unknown[]) => updateSubscriptionSettings(...a),
  },
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
  for (const m of [updateDnsPreset, createDnsPreset, deleteDnsPreset, listDnsPresets, updateSubscriptionSettings]) m.mockReset();
});

const data: DnsData = { presets: [], providers: [], clientSupport: [] };
const server = { kind: DnsServerKind.PLAIN, address: "223.5.5.5", providerVariant: "" };
const presetWith = (split: Preset["split"]): Preset => ({
  id: "dns_custom_cn", name: "China split", description: "", builtin: false, servers: [{ ...server, address: "1.1.1.1" }], split,
  ipv4Only: false, userCount: 0, isDefault: false, splitDirect: true, preferredTransport: DnsTransport.PLAIN, category: DnsCategory.REGIONAL, country: "CN", blockAds: false,
});

function Editor({ preset, onClose = () => {}, with: d = data }: { preset?: Preset; onClose?: () => void; with?: DnsData }) {
  const t = useTx();
  return <PresetEditor preset={preset} data={d} t={t} fallback="Default" onClose={onClose} />;
}

type Opts = { list?: Preset[]; role?: Role; providers?: DnsData["providers"]; guard?: (d: boolean) => void };
async function mount(preset?: Preset, screen = <Editor preset={preset} />, o: Opts = {}) {
  act(() => root?.unmount()); // a second mount in one test replaces the first
  host?.remove();
  const list = o.list ?? [];
  listDnsPresets.mockImplementation(() => Promise.resolve({ presets: list, providers: o.providers ?? [], clientSupport: [] }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role: o.role ?? Role.OWNER } } as never);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <LeaveGuard.Provider value={o.guard ?? (() => {})}>
          <ToastProvider>{screen}</ToastProvider>
        </LeaveGuard.Provider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async (n = 3) => {
  for (let i = 0; i < n; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const china = () => document.querySelector<HTMLButtonElement>('[role=group][aria-label="Rule 1 · All sites of a country"] button');
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const alertText = () => document.querySelector("[role=alert]")?.textContent ?? "";
const type = (selector: string, value: string) =>
  act(async () => {
    const el = document.querySelector<HTMLInputElement>(selector)!;
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
/** The switch of the option with this title (the board's .sb-tg). */
const option = (title: string) => [...document.querySelectorAll(".sb-tg")].find((l) => l.querySelector("b")?.textContent === title)!.querySelector<HTMLElement>("[role=switch]")!;

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
      ["Russia", "false"],
      ["Iran", "true"],
    ]);
  });

  it("takes a suffix typed in a rule on Enter and shows the chip", async () => {
    await mount(presetWith([{ suffixes: [".cn"], geo: [], servers: [server] }]));
    const box = 'input[aria-label="Rule 1 · Domain ending"]';
    await type(box, "com.cn");
    await act(async () => void document.querySelector(box)!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    expect([...document.querySelectorAll(".sb-chips > span")].map((c) => c.textContent)).toEqual([".cn", "com.cn"]);
  });
});

describe("blocking ads in the apps", () => {
  const ads = () => option("Block ads and trackers in the apps");
  const sent = () => updateDnsPreset.mock.calls[0]![0] as { blockAds?: boolean; ipv4Only: boolean };

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
    await click(option("IPv4 only"));
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
    await click(button("Custom server"));
    await type('input[aria-label="Address of server 1"]', "9.9.9.9");
    await click(button("Create"));
    expect(createDnsPreset.mock.calls[0]![0]).toMatchObject({ name: "Office", blockAds: false });
    await mount();
    await type("#dns-name", "No ads");
    await click(button("Custom server"));
    await type('input[aria-label="Address of server 1"]', "9.9.9.9");
    await click(ads());
    await click(button("Create"));
    expect(createDnsPreset.mock.calls[1]![0]).toMatchObject({ name: "No ads", blockAds: true });
  });

  it("marks the presets that block ads in the list", async () => {
    await mount(undefined, <DnsTab />, {
      list: [
        { ...presetWith([]), id: "dns_builtin_ads_device", name: "Без рекламы на устройстве", builtin: true, blockAds: true, category: DnsCategory.NO_ADS, country: "" },
        { ...presetWith([]), id: "dns_plain", name: "Plain", blockAds: false, category: DnsCategory.REGULAR, country: "" },
      ],
    });
    expect(tagsOf("No ads on the device")).toContain("no ads");
    expect(tagsOf("Plain")).not.toContain("no ads");
  });
});

/** The name of a row's preset and the small tags after it. */
const nameOf = (row: Element) => row.querySelector(".dq-n > b")!.childNodes[0]!.textContent;
const rows = () => [...document.querySelectorAll(".dq-list .dq-r:not(.dq-rh)")];
const rowOf = (name: string) => rows().find((r) => nameOf(r) === name)!;
const tagsOf = (name: string) => [...rowOf(name).querySelectorAll(".dq-n > b > i")].map((i) => i.textContent);
const dot = (name: string) => rowOf(name).querySelector<HTMLButtonElement>(".dq-def")!;

const cf = { providerVariant: "cloudflare/standard", kind: DnsServerKind.PLAIN, address: "1.1.1.1" };
const pr = (over: Partial<Preset> & { id: string; name: string }): Preset => ({ ...presetWith([]), country: "", category: DnsCategory.REGULAR, servers: [cf], userCount: 0, ...over });
const providers = [
  {
    id: "cloudflare",
    name: "Cloudflare",
    variants: [
      { id: "cloudflare/standard", nameRu: "Обычный", nameEn: "Standard", category: DnsCategory.REGULAR, country: "", ipv4: ["1.1.1.1", "1.0.0.1"], ipv6: [], plain: true, dotHost: "one.one.one.one", dotPort: 853, dohUrl: "https://dns.example.com/dns-query", noteRu: "Быстрый", noteEn: "Fast, no filtering" },
      { id: "cloudflare/family", nameRu: "Семейный", nameEn: "Family", category: DnsCategory.FAMILY, country: "", ipv4: ["1.1.1.3"], ipv6: [], plain: true, dotHost: "", dotPort: 0, dohUrl: "", noteRu: "Без 18+", noteEn: "No adult sites" },
    ],
  },
] as unknown as DnsData["providers"];

describe("the list of presets", () => {
  const list = [
    pr({ id: "a", name: "Alpha", isDefault: true, builtin: true, description: "First one", userCount: 5 }),
    pr({ id: "b", name: "Beta", userCount: 2, splitDirect: true, split: [{ suffixes: [".ru"], geo: [], servers: [cf] }], preferredTransport: DnsTransport.DOH }),
    pr({ id: "c", name: "Gamma", userCount: 0, category: DnsCategory.NO_ADS }),
  ];

  it("draws the default on top and a row for every preset: servers, how they are asked, who has it", async () => {
    await mount(undefined, <DnsTab />, { list, providers });
    expect(document.querySelector(".dq-now")!.textContent).toContain("Alpha");
    expect(document.querySelector(".dq-now")!.textContent).toContain("Link-based apps (Happ) — at the next subscription refresh");
    expect(rows().map(nameOf)).toEqual(["Alpha", "Beta", "Gamma"]);
    // the default says "all", the others how many have it (or that nobody does)
    expect(rowOf("Alpha").querySelector(".dq-u")!.textContent).toBe("alldefault");
    expect(rowOf("Beta").querySelector(".dq-u")!.textContent).toBe("2assignments");
    expect(rowOf("Gamma").querySelector(".dq-u")!.textContent).toBe("—not assigned");
    // the servers by their catalog names, the transport and what goes direct
    expect(rowOf("Beta").querySelector(".dq-s .mono")!.textContent).toBe("Cloudflare · Standard");
    expect([...rowOf("Beta").querySelectorAll(".dq-m i")].map((i) => i.textContent)).toEqual(["DoH", ".ru direct"]);
    expect(dot("Alpha").getAttribute("aria-checked")).toBe("true");
    expect(dot("Beta").getAttribute("aria-checked")).toBe("false");
  });

  it("filters by the category chip and keeps the count of the whole list in the head", async () => {
    await mount(undefined, <DnsTab />, { list, providers });
    expect(document.querySelector(".dq-h .mono")!.textContent).toBe("3");
    const chip = (label: string) => [...document.querySelectorAll('.dq-cat [role="radio"]')].find((c) => c.textContent?.startsWith(label));
    expect(chip("No ads")!.textContent).toBe("No ads1");
    await click(chip("No ads"));
    expect(rows().map(nameOf)).toEqual(["Gamma"]);
    expect(document.querySelector(".dq-h .mono")!.textContent).toBe("3");
    await click(chip("All"));
    expect(rows()).toHaveLength(3);
  });

  it("makes a preset the default with one click, says so with Undo, and Undo puts the old one back", async () => {
    let def = "a";
    listDnsPresets.mockImplementation(() => Promise.resolve({ presets: list.map((p) => ({ ...p, isDefault: p.id === def })), providers, clientSupport: [] }));
    updateSubscriptionSettings.mockImplementation((r: { settings: { defaultDnsPresetId: string } }) => {
      def = r.settings.defaultDnsPresetId;
      return Promise.resolve({ settings: r.settings });
    });
    await mount(undefined, <DnsTab />, { list, providers });
    listDnsPresets.mockImplementation(() => Promise.resolve({ presets: list.map((p) => ({ ...p, isDefault: p.id === def })), providers, clientSupport: [] }));
    await click(dot("Gamma"));
    await settle(6);
    expect(updateSubscriptionSettings.mock.calls[0]![0].settings.defaultDnsPresetId).toBe("c");
    expect(dot("Gamma").getAttribute("aria-checked")).toBe("true");
    expect(dot("Alpha").getAttribute("aria-checked")).toBe("false");
    expect(document.querySelector(".dq-now")!.textContent).toContain("Gamma");
    expect(document.body.textContent).toContain("Gamma is now the default for everyone");
    expect(rowOf("Gamma").className).toContain("just");
    // the same dot again is no change
    await click(dot("Gamma"));
    expect(updateSubscriptionSettings).toHaveBeenCalledTimes(1);
    await click(button("Undo"));
    await settle(6);
    expect(updateSubscriptionSettings.mock.calls[1]![0].settings.defaultDnsPresetId).toBe("a");
    expect(dot("Alpha").getAttribute("aria-checked")).toBe("true");
  });

  it("takes the shown default back, with the panel's words, when the change is refused", async () => {
    updateSubscriptionSettings.mockRejectedValue(new ConnectError("the panel says no", Code.InvalidArgument));
    await mount(undefined, <DnsTab />, { list, providers });
    await click(dot("Beta"));
    await settle(6);
    expect(dot("Alpha").getAttribute("aria-checked")).toBe("true");
    expect(dot("Beta").getAttribute("aria-checked")).toBe("false");
    expect(document.body.textContent).toContain("the panel says no");
  });

  it("lets a helper pick the default but not make a preset; a reader neither", async () => {
    await mount(undefined, <DnsTab />, { list, providers, role: Role.HELPER });
    expect(button("New preset")).toBeUndefined();
    expect(dot("Beta").disabled).toBe(false);
    await mount(undefined, <DnsTab />, { list, providers, role: Role.READONLY });
    expect(dot("Beta").disabled).toBe(true);
    expect(dot("Alpha").disabled).toBe(false); // the one that is the default is only shown
  });

  it("says so when there is no preset, and when none is the default", async () => {
    await mount(undefined, <DnsTab />, { list: [], providers });
    expect(document.querySelector(".dq-emp")!.textContent).toBe("No presets yet.");
    expect(document.querySelector(".dq-now")!.textContent).toContain("Not chosen");
  });

  it("opens a preset in place of the list and comes back with the back button", async () => {
    await mount(undefined, <DnsTab />, { list, providers });
    await click(rowOf("Beta").querySelector(".dq-open"));
    expect(document.querySelector(".dq-list")).toBeNull();
    expect(document.querySelector<HTMLInputElement>("#dns-name")!.value).toBe("Beta");
    await click(document.querySelector(".nd-back"));
    expect(document.querySelector(".dq-list")).not.toBeNull();
    // a new one: the same editor, empty
    await click(button("New preset"));
    expect(document.querySelector<HTMLInputElement>("#dns-name")!.value).toBe("");
    expect(button("Create")?.disabled).toBe(true);
  });

  it("moves the focus with the view: to Back when a preset opens, to its row when Back is pressed", async () => {
    await mount(undefined, <DnsTab />, { list, providers });
    const later = () => act(async () => void (await new Promise((r) => setTimeout(r, 60))));
    await click(rowOf("Beta").querySelector(".dq-open"));
    await later();
    expect(document.activeElement).toBe(document.querySelector(".nd-back"));
    await click(document.querySelector(".nd-back"));
    await later();
    expect(document.activeElement).toBe(rowOf("Beta").querySelector(".dq-open"));
  });

  it("asks before the back button throws a change away, and tells the screen so it asks before another tab", async () => {
    const guard = vi.fn();
    await mount(undefined, <DnsTab />, { list, providers, guard });
    await click(rowOf("Beta").querySelector(".dq-open"));
    await type("#dns-name", "Beta two");
    expect(guard).toHaveBeenLastCalledWith(true);
    await click(document.querySelector(".nd-back"));
    expect(document.body.textContent).toContain("Discard the changes?");
    expect(document.querySelector("#dns-name")).not.toBeNull();
    await click(button("Discard"));
    await settle();
    expect(document.querySelector(".dq-list")).not.toBeNull();
  });
});

describe("the editor", () => {
  it("adds a server from the catalog as a numbered row with its note, once, and says what each app gets", async () => {
    const support = [
      { client: DnsClient.HAPP, transports: [DnsTransport.DOH, DnsTransport.PLAIN], ipv6: false },
      { client: DnsClient.AMNEZIAWG, transports: [DnsTransport.PLAIN], ipv6: false },
    ];
    const withCatalog = { presets: [], providers, clientSupport: support } as unknown as DnsData;
    await mount(undefined, <Editor with={withCatalog} />);
    expect(document.querySelector(".sb-emp")!.textContent).toBe("No servers yet. Add one from the catalog or type your own.");
    expect([...document.querySelectorAll(".sb-get > div")].map((r) => r.querySelector("b")!.textContent)).toEqual(["nothing", "nothing"]);
    await click(button("From the catalog"));
    expect(document.querySelector(".dq-cp")).not.toBeNull();
    const pick = (label: string) => [...document.querySelectorAll<HTMLButtonElement>(".dq-pk")].find((b) => b.textContent === label)!;
    await click(pick("Cloudflare · Standard"));
    expect(document.querySelector(".dq-cp")).toBeNull();
    const row = document.querySelector(".sb-srvs .sb-srv")!;
    expect([...row.querySelectorAll("span, b, em")].map((x) => x.textContent)).toContain("Fast, no filtering");
    expect(row.querySelector(".sb-no")!.textContent).toBe("1");
    // asked in plain DNS: Happ and Amnezia both get it, with nothing to add
    expect([...document.querySelectorAll(".sb-get > div")].map((r) => r.querySelector("b")!.textContent)).toEqual(["Plain", "Plain"]);
    // DoH: Happ keeps it, an Amnezia key only holds addresses
    await click([...document.querySelectorAll<HTMLElement>("[role=radio]")].find((r) => r.textContent === "DoH"));
    const get = [...document.querySelectorAll(".sb-get > div")].map((r) => [r.querySelector("b")!.textContent, r.querySelector("em")!.textContent]);
    expect(get).toEqual([
      ["DoH", ""],
      ["Plain", "a key can only hold IP addresses"],
    ]);
    // the same variant cannot be added twice
    await click(button("From the catalog"));
    expect(pick("Cloudflare · Standard").disabled).toBe(true);
    expect(pick("Cloudflare · Family").disabled).toBe(false);
  });

  it("flags a bad address under its row and in the bar, names the missing name, and keeps Create off", async () => {
    await mount();
    await click(button("Custom server"));
    await type('input[aria-label="Address of server 1"]', "1.1.1");
    expect(document.querySelector(".sb-srv .dq-er")!.textContent).toBe("Use an IP address, like 1.1.1.1 or 1.1.1.1:53.");
    expect(document.querySelector('input[aria-label="Address of server 1"]')!.getAttribute("aria-invalid")).toBe("true");
    expect(button("Create")?.disabled).toBe(true);
    expect(document.querySelector(".sb-edf .dq-em")!.textContent).toBe("Give the preset a name.");
    await type("#dns-name", "Office");
    expect(document.querySelector(".sb-edf .dq-em")!.textContent).toBe("Every server needs a valid address.");
    await type('input[aria-label="Address of server 1"]', "9.9.9.9");
    expect(document.querySelector(".sb-edf .dq-em")!.textContent).toBe("Unsaved changes");
    expect(button("Create")?.disabled).toBe(false);
  });

  it("adds and removes a split rule", async () => {
    await mount(presetWith([]));
    expect(document.querySelector(".sb-emp")!.textContent).toBe("No split: all domains go to the main servers");
    await click(button("Add split rule"));
    expect(document.querySelectorAll(".dq-rule")).toHaveLength(1);
    expect(document.querySelector(".sb-edf .dq-em")!.textContent).toBe("A split rule needs a suffix or a country, and a server.");
    await click(document.querySelector('button[aria-label="Remove rule 1"]'));
    expect(document.querySelectorAll(".dq-rule")).toHaveLength(0);
    expect(button("Save")?.disabled).toBe(true);
  });

  it("Reset puts the saved text back and stays", async () => {
    await mount(presetWith([]));
    await type("#dns-name", "Other");
    expect(button("Save")?.disabled).toBe(false);
    await click(button("Reset"));
    expect(document.querySelector<HTMLInputElement>("#dns-name")!.value).toBe("China split");
    expect(button("Save")?.disabled).toBe(true);
  });

  it("shows the panel's refusal of a save in the bar, as an alert", async () => {
    updateDnsPreset.mockRejectedValue(new ConnectError("the panel says no", Code.InvalidArgument));
    await mount(presetWith([]));
    await type("#dns-name", "Other");
    await click(button("Save"));
    expect(alertText()).toContain("the panel says no");
  });

  it("deletes a preset of its own after the question, and only a preset that is not the default", async () => {
    deleteDnsPreset.mockResolvedValue({});
    const onClose = vi.fn();
    await mount(presetWith([]), <Editor preset={presetWith([])} onClose={onClose} />);
    await click(button("Delete preset"));
    expect(document.body.textContent).toContain("Delete “China split”?");
    await click([...document.querySelectorAll("[role=dialog] button")].find((b) => b.textContent === "Delete preset"));
    await settle();
    expect(deleteDnsPreset).toHaveBeenCalledWith({ id: "dns_custom_cn" });
    expect(onClose).toHaveBeenCalled();
    await mount({ ...presetWith([]), isDefault: true });
    expect(button("Delete preset")?.disabled).toBe(true);
    await mount({ ...presetWith([]), builtin: true });
    expect(button("Delete preset")).toBeUndefined();
  });

  it("offers to make a preset the default unless it is one", async () => {
    updateSubscriptionSettings.mockResolvedValue({ settings: {} });
    await mount(presetWith([]));
    await click(button("Make default"));
    await settle(4);
    expect(updateSubscriptionSettings.mock.calls[0]![0].settings.defaultDnsPresetId).toBe("dns_custom_cn");
    await mount({ ...presetWith([]), isDefault: true });
    expect(button("Make default")).toBeUndefined();
  });

  it("is read-only for anybody but the owner: the text is there, nothing changes, nothing saves", async () => {
    await mount(presetWith([{ suffixes: [".cn"], geo: ["cn"], servers: [server] }]), <Editor preset={presetWith([{ suffixes: [".cn"], geo: ["cn"], servers: [server] }])} />, { role: Role.READONLY });
    expect(document.querySelector<HTMLInputElement>("#dns-name")!.readOnly).toBe(true);
    expect(document.querySelector<HTMLInputElement>("#dns-name")!.value).toBe("China split");
    expect(button("Save")).toBeUndefined();
    expect(button("Custom server")).toBeUndefined();
    expect(button("Add split rule")).toBeUndefined();
    expect(button("Delete preset")).toBeUndefined();
    expect(document.querySelector(".sb-edf")!.textContent).toBe("Only the owner can change presets");
    expect(option("IPv4 only").hasAttribute("data-disabled")).toBe(true);
    expect(china()!.disabled).toBe(true);
    expect(document.querySelector('button[aria-label^="Remove"]')).toBeNull();
  });
});
