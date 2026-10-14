import { create } from "@bufbuild/protobuf";
import { readFileSync } from "node:fs";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { DnsPresetSchema } from "@/gen/mistgate/admin/v1/dns_pb";
import { Platform, SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { zh } from "@/i18n/zh";
import { fill, pickForm } from "@/i18n";
import { DnsSelect } from "@/screens/users/dns-select";
import type { Tx } from "@/screens/users/t";
import { FormatsTab, whoByRules } from "./formats";
import { ruleFor, type Rule, type Settings } from "./model";
import { RulesTab } from "./rules";

// "Apps & formats" and "Who gets what" tell the same story; a removed rule can be put back; every preset select
// says when a change reaches the apps.

// What the panel's subs.Choose picks for the example User-Agents, with the default rules and an owner's v2rayng rule: the
// same file is checked against Choose by internal/panel/subs TestChooseMatchesTheAdmin (vitest runs in web/).
type Table = { rules: { ua_contains: string; format: keyof typeof SubFormat }[]; cases: { ua: string; rule: number; format: keyof typeof SubFormat }[] };
const table = JSON.parse(readFileSync(`${process.cwd()}/../internal/panel/subs/testdata/choose_admin.json`, "utf8")) as Table;
const tableRules = table.rules.map((r) => ({ uaContains: r.ua_contains, format: SubFormat[r.format] }) as Rule);
const defaultRules = tableRules.slice(0, 3); // subsettings.Defaults()

let settings: Settings;
const updateSubscriptionSettings = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  subscriptions: {
    getSubscriptionSettings: async () => ({ settings, effectiveTitle: "" }),
    updateSubscriptionSettings: (...a: unknown[]) => updateSubscriptionSettings(...a),
    listClients: async () => ({
      clients: [
        // as ListClients answers: Happ can read Xray JSON, although no default rule hands it out
        { id: "happ", name: "Happ", protocols: [], formats: [SubFormat.BASE64_URIS, SubFormat.XRAY_JSON] },
        { id: "singbox", name: "sing-box", protocols: ["hysteria2", "vless"], formats: [SubFormat.SINGBOX_JSON] },
        { id: "amnezia", name: "AmneziaVPN", protocols: [], formats: [] },
      ],
    }),
    testUserAgent: async ({ userAgent }: { userAgent: string }) => {
      const c = table.cases.find((x) => x.ua === userAgent);
      return { ruleIndex: c?.rule ?? -1, format: c ? SubFormat[c.format] : SubFormat.BASE64_URIS, browser: false };
    },
  },
  profiles: { listProtocols: async () => ({ protocols: [] }) },
  groups: { listGroups: async () => ({ groups: [] }) },
  dns: {
    listDnsPresets: async () => ({ presets: [create(DnsPresetSchema, { id: "p1", name: "Cloudflare", isDefault: true })], providers: [], clientSupport: [] }),
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
  updateSubscriptionSettings.mockReset();
});

const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
/** Base UI opens a Select on the press. */
const pressOn = (el: Element) =>
  act(async () => {
    for (const ev of ["pointerdown", "mousedown", "pointerup", "mouseup", "click"]) el.dispatchEvent(new MouseEvent(ev, { bubbles: true, button: 0 }));
  });
const text = () => document.body.textContent ?? "";
const click = async (el: Element | null | undefined) => {
  await act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  for (let i = 0; i < 4; i++) await settle();
};
const rule = (uaContains: string, format: SubFormat): Rule => ({ uaContains, format }) as Rule;
const settingsWith = (rules: Rule[]) => ({ title: "", announcement: "", supportUrl: "", updateIntervalHours: 12, serverNameTemplate: "", rules, defaultDnsPresetId: "", apps: [] }) as unknown as Settings;

async function mount(ui: ReactElement) {
  updateSubscriptionSettings.mockImplementation(async (r: { settings: Settings }) => {
    settings = r.settings;
    return { settings: r.settings };
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(<QueryClientProvider client={qc}><ToastProvider>{ui}</ToastProvider></QueryClientProvider>));
  for (let i = 0; i < 4; i++) await settle();
}

const tx = (lang: "en" | "ru" | "zh") =>
  Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill({ en, ru, zh }[lang][key], vars), {
    n: (key: keyof typeof en, n: number, vars?: Record<string, string | number>) => fill(pickForm({ en, ru, zh }[lang][key], n, lang), { n, ...vars }),
    opt: () => undefined,
    lang,
    locale: lang,
  }) as unknown as Tx;

describe("who gets a format", () => {
  it("is read from the rules that pick it, with the rules named", () => {
    const rules = [rule("mihomo", SubFormat.MIHOMO_YAML), rule("clash", SubFormat.MIHOMO_YAML), rule("curl", SubFormat.DECOY)];
    expect(whoByRules(tx("ru"), SubFormat.MIHOMO_YAML, rules)).toBe("Приложениям на ядре mihomo (Clash Meta, FlClash, Clash Verge) — по правилам «mihomo» и «clash»");
    expect(whoByRules(tx("ru"), SubFormat.MIHOMO_YAML, rules.slice(0, 1))).toBe("Приложениям на ядре mihomo (Clash Meta, FlClash, Clash Verge) — по правилу «mihomo»");
    expect(whoByRules(tx("en"), SubFormat.DECOY, rules)).toBe("Apps that the rule “curl” catches");
    expect(whoByRules(tx("zh"), SubFormat.DECOY, rules)).toBe("被规则 「curl」 匹配的应用");
    expect(whoByRules(tx("ru"), SubFormat.MIHOMO_YAML, [])).toBe(ru["subs.fmt.who.decoy"]);
  });

  it("shows on the formats tab: Mihomo is served (no “Soon”), AmneziaVPN reads no subscription", async () => {
    settings = settingsWith([rule("mihomo", SubFormat.MIHOMO_YAML), rule("clash", SubFormat.MIHOMO_YAML)]);
    await mount(<FormatsTab go={() => {}} />);
    expect(text()).toContain("Apps on the mihomo core (Clash Meta, FlClash, Clash Verge), by the rules “mihomo” and “clash”");
    expect(text()).toContain(en["subs.clientNoSub"]);
    expect(text()).toContain(en["subs.fmt.who.decoy"]); // the fake 404: no rule picks it
    expect(text()).not.toMatch(/\bSoon\b/);
  });
});

describe("the JSON formats", () => {
  const card = (title: string) => [...document.querySelectorAll("b")].find((b) => b.textContent === title)!.parentElement!.parentElement!;

  it("are picked for the example User-Agents as the panel's subs.Choose picks them", () => {
    for (const c of table.cases) {
      const i = ruleFor(tableRules, c.ua);
      expect([i, i < 0 ? SubFormat.BASE64_URIS : tableRules[i]!.format], c.ua).toEqual([c.rule, SubFormat[c.format]]);
    }
    expect(ruleFor(defaultRules, "v2rayNG/1.10.20")).toBe(-1); // no default rule hands out Xray JSON
  });

  it("do not list Happ under Xray JSON while no rule names it; sing-box goes to the official apps by the default rule", async () => {
    settings = settingsWith(defaultRules);
    await mount(<FormatsTab go={() => {}} />);
    expect(card("Xray JSON").textContent).toContain(en["subs.fmt.who.decoy"]);
    expect(card("Xray JSON").textContent).not.toContain("Happ");
    expect(card("sing-box JSON").textContent).toContain("The official sing-box apps (SFA, SFI, SFM), by the rule “sing-box”");
    // what each leaves out, and what the sing-box app carries
    expect(card("Xray JSON").textContent).toContain(en["subs.fmt.xray.out"]);
    expect(card("sing-box JSON").textContent).toContain(en["subs.fmt.singbox.out"]);
    expect(card("sing-box").textContent).toContain(en["subs.clientSingbox"]);
    expect(card("Happ").textContent).toContain("Xray JSON"); // what Happ can read is not who gets it
  });

  it("follow the owner's rules: a v2rayng rule, a sing-box rule narrowed to SFA, an earlier rule, a rule removed", async () => {
    const inRu = (rules: Rule[]) => whoByRules(tx("ru"), SubFormat.SINGBOX_JSON, rules);
    const narrowed = tableRules.map((r) => (r.uaContains === "sing-box" ? { ...r, uaContains: "SFA" } : r));
    expect(whoByRules(tx("en"), SubFormat.XRAY_JSON, tableRules)).toBe("Apps that the rule “v2rayng” catches");
    expect(inRu(defaultRules)).toBe("Официальным приложениям sing-box (SFA, SFI, SFM) — по правилу «sing-box»");
    expect(whoByRules(tx("zh"), SubFormat.SINGBOX_JSON, defaultRules)).toBe(fill(zh["subs.fmt.who.singbox"], { rules: "「sing-box」" }));
    expect(inRu(narrowed)).toBe(fill(pickForm(ru["subs.fmt.who.rules"], 1, "ru"), { rules: "«SFA»" })); // SFI and SFM no longer get it
    expect(inRu([rule("sing", SubFormat.BASE64_URIS), ...defaultRules])).toBe(fill(pickForm(ru["subs.fmt.who.rules"], 1, "ru"), { rules: "«sing-box»" })); // an earlier rule takes them first
    expect(inRu(defaultRules.slice(0, 2))).toBe(ru["subs.fmt.who.decoy"]);
    settings = settingsWith(tableRules);
    await mount(<FormatsTab go={() => {}} />);
    expect(card("Xray JSON").textContent).toContain("Apps that the rule “v2rayng” catches");
  });

  it("are offered by a rule's format select, and the examples land on them as the panel decides", async () => {
    settings = settingsWith(tableRules);
    await mount(<RulesTab settings={settings} />);
    await pressOn(document.querySelector("[aria-label='Format of rule 3']")!);
    for (let i = 0; i < 100 && document.querySelectorAll("[role=option]").length === 0; i++) await settle();
    expect([...document.querySelectorAll("[role=option]")].map((o) => o.textContent?.trim())).toEqual(["Link list (base64)", "Mihomo YAML", "Xray JSON", "sing-box JSON", "Web page", "Fake 404"]);
    for (const [chip, want] of [
      ["sing-box", "Rule 3 matches → sing-box JSON"],
      ["v2rayNG", "Rule 4 matches → Xray JSON"],
    ] as const) {
      await click([...document.querySelectorAll("button")].find((b) => b.textContent === chip));
      for (let i = 0; i < 40 && !text().includes(want); i++) await act(async () => void (await new Promise((r) => setTimeout(r, 25)))); // the probe is debounced
      expect(text()).toContain(want);
    }
  });
});

describe("removing a rule", () => {
  it("names the rule in the toast and puts it back where it was on Undo", async () => {
    settings = settingsWith([rule("mihomo", SubFormat.MIHOMO_YAML), rule("curl", SubFormat.DECOY)]);
    await mount(<RulesTab settings={settings} />);
    expect([...document.querySelectorAll("[role=combobox], button")].some((b) => /soon/i.test(b.textContent ?? ""))).toBe(false);
    await click(document.querySelector("button[aria-label='Remove rule 1']"));
    expect((updateSubscriptionSettings.mock.calls[0]![0] as { settings: Settings }).settings.rules.map((r) => r.uaContains)).toEqual(["curl"]);
    expect(text()).toContain("Rule “mihomo” removed");
    await click([...document.querySelectorAll("button")].find((b) => b.textContent === en["common.undo"]));
    const back = (updateSubscriptionSettings.mock.calls[1]![0] as { settings: Settings }).settings.rules;
    expect(back.map((r) => [r.uaContains, r.format])).toEqual([
      ["mihomo", SubFormat.MIHOMO_YAML],
      ["curl", SubFormat.DECOY],
    ]);
    expect(text()).toContain(en["subs.rules.restored"]);
  });
});

describe("the DNS preset select", () => {
  it("says when a change reaches each app, naming the link apps of the settings, unless told not to", async () => {
    const app = (platform: Platform, kind: App, name: string) => ({ platform, kind, name });
    settings = { ...settingsWith([]), apps: [app(Platform.IOS, App.HAPP, "Happ"), app(Platform.ANDROID, App.HAPP, "Happ"), app(Platform.WINDOWS, App.HAPP, "FlClash"), app(Platform.ANDROID, App.AMNEZIA, "AmneziaVPN")] } as unknown as Settings;
    await mount(<DnsSelect value="" onChange={() => {}} inherited={{ id: "p1", fromGroup: false }} />);
    expect(text()).toContain(fill(en["subs.dns.delivery"], { apps: "Happ, FlClash" }));
    act(() => root?.unmount());
    host?.remove();
    await mount(<DnsSelect value="" onChange={() => {}} inherited={{ id: "p1", fromGroup: false }} note={false} />);
    expect(text()).not.toContain("When it arrives");
  });

  it("falls back to the generic words when the settings name no link app", async () => {
    settings = settingsWith([]);
    await mount(<DnsSelect value="" onChange={() => {}} inherited={{ id: "p1", fromGroup: false }} />);
    expect(text()).toContain(fill(en["subs.dns.delivery"], { apps: en["subs.kind.happ"] }));
    expect(text()).toContain(en["subs.dns.scopeUser"]);
  });

  it("explains that a group preset is shared and a user override wins", async () => {
    settings = settingsWith([]);
    await mount(<DnsSelect value="" onChange={() => {}} inherited={{ id: "p1", fromGroup: false }} scope="group" />);
    expect(text()).toContain(en["subs.dns.scopeGroup"]);
  });
});
