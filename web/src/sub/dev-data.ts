import type { AmneziaData, AppEntry, AwgDevice, AwgProfile, Device, DnsInfo, DnsPreset, MgData, Platform, ServerEntry } from "./types";

// Sample mg-data for working on the page, one per state. Used ONLY by the dev server (vite.sub.config.ts injects it
// in place of the <!--MG_DATA--> marker for `?case=<name>&lang=ru|en`); it is not imported by the page and never
// ships in web/dist/sub.html. The shapes are what the server sends (subs/page.go, subs/devices.go): the link apps share
// one device without platform or model, a device added without a name comes back as the page named it.
//
// Dev only, besides ?case: `&as=ios|android|windows|macos|linux` pretends the page is open on that device (main.ts), `&fail=<code>`
// makes every self-service and DNS call fail with that code (dev-api.ts).

const now = Math.floor(Date.now() / 1000);
const day = 86400;
const hour = 3600;
const GB = 1024 ** 3;
const url = "https://sub.example.com/s/3f9k2v8q1x7m4c6d9b2n5h8j";

const link = {
  ios: "https://apps.apple.com/us/app/happ-proxy-utility/id6504287215",
  android: "https://play.google.com/store/apps/details?id=com.happproxy",
  windows: "https://github.com/Happ-proxy/happ-desktop/releases/latest/download/setup-Happ.x64.exe",
  macos: "https://github.com/Happ-proxy/happ-desktop/releases/latest/download/Happ.macOS.universal.dmg",
};
// AmneziaVPN as a fresh install has it (subsettings.Defaults): the stores on phones, the site on computers
const amneziaLink = (p: Platform) =>
  p === "ios" ? "https://apps.apple.com/us/app/amneziavpn/id1600529900" : p === "android" ? "https://play.google.com/store/apps/details?id=org.amnezia.vpn" : "https://amnezia.org/downloads";
const happ = (p: Exclude<Platform, "linux">, recommended = true): AppEntry => ({ platform: p, kind: "happ", name: "Happ", download_url: link[p], add_url: `happ://add/${url}`, description: "", recommended });
const amnezia = (p: Platform): AppEntry => ({ platform: p, kind: "amnezia", name: "AmneziaVPN", download_url: amneziaLink(p), add_url: "", description: "", recommended: false });
// the app of the instance for computers: a link app with its own add scheme, first on Windows and Mac
const desktopLink = (p: "windows" | "macos"): AppEntry => ({
  platform: p,
  kind: "happ",
  name: "kl!ck",
  download_url: p === "windows" ? "https://github.com/vbu00/klick/releases/latest" : "https://github.com/vbu00/klick/releases/latest/download/klick-macos.pkg",
  add_url: `klick://add?url=${encodeURIComponent(url)}&name=Mistgate`,
  description: "Все ваши серверы в одном приложении, подписка обновляется сама",
  recommended: true,
});

const phones: Platform[] = ["ios", "android"];
const apps: AppEntry[] = [
  ...phones.map((p) => happ(p as "ios" | "android")),
  desktopLink("windows"),
  { ...happ("windows"), recommended: false },
  desktopLink("macos"),
  { ...happ("macos"), recommended: false },
  ...(["ios", "android", "windows", "macos", "linux"] as Platform[]).map(amnezia),
];
const noLinux = apps.filter((a) => a.platform !== "linux");

// Several apps on one platform without an add scheme: copy the link, then paste it in the app. The name is made up.
const exampleApp: AppEntry = { platform: "windows", kind: "happ", name: "Example Client", download_url: "https://example.com/client/releases/latest", add_url: "", description: "Несколько протоколов в одном приложении", recommended: false };

const dev = (id: string, platform: string, model: string, app: string, ago: number, online = false): Device => ({ id, platform, model, app, last_seen_unix: now - ago, online });
// what the server lists for the link apps: one shared device, whichever apps and however many phones use the link
const shared = (ago: number, online = false) => [dev("d1", "", "", "happ", ago, online)];
const awg = (id: string, platform: string, label: string, ago: number, extra: Partial<AwgDevice> = {}): AwgDevice => ({
  id,
  platform,
  label,
  profile_id: "p31",
  profile_name: "Main",
  version: "3.1",
  address: `10.66.4.${id.length + 4}, fd66:66:0:1::${id.length + 4}`,
  last_handshake_unix: ago < 0 ? 0 : now - ago,
  online: ago >= 0 && ago < 120,
  stale: false,
  stale_reason: "profile",
  min_clients: [
    { client: "amnezia", app: "AmneziaVPN", min: "5.0.1.5" },
    { client: "amnezia", app: "AmneziaWG Android", min: "v3.1.20260814" },
    { client: "mihomo", app: "Mihomo", min: "v1.19.30" },
  ],
  ...extra,
});
const pixel = awg("d3", "android", "Pixel 7", 40);
const macbook = awg("d4", "macos", "MacBook", 12 * day);
const winbox = awg("d5", "windows", "Windows", -1);
// the page lists them among the devices too (app "amnezia"): the page shows them once, in their own rows
const asDevice = (x: AwgDevice): Device => ({ id: x.id, platform: x.platform, model: x.label, app: "amnezia", last_seen_unix: x.last_handshake_unix, online: x.online });

// generic names for the screenshots: an iPhone, a router (platform "other"), an Android phone and a PC
const richKeys = [awg("d3", "ios", "iPhone", 40), awg("d6", "other", "Router", 3 * hour), awg("d7", "android", "Pixel 7", 2 * day), awg("d8", "windows", "Work PC", 9 * day)];

const main: AwgProfile ={ id: "p31", name: "Main", version: "3.1", egress: "direct", countries: ["DE", "FI"] };
// "dev:" = the sample answers the self-service calls itself (dev-api.ts); a real page carries the link's own address
const amz = (devices: AwgDevice[], extra: Partial<AmneziaData> = {}): AmneziaData => ({ devices, profiles: [main], can_add: true, self_service: true, endpoints: "dev:", ...extra });

// ---- servers and DNS ----
const presets: DnsPreset[] = [
  { id: "dns_adblock", name: "AdGuard — без рекламы", description: "Блокирует рекламу и трекеры", category: "no_ads" },
  { id: "dns_standard", name: "Обычный", description: "Cloudflare и Google, без фильтров", category: "regular" },
  { id: "dns_family", name: "Семейный", description: "Без рекламы и сайтов для взрослых", category: "family" },
  { id: "dns_yandex", name: "Яндекс", description: "российские сайты открываются надёжнее", category: "regional" },
];
const dnsOf = (effective: string, options: string[], choice = ""): ServerEntry["dns"] => ({ choice, effective, options, default: "", keys_to_refresh: [] });
const fra: ServerEntry = {
  id: "nod_fra",
  country_code: "DE",
  place: "Франкфурт",
  label: "Германия · Франкфурт",
  app_names: ["DE · Hysteria2", "DE · Hysteria2 WARP"],
  connections: [
    { way: "link", exit: "direct", app_name: "DE · Hysteria2", profile_id: "" },
    { way: "link", exit: "warp", app_name: "DE · Hysteria2 WARP", profile_id: "" },
    { way: "key", exit: "direct", app_name: "", profile_id: "p31" },
  ],
  online: true,
  load: "high",
  dns: dnsOf("dns_adblock", ["dns_adblock", "dns_standard", "dns_family"]),
};
const ams: ServerEntry = {
  id: "nod_ams",
  country_code: "NL",
  place: "",
  label: "Нидерланды",
  app_names: ["NL · Hysteria2"],
  connections: [
    { way: "link", exit: "direct", app_name: "NL · Hysteria2", profile_id: "" },
    { way: "key", exit: "direct", app_name: "", profile_id: "p31" },
  ],
  online: true,
  load: "medium",
  dns: dnsOf("dns_standard", ["dns_standard", "dns_family", "dns_adblock"]),
};
const hel: ServerEntry = { id: "nod_hel", country_code: "FI", place: "", label: "Финляндия", app_names: ["FI · Hysteria2"], connections: [{ way: "link", exit: "direct", app_name: "FI · Hysteria2", profile_id: "" }], online: false, load: null, dns: null };
const ber: ServerEntry = { id: "nod_ber", country_code: "DE", place: "Берлин", label: "Германия · Берлин", app_names: ["DE · Hysteria2 2"], connections: [{ way: "link", exit: "direct", app_name: "DE · Hysteria2 2", profile_id: "" }], online: true, load: null, dns: dnsOf("dns_adblock", ["dns_adblock", "dns_standard"]) };
const mow: ServerEntry = { id: "nod_mow", country_code: "RU", place: "Москва", label: "Россия · Москва", app_names: [], connections: [{ way: "key", exit: "direct", app_name: "", profile_id: "p31" }], online: true, load: "medium", dns: dnsOf("dns_yandex", ["dns_yandex"]) };
const dnsOn: DnsInfo = { enabled: true, endpoint: "dev:", refresh_hours: 12, link: { per_server: false, effective: "dns_adblock" } };

const base: MgData = {
  v: 1,
  lang: "ru",
  brand: { parts: ["mist", "gate"], logo_svg: "", accent: "#b8acf2" },
  title: "Mistgate",
  subscription_url: url,
  server_count: 3,
  server_loads: [
    { name: "Германия · Франкфурт", level: "high" },
    { name: "Нидерланды", level: "medium" },
    { name: "Финляндия", level: "low" },
  ],
  servers: [fra, ams, hel],
  dns: dnsOn,
  dns_presets: presets,
  user: {
    name: "Alice",
    status: "active",
    expires_unix: now + 76 * day,
    used_bytes: 12 * GB,
    quota_bytes: 100 * GB,
    quota_reset: "month",
    next_reset_unix: now + 9 * day,
    device_limit: 3,
    devices_used: 0,
  },
  announcement: "В субботу с 02:00 до 03:00 обновляю серверы — может моргнуть на пару минут.",
  support_url: "https://t.me/example_support",
  options: { show_announcement: true, show_support: true, show_qr: true },
  apps,
  access: { happ: true, amnezia: true },
  devices: [],
  amnezia: amz([]),
  locked: false,
  unlock_url: "",
};

const withUser = (u: Partial<MgData["user"]>): MgData["user"] => ({ ...base.user, ...u });
const keys = (list: AwgDevice[], extra: Partial<AmneziaData> = {}) => amz(list, extra);

// a returning visit: the app fetched the subscription, two keys
const returning: MgData = {
  ...base,
  user: withUser({ expires_unix: now + 45 * day, used_bytes: 64 * GB, next_reset_unix: now + 40 * day, devices_used: 3, device_limit: 4 }),
  announcement: "",
  devices: [...shared(3 * hour, true), asDevice(pixel), asDevice(macbook)],
  amnezia: keys([pixel, macbook]),
};

// the person has keys and the subscription is off: the devices stay, nothing is issued
const off = (user: Partial<MgData["user"]>): MgData => ({
  ...base,
  user: withUser({ device_limit: 3, devices_used: 2, ...user }),
  announcement: "",
  server_count: 0,
  server_loads: [],
  servers: [],
  access: { happ: false, amnezia: false },
  devices: [...shared(2 * day), asDevice({ ...pixel, last_handshake_unix: now - 2 * day, online: false })],
  amnezia: amz([{ ...pixel, last_handshake_unix: now - 2 * day, online: false }], { can_add: false, profiles: [] }),
  dns: { ...dnsOn, endpoint: "" },
});

export const cases: Record<string, MgData> = {
  // a first visit: nothing set up yet
  first: base,
  // a returning visit
  return: returning,
  happ: { ...base, access: { happ: true, amnezia: false }, amnezia: null, devices: shared(30, true), user: withUser({ devices_used: 1 }) },
  both: { ...base, devices: [], amnezia: amz([]) },
  amnezia: { ...base, user: withUser({ devices_used: 2 }), devices: [...shared(30, true), asDevice(pixel)], amnezia: keys([pixel]) },
  // a friend of the AmneziaVPN app alone: no link apps, the keys only
  "keys-only": { ...base, access: { happ: false, amnezia: true }, server_count: 0, user: withUser({ devices_used: 2 }), devices: [pixel, macbook].map(asDevice), amnezia: keys([pixel, macbook]), servers: [{ ...fra, connections: fra.connections.filter((c) => c.way === "key") }, mow] },
  "amnezia-empty": { ...base, user: withUser({ devices_used: 0 }), devices: [], amnezia: amz([]) },
  // the owner issues the keys: no buttons, "ask for a key"
  "amnezia-off": { ...base, user: withUser({ devices_used: 4, device_limit: 8 }), devices: [...shared(30, true), ...[pixel, macbook, winbox].map(asDevice)], amnezia: amz([pixel, macbook, winbox], { self_service: false, can_add: false, endpoints: "" }) },
  // no connection option exists yet
  "amnezia-none": { ...base, devices: [], amnezia: amz([], { profiles: [], can_add: false }) },
  stale: { ...base, user: withUser({ devices_used: 2 }), devices: [...shared(30, true), asDevice(pixel)], amnezia: keys([awg("d3", "android", "Pixel 7", 2 * day, { stale: true, online: false })]) },
  // a key that needs fetching again only because the DNS of its server changed
  "stale-dns": {
    ...returning,
    amnezia: keys([awg("d3", "android", "Pixel 7", 2 * day, { stale: true, stale_reason: "dns", online: false }), macbook]),
    servers: [{ ...fra, dns: { ...fra.dns!, choice: "dns_family", effective: "dns_family", keys_to_refresh: ["d3"] } }, ams, hel],
  },
  // the list a friend complained about: the link apps online and several keys (4 keys + the link = 5 of 10)
  "devices-rich": { ...base, announcement: "", user: withUser({ device_limit: 10, devices_used: 5 }), devices: [...shared(8 * hour, true), ...richKeys.map(asDevice)], amnezia: keys(richKeys) },
  // one slot left
  "devices-near": { ...base, announcement: "", user: withUser({ device_limit: 6, devices_used: 5 }), devices: [...shared(8 * hour, true), ...richKeys.map(asDevice)], amnezia: keys(richKeys) },
  // the owner's preview of the page: no address for the self-service calls
  preview: { ...base, announcement: "", user: withUser({ device_limit: 10, devices_used: 5 }), devices: [...shared(8 * hour, true), ...richKeys.map(asDevice)], amnezia: keys(richKeys, { endpoints: "" }) },
  // every slot taken, with the link and with keys
  "devices-full": { ...base, announcement: "", user: withUser({ device_limit: 5, devices_used: 5 }), devices: [...shared(8 * hour, true), ...richKeys.map(asDevice)], amnezia: keys(richKeys) },
  // every slot taken
  limit:{ ...returning, user: withUser({ device_limit: 3, devices_used: 3 }), amnezia: keys([pixel, macbook]) },
  devices: { ...base, user: withUser({ devices_used: 3 }), devices: [...shared(30, true), asDevice(pixel), asDevice(macbook)], amnezia: keys([pixel, macbook], { can_add: false }) },
  many: {
    ...base,
    user: withUser({ device_limit: 8, devices_used: 4 }),
    devices: [...shared(30, true), ...[pixel, macbook, winbox].map(asDevice)],
    amnezia: keys([pixel, macbook, winbox], {
      profiles: [
        { id: "p20", name: "Old apps", version: "2.0", egress: "direct", countries: ["DE"] },
        { id: "pw", name: "Main · WARP", version: "3.1", egress: "warp", countries: ["DE", "FI"] },
        main,
      ],
    }),
  },
  // several apps per platform on Windows: a link app with a scheme (first), one without, and the key app
  multi: { ...base, apps: [...apps.filter((a) => a.platform !== "windows" || a.kind === "amnezia" || a.recommended), exampleApp], devices: shared(30, true), amnezia: amz([]) },
  // an instance with its own brand and accent, phone apps only and no add scheme (copy the link)
  custom: {
    ...base,
    brand: { parts: ["kitti", "wake"], logo_svg: '<svg viewBox="0 0 10 10"><circle cx="5" cy="5" r="4" fill="#e6a6b0"/></svg>', accent: "#7dd3a0" },
    title: "kittiwake",
    apps: apps.filter((a) => a.platform === "ios" || a.platform === "android").map((a) => ({ ...a, add_url: "" })),
  },
  // the password form (the page answers with nothing but the brand): password abcd-2345 in the dev server
  locked: { ...base, locked: true, unlock_url: "dev:", subscription_url: "", server_count: 0, servers: [], dns: null, dns_presets: [], user: { ...base.user, name: "" }, apps: [], devices: [], announcement: "", support_url: "", amnezia: null },
  // states of the subscription
  soon: { ...returning, announcement: "", user: withUser({ expires_unix: now + 3 * day - 600, used_bytes: 12 * GB, device_limit: 3, devices_used: 2 }), devices: [...shared(3 * hour, true), asDevice(pixel)], amnezia: keys([pixel]) },
  "low-traffic": { ...returning, announcement: "", user: withUser({ used_bytes: 92 * GB, device_limit: 4, devices_used: 3 }) },
  expired: off({ status: "expired", expires_unix: now - 2 * day }),
  quota: off({ status: "limited", used_bytes: 100 * GB, expires_unix: now + 30 * day }),
  disabled: off({ status: "disabled" }),
  // no limit, no term, no support, no QR code
  plain: {
    ...base,
    title: "",
    announcement: "",
    support_url: "",
    server_count: 1,
    options: { show_announcement: false, show_support: false, show_qr: false },
    user: withUser({ expires_unix: 0, quota_bytes: 0, quota_reset: "none", next_reset_unix: 0, used_bytes: 312 * 1024 ** 2, device_limit: 0, devices_used: 0 }),
    devices: [],
    access: { happ: true, amnezia: false },
    amnezia: null,
  },
  // an expired subscription, nobody to write to
  "expired-plain": { ...off({ status: "expired", expires_unix: now - 2 * day }), support_url: "", options: { show_announcement: false, show_support: false, show_qr: false } },
  // used up with no reset rule
  "quota-plain": { ...off({ status: "limited", used_bytes: 100 * GB, expires_unix: now + 30 * day, quota_reset: "none", next_reset_unix: 0 }) },
  // Linux with no app but a link: "no apps for Linux yet" (open with &as=linux)
  nolinux: { ...base, access: { happ: true, amnezia: false }, amnezia: null, apps: noLinux.filter((a) => a.kind === "happ"), devices: [], user: withUser({ devices_used: 0 }) },
  // nothing to connect with at all
  noapps: { ...base, apps: [], access: { happ: false, amnezia: false }, amnezia: null, devices: [] },
  // an older panel: no servers[], no dns; the cards come from server_loads and there is no DNS part
  old: { ...base, servers: [], dns: null, dns_presets: [], devices: shared(30, true), amnezia: amz([pixel]), user: withUser({ devices_used: 2 }) },
  // the DNS choice works for every app that uses the link: no remark above the list
  "per-server": { ...returning, dns: { ...dnsOn, link: { per_server: true, effective: "dns_adblock" } }, servers: [fra, ams, hel, ber, mow] },
  // the owner has not turned the DNS choice on: the rows only say what applies
  "dns-off": { ...returning, dns: { ...dnsOn, enabled: false, endpoint: "" } },
  // server variants: no load level, a key-only server, one that is down
  variants: { ...returning, servers: [fra, ams, ber, mow, hel], dns: { ...dnsOn, link: { per_server: true, effective: "dns_adblock" } } },
  long: {
    ...base,
    title: "Мой очень длинный VPN для друзей и семьи",
    announcement: "Первая строка объявления.\nВторая строка: " + "длинный текст ".repeat(12),
    user: withUser({ name: "Александра-Мария Константиновна", devices_used: 2 }),
    devices: [...shared(30, true), asDevice(awg("d9", "windows", "Рабочий ноутбук в бухгалтерии", 100))],
    amnezia: keys([awg("d9", "windows", "Рабочий ноутбук в бухгалтерии", 100)]),
  },
};

export function devPage(query: string): MgData {
  const p = new URLSearchParams(query);
  const c = cases[p.get("case") ?? ""] ?? base;
  return p.get("lang") === "en" ? { ...c, lang: "en" } : c;
}
