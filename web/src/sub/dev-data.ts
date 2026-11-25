import { isLang } from "./i18n";
import type { AmneziaData, AppEntry, AwgDevice, AwgProfile, Device, DnsInfo, DnsPreset, Lang, MgData, Platform, Qr, ServerEntry } from "./types";

// Sample mg-data for working on the page, one per state. Used ONLY by the dev server (vite.sub.config.ts injects it
// in place of the <!--MG_DATA--> marker for `?case=<name>&lang=ru|en|zh|fa|tr`); it is not imported by the page and never
// ships in web/dist/sub.html. The shapes are what the server sends (subs/page.go, subs/devices.go): the link apps share
// one device without platform or model, a device added without a name comes back as the page named it.
//
// The scenes (dev-scene.ts has the parameters, dev-scene.test.ts the whole list of URLs, with the width to look at each in: 1280 a computer, 390 a phone):
//   states   ?case=return | first | soon | quota (quota-plain: no reset day) | expired | disabled | plain
//   pieces   first (announcement) | long | nosupport | noqr | keys-only | happ | both        lists  servers-2 | -11 | -30;  dev-0 | -3 | -16 | -20 | -nolimit &tab=devices[&more=1]
//   tabs     &tab=telegram|trusttunnel (cases telegram, trusttunnel, and -soon for none yet) [&proxy=2]
//   key window  case=key-one | key-pair | key-many  &sheet=key|made [&profile=p20w] [&list=1]   (one profile; a profile of each version; several of one version)
//   windows  &sheet=pick|link|conn|key|full|made   &key=d3[&country=1]   &renew=d3 (stale)   &menu=d3|l1   &rename=d3   &ask=remove:d3|rotate:d3   &stale=d3
//            &dns=de1|link [&dnsall=0|1] [&dnsdone=1 [&dnsskip=nl1]]   (cases dns-both, dns-link, stale-dns)       password: case=locked[&lockerr=wrong|short|latin|later|net]
//   all      &lang=en|ru|zh|fa|tr  &theme=light|dark  &as=ios|android|windows|macos|linux
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

const dev = (id: string, platform: string, model: string, app: string, ago: number, online = false, kind: Device["kind"] = "link", os_version = "", client = ""): Device => {
  const own = kind !== "shared";
  return { id, platform, model, app, last_seen_unix: now - ago, online, kind, can_remove: own, can_rename: own, os_version, client };
};
// the place the link apps without a device ID share (Hiddify, v2rayNG; Happ with its device ID turned off)
const shared = (ago: number, online = false, client = "") => [dev("d1", "", "", "happ", ago, online, "shared", "", client)];
// the devices of the apps that report a device ID: each one is a device of its own (made-up models)
const iphone = dev("l1", "ios", "iPhone 15", "happ", 40, true, "link", "18.1", "Happ/3.4.2");
const galaxy = dev("l2", "android", "Pixel 8", "happ", 2 * day, false, "link", "15", "Happ/3.4.1");
const desktop = dev("l3", "windows", "DESKTOP-4F7K2LM", "happ", 5 * day, false, "link", "10.0.26100", "mihomo/1.19.31");
const tablet = dev("l4", "ios", "iPad", "happ", 20 * day, false, "link", "17.6", "Happ/3.3.0");
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
  route: "",
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
const asDevice = (x: AwgDevice): Device => ({ ...dev(x.id, x.platform, x.label, "amnezia", 0, x.online, "key"), last_seen_unix: x.last_handshake_unix });

// generic names for the screenshots: an iPhone, a router (platform "other"), an Android phone and a PC
const richKeys = [awg("d3", "ios", "iPhone", 40), awg("d6", "other", "Router", 3 * hour), awg("d7", "android", "Pixel 7", 2 * day), awg("d8", "windows", "Work PC", 9 * day)];

const main: AwgProfile ={ id: "p31", name: "Main", version: "3.1", egress: "direct", countries: ["DE", "FI"] };
// "dev:" = the sample answers the self-service calls itself (dev-api.ts); a real page carries the link's own address
const amz = (devices: AwgDevice[], extra: Partial<AmneziaData> = {}): AmneziaData => ({ devices, profiles: [main], can_add: true, self_service: true, ru_direct: false, endpoints: "dev:", ...extra });

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
  host: "fra1.example.com",
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
  host: "ams1.example.com",
  app_names: ["NL · Hysteria2"],
  connections: [
    { way: "link", exit: "direct", app_name: "NL · Hysteria2", profile_id: "" },
    { way: "key", exit: "direct", app_name: "", profile_id: "p31" },
  ],
  online: true,
  load: "medium",
  dns: dnsOf("dns_standard", ["dns_standard", "dns_family", "dns_adblock"]),
};
const hel: ServerEntry = { id: "nod_hel", country_code: "FI", place: "", label: "Финляндия", host: "hel1.example.com", app_names: ["FI · Hysteria2"], connections: [{ way: "link", exit: "direct", app_name: "FI · Hysteria2", profile_id: "" }], online: false, load: null, dns: null };
const ber: ServerEntry = { id: "nod_ber", country_code: "DE", place: "Берлин", label: "Германия · Берлин", host: "ber1.example.com", app_names: ["DE · Hysteria2 2"], connections: [{ way: "link", exit: "direct", app_name: "DE · Hysteria2 2", profile_id: "" }], online: true, load: null, dns: dnsOf("dns_adblock", ["dns_adblock", "dns_standard"]) };
const mow: ServerEntry = { id: "nod_mow", country_code: "RU", place: "Москва", label: "Россия · Москва", host: "msk1.example.com", app_names: [], connections: [{ way: "key", exit: "direct", app_name: "", profile_id: "p31" }], online: true, load: "medium", dns: dnsOf("dns_yandex", ["dns_yandex"]) };
const dnsOn: DnsInfo = { enabled: true, endpoint: "dev:", refresh_hours: 12, link: { effective: "dns_adblock", choice: "", default: "dns_adblock", options: [] } };

/**
 * A real code (Mistgate's own encoder, internal/qr: a made-up link, level Q, with the logo's window) for every place the page
 * draws one while it has no server to ask: the dev scenes and the tests. It does not read as the value next to it.
 */
export const sampleQr: Qr = {
  n: 41,
  bits: "/s4oHz/BIHyNEG6wvQ6rt1eSC+XboUChIuwXP1vpB/qqqqr+Aa5hLABXufbw9pQFgiVzVMTo2keHIsd/BWS2mUbeRmgUeaKyH8wFcKrTlKLdUFoveD2VQFIYgBnXyOcwkjqPXJviHvoT8ud8h17iNT9qVTGkDZOdErQG/5zAzfCAPoRAqjfknOO+Lq38rdscvbWLzBOrkUaDtqoUnhBSKfM1OZurhrpC+4BbojHE/6Y0mSpQUuyakeuizcKP1dafAydO6SvmiDcFWlTLov4xcgf9AA==",
  window: 9.98,
};

const base: MgData = {
  v: 1,
  lang: "ru",
  brand: { parts: ["mist", "gate"], logo_svg: "", accent: "#b8acf2" },
  title: "Mistgate",
  qr: sampleQr,
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
  devices_endpoint: "dev:",
  // an install that was upgraded counts the devices only; the hwid-* cases show the other modes
  link_device_limit: "track",
  amnezia: amz([]),
  locked: false,
  unlock_url: "",
};

const withUser = (u: Partial<MgData["user"]>): MgData["user"] => ({ ...base.user, ...u });
const ruKey = awg("d9", "ios", "iPhone", 40, { route: "ru-direct" });
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

// a key that is not online, for the lists that must have one device online
const router = awg("d6", "other", "Router", 6 * hour);
// the apps on the link alone, each with its own device (the page of step 6 of the device limit)
const linkOnly = (devices: Device[], user: Partial<MgData["user"]>, mode: MgData["link_device_limit"], extra: Partial<MgData> = {}): MgData => ({
  ...base,
  announcement: "",
  access: { happ: true, amnezia: false },
  amnezia: null,
  devices,
  user: withUser({ devices_used: devices.length, ...user }),
  link_device_limit: mode,
  ...extra,
});

export const cases: Record<string, MgData> = {
  // three devices of their own (one online) and the shared place; every place taken and a new app refused (ENFORCE)
  "hwid-full": linkOnly([iphone, galaxy, desktop, ...shared(9 * day, false, "HiddifyNext/2.5.7")], { device_limit: 4 }, "enforce"),
  // the same, counted only (TRACK): no hint, nobody is refused
  "hwid-track": linkOnly([iphone, galaxy, desktop, ...shared(9 * day, false, "HiddifyNext/2.5.7")], { device_limit: 5 }, "track"),
  // STRICT: no shared place; the row says the apps without a device ID do not connect
  "hwid-strict": linkOnly([iphone, galaxy, desktop], { device_limit: 5 }, "strict"),
  // the dense list: 5 of 5, one online, with a key (every place taken, so nothing folds)
  "hwid-dense": { ...linkOnly([iphone, galaxy, desktop, ...shared(9 * day, false, "HiddifyNext/2.5.7"), asDevice(router)], { device_limit: 5 }, "enforce"), access: { happ: true, amnezia: true }, amnezia: keys([router]) },
  // a long list with room left: it folds to three rows
  "hwid-long": { ...linkOnly([iphone, galaxy, desktop, tablet, ...shared(9 * day, false, "HiddifyNext/2.5.7"), asDevice(router)], { device_limit: 10 }, "enforce"), access: { happ: true, amnezia: true }, amnezia: keys([router]) },
  // a first visit: nothing set up yet
  first: base,
  // a returning visit
  return: returning,
  happ: { ...base, access: { happ: true, amnezia: false }, amnezia: null, devices: shared(30, true), user: withUser({ devices_used: 1 }) },
  both: { ...base, devices: [], amnezia: amz([]) },
  amnezia: { ...base, user: withUser({ devices_used: 2 }), devices: [...shared(30, true), asDevice(pixel)], amnezia: keys([pixel]) },
  // a friend of the AmneziaVPN app alone: no link apps, the keys only
  "keys-only": { ...base, access: { happ: false, amnezia: true }, server_count: 0, user: withUser({ devices_used: 2 }), devices: [pixel, macbook].map(asDevice), amnezia: keys([pixel, macbook]), servers: [{ ...fra, connections: fra.connections.filter((c) => c.way === "key") }, mow] },
  // the owner offers "Russian services direct": two choices in the key form, a key of that route in the list (open with &sheet=key[&route=ru-direct], &key=d9)
  "ru-direct": { ...base, user: withUser({ device_limit: 8, devices_used: 3 }), devices: [...shared(30, true), asDevice(pixel), asDevice(ruKey)], amnezia: amz([pixel, ruKey], { ru_direct: true }) },
  // its list of networks went out of date (&stale=d9)
  "ru-direct-stale": { ...base, user: withUser({ device_limit: 8, devices_used: 3 }), devices: [...shared(30, true), asDevice(pixel), asDevice(ruKey)], amnezia: amz([pixel, { ...ruKey, stale: true, stale_reason: "routes" }], { ru_direct: true }) },
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
  // the owner has not turned the DNS choice on: the rows only say what applies
  "dns-off": { ...returning, dns: { ...dnsOn, enabled: false, endpoint: "" } },
  // server variants: no load level, a key-only server, one that is down
  variants: { ...returning, servers: [fra, ams, ber, mow, hel] },
  // a server that also comes through a relay node in Russia (its twin, "· через RU")
  relay: {
    ...returning,
    servers: [
      { ...ams, app_names: [...ams.app_names, "NL · Hysteria2 · через RU"], connections: [...ams.connections, { way: "link", exit: "direct", app_name: "NL · Hysteria2 · через RU", profile_id: "", relay: true, via: "RU" }] },
      fra,
      hel,
    ],
  },
  long: {
    ...base,
    title: "Мой очень длинный VPN для друзей и семьи",
    announcement: "Первая строка объявления.\nВторая строка: " + "длинный текст ".repeat(12),
    user: withUser({ name: "Александра-Мария Константиновна", devices_used: 2 }),
    devices: [...shared(30, true), asDevice(awg("d9", "windows", "Рабочий ноутбук в бухгалтерии", 100))],
    amnezia: keys([awg("d9", "windows", "Рабочий ноутбук в бухгалтерии", 100)]),
  },
};

// ---- one DNS for the link apps, one per server for the keys (alice, servers de1 and nl1) ----
// The built-in presets as the server sends them: the stock name and description in the page's languages, and `name` /
// `description` in the language of the browser (Accept-Language), which devPage puts in from `lang`.
type Five = [ru: string, en: string, zh: string, fa: string, tr: string];
const stock = (id: string, category: string, name: Five, description: Five): DnsPreset => ({
  id,
  name: name[0],
  description: description[0],
  category,
  names: { ru: name[0], en: name[1], zh: name[2], fa: name[3], tr: name[4] },
  descriptions: { ru: description[0], en: description[1], zh: description[2], fa: description[3], tr: description[4] },
});
const builtins: DnsPreset[] = [
  stock("dns_builtin_standard", "regular", ["Cloudflare + Google", "Cloudflare + Google", "Cloudflare + Google", "Cloudflare + Google", "Cloudflare + Google"], ["Cloudflare и Google для всего", "Cloudflare and Google for everything", "全部使用 Cloudflare 和 Google", "Cloudflare و Google برای همه‌چیز", "Her şey için Cloudflare ve Google"]),
  stock("dns_builtin_adblock", "no_ads", ["AdGuard: без рекламы", "AdGuard: no ads", "AdGuard：无广告", "AdGuard: بدون تبلیغات", "AdGuard: reklamsız"], ["AdGuard DNS: без рекламы и трекеров", "AdGuard DNS: no ads, no trackers", "AdGuard DNS：无广告，无跟踪器", "AdGuard DNS: بدون تبلیغات و ردیاب", "AdGuard DNS: reklam yok, izleyici yok"]),
  stock("dns_builtin_family", "family", ["AdGuard Family: без рекламы и 18+", "AdGuard Family: no ads, no 18+", "AdGuard Family：无广告和成人内容", "AdGuard Family: بدون تبلیغات و محتوای بزرگسالان", "AdGuard Family: reklamsız, 18+ yok"], ["AdGuard Family: без рекламы и сайтов для взрослых", "AdGuard Family: no ads, no adult sites", "AdGuard Family：无广告，无成人网站", "AdGuard Family: بدون تبلیغات و سایت‌های بزرگسالان", "AdGuard Family: reklam yok, yetişkin sitesi yok"]),
  stock("dns_builtin_ru_split", "regional", ["Россия: .ru напрямую", "Russia: .ru direct", "俄罗斯：.ru 直连", "روسیه: سایت‌های روسی مستقیم", "Rusya: .ru doğrudan"], ["Российские сайты напрямую, остальное через VPN", "Russian sites direct, everything else through the VPN", "俄罗斯网站直连，其余走 VPN", "سایت‌های روسی مستقیم، بقیه از طریق VPN", "Rus siteleri doğrudan, geri kalan her şey VPN üzerinden"]),
];
const keyDns = (): ServerEntry["dns"] => ({ choice: "", effective: "dns_builtin_standard", options: ["dns_builtin_standard", "dns_builtin_adblock", "dns_builtin_family"], default: "dns_builtin_standard", keys_to_refresh: [] });
const de1: ServerEntry = {
  id: "de1",
  country_code: "DE",
  place: "",
  label: "Германия",
  host: "de1.example.com",
  app_names: ["DE · Hysteria2", "DE · Hysteria2 WARP"],
  connections: [
    { way: "link", exit: "direct", app_name: "DE · Hysteria2", profile_id: "" },
    { way: "link", exit: "warp", app_name: "DE · Hysteria2 WARP", profile_id: "" },
    { way: "key", exit: "direct", app_name: "", profile_id: "p31" },
  ],
  online: true,
  load: "low",
  dns: keyDns(),
};
const nl1: ServerEntry = {
  id: "nl1",
  country_code: "NL",
  place: "",
  label: "Нидерланды",
  host: "nl1.example.com",
  app_names: ["NL · Hysteria2"],
  connections: [
    { way: "link", exit: "direct", app_name: "NL · Hysteria2", profile_id: "" },
    { way: "key", exit: "direct", app_name: "", profile_id: "p31" },
  ],
  online: true,
  load: "medium",
  dns: keyDns(),
};
const linkOnlyServer = (s: ServerEntry): ServerEntry => ({ ...s, connections: s.connections.filter((x) => x.way === "link") });
const linkDns = (options: string[], choice = ""): DnsInfo => ({
  enabled: true,
  endpoint: "dev:",
  refresh_hours: 12,
  link: { effective: choice || "dns_builtin_standard", choice, options, default: "dns_builtin_standard" },
});
const offered = ["dns_builtin_standard", "dns_builtin_adblock", "dns_builtin_family", "dns_builtin_ru_split"];
const alice = withUser({ name: "alice", devices_used: 2, device_limit: 5 });
const dnsBoth: MgData = {
  ...returning,
  user: alice,
  server_count: 2,
  server_loads: [],
  servers: [de1, nl1],
  dns: linkDns(offered),
  dns_presets: builtins,
  devices: [...shared(3 * hour, true), asDevice(pixel)],
  amnezia: keys([pixel]),
};
cases["dns-both"] = dnsBoth;
// the apps on the link alone: the DNS of the keys means nothing to this person
cases["dns-link"] = { ...dnsBoth, access: { happ: true, amnezia: false }, amnezia: null, devices: [...shared(3 * hour, true), iphone], servers: [linkOnlyServer(de1), linkOnlyServer(nl1)] };
// the owner offers nothing for the link apps: the note about the link apps stays, the keys keep their choice per server
cases["dns-both-off"] = { ...dnsBoth, dns: linkDns([]) };

// an AmneziaWG server that also comes through the entry in Russia: its key stays direct, the Mihomo apps get the twin "· через RU"
// (a link, Mihomo only), and a key through the entry is another config of the same server (open the key with &open=d3, pick the
// second country; dev-api.ts answers it for this case)
const de1Awg: ServerEntry = {
  ...de1,
  app_names: ["DE · AmneziaWG", "DE · AmneziaWG · через RU"],
  connections: [
    { way: "key", exit: "direct", app_name: "", profile_id: "p31" },
    { way: "link", exit: "direct", app_name: "DE · AmneziaWG · через RU", profile_id: "", mihomo_only: true, relay: true, via: "RU" },
  ],
};
cases["awg-relay"] = { ...dnsBoth, servers: [de1Awg, nl1], dns: linkDns(offered) };

// ---- the window "New device → key", by what the owner made: one profile, one of each version, several of a kind ----
// Eleven servers (the second of a country is "DE-2", a place stands for the number, two have an address and no name); the keys of a
// profile hold the servers it lists. Open: ?case=key-one | key-pair | key-many &sheet=key (the form), &sheet=made (the key just made,
// dev-api.ts gives it one config a server of the profile). Phone: width 390; tablet: 820.
const countryRu: Record<string, string> = { by: "Беларусь", de: "Германия", ee: "Эстония", nl: "Нидерланды", pl: "Польша", ro: "Румыния", ru: "Россия" };
// [country, its number among twins (0: alone), place, host, how the link reaches it, whether its keys have a DNS of their own]
const landSpecs: [string, number, string, string, "ru" | "warp" | "", boolean][] = [
  ["by", 0, "", "by1.example.com", "ru", true],
  ["de", 1, "", "de1.example.com", "warp", true],
  ["de", 2, "", "de2.example.com", "warp", true],
  ["de", 3, "", "203.0.113.24", "", false],
  ["ee", 1, "", "est1.example.com", "ru", true],
  ["ee", 2, "", "est2.example.com", "ru", false],
  ["nl", 0, "", "nl1.example.com", "warp", true],
  ["pl", 0, "", "pl1.example.com", "ru", true],
  ["ro", 0, "Бухарест", "ro1.example.com", "warp", true],
  ["ru", 1, "", "msk1.example.com", "", true],
  ["ru", 2, "", "203.0.113.41", "", false],
];
/** The ids of the made-up servers of two countries are spelled out ("est1", "rus2"), so that none reads as the name of a real node. */
const pre: Record<string, string> = { ee: "est", ru: "rus" };
/** The eleven servers; `on[slug]` lists the profiles whose key holds a server ("de1", "ro1": the country and its number). */
const keyLands = (on: Record<string, string[]>): ServerEntry[] =>
  landSpecs.map(([cc, n, place, host, route, ownDns]) => {
    const slug = `${pre[cc] ?? cc}${n || 1}`;
    const name = `${cc.toUpperCase()} · Hysteria2${n > 1 ? ` ${n}` : ""}`;
    return {
      id: `nod_${slug}`,
      country_code: cc.toUpperCase(),
      place,
      label: place ? `${countryRu[cc]} · ${place}` : countryRu[cc] + (n > 1 ? ` ${n}` : ""),
      host,
      app_names: [name],
      connections: [
        { way: "link", exit: route === "warp" ? "warp" : "direct", app_name: name, profile_id: "", ...(route === "ru" ? { relay: true, via: "RU" } : {}) },
        ...(on[slug] ?? []).map((profile_id) => ({ way: "key" as const, exit: "direct" as const, app_name: "", profile_id })),
      ],
      online: true,
      load: "low",
      dns: ownDns ? dnsOf("dns_standard", ["dns_standard", "dns_family"]) : null,
    };
  });
const slugs = landSpecs.map(([cc, n]) => `${pre[cc] ?? cc}${n || 1}`);
/** Every server but the last (it has no key). */
const allBut = (...ps: string[]) => (slug: string) => (slug === "rus2" ? [] : ps);
const onProfiles = (f: (slug: string) => string[]) => Object.fromEntries(slugs.map((x) => [x, f(x)]));
const profile = (id: string, name: string, version: "3.1" | "2.0", egress: AwgProfile["egress"] = "direct"): AwgProfile => ({ id, name, version, egress, countries: [] });
const keyCase = (profiles: AwgProfile[], servers: ServerEntry[]): MgData => ({
  ...base,
  user: withUser({ device_limit: 8, devices_used: 0 }),
  devices: [],
  servers,
  server_count: servers.length,
  server_loads: [],
  amnezia: amz([], { profiles: profiles.map((p) => ({ ...p, countries: [...new Set(servers.filter((x) => x.connections.some((c) => c.profile_id === p.id)).map((x) => x.country_code))] })) }),
});
const forKey = (all: ServerEntry[]) => all.filter((x) => x.connections.some((c) => c.way === "key"));
// one profile on one server: no choice, one card
cases["key-one"] = keyCase([profile("p31", "AWG 3.1", "3.1")], forKey(keyLands(onProfiles((s) => (s === "est1" ? ["p31"] : [])))));
// one profile of each version: the usual good case
cases["key-pair"] = keyCase(
  [profile("p31", "AWG 3.1", "3.1"), profile("p20", "AWG 2.0", "2.0")],
  forKey(keyLands(onProfiles((s) => [...(["de1", "est1", "nl1", "ro1"].includes(s) ? ["p31"] : []), ...(["de1", "nl1", "ro1"].includes(s) ? ["p20"] : [])]))),
);
// several profiles of one version: two new ones, three old ones (the third leaves through the spare exit)
cases["key-many"] = keyCase(
  [profile("p31", "AWG3.1", "3.1"), profile("p31b", "AWG 3.1 (new)", "3.1"), profile("p20", "AWG2", "2.0"), profile("p20b", "AWG 2.0 (new)", "2.0"), profile("p20w", "AWG 2.0 (new) · WARP", "2.0", "warp")],
  keyLands(onProfiles((s) => [...allBut("p31", "p20")(s), ...(["est1", "est2"].includes(s) ? ["p31b", "p20b"] : []), ...(s === "est2" ? ["p20w"] : [])])),
);

// the Telegram proxy: one card per tg://proxy link, the twins of a server's link (its IPv6 entry, a relay in RU) marked
const tgUrl = (host: string, port = 443) => `tg://proxy?server=${host}&port=${port}&secret=ee0123456789abcdef0123456789abcdef7777772e676f6f676c652e636f6d`;
const tgFra = { label: "Германия · Франкфурт", country_code: "DE", place: "Франкфурт", ipv6: false, relay: false, via: "", url: tgUrl("de1.example.com"), qr: sampleQr };
const tgLinks = [tgFra, { ...tgFra, ipv6: true, url: tgUrl("[2001:db8::7]") }, { ...tgFra, relay: true, via: "RU", url: tgUrl("198.51.100.20", 8443) }, { label: "Финляндия", country_code: "FI", place: "", ipv6: false, relay: false, via: "", url: tgUrl("fi1.example.com"), qr: sampleQr }];
// the person's Telegram key among the devices: issued by the admin, used by Telegram two hours ago
const tgKey = { ...dev("tg1", "", "Telegram proxy", "telegram", 2 * hour, false, "telegram"), can_remove: false, can_rename: false };
cases.telegram = { ...returning, access: { ...returning.access, telegram: true }, telegram: { links: tgLinks }, devices: [...returning.devices, tgKey] };
// a first visit with Telegram on: the steps first, then the servers and the proxy
cases["telegram-first"] = { ...base, access: { ...base.access, telegram: true }, telegram: { links: tgLinks.slice(0, 2) } };
// Telegram is on, no MTProto profile runs yet
cases["telegram-soon"] = { ...returning, access: { ...returning.access, telegram: true }, telegram: { links: [] } };

// TrustTunnel: one card per tt:// link (the secret is made up; a real one is about this long, so the QR code has the density a
// person will see), the twins of a server marked ("IPv6" on the exit, a relay in RU), and a server with no link (a self-signed
// certificate: only the Mistgate app trusts it) as a card that says so
const ttUrl = (name: string) => `tt://?AAEBAQ9kZTEuZXhhbXBsZS5jb20FEHVhbGljZTIzNDU2NzIzNDUGIGFCMjM0NTY3Y0QyMzQ1NjdlRjIzNDU2N2dIMjM0NTY3AhNkZTEuZXhhbXBsZS5jb206NDQzBAEACQEBDA${name}DRYKMTkyLjAuMi41MwoxOTIuMC4yLjU0`;
const ttDe = { label: "Германия · Франкфурт", country_code: "DE", place: "Франкфурт", has_ipv6: false, relay: false, via: "", note: "", url: ttUrl("DZGUx"), qr: sampleQr };
const ttLinks = [
  ttDe,
  { ...ttDe, relay: true, via: "RU", url: ttUrl("DZGUy") },
  { label: "Нидерланды", country_code: "NL", place: "", has_ipv6: true, relay: false, via: "", note: "", url: ttUrl("DbmwxA"), qr: sampleQr },
  { label: "Финляндия", country_code: "FI", place: "", has_ipv6: false, relay: false, via: "", note: "self_signed", url: "", qr: null },
];
// the TrustTunnel key among the devices: issued by the admin, used by an app yesterday
const ttKey = { ...dev("tt1", "", "TrustTunnel key", "trusttunnel", day, false, "trusttunnel"), can_remove: false, can_rename: false };
cases.trusttunnel = { ...returning, access: { ...returning.access, trust_tunnel: true }, trust_tunnel: { links: ttLinks }, devices: [...returning.devices, ttKey] };
// a first visit with it on: the steps first, then the servers and the key
cases["trusttunnel-first"] = { ...base, access: { ...base.access, trust_tunnel: true }, trust_tunnel: { links: ttLinks.slice(0, 2) } };
// on, but no TrustTunnel profile runs yet
cases["trusttunnel-soon"] = { ...returning, access: { ...returning.access, trust_tunnel: true }, trust_tunnel: { links: [] } };
// the subscription is off: the page shows the devices only, the key among them (no block, no links)
cases["trusttunnel-disabled"] = { ...off({ status: "disabled" }), access: { happ: false, amnezia: false, trust_tunnel: true }, trust_tunnel: { links: [] }, devices: [...shared(2 * day), ttKey] };

// ---- the scenes: lists of every size, and the pieces a page may lack (dev-scene.ts has the URL parameters, dev-scene.test.ts the list of URLs) ----
// 2, 11 and 30 servers (de1, nl1, fi1, ...; every third has a WARP exit, one in four comes through the entry in Russia, the last of eleven is down)
const lands = ["DE", "NL", "FI", "EE", "PL", "RO", "SE", "FR", "GB", "ES"];
const crowdServer = (i: number): ServerEntry => {
  const cc = lands[i % lands.length]!;
  const n = Math.floor(i / lands.length) + 1;
  const id = `${cc.toLowerCase()}${n}`;
  const name = `${cc} · Hysteria2${n > 1 ? ` ${n}` : ""}`;
  const key = i % 5 !== 4;
  return {
    id,
    country_code: cc,
    place: "",
    label: n > 1 ? `${cc} ${n}` : cc,
    host: `${id}.example.com`,
    app_names: [name, ...(i % 3 === 1 ? [`${name} WARP`] : [])],
    connections: [
      { way: "link", exit: "direct", app_name: name, profile_id: "" },
      ...(i % 3 === 1 ? [{ way: "link" as const, exit: "warp" as const, app_name: `${name} WARP`, profile_id: "" }] : []),
      ...(i % 4 === 2 ? [{ way: "link" as const, exit: "direct" as const, app_name: `${name} · через RU`, profile_id: "", relay: true, via: "RU" }] : []),
      ...(key ? [{ way: "key" as const, exit: "direct" as const, app_name: "", profile_id: "p31" }] : []),
    ],
    online: i % 11 !== 10,
    load: (["low", "medium", "high", null] as const)[i % 4]!,
    dns: key ? dnsOf("dns_standard", ["dns_standard", "dns_family", "dns_adblock"]) : null,
  };
};
const crowdServers = (n: number): MgData => ({ ...returning, servers: Array.from({ length: n }, (_, i) => crowdServer(i)), server_count: n, server_loads: [] });
cases["servers-2"] = crowdServers(2);
cases["servers-11"] = crowdServers(11);
cases["servers-30"] = crowdServers(30);

// 0, 3, 16 and 20 of 20 devices on the link apps (made-up models), and a subscription without a limit
const gadgets: [string, string][] = [
  ["ios", "iPhone 15"], ["android", "Pixel 8"], ["windows", "DESKTOP-4F7K2LM"], ["ios", "iPad"], ["macos", "MacBook Air"], ["android", "Galaxy Tab"], ["windows", "Work laptop"], ["android", "Fold 5"],
  ["ios", "Spare iPhone"], ["windows", "Mini PC"], ["android", "Kitchen tablet"], ["macos", "Mac mini"], ["ios", "iPhone 13"], ["windows", "Game PC"], ["android", "Travel phone"], ["linux", "Steam Deck"],
  ["android", "Guest phone"], ["windows", "Surface"], ["ios", "iPad mini"], ["android", "Old Android"],
];
const crowd = (n: number): Device[] => gadgets.slice(0, n).map(([p, m], i) => dev(`c${i + 1}`, p, m, "happ", (i + 1) * 7 * hour, i === 0, "link", "18.1", "Happ/3.4.2"));
const linkSide = { access: { happ: true, amnezia: false }, amnezia: null };
const withDevices = (n: number, limit: number, extra: Partial<MgData> = {}): MgData => ({ ...returning, ...linkSide, user: withUser({ device_limit: limit, devices_used: n }), devices: crowd(n), ...extra });
cases["dev-0"] = withDevices(0, 20, { announcement: "" });
cases["dev-3"] = withDevices(3, 20);
cases["dev-16"] = withDevices(16, 20);
// every place taken and a new app refused (ENFORCE): "no place left" in the window "New device"
cases["dev-20"] = withDevices(20, 20, { link_device_limit: "enforce" });
cases["dev-nolimit"] = withDevices(5, 0);

// a page without the way to the support, and one without the QR code of the page
cases.nosupport = { ...returning, support_url: "", options: { show_announcement: true, show_support: false, show_qr: true } };
cases.noqr = { ...base, options: { show_announcement: true, show_support: true, show_qr: false } };

export function devPage(query: string): MgData {
  const p = new URLSearchParams(query);
  const c = cases[p.get("case") ?? ""] ?? base;
  const lang = p.get("lang");
  const out: MgData = isLang(lang) ? ownWords({ ...c, lang }) : c;
  // the server words the built-in presets in the language of the browser; the page may be switched to another one
  return { ...out, dns_presets: out.dns_presets.map((x) => (x.names ? { ...x, name: x.names[out.lang] ?? x.name, description: x.descriptions?.[out.lang] ?? x.description } : x)) };
}

// What the owner typed (places, the announcement, an app's description, their own DNS presets) is Russian in the cases above; the
// other pages show it in the page's language, as an owner of such a page would write it. Dev only.
const own: Record<string, Partial<Record<Lang, string>>> = {
  "Франкфурт": { en: "Frankfurt", fa: "فرانکفورت", tr: "Frankfurt" },
  "Германия · Франкфурт": { en: "Germany · Frankfurt", fa: "آلمان · فرانکفورت", tr: "Almanya · Frankfurt" },
  "Берлин": { en: "Berlin", fa: "برلین", tr: "Berlin" },
  "Германия · Берлин": { en: "Germany · Berlin", fa: "آلمان · برلین", tr: "Almanya · Berlin" },
  "Москва": { en: "Moscow", fa: "مسکو", tr: "Moskova" },
  "Бухарест": { en: "Bucharest", fa: "بخارست", tr: "Bükreş" },
  "Россия · Москва": { en: "Russia · Moscow", fa: "روسیه · مسکو", tr: "Rusya · Moskova" },
  "Нидерланды": { en: "Netherlands", fa: "هلند", tr: "Hollanda" },
  "Финляндия": { en: "Finland", fa: "فنلاند", tr: "Finlandiya" },
  "В субботу с 02:00 до 03:00 обновляю серверы — может моргнуть на пару минут.": {
    en: "On Saturday from 02:00 to 03:00 I am updating the servers — the connection may drop for a couple of minutes.",
    fa: "شنبه از ساعت 02:00 تا 03:00 سرورها را به‌روز می‌کنم — ممکن است اتصال چند دقیقه قطع و وصل شود.",
    tr: "Cumartesi 02:00–03:00 arasında sunucuları güncelliyorum — bağlantı birkaç dakika kesilebilir.",
  },
  "Все ваши серверы в одном приложении, подписка обновляется сама": {
    en: "All your servers in one app, the subscription updates itself",
    fa: "همهٔ سرورهای شما در یک برنامه، اشتراک خودش به‌روز می‌شود",
    tr: "Tüm sunucularınız tek uygulamada, abonelik kendiliğinden güncellenir",
  },
  "Несколько протоколов в одном приложении": { en: "Several protocols in one app", fa: "چند پروتکل در یک برنامه", tr: "Tek uygulamada birden çok protokol" },
  "AdGuard — без рекламы": { en: "AdGuard — no ads", fa: "AdGuard — بدون تبلیغات", tr: "AdGuard — reklamsız" },
  "Блокирует рекламу и трекеры": { en: "Blocks ads and trackers", fa: "تبلیغات و ردیاب‌ها را مسدود می‌کند", tr: "Reklamları ve izleyicileri engeller" },
  "Обычный": { en: "Regular", fa: "معمولی", tr: "Standart" },
  "Cloudflare и Google, без фильтров": { en: "Cloudflare and Google, no filtering", fa: "Cloudflare و Google، بدون فیلتر", tr: "Cloudflare ve Google, filtresiz" },
  "Семейный": { en: "Family", fa: "خانوادگی", tr: "Aile" },
  "Без рекламы и сайтов для взрослых": { en: "No ads, no adult sites", fa: "بدون تبلیغات و سایت‌های بزرگسالان", tr: "Reklam ve yetişkin sitesi yok" },
  "Яндекс": { en: "Yandex", fa: "Yandex", tr: "Yandex" },
  "российские сайты открываются надёжнее": { en: "Russian sites open more reliably", fa: "سایت‌های روسی مطمئن‌تر باز می‌شوند", tr: "Rus siteleri daha güvenilir açılır" },
};
function ownWords(d: MgData): MgData {
  let json = JSON.stringify(d);
  for (const [ru, to] of Object.entries(own)) {
    const w = to[d.lang];
    if (w) json = json.split(JSON.stringify(ru)).join(JSON.stringify(w));
  }
  return JSON.parse(json) as MgData;
}
