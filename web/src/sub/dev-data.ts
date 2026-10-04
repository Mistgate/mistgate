import type { AmneziaData, AppEntry, AwgDevice, AwgProfile, Device, MgData, Platform } from "./types";

// Sample mg-data for working on the page, one per state. Used ONLY by the dev server (vite.sub.config.ts injects it
// in place of the <!--MG_DATA--> marker for `?case=<name>&lang=ru|en`); it is not imported by the page and never
// ships in web/dist/sub.html. The shapes are what the server sends (subs/page.go, subs/devices.go): the link apps share
// one device without platform or model, a device added without a name comes back as the page named it.

const now = Math.floor(Date.now() / 1000);
const day = 86400;
const GB = 1024 ** 3;
const url = "https://sub.example.com/s/3f9k2v8q1x7m4c6d9b2n5h8j";

const link = {
  ios: "https://apps.apple.com/app/happ-proxy-utility/id6504287215",
  android: "https://play.google.com/store/apps/details?id=com.happproxy",
  windows: "https://github.com/Happ-proxy/happ-desktop/releases/latest/download/setup-Happ.x64.exe",
  macos: "https://github.com/Happ-proxy/happ-desktop/releases/latest/download/Happ.macOS.universal.dmg",
};
// AmneziaVPN as a fresh install has it (subsettings.Defaults): the stores on phones, the site on computers
const amneziaLink = (p: Platform) =>
  p === "ios" ? "https://apps.apple.com/us/app/amneziavpn/id1600529900" : p === "android" ? "https://play.google.com/store/apps/details?id=org.amnezia.vpn" : "https://amnezia.org/downloads";

const platforms: Platform[] = ["ios", "android", "windows", "macos"];
const apps: AppEntry[] = [
  ...platforms.map((p) => ({
    platform: p,
    kind: "happ" as const,
    name: "Happ",
    download_url: link[p as keyof typeof link],
    add_url: `happ://add/${url}`,
    description: "",
    recommended: true,
  })),
  ...([...platforms, "linux"] as Platform[]).map((p) => ({
    platform: p,
    kind: "amnezia" as const,
    name: "AmneziaVPN",
    download_url: amneziaLink(p),
    add_url: "",
    description: "",
    recommended: false,
  })),
];

// Several apps on one platform, as an owner sets them up: a link app with an add-link scheme (recommended), a link app
// without one (copy the link, then paste it in the app) and the per-device key app. The names are made up.
const desktopApps = (p: Platform): AppEntry[] => [
  { platform: p, kind: "happ", name: "Happ", download_url: link.windows, add_url: `happ://add/${url}`, description: "Hysteria2 по ссылке подписки, обновляется сама", recommended: true },
  { platform: p, kind: "happ", name: "Example Client", download_url: "https://example.com/client/releases/latest", add_url: "", description: "Несколько протоколов в одном приложении", recommended: false },
  { platform: p, kind: "amnezia", name: "AmneziaVPN", download_url: amneziaLink(p), add_url: "", description: "", recommended: false },
];
const multiApps: AppEntry[] = [...apps.filter((a) => a.platform === "ios" || a.platform === "android"), ...(["windows", "macos"] as Platform[]).flatMap(desktopApps)];

const dev = (id: string, platform: string, model: string, app: string, ago: number, online = false): Device => ({
  id,
  platform,
  model,
  app,
  last_seen_unix: now - ago,
  online,
});
// what the server lists for the link apps: one shared device, whichever apps and however many phones use the link
const shared = [dev("d1", "", "", "happ", 30, true)];
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
  min_clients: [
    { client: "amnezia", app: "AmneziaVPN", min: "5.0.1.5" },
    { client: "amnezia", app: "AmneziaWG Android", min: "v3.1.20260814" },
    { client: "mihomo", app: "Mihomo", min: "v1.19.30" },
  ],
  ...extra,
});
const awgDevices = [awg("d3", "android", "Pixel 7", 40), awg("d4", "macos", "MacBook", 12 * day), awg("d5", "windows", "Windows", -1)];
// the page lists them among the devices too (app "amnezia"): the page shows them once, in their own rows
const asDevice = (x: AwgDevice): Device => ({ id: x.id, platform: x.platform, model: x.label, app: "amnezia", last_seen_unix: x.last_handshake_unix, online: x.online });

const main: AwgProfile = { id: "p31", name: "Main", version: "3.1", egress: "direct", countries: ["DE", "FI"] };
// "dev:" = the sample answers the self-service calls itself (dev-api.ts); a real page carries the link's own address
const amz = (devices: AwgDevice[], extra: Partial<AmneziaData> = {}): AmneziaData => ({
  devices,
  profiles: [main],
  can_add: true,
  self_service: true,
  endpoints: "dev:",
  ...extra,
});

const base: MgData = {
  v: 1,
  lang: "ru",
  brand: { parts: ["mist", "gate"], logo_svg: "", accent: "#b8acf2" },
  title: "Mistgate",
  subscription_url: url,
  server_count: 3,
  server_loads: [
    { name: "Эстония", load_percent: 86, rx_bps: 86_000_000, tx_bps: 72_000_000, capacity_mbps: 100 },
    { name: "Германия", load_percent: 42, rx_bps: 42_000_000, tx_bps: 37_000_000, capacity_mbps: 100 },
    { name: "Россия", load_percent: 18, rx_bps: 18_000_000, tx_bps: 14_000_000, capacity_mbps: 100 },
  ],
  user: {
    name: "Лена",
    status: "active",
    expires_unix: now + 76 * day,
    used_bytes: 12 * GB,
    quota_bytes: 100 * GB,
    quota_reset: "month",
    next_reset_unix: now + 9 * day,
    device_limit: 3,
    devices_used: 1,
  },
  announcement: "В субботу с 02:00 до 03:00 обновляю серверы — может моргнуть на пару минут.",
  support_url: "https://t.me/example_support",
  options: { show_announcement: true, show_support: true, show_qr: true },
  apps,
  access: { happ: true, amnezia: false },
  devices: shared,
  amnezia: null,
  locked: false,
  unlock_url: "",
};

const both = { ...base.access, amnezia: true };
const withUser = (u: Partial<MgData["user"]>): MgData["user"] => ({ ...base.user, ...u });

export const cases: Record<string, MgData> = {
  happ: base,
  both: { ...base, access: both, devices: shared, amnezia: amz([]) },
  amnezia: {
    ...base,
    access: both,
    user: withUser({ devices_used: 2 }),
    devices: [...shared, ...awgDevices.slice(0, 1).map(asDevice)],
    amnezia: amz(awgDevices.slice(0, 1), { can_add: true }),
  },
  // a friend of the AmneziaVPN app alone: one way, the whole width
  "keys-only": {
    ...base,
    access: { happ: false, amnezia: true },
    server_count: 0,
    user: withUser({ devices_used: 2 }),
    devices: awgDevices.slice(0, 2).map(asDevice),
    amnezia: amz(awgDevices.slice(0, 2)),
  },
  "amnezia-empty": { ...base, access: both, user: withUser({ devices_used: 0 }), devices: [], amnezia: amz([]) },
  "amnezia-off": { ...base, access: both, user: withUser({ devices_used: 4 }), devices: [...shared, ...awgDevices.map(asDevice)], amnezia: amz(awgDevices, { self_service: false, can_add: false, endpoints: "" }) },
  "amnezia-none": { ...base, access: both, devices: [], amnezia: amz([], { profiles: [], can_add: false }) },
  stale: {
    ...base,
    access: both,
    user: withUser({ devices_used: 2 }),
    devices: [...shared, asDevice(awgDevices[0]!)],
    amnezia: amz([awg("d3", "android", "Pixel 7", 40, { stale: true, online: false })]),
  },
  devices: {
    ...base,
    access: both,
    user: withUser({ devices_used: 3 }),
    devices: [...shared, ...awgDevices.slice(0, 2).map(asDevice)],
    amnezia: amz(awgDevices.slice(0, 2), { can_add: false }),
  },
  many: {
    ...base,
    access: both,
    user: withUser({ device_limit: 8, devices_used: 4 }),
    devices: [...shared, ...awgDevices.map(asDevice)],
    amnezia: amz(awgDevices, {
      profiles: [
        { id: "p20", name: "Old apps", version: "2.0", egress: "direct", countries: ["DE"] },
        { id: "pw", name: "Main · WARP", version: "3.1", egress: "warp", countries: ["DE", "FI"] },
        main,
      ],
    }),
  },
  // several apps per platform (Windows and Mac): link app with a scheme, link app without, key app
  multi: { ...base, access: both, apps: multiApps, devices: shared, amnezia: amz([]) },
  // the password form (the page answers with nothing but the brand): password abcd-2345 in the dev server
  locked: { ...base, locked: true, unlock_url: "dev:", subscription_url: "", server_count: 0, user: { ...base.user, name: "" }, apps: [], devices: [], announcement: "", support_url: "" },
  expired: { ...base, user: withUser({ status: "expired", expires_unix: now - 2 * day }), devices: [] },
  quota: { ...base, user: withUser({ status: "limited", used_bytes: 100 * GB }) },
  disabled: { ...base, user: withUser({ status: "disabled" }) },
  plain: {
    ...base,
    title: "",
    announcement: "",
    support_url: "",
    server_count: 1,
    options: { show_announcement: false, show_support: false, show_qr: false },
    user: withUser({ expires_unix: 0, quota_bytes: 0, quota_reset: "none", next_reset_unix: 0, used_bytes: 312 * 1024 ** 2, device_limit: 0, devices_used: 0 }),
    devices: [],
  },
  long: {
    ...base,
    title: "Мой очень длинный VPN для друзей и семьи",
    announcement: "Первая строка объявления.\nВторая строка: " + "длинный текст ".repeat(12),
    access: both,
    user: withUser({ name: "Александра-Екатерина Константинопольская", devices_used: 2 }),
    devices: [...shared, asDevice(awg("d9", "windows", "Рабочий ноутбук в бухгалтерии", 100))],
    amnezia: amz([awg("d9", "windows", "Рабочий ноутбук в бухгалтерии", 100)]),
  },
  custom: {
    ...base,
    brand: {
      parts: ["kitti", "wake"],
      logo_svg: '<svg viewBox="0 0 10 10"><circle cx="5" cy="5" r="4" fill="#e6a6b0"/></svg>',
      accent: "#7dd3a0",
    },
    title: "kittiwake",
    apps: apps.filter((a) => a.platform === "ios" || a.platform === "android").map((a) => ({ ...a, add_url: "" })),
  },
};

export function devPage(query: string): MgData {
  const p = new URLSearchParams(query);
  const c = cases[p.get("case") ?? ""] ?? base;
  return p.get("lang") === "en" ? { ...c, lang: "en" } : c;
}
