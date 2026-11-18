import { Code, ConnectError } from "@connectrpc/connect";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { HappPing, LinkDeviceLimit, Platform, SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { clientCompat, dns, subscriptions } from "@/lib/api";
import { clientApiQuery } from "@/lib/clientapi";
import { ClientCompatActual, ClientCompatState, clientCompatQuery, outdatedDevicesQuery, type CompatDevice } from "@/lib/clientcompat";
import { userCountQuery } from "@/lib/queries";
import { clientsQuery, dnsPresetsQuery, settingsQuery } from "./subscriptions/queries";
import { setSbDemo, type SbDemo } from "./subscriptions/demo-sb";
import type { DnsData, Settings } from "./subscriptions/model";
import { ruleFor } from "./subscriptions/model";
import { classicDevices, compatActual, compatCounts, compatDefaults, compatDevices, compatPage, compatRules, count, B, K, U } from "./dev-kit-subs-data";
import { boardPresets, clientSupport, providers } from "./dev-kit-subs-dns-data";
import type { SceneDef, World } from "./dev-kit-subs-page";

// Development only. The scenes of the tabs "Apps and formats", "Who gets what", "App versions", "Names and texts" and "Page": every state of
// the board's switch "Раздел" (its tabs 1, 2, 3, 4 and 7) and what the product has beyond the board (a note under the rules with its Undo,
// a rule being added, the switches off, the questions before AmneziaWG goes and before the strict limit, the Mistgate app off and for a
// helper, the devices of every state and of every class of what the nodes saw, an empty list, the texts that are too long or not
// saved, a Happ ping by TCP, the page with a card open or dragged, the DNS offer on and off, a person list that is loading or failed),
// each also in the light theme, on a phone and in Persian where the layout changes. The panel answers from here: a save changes what the
// page keeps in memory, nothing leaves the page. Registered in dev-kit-subs-page.tsx.

const hung = () => new Promise<never>(() => {});

/** The settings of the scene kept in memory: what a save writes is what the next read gives, as the panel does. */
function store(w: World) {
  w.patch(subscriptions, {
    getSubscriptionSettings: (async () => ({ settings: w.settings, ...w.extra, serverSamples: w.extra.samples, reservedHeaderNames: [] })) as () => unknown,
    updateSubscriptionSettings: (async (r: { settings: Settings }) => {
      w.settings = { ...w.settings, ...r.settings } as Settings;
      return { settings: w.settings };
    }) as (r: never) => unknown,
    // what the panel does with a User-Agent: the first rule whose text it contains, else the page for a browser and the link list for an app
    testUserAgent: (async ({ userAgent }: { userAgent: string }) => {
      const i = ruleFor(w.settings.rules, userAgent);
      const browser = /mozilla|safari|chrome/i.test(userAgent);
      return { ruleIndex: i, format: i >= 0 ? w.settings.rules[i]!.format : browser ? SubFormat.USER_PAGE : SubFormat.BASE64_URIS, browser };
    }) as (r: never) => unknown,
  });
}

type Make = { demo?: SbDemo; tune?: (w: World) => void; w?: number; h?: number; params?: string; user?: string };
const scene = (state: string, tab: SceneDef["tab"], title: string, o: Make = {}): SceneDef => ({
  state,
  tab,
  title,
  w: o.w,
  h: o.h,
  params: o.params,
  user: o.user,
  setup: (w) => {
    setSbDemo(o.demo ?? {});
    store(w);
    w.seed(clientApiQuery.queryKey, { enabled: true, stats: { people: 3, peopleOn: 2, devices: 5 } });
    w.seed(userCountQuery.queryKey, { counts: { overDeviceLimit: 0 } });
    o.tune?.(w);
  },
});
/** The same scene in the light theme, on a phone and in Persian. */
const looks = (state: string, tab: SceneDef["tab"], title: string, h: number, o: Make = {}): SceneDef[] => [
  { ...scene(state, tab, `${title}: light`, { ...o, params: "&theme=light", h }), state },
  { ...scene(state, tab, `${title}: a phone`, { ...o, params: "", w: 400, h: h * 1.6 }), state },
  { ...scene(state, tab, `${title}: Persian`, { ...o, params: "&lang=fa", h }), state },
];

const off = (patch: Partial<Settings>) => (w: World) => void (w.settings = { ...w.settings, ...patch } as Settings);
const failing = (w: World) => void w.qc.prefetchQuery({ queryKey: settingsQuery.queryKey, queryFn: () => Promise.reject(new ConnectError("the panel does not answer", Code.Unavailable)), retry: false });

// ---- 1. Apps and formats
const apps = [
  scene("apps", "formats", "Apps and formats: the apps with the protocols they carry, the six formats of the link", { h: 1500 }),
  ...looks("apps", "formats", "Apps and formats", 1500),
  scene("apps-awg-off", "formats", "Apps and formats: AmneziaWG is off for the apps on the link (the note of the Mihomo row)", { h: 1300, tune: off({ linkAwg: false }) }),
  scene("apps-bare", "formats", "Apps and formats: no rule picks a format but the first, the fake 404 stands", {
    h: 1300,
    tune: off({ rules: [{ uaContains: "mihomo", format: SubFormat.MIHOMO_YAML }] } as unknown as Partial<Settings>),
  }),
  scene("apps-none", "formats", "Apps and formats: no app is registered yet", { h: 900, tune: (w) => w.seed(clientsQuery.queryKey, []) }),
  scene("apps-err", "formats", "Apps and formats: the list of apps did not load", {
    h: 700,
    tune: (w) => void w.qc.prefetchQuery({ queryKey: clientsQuery.queryKey, queryFn: () => Promise.reject(new ConnectError("the panel does not answer", Code.Unavailable)), retry: false }),
  }),
];

// ---- 2. Who gets what
const sing = "SFA (sing-box 1.14.3; language en_US)";
const rules = [
  scene("rules", "rules", "Who gets what: the rules, what the apps get, the limits, the tester, the Mistgate app", { h: 1900, demo: { ua: "mihomo/1.19.30" } }),
  ...looks("rules", "rules", "Who gets what", 1900, { demo: { ua: "mihomo/1.19.30" } }),
  scene("rules-hit", "rules", "Who gets what: the tester lands on rule 4 (sing-box JSON), the row is marked", { h: 1900, demo: { ua: sing, hit: 3 } }),
  scene("rules-rest", "rules", "Who gets what: no rule fits, the line “Everything else” is the one (a browser)", { h: 1900, demo: { ua: "Mozilla/5.0 (iPhone) Safari", rest: true } }),
  scene("rules-new", "rules", "Who gets what: a rule being added (the text, the format, saved when the text is left)", { h: 1500, demo: { adding: true, ua: "mihomo/1.19.30" } }),
  scene("rules-saved", "rules", "Who gets what: the note under the rules after a change", { h: 1500, demo: { note: { text: "Сохранено" }, ua: "mihomo/1.19.30" } }),
  scene("rules-undo", "rules", "Who gets what: a rule removed, with Undo", { h: 1500, demo: { note: { text: "Правило «curl» удалено", undo: () => {} }, ua: "mihomo/1.19.30" } }),
  scene("rules-empty", "rules", "Who gets what: no rules yet, only “Everything else”", { h: 1500, tune: off({ rules: [] } as unknown as Partial<Settings>), demo: { ua: "Happ/3.2.1" } }),
  scene("rules-off", "rules", "Who gets what: automatic server choice and AmneziaWG off, the inactive-devices switch off", {
    h: 1500,
    demo: { ua: "mihomo/1.19.30" },
    tune: off({ linkAutoSelect: false, linkAwg: false, deviceIdleAfterDays: 0 } as unknown as Partial<Settings>),
  }),
  scene("rules-strict", "rules", "Who gets what: the strict device limit chosen", { h: 1500, demo: { ua: "mihomo/1.19.30" }, tune: off({ linkDeviceLimit: LinkDeviceLimit.STRICT } as unknown as Partial<Settings>) }),
  scene("rules-mg-off", "rules", "Who gets what: the Mistgate app is off for the whole panel", {
    h: 1500,
    demo: { ua: "mihomo/1.19.30" },
    tune: (w) => w.seed(clientApiQuery.queryKey, { enabled: false, stats: { people: 3, peopleOn: 0, devices: 0 } }),
  }),
  scene("rules-mg-helper", "rules", "Who gets what: the Mistgate app as a helper sees it (reads, does not change)", { h: 1500, demo: { ua: "mihomo/1.19.30" } }),
  scene("rules-mg-empty", "rules", "Who gets what: the Mistgate app on, nobody has connected yet", {
    h: 1500,
    demo: { ua: "mihomo/1.19.30" },
    tune: (w) => w.seed(clientApiQuery.queryKey, { enabled: true, stats: { people: 3, peopleOn: 0, devices: 0 } }),
  }),
  scene("rules-load", "rules", "Who gets what: the settings are on their way", { h: 700, tune: (w) => void w.qc.prefetchQuery({ queryKey: settingsQuery.queryKey, queryFn: hung }) }),
  scene("rules-err", "rules", "Who gets what: the settings did not load", { h: 700, tune: failing }),
];

// ---- 3. App versions
const none = [count(ClientCompatState.OK, 0, 0), count(B, 0, 0), count(U, 0, 0), count(K, 0, 0)];
const noneActual = compatActual.map((a) => ({ ...a, devices: 0, people: 0 }));
/** The first page of the devices the panel would send for a state tile and a class of what the nodes saw. */
function devices(w: World, list: CompatDevice[], o: { counts?: typeof compatCounts; actual?: typeof compatActual; page?: Partial<ReturnType<typeof compatPage>>; owner?: boolean } = {}) {
  const page = { ...compatPage(list, o.counts, o.actual), ...o.page };
  w.seed(outdatedDevicesQuery({ state: ClientCompatState.UNSPECIFIED, actual: ClientCompatActual.UNSPECIFIED, userId: "", page: 1, size: 50 }).queryKey, page);
  w.seed(clientCompatQuery.queryKey, { rules: compatRules, defaults: compatDefaults });
  w.patch(clientCompat, {
    listOutdatedDevices: (async (r: { state: ClientCompatState; actual: ClientCompatActual; userId: string }) => {
      const all = [...compatDevices, ...classicDevices];
      const rows = all.filter((d) => (r.state === ClientCompatState.UNSPECIFIED ? d.state !== ClientCompatState.OK : d.state === r.state)).filter((d) => r.actual === ClientCompatActual.UNSPECIFIED || d.actual === r.actual).filter((d) => !r.userId || d.userId === r.userId);
      return { ...page, devices: rows, total: rows.length };
    }) as (r: never) => unknown,
    getClientCompat: (async () => ({ rules: compatRules, defaults: compatDefaults })) as () => unknown,
    updateClientCompat: (async (r: { rules: typeof compatRules }) => ({ rules: r.rules, defaults: compatDefaults })) as (r: never) => unknown,
  });
}
const firstPage = outdatedDevicesQuery({ state: ClientCompatState.UNSPECIFIED, actual: ClientCompatActual.UNSPECIFIED, userId: "", page: 1, size: 50 }).queryKey;
/** The first page of the devices left to load (or to fail), instead of the one seeded. */
const reseed = (w: World, fn: () => Promise<never>) => {
  w.qc.removeQueries({ queryKey: firstPage });
  void w.qc.prefetchQuery({ queryKey: firstPage, queryFn: fn, retry: false });
};
const versions = [
  scene("ver", "clients", "App versions: the devices that need a look, what the nodes saw, the minimum versions folded", { h: 1500, tune: (w) => devices(w, compatDevices) }),
  ...looks("ver", "clients", "App versions", 1500, { tune: (w) => devices(w, compatDevices) }),
  scene("ver-ok", "clients", "App versions: the tile “OK” chosen", { h: 1500, demo: { pick: "ok" }, tune: (w) => devices(w, compatDevices) }),
  scene("ver-below", "clients", "App versions: the tile “Below the minimum” chosen", { h: 1300, demo: { pick: "below" }, tune: (w) => devices(w, compatDevices) }),
  scene("ver-unsupported", "clients", "App versions: the tile “Not supported” chosen", { h: 1300, demo: { pick: "unsupported" }, tune: (w) => devices(w, compatDevices) }),
  scene("ver-unknown", "clients", "App versions: the tile “Unknown” chosen", { h: 1300, demo: { pick: "unknown" }, tune: (w) => devices(w, compatDevices) }),
  scene("ver-min", "clients", "App versions: the table of minimum versions open (one row changed, one of the owner's own)", { h: 2000, demo: { minOpen: true }, tune: (w) => devices(w, compatDevices) }),
  scene("ver-min-helper", "clients", "App versions: the table of minimum versions as a helper sees it", { h: 1900, demo: { minOpen: true }, tune: (w) => devices(w, compatDevices) }),
  scene("ver-user", "clients", "App versions: the devices of one person (?user=)", { h: 1100, user: "alice", tune: (w) => devices(w, compatDevices.filter((d) => d.userId === "alice")) }),
  scene("ver-only-ok", "clients", "App versions: every device is in order", {
    h: 800,
    tune: (w) => devices(w, [], { counts: [count(ClientCompatState.OK, 14, 6), count(B, 0, 0), count(U, 0, 0), count(K, 0, 0)], page: { mismatchWorse: 0, mismatchBetter: 0, helloKexNodesWithout: [] } }),
  }),
  scene("ver-empty", "clients", "App versions: no device gets REALITY servers yet", { h: 800, tune: (w) => devices(w, [], { counts: none, actual: noneActual, page: { mismatchWorse: 0, mismatchBetter: 0, helloKexNodesWithout: [] } }) }),
  scene("ver-nodes", "clients", "App versions: a node does not count what it saw yet, the table and the nodes disagree both ways", {
    h: 1500,
    tune: (w) => devices(w, compatDevices, { page: { mismatchWorse: 2, mismatchBetter: 1, helloKexNodesWithout: ["nl1", "fi1"] } }),
  }),
  scene("ver-load", "clients", "App versions: the devices are on their way", { h: 700, tune: (w) => (devices(w, compatDevices), reseed(w, hung)) }),
  scene("ver-err", "clients", "App versions: the panel did not answer", { h: 700, tune: (w) => (devices(w, compatDevices), reseed(w, () => Promise.reject(new ConnectError("the panel does not answer", Code.Unavailable)))) }),
];

// ---- 4. Names and texts
const manySamples = ["nl", "ru", "de", "tr", "fi", "be"].flatMap((cc, i) => ["HY2", "VLESS · XHTTP", "VLESS · TCP"].map((profile) => ({ node: `${cc}1`, countryCode: cc === "be" ? "BY" : cc.toUpperCase(), profile, loadPercent: i + 1 })));
const groupSamples = [
  { node: "de1", countryCode: "DE", profile: "VLESS · Reality", loadPercent: 9 },
  { node: "de1", countryCode: "DE", profile: "Hysteria2", loadPercent: 9 },
  { node: "nl1", countryCode: "NL", profile: "Hysteria2", loadPercent: 7 },
  { node: "fi1", countryCode: "FI", profile: "VLESS · Reality", loadPercent: 5 },
  { node: "msk1", countryCode: "RU", profile: "VLESS · Reality", loadPercent: 4 },
];
const withSamples = (samples: typeof groupSamples, group = "Друзья") => (w: World) => {
  w.extra.samples = samples;
  w.extra.sampleGroup = group;
  w.extra.namesLanguage = "ru";
};
const texts = [
  scene("txt", "texts", "Names and texts: the three cards and the phone of Happ with the servers of the group", { h: 1500, tune: withSamples(groupSamples) }),
  ...looks("txt", "texts", "Names and texts", 1500, { tune: withSamples(groupSamples) }),
  scene("txt-made-up", "texts", "Names and texts: an install with no server yet, the preview with made-up ones", { h: 1500 }),
  scene("txt-many", "texts", "Names and texts: a group with as many servers as a fleet gives, the preview scrolls", { h: 1700, tune: withSamples(manySamples as typeof groupSamples, "Все") }),
  scene("txt-dirty", "texts", "Names and texts: typed and not saved yet, the strip that saves is open", {
    h: 1600,
    demo: { draft: { title: "Мой VPN", announcement: "Плановые работы в воскресенье с 03:00 до 03:30", supportUrl: "https://t.me/example", template: "{flag} {node} · {profile}" } },
    tune: withSamples(groupSamples),
  }),
  scene("txt-over", "texts", "Names and texts: the announcement is longer than Happ shows", {
    h: 1600,
    demo: { draft: { announcement: "Дорогие друзья! ".repeat(16).trim() } },
    tune: withSamples(groupSamples),
  }),
  scene("txt-bad", "texts", "Names and texts: the support link is no link, the save is off", { h: 1600, demo: { draft: { supportUrl: "t.me/example" } }, tune: withSamples(groupSamples) }),
  scene("txt-tcp", "texts", "Names and texts: Happ pings by TCP, on open", { h: 1500, tune: (w) => (withSamples(groupSamples)(w), off({ happPing: HappPing.TCP, happPingOnOpen: true } as Partial<Settings>)(w)) }),
  scene("txt-helper", "texts", "Names and texts: as a helper sees it", { h: 1500, tune: withSamples(groupSamples) }),
  scene("txt-load", "texts", "Names and texts: the settings are on their way", { h: 700, tune: (w) => void w.qc.prefetchQuery({ queryKey: settingsQuery.queryKey, queryFn: hung }) }),
];

// ---- 7. Page
const boardApps = [
  ["Windows", "Mistgate", 0, true],
  ["iOS", "Happ", 25, true],
  ["Android", "Happ", 25, false],
  ["Windows", "kl!ck", 22, false],
  ["macOS", "Happ", 0, false],
  ["Linux", "Mihomo Party", 21, false],
  ["iOS", "AmneziaVPN", 36, false],
  ["Android", "AmneziaVPN", 0, false],
  ["iOS", "Telegram", 23, false],
] as const;
const platformOf: Record<string, Platform> = { iOS: Platform.IOS, Android: Platform.ANDROID, Windows: Platform.WINDOWS, macOS: Platform.MACOS, Linux: Platform.LINUX };
const pageApps = boardApps.map(([plat, name, len, rec]) => ({
  platform: platformOf[plat]!,
  kind: name === "AmneziaVPN" ? App.AMNEZIA : App.HAPP,
  name,
  downloadUrl: `https://example.com/${encodeURIComponent(name)}`,
  addLinkTemplate: name === "Happ" ? "happ://add/{url}" : "",
  description: "x".repeat(len),
  recommended: rec,
}));
const pageSettings = (over: Record<string, unknown> = {}, offer: { presetIds: string[]; defaultPresetId: string } = { presetIds: ["dns_builtin_cloudflare", "dns_builtin_quad9", "dns_builtin_adblock"], defaultPresetId: "dns_builtin_cloudflare" }) =>
  (w: World) => {
    w.settings = { ...w.settings, apps: pageApps, userPage: { showAnnouncement: true, showSupport: true, showQr: true, allowDeviceSelfService: true, requirePagePassword: false, allowDnsChoice: false, ...over }, linkDns: offer } as unknown as Settings;
    const data: DnsData = { presets: boardPresets, providers, clientSupport } as unknown as DnsData;
    w.seed(dnsPresetsQuery.queryKey, data);
    w.patch(dns, { listDnsPresets: (async () => data) as () => unknown });
  };
const page = [
  scene("page", "page", "Page: the apps of the page in a list, what the page has, the DNS offer (the choice off, so dimmed)", { h: 1900, tune: pageSettings() }),
  ...looks("page", "page", "Page", 1900, { tune: pageSettings() }),
  scene("page-open", "page", "Page: a card of an app open", { h: 2000, demo: { appOpen: 1 }, tune: pageSettings() }),
  scene("page-dns", "page", "Page: the DNS choice on, the offer and its default", { h: 1900, tune: pageSettings({ allowDnsChoice: true }) }),
  scene("page-dns-nodef", "page", "Page: the DNS choice on, nothing offered", { h: 1900, tune: pageSettings({ allowDnsChoice: true }, { presetIds: [], defaultPresetId: "" }) }),
  scene("page-password", "page", "Page: the password on the page is on", { h: 1900, tune: pageSettings({ requirePagePassword: true }) }),
  scene("page-add", "page", "Page: the bar that adds an app (Windows chosen, the known apps of that platform)", { h: 1900, demo: { addPlatform: Platform.WINDOWS }, tune: pageSettings() }),
  scene("page-none", "page", "Page: no app is listed, the page offers only the link to copy", { h: 1200, tune: (w) => (pageSettings()(w), (w.settings = { ...w.settings, apps: [] } as unknown as Settings)) }),
  scene("page-helper", "page", "Page: as a helper sees it", { h: 1900, tune: pageSettings() }),
  scene("page-load", "page", "Page: the settings are on their way", { h: 700, tune: (w) => void w.qc.prefetchQuery({ queryKey: settingsQuery.queryKey, queryFn: hung }) }),
];


export const baseScenes: SceneDef[] = [...apps, ...rules, ...versions, ...texts, ...page];
