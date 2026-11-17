import { Code, ConnectError } from "@connectrpc/connect";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { DnsCategory, DnsServerKind, DnsTransport } from "@/gen/mistgate/admin/v1/dns_pb";
import { Platform } from "@/gen/mistgate/admin/v1/subscription_pb";
import { dns, subscriptions } from "@/lib/api";
import { setDnsDemo, type DnsDemo } from "./subscriptions/demo-dns";
import type { DnsData, Preset } from "./subscriptions/model";
import { dnsPresetsQuery } from "./subscriptions/queries";
import { boardPresets, cat, clientSupport, ids, longPresets, manyPresets, own, ownDefault, providers, rule } from "./dev-kit-subs-dns-data";
import type { SceneDef, World } from "./dev-kit-subs-page";

// Development only. The scenes of the tab "DNS" (screens/subscriptions/dns.tsx, dns-editor.tsx): every state of the board's switch "DNS"
// (the list, a new preset, Cloudflare open), then what the product has beyond the board (a chip chosen, the default just changed with its
// note and Undo, the people each preset has, the built-in "No ads on the device", an own preset with its delete, the questions before a
// delete and before leaving, what is changed and not saved, a save on its way and refused, the bad address and the missing name, the
// split rules with their countries, the catalog panel, DoT and several servers, the list that is loading, failed or empty, the helper
// (a state ending in -helper is the screen as a helper sees it) and the Edge edition (-edge), long names), and the same pages on the phone,
// in the light theme and in Persian. Registered in dev-kit-subs-page.tsx. The panel answers from here: a click on a dot, a save, a delete
// change the list the page keeps in memory, nothing leaves the page.

const hung = () => new Promise<never>(() => {});
const unavailable = () => Promise.reject(new ConnectError("the panel does not answer", Code.Unavailable));

/** The panel of a scene: the presets it keeps, the default, and the calls of the two services the tab uses, answered from memory. */
function panel(w: World, presets: Preset[]) {
  let list = presets.map((p) => ({ ...p }));
  let def = list.find((p) => p.isDefault)?.id ?? "";
  let n = 0;
  const view = (): DnsData => ({ presets: list.map((p) => ({ ...p, isDefault: p.id === def })), providers, clientSupport });
  const server = (s: Preset["servers"][number]) => ({ kind: s.kind, address: s.address, providerVariant: s.providerVariant ?? "" });
  type Req = { id?: string; name: string; description: string; servers: Preset["servers"]; split: Preset["split"]; ipv4Only: boolean; splitDirect: boolean; blockAds?: boolean; preferredTransport: DnsTransport };
  w.patch(dns, {
    listDnsPresets: (async () => view()) as () => unknown,
    createDnsPreset: (async (r: Req) => {
      const p: Preset = { ...r, id: `dns_own_${++n}`, builtin: false, servers: r.servers.map(server), split: r.split, userCount: 0, isDefault: false, category: DnsCategory.UNSPECIFIED, country: "", blockAds: !!r.blockAds } as Preset;
      list = [...list, p];
      return { preset: p };
    }) as (r: never) => unknown,
    updateDnsPreset: (async (r: Req) => {
      list = list.map((p) => (p.id === r.id ? ({ ...p, ...r, servers: r.servers.map(server), blockAds: r.blockAds ?? p.blockAds } as Preset) : p));
      return { preset: list.find((p) => p.id === r.id) };
    }) as (r: never) => unknown,
    deleteDnsPreset: (async (r: { id: string }) => {
      list = list.filter((p) => p.id !== r.id);
      return {};
    }) as (r: never) => unknown,
  });
  w.patch(subscriptions, {
    getSubscriptionSettings: (async () => ({ settings: { ...w.settings, defaultDnsPresetId: def }, ...w.extra, serverSamples: [], reservedHeaderNames: [] })) as () => unknown,
    updateSubscriptionSettings: (async (r: { settings: { defaultDnsPresetId: string } }) => {
      def = r.settings.defaultDnsPresetId;
      return { settings: r.settings };
    }) as (r: never) => unknown,
  });
  w.seed(dnsPresetsQuery.queryKey, view());
}

const app = (name: string, platform: Platform) => ({ platform, kind: App.HAPP, name, downloadUrl: `https://example.com/${name}`, addLinkTemplate: "", description: "", recommended: false });

type Make = { demo?: DnsDemo; list?: Preset[]; tune?: (w: World) => void; w?: number; h?: number; params?: string };

/** A scene of the tab: the presets the panel keeps, what the tab starts with, what the scene changes in the world. */
function scene(state: string, title: string, o: Make = {}): SceneDef {
  return {
    state,
    tab: "dns",
    title,
    h: 1000,
    ...(o.w && { w: o.w }),
    ...(o.h && { h: o.h }),
    ...(o.params && { params: o.params }),
    setup: (w) => {
      setDnsDemo(o.demo ?? {});
      // the three apps the board names under "Когда дойдёт"
      w.settings.apps = [app("Happ", Platform.IOS), app("Klick", Platform.WINDOWS), app("Mihomo Party", Platform.MACOS)] as never;
      panel(w, o.list ?? boardPresets);
      o.tune?.(w);
    },
  };
}
/** A scene of the editor: the page is taller, the form is long. */
const editor = (state: string, title: string, open: string, o: Make = {}) => scene(state, title, { h: 1700, ...o, demo: { open, ...o.demo } });

/** The list that does not arrive: still loading, or failed (the screen's own query is told, and the client too). */
const stuck = (kind: "load" | "error") => (w: World) => {
  w.qc.removeQueries({ queryKey: dnsPresetsQuery.queryKey });
  w.patch(dns, { listDnsPresets: kind === "load" ? hung : unavailable });
  void w.qc.prefetchQuery({ queryKey: dnsPresetsQuery.queryKey, queryFn: kind === "load" ? hung : unavailable });
};

/** The same scene on the phone, in the light theme and in Persian (right to left). */
const looks = (s: SceneDef): SceneDef[] => [
  { ...s, state: `${s.state}-light`, title: `${s.title}, light`, params: "&theme=light" },
  { ...s, state: `${s.state}-phone`, title: `${s.title}, phone 400`, w: 400, h: s.h && s.h > 1000 ? 2600 : 1700 },
  { ...s, state: `${s.state}-fa`, title: `${s.title}, Persian`, params: "&lang=fa" },
];

const list = scene("dns", "DNS: the board's «список» (five presets, Cloudflare the default)");
const fresh = editor("dns-new", "DNS: the board's «новый» (an empty preset, nothing to get)", "new");
const cf = editor("dns-cf", "DNS: the board's «Cloudflare» (open: the server, DoH, what each app gets)", ids.cf);
const users = scene("dns-users", "DNS: a panel in use (people on the presets, presets of its own, the built-ins)", { list: manyPresets, h: 1300 });
const split = editor("dns-split", "DNS: a preset with split rules (suffixes, a country, servers of their own)", ids.ru, {
  list: [...boardPresets.slice(0, 4), { ...boardPresets[4]!, split: [rule([".ru", ".su", ".xn--p1ai"], [cat("yandex/basic")]), rule([], [cat("alidns/standard"), own("203.0.113.9")], ["cn"])] }],
});

export const dnsScenes: SceneDef[] = [
  // ---- the switch «DNS» of the board
  list,
  fresh,
  cf,
  // ---- the list, beyond the board
  scene("dns-cat", "DNS: a category chosen (No ads)", { list: manyPresets, demo: { cat: String(DnsCategory.NO_ADS) }, h: 700 }),
  scene("dns-just", "DNS: the default was just changed (the row's flash, the new name, the note with Undo)", { demo: { just: ids.q9, nowDefault: "Quad9: защита от вредоносных" }, list: boardPresets.map((p) => ({ ...p, isDefault: p.id === ids.q9 })) }),
  users,
  scene("dns-ads", "DNS: the built-in «No ads on the device» (its tag) among the others", { list: [...boardPresets.slice(0, 3), manyPresets.find((p) => p.id === ids.ads)!], h: 700 }),
  scene("dns-nodef", "DNS: no preset is the default yet", { list: boardPresets.map((p) => ({ ...p, isDefault: false })), h: 800 }),
  scene("dns-empty", "DNS: no presets at all", { list: [], h: 600 }),
  scene("dns-long", "DNS: names and descriptions as long as a person makes them", { list: longPresets, h: 800 }),
  scene("dns-load", "DNS: the presets are being asked for", { tune: stuck("load"), h: 500 }),
  scene("dns-err", "DNS: the panel does not answer", { tune: stuck("error"), h: 500 }),
  scene("dns-helper", "DNS: a helper (the dots work, no «New preset»)", { list: manyPresets, h: 1300 }),
  scene("dns-edge", "DNS: the Edge edition", { list: manyPresets, h: 1300 }),
  // ---- the editor, beyond the board
  editor("dns-own", "DNS: an own preset (a catalog server and a typed one, a split, ads blocked, the delete)", ids.office, { list: manyPresets }),
  editor("dns-dirty", "DNS: changed and not saved (Reset beside Save)", ids.cf, { demo: { draft: { name: "Cloudflare для всех", description: "Быстрый публичный резолвер, без фильтров", ipv4Only: true } } }),
  editor("dns-bad-name", "DNS: a server and no name (the bar says so, Create is off)", "new", { demo: { draft: { servers: [cat("cloudflare/standard"), cat("google/standard")] } } }),
  editor("dns-bad-addr", "DNS: addresses that are not (each under its row, the bar, Create is off)", "new", {
    demo: { draft: { name: "Офис", servers: [own("1.1.1"), own("http://dns.example.com/query", DnsServerKind.DOH), own("dns example", DnsServerKind.DOT), cat("quad9/standard")] } },
  }),
  editor("dns-split-new", "DNS: a split rule just added (empty: it needs a suffix or a country, and a server)", ids.cf, { demo: { draft: { split: [rule([], [])] } } }),
  split,
  editor("dns-catalog", "DNS: the catalog panel open (what is in the list is ticked)", "new", { demo: { catalog: true, draft: { name: "Новый", servers: [cat("cloudflare/standard")] } } }),
  editor("dns-dot", "DNS: asked over DoT (what each app gets: Happ and Xray have none)", ids.cf, { demo: { draft: { transport: DnsTransport.DOT } } }),
  editor("dns-multi", "DNS: several servers (sing-box keeps the first)", ids.cf, { demo: { draft: { servers: [cat("cloudflare/standard"), cat("google/standard"), cat("quad9/standard")] } } }),
  editor("dns-gone", "DNS: a server that is not in the catalog any more", ids.cf, { demo: { draft: { servers: [cat("cloudflare/standard"), cat("retired/standard")] } } }),
  editor("dns-busy", "DNS: the save is on its way", ids.cf, { demo: { busy: true, draft: { name: "Cloudflare для всех" } } }),
  editor("dns-fail", "DNS: the panel refuses the save", ids.cf, { demo: { fail: "the panel does not answer", draft: { name: "Cloudflare для всех" } } }),
  editor("dns-ask", "DNS: the question before a delete (nobody has the preset)", "dns_own_spare", { list: manyPresets, demo: { ask: true } }),
  editor("dns-ask-used", "DNS: the question before a delete (four people have it)", ids.office, { list: manyPresets, demo: { ask: true } }),
  editor("dns-del-default", "DNS: an own preset that is the default (deleting it is not offered)", ids.office, { list: ownDefault }),
  editor("dns-leave", "DNS: the question «Discard the changes?» (Back after a change)", ids.cf, { demo: { leave: true, draft: { name: "Cloudflare для всех" } } }),
  editor("dns-ro-helper", "DNS: a helper reads a preset (the text, nothing to change, nothing to save)", ids.office, { list: manyPresets }),
  editor("dns-long-ed", "DNS: a long name, a long description, long addresses and five servers", ids.long, { list: longPresets }),
  // ---- the same pages on the phone, in the light theme and in Persian
  ...looks(list),
  ...looks(fresh),
  ...looks(cf),
  ...looks(users),
  ...looks(split),
  ...looks(editor("dns-bad-addr-look", "DNS: bad addresses", "new", { demo: { draft: { name: "Офис", servers: [own("1.1.1"), cat("quad9/standard")] } } })),
];
