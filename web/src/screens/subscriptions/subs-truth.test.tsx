import { create } from "@bufbuild/protobuf";
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
import { fill, pickForm } from "@/i18n";
import { DnsSelect } from "@/screens/users/dns-select";
import type { Tx } from "@/screens/users/t";
import { FormatsTab, whoByRules } from "./formats";
import type { Rule, Settings } from "./model";
import { RulesTab } from "./rules";

// "Apps & formats" and "Who gets what" tell the same story; a removed rule can be put back; every preset select
// says when a change reaches the apps.

let settings: Settings;
const updateSubscriptionSettings = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  subscriptions: {
    getSubscriptionSettings: async () => ({ settings, effectiveTitle: "" }),
    updateSubscriptionSettings: (...a: unknown[]) => updateSubscriptionSettings(...a),
    listClients: async () => ({
      clients: [
        { id: "happ", name: "Happ", protocols: [], formats: [SubFormat.BASE64_URIS] },
        { id: "amnezia", name: "AmneziaVPN", protocols: [], formats: [] },
      ],
    }),
    testUserAgent: async () => ({ ruleIndex: -1, format: SubFormat.BASE64_URIS, browser: false }),
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

const tx = (lang: "en" | "ru") =>
  Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill((lang === "ru" ? ru : en)[key], vars), {
    n: (key: keyof typeof en, n: number, vars?: Record<string, string | number>) => fill(pickForm((lang === "ru" ? ru : en)[key], n, lang), { n, ...vars }),
    opt: () => undefined,
    lang,
  }) as unknown as Tx;

describe("who gets a format", () => {
  it("is read from the rules that pick it, with the rules named", () => {
    const rules = [rule("mihomo", SubFormat.MIHOMO_YAML), rule("clash", SubFormat.MIHOMO_YAML), rule("curl", SubFormat.DECOY)];
    expect(whoByRules(tx("ru"), SubFormat.MIHOMO_YAML, rules)).toBe("Приложениям на ядре mihomo (Clash Meta, FlClash, Clash Verge) — по правилам «mihomo» и «clash»");
    expect(whoByRules(tx("ru"), SubFormat.MIHOMO_YAML, rules.slice(0, 1))).toBe("Приложениям на ядре mihomo (Clash Meta, FlClash, Clash Verge) — по правилу «mihomo»");
    expect(whoByRules(tx("en"), SubFormat.DECOY, rules)).toBe("Apps that the rule “curl” catches");
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
  });
});
