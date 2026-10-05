import { dict, type Dict } from "./i18n";
import type {
  AmneziaData,
  AppEntry,
  AwgConfig,
  AwgDevice,
  AwgProfile,
  Device,
  DnsInfo,
  DnsPreset,
  Kind,
  Lang,
  LoadLevel,
  MgData,
  MinClient,
  Platform,
  ServerConn,
  ServerDns,
  ServerEntry,
  ServerLoad,
  Status,
  Theme,
} from "./types";

// Pure functions of the page data: normalisation, platform detection, the numbers in the status card, the server cards.
// No DOM here.

export const platformOrder: Platform[] = ["ios", "android", "windows", "macos", "linux"];
const kindOrder: Kind[] = ["happ", "amnezia"];
const statuses: Status[] = ["active", "expired", "limited", "disabled"];
const levels: LoadLevel[] = ["low", "medium", "high"];
const resets = ["none", "day", "week", "month", "rolling_month"] as const;
const day = 86400;

/** "Soon" and "nearly used up": the page's own thresholds (the server sends none). */
export const soonDays = 7;
export const lowTraffic = 90;

// --- reading mg-data ---------------------------------------------------------------------------------------

const str = (v: unknown, d = "") => (typeof v === "string" ? v : d);
const num = (v: unknown) => (typeof v === "number" && Number.isFinite(v) && v > 0 ? v : 0);
const rec = (v: unknown): Record<string, unknown> => (v && typeof v === "object" ? (v as Record<string, unknown>) : {});
const list = (v: unknown): Record<string, unknown>[] => (Array.isArray(v) ? v.map(rec) : []);
const strs = (v: unknown): string[] => (Array.isArray(v) ? v.map((x) => str(x)).filter(Boolean) : []);

const minClients = (v: unknown): MinClient[] => list(v).map((m) => ({ client: str(m.client), app: str(m.app), min: str(m.min) })).filter((m) => m.app !== "");

/** An AmneziaWG device as the page data and the endpoints describe it; a missing field gets a safe default. */
export function awgDevice(raw: unknown): AwgDevice {
  const d = rec(raw);
  return {
    id: str(d.id),
    platform: str(d.platform),
    label: str(d.label),
    profile_id: str(d.profile_id),
    profile_name: str(d.profile_name),
    version: str(d.version) === "2.0" ? "2.0" : "3.1",
    address: str(d.address),
    last_handshake_unix: num(d.last_handshake_unix),
    online: d.online === true,
    stale: d.stale === true,
    stale_reason: d.stale_reason === "dns" ? "dns" : "profile",
    min_clients: minClients(d.min_clients),
  };
}

/** One config answered by the endpoints; the strings are used as text only. The server's public name is `label` (country + place); the panel's node name is never read. */
export function awgConfig(raw: unknown): AwgConfig {
  const c = rec(raw);
  return {
    node_id: str(c.node_id),
    label: str(c.label),
    country_code: str(c.country_code),
    version: str(c.version),
    conf: str(c.conf),
    vpn_key: str(c.vpn_key),
    filename: str(c.filename),
    stale: c.stale === true,
    warnings: strs(c.warnings),
    min_clients: minClients(c.min_clients),
  };
}

const awgProfile = (p: Record<string, unknown>): AwgProfile => ({
  id: str(p.id),
  name: str(p.name),
  version: str(p.version),
  egress: p.egress === "warp" ? "warp" : "direct",
  countries: strs(p.countries),
});

export const isHex = (v: string) => /^#[0-9a-f]{6}$/i.test(v);

const conn = (c: Record<string, unknown>): ServerConn => ({
  way: c.way === "key" ? "key" : "link",
  exit: c.exit === "warp" ? "warp" : "direct",
  app_name: str(c.app_name),
  profile_id: str(c.profile_id),
  mihomo_only: c.mihomo_only === true,
});

/** The DNS part of one server; null when the server has no choice. */
export function serverDns(raw: unknown): ServerDns | null {
  if (!raw || typeof raw !== "object") return null;
  const x = rec(raw);
  return { choice: str(x.choice), effective: str(x.effective), options: strs(x.options), default: str(x.default), keys_to_refresh: strs(x.keys_to_refresh) };
}

/** One element of `servers[]`, as the page data and the DNS answer carry it. */
export function serverEntry(raw: unknown): ServerEntry {
  const s = rec(raw);
  return {
    id: str(s.id),
    country_code: str(s.country_code).trim().toUpperCase(),
    place: str(s.place),
    label: str(s.label),
    app_names: strs(s.app_names),
    connections: list(s.connections).map(conn),
    online: s.online === true,
    load: levels.includes(s.load as LoadLevel) ? (s.load as LoadLevel) : null,
    dns: serverDns(s.dns),
  };
}

const dnsPreset = (p: Record<string, unknown>): DnsPreset => ({ id: str(p.id), name: str(p.name), description: str(p.description), category: str(p.category) });

function dnsInfo(raw: unknown): DnsInfo | null {
  if (!raw || typeof raw !== "object") return null;
  const x = rec(raw);
  const link = rec(x.link);
  return {
    enabled: x.enabled === true,
    endpoint: str(x.endpoint),
    refresh_hours: num(x.refresh_hours) || 12,
    link: { per_server: link.per_server === true, effective: str(link.effective) },
  };
}

/** Turns whatever the server embedded into a complete MgData: a missing or malformed field gets a safe default. */
export function normalize(raw: unknown): MgData {
  const r = rec(raw);
  const brand = rec(r.brand);
  const parts = Array.isArray(brand.parts) ? brand.parts.map((p) => str(p)) : [];
  const u = rec(r.user);
  const o = rec(r.options);
  const acc = rec(r.access);
  const am = r.amnezia && typeof r.amnezia === "object" ? rec(r.amnezia) : null;
  const amnezia: AmneziaData | null = am
    ? {
        devices: list(am.devices).map(awgDevice).filter((x) => x.id !== ""),
        profiles: list(am.profiles).map(awgProfile).filter((p) => p.id !== ""),
        can_add: am.can_add === true,
        self_service: am.self_service === true,
        endpoints: str(am.endpoints),
      }
    : null;
  return {
    v: num(r.v) || 1,
    lang: r.lang === "en" ? "en" : "ru",
    brand: {
      parts: [parts[0] ?? "", parts[1] ?? ""],
      logo_svg: str(brand.logo_svg),
      accent: isHex(str(brand.accent)) ? str(brand.accent) : "",
    },
    title: str(r.title),
    subscription_url: str(r.subscription_url),
    server_count: num(r.server_count),
    server_loads: list(r.server_loads).flatMap((s): ServerLoad[] =>
      typeof s.name === "string" && s.name.trim() && levels.includes(s.level as LoadLevel) ? [{ name: s.name, level: s.level as LoadLevel }] : [],
    ),
    servers: list(r.servers).map(serverEntry).filter((s) => s.country_code !== "" || s.label !== ""),
    dns: dnsInfo(r.dns),
    dns_presets: list(r.dns_presets).map(dnsPreset).filter((p) => p.id !== "" && p.name !== ""),
    user: {
      name: str(u.name),
      status: statuses.includes(u.status as Status) ? (u.status as Status) : "active",
      expires_unix: num(u.expires_unix),
      used_bytes: num(u.used_bytes),
      quota_bytes: num(u.quota_bytes),
      quota_reset: resets.find((x) => x === u.quota_reset) ?? "none",
      next_reset_unix: num(u.next_reset_unix),
      device_limit: num(u.device_limit),
      devices_used: num(u.devices_used),
    },
    announcement: str(r.announcement).trim(),
    support_url: str(r.support_url),
    options: { show_announcement: o.show_announcement !== false, show_support: o.show_support !== false, show_qr: o.show_qr !== false },
    apps: list(r.apps).flatMap((a): AppEntry[] =>
      platformOrder.includes(a.platform as Platform) && kindOrder.includes(a.kind as Kind)
        ? [{ platform: a.platform as Platform, kind: a.kind as Kind, name: str(a.name), download_url: str(a.download_url), add_url: str(a.add_url), description: str(a.description).trim(), recommended: a.recommended === true }]
        : [],
    ),
    access: { happ: acc.happ === true, amnezia: acc.amnezia === true },
    devices: list(r.devices).map(
      (d): Device => ({
        id: str(d.id),
        platform: str(d.platform),
        model: str(d.model),
        app: str(d.app),
        last_seen_unix: num(d.last_seen_unix),
        online: d.online === true,
      }),
    ),
    amnezia,
    locked: r.locked === true,
    unlock_url: str(r.unlock_url),
  };
}

/** Links the page may open: http(s), tg and mailto. Anything else (javascript:, data:, ...) becomes "". */
export function safeUrl(u: string): string {
  return /^(https?|tg|mailto):/i.test(u.trim()) && canParse(u) ? u.trim() : "";
}
/** Add links are app deep links (happ://add/...), so any scheme goes except the script-carrying ones. */
export function safeAddUrl(u: string): string {
  try {
    const p = new URL(u.trim());
    return ["javascript:", "data:", "vbscript:", "file:", "blob:", "about:"].includes(p.protocol) ? "" : u.trim();
  } catch {
    return "";
  }
}
function canParse(u: string) {
  try {
    return !!new URL(u.trim());
  } catch {
    return false;
  }
}

// --- platform and apps -------------------------------------------------------------------------------------

export function detectPlatform(ua: string, touchPoints = 0): Platform | null {
  if (/iPhone|iPad|iPod/i.test(ua)) return "ios";
  if (/Android/i.test(ua)) return "android";
  if (/Windows/i.test(ua)) return "windows";
  // iPadOS 13+ presents itself as a Mac; a Mac has no multi-touch screen
  if (/Macintosh|Mac OS X/i.test(ua)) return touchPoints > 1 ? "ios" : "macos";
  if (/Linux|X11|CrOS/i.test(ua)) return "linux";
  return null;
}

/** A value read back from storage: a platform of the page or nothing. */
export const asPlatform = (v: string | null | undefined): Platform | null => (platformOrder.includes(v as Platform) ? (v as Platform) : null);
export const asTheme = (v: string | null | undefined): Theme => (v === "light" || v === "dark" ? v : "auto");

/** Kinds the user may use (access) and the instance has an app for, in display order (Happ first). */
export function kinds(d: MgData): Kind[] {
  return kindOrder.filter((k) => d.access[k] && d.apps.some((a) => a.kind === k));
}

/** The apps the user may use, in the settings' order. */
export const usableApps = (d: MgData): AppEntry[] => d.apps.filter((a) => d.access[a.kind]);

/** Platforms that have at least one app the user may use, in display order. */
export function platformsOf(d: MgData): Platform[] {
  const apps = usableApps(d);
  return platformOrder.filter((p) => apps.some((a) => a.platform === p));
}

/** Every app for a platform, in the settings' order. */
export const appsOn = (d: MgData, platform: Platform): AppEntry[] => usableApps(d).filter((a) => a.platform === platform);

/** The platform to start on: the person's remembered one, else the detected one, else the first that has an app, else the first. */
export function pickPlatform(d: MgData, remembered: Platform | null, detected: Platform | null): Platform {
  return remembered ?? detected ?? platformsOf(d)[0] ?? platformOrder[0]!;
}

/** How an app connects: by the subscription link ("link") or by a per-device AmneziaWG key ("key"). */
export const connectBy = (a: AppEntry): "link" | "key" => (a.kind === "amnezia" ? "key" : "link");

/** The two ways to connect: the subscription link (every server, it updates itself) and the AmneziaVPN key (one per device). */
export type Way = "link" | "key";

/** The ways the user has: the link (a link app usable on some platform) and the key (AmneziaVPN with its data). */
export function ways(d: MgData): Way[] {
  const out: Way[] = [];
  if (d.access.happ) out.push("link");
  if (d.access.amnezia && d.amnezia) out.push("key");
  return out;
}

/**
 * The apps of a platform as the app step lists them: the recommended ones first (the settings' order inside each group),
 * whatever their kind. The first is the big card, the rest sit under "Other apps".
 */
export function appList(d: MgData, platform: Platform): AppEntry[] {
  const here = appsOn(d, platform).filter((a) => a.kind === "happ" || d.amnezia !== null);
  return [...here.filter((a) => a.recommended), ...here.filter((a) => !a.recommended)];
}

/** The key that tells one app from another on a platform ("happ|Happ"). */
export const appKey = (a: AppEntry) => `${a.kind}|${a.name}`;

/** The chosen app of a platform: the one picked when it is still there, else the first. */
export function chosenApp(d: MgData, platform: Platform, key: string): AppEntry | undefined {
  const all = appList(d, platform);
  return all.find((a) => appKey(a) === key) ?? all[0];
}

/** The names of an app list, without repeats. */
export const appNames = (apps: AppEntry[]) => [...new Set(apps.map((a) => a.name).filter(Boolean))];

/** The apps of one kind on a platform: the recommended one first, the rest in the settings' order. */
export function appsFor(d: MgData, platform: Platform | null, kind: Kind): AppEntry[] {
  return platform ? appList(d, platform).filter((a) => a.kind === kind) : [];
}

/** The key app's name: the platform's, else any in the settings; AmneziaVPN when the settings name none. */
export function keyAppName(d: MgData, platform: Platform | null): string {
  return appsFor(d, platform, "amnezia")[0]?.name || d.apps.find((a) => a.kind === "amnezia")?.name || "AmneziaVPN";
}

/** The name of the link app a phone would scan the QR code in: the recommended one, else the first, on iPhone or Android. "" when there is none. */
export function qrApp(d: MgData): string {
  const phone = usableApps(d).filter((a) => a.kind === "happ" && (a.platform === "ios" || a.platform === "android") && a.name !== "");
  return (phone.find((a) => a.recommended) ?? phone[0])?.name ?? "";
}

/** The subscription link is shown as a QR code when the page has the option on and some link app is usable. */
export const hasLinkApp = (d: MgData) => usableApps(d).some((a) => a.kind === "happ");

export function storeLabel(url: string, t: Dict): string {
  let host = "";
  try {
    host = new URL(url).hostname;
  } catch {
    // not a URL: treated as a plain website link
  }
  if (host === "apps.apple.com" || host === "itunes.apple.com") return t.store.appStore;
  if (host === "play.google.com") return t.store.play;
  return t.store.site;
}

export const isTelegram = (url: string) => /^(tg:|https?:\/\/(t\.me|telegram\.me|telegram\.dog)\/)/i.test(url);

/** What a device row draws: a phone, a Mac (laptop), a computer, or a plain device. */
export function deviceIcon(platform: string): "phone" | "android" | "monitor" | "laptop" | "terminal" | "other" {
  if (platform === "ios") return "phone";
  if (platform === "android") return "android";
  if (platform === "windows") return "monitor";
  if (platform === "macos") return "laptop";
  if (platform === "linux") return "terminal";
  return "other";
}

/** The platform as a word ("iPhone", "Android"); "" for none or one the page does not know. */
export const platformWord = (platform: string, t: Dict) => (platform && platform !== "other" ? (t.platforms[platform as keyof Dict["platforms"]] ?? "") : "");

// --- countries and servers ---------------------------------------------------------------------------------

const flagOf = (cc: string) => String.fromCodePoint(...[...cc].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65));
const validCc = (cc: string) => /^[A-Z]{2}$/.test(cc.trim().toUpperCase());

/** The country's name in the page language; the code when the browser has no region names. "" for no country. */
export function countryName(cc: string, lang: Lang): string {
  const code = cc.trim().toUpperCase();
  if (!validCc(code)) return "";
  try {
    return new Intl.DisplayNames([locale(lang)], { type: "region" }).of(code) ?? code;
  } catch {
    return code;
  }
}

/** The flag emoji of a country code; "" for none. (The page shows the code instead where the system draws no flags.) */
export const flagEmoji = (cc: string) => (validCc(cc) ? flagOf(cc.trim().toUpperCase()) : "");

/**
 * "🇩🇪 Германия": the flag and the country in the page language; "" for no country. `flags` off where the system has no
 * flag emoji (Windows draws the two letters, "DE Германия"): the name alone reads better there.
 */
export function countryLabel(cc: string, lang: Lang, flags = true): string {
  const name = countryName(cc, lang);
  return name && flags ? `${flagEmoji(cc)} ${name}` : name;
}

/** A server's name split the way the card writes it: the country, and the owner's place after a dot ("Германия" "Франкфурт"). */
export function serverName(s: ServerEntry, lang: Lang): { country: string; place: string } {
  const country = countryName(s.country_code, lang);
  if (country) return { country, place: s.place };
  const [head, ...rest] = s.label.split(" · ");
  return { country: head ?? s.label, place: rest.join(" · ") };
}

/** One text for a server ("Германия · Франкфурт"). */
export function serverText(s: ServerEntry, lang: Lang): string {
  const n = serverName(s, lang);
  return n.place ? `${n.country} · ${n.place}` : n.country;
}

/**
 * The servers the page lists: `servers[]` with the working ones first (the order of the subscription kept inside each
 * group), or, from a panel that does not send it, one card per `server_loads` row.
 */
export function serversOf(d: MgData): ServerEntry[] {
  if (d.servers.length > 0) return [...d.servers.filter((s) => s.online), ...d.servers.filter((s) => !s.online)];
  return d.server_loads.map((s) => ({ id: "", country_code: "", place: "", label: s.name, app_names: [], connections: [], online: true, load: s.level, dns: null }));
}

/**
 * The choices of a device's configs, one per node: the panel's public name of the server, as the server list and the
 * user's app name it ("Germany 2", "Germany · Frankfurt", "Server"), with the country's flag where the system draws flags.
 * The page never shows the panel's node name.
 */
export function nodeLabels(configs: AwgConfig[], flags = true): string[] {
  return configs.map((c, k) => {
    const name = c.label || String(k + 1);
    const cc = c.country_code.trim().toUpperCase();
    return flags && validCc(cc) ? `${flagOf(cc)} ${name}` : name;
  });
}

const profileRank = (p: AwgProfile) => (p.version === "2.0" ? 2 : p.egress === "warp" ? 1 : 0);

/**
 * "Add a device" names a profile by what a friend can tell apart, never by the panel's name: the countries of the main
 * one, "the spare exit" for a WARP one, "for old versions" for 2.0. The main one (3.x, direct) first; a label that repeats
 * gets a number. `kind` says which of the three it is, for the line under the choice.
 */
export function profileChoices(profiles: AwgProfile[], t: Dict, lang: Lang, app: string, flags = true): { id: string; label: string; kind: "main" | "warp" | "old"; countries: string[] }[] {
  const sorted = [...profiles].sort((a, b) => profileRank(a) - profileRank(b));
  const seen = new Map<string, number>();
  return sorted.map((p) => {
    const base =
      p.version === "2.0" ? t.profileOld(app) : p.egress === "warp" ? t.profileWarp : p.countries.map((c) => countryLabel(c, lang, flags)).filter(Boolean).join(", ") || t.profileMain;
    const n = (seen.get(base) ?? 0) + 1;
    seen.set(base, n);
    return { id: p.id, label: n > 1 ? `${base} (${n})` : base, kind: p.version === "2.0" ? "old" : p.egress === "warp" ? "warp" : "main", countries: p.countries };
  });
}

/** The profile "add a device" starts on: the main one. */
export const mainProfile = (profiles: AwgProfile[]): string => [...profiles].sort((a, b) => profileRank(a) - profileRank(b))[0]?.id ?? "";

/** The name of a device whose field was left empty: the platform's word, numbered when it is taken ("iPhone 2"). */
export function defaultLabel(platform: string, taken: string[], t: Dict): string {
  const base = platformWord(platform, t) || t.devGeneric;
  const used = new Set(taken.map((s) => s.trim().toLowerCase()));
  let name = base;
  for (let n = 2; used.has(name.toLowerCase()); n++) name = `${base} ${n}`;
  return name;
}

// --- DNS ---------------------------------------------------------------------------------------------------

export const dnsPresetOf = (d: MgData, id: string): DnsPreset | undefined => d.dns_presets.find((p) => p.id === id);
/** A preset's name for the page; "" when the id is unknown. */
export const dnsName = (d: MgData, id: string): string => dnsPresetOf(d, id)?.name ?? "";

/** The preset a server falls back to when nothing was chosen: what the server says, else what applies now when nothing is chosen, else the first allowed. */
export function dnsDefault(s: ServerDns): string {
  return s.default || (s.choice === "" ? s.effective : "") || s.options[0] || "";
}

/** Server "key" connections: it carries AmneziaVPN keys. */
export const hasKeys = (s: ServerEntry) => s.connections.some((c) => c.way === "key");
export const hasLink = (s: ServerEntry) => s.connections.some((c) => c.way === "link");

/** The choice may be made on this card: the owner turned it on, the page has an address to post to, there is something to choose from,
 * and the choice reaches something (the link apps share one DNS while `per_server` is off: then it is for the keys). */
export function canChooseDns(d: MgData, s: ServerEntry): boolean {
  if (!d.dns || !d.dns.enabled || d.dns.endpoint === "" || !s.dns || s.id === "") return false;
  return dnsChoices(d, s, "ru").length > 1 && (d.dns.link.per_server || hasKeys(s));
}

/** What the picker lists: the server's default first, then the other allowed presets that the page knows. */
export function dnsChoices(d: MgData, s: ServerEntry, lang: Lang): { id: string; name: string; description: string; isDefault: boolean }[] {
  if (!s.dns) return [];
  const def = dnsDefault(s.dns);
  const ids = [...new Set([def, ...s.dns.options].filter(Boolean))];
  return ids.flatMap((id) => {
    const p = dnsPresetOf(d, id);
    return p ? [{ id, name: p.name, description: describePreset(p.description, lang), isDefault: id === def }] : [];
  });
}

/** A preset description: the built-in ones hold "ru text\nen text" in one field. */
export function describePreset(text: string, lang: Lang): string {
  const at = text.indexOf("\n");
  if (at < 0) return text.trim();
  return lang === "en" ? text.slice(at + 1).trim() : text.slice(0, at).trim();
}

// --- setup and visits --------------------------------------------------------------------------------------

/** When an app last fetched the subscription (the one shared device of the link); 0 = never. */
export const fetchedUnix = (d: MgData): number => Math.max(0, ...d.devices.filter(isShared).map((x) => x.last_seen_unix));

/** A returning visitor: an app fetched the subscription, there are keys, or this device pressed "add" before (`marked`). */
export const isReturning = (d: MgData, marked: boolean): boolean => fetchedUnix(d) > 0 || (d.amnezia?.devices.length ?? 0) > 0 || marked;

// --- numbers and dates -------------------------------------------------------------------------------------

const locale = (lang: Lang) => (lang === "ru" ? "ru" : "en-GB");
const units: Record<Lang, string[]> = { ru: ["Б", "КБ", "МБ", "ГБ", "ТБ"], en: ["B", "KB", "MB", "GB", "TB"] };

export function fmtNum(n: number, lang: Lang): string {
  return new Intl.NumberFormat(locale(lang), { maximumFractionDigits: n < 1 ? 2 : n < 10 ? 1 : 0 }).format(n);
}

export function plural(n: number, lang: Lang, forms: Record<string, string>): string {
  return forms[new Intl.PluralRules(locale(lang)).select(n)] ?? forms.other ?? "";
}
export const daysWord = (n: number, lang: Lang) =>
  lang === "ru" ? plural(n, lang, { one: "день", few: "дня", many: "дней", other: "дня" }) : n === 1 ? "day" : "days";

export function fmtDate(unix: number, lang: Lang, now = Date.now() / 1000): string {
  const d = new Date(unix * 1000);
  const sameYear = d.getFullYear() === new Date(now * 1000).getFullYear();
  return new Intl.DateTimeFormat(locale(lang), { day: "numeric", month: "long", ...(sameYear ? {} : { year: "numeric" }) }).format(d);
}

/** "3 days ago", "yesterday", "now": relative up to a month, a date after that. */
export function fmtAgo(unix: number, lang: Lang, now = Date.now() / 1000): string {
  const s = Math.max(0, now - unix);
  const rtf = new Intl.RelativeTimeFormat(locale(lang), { numeric: "auto" });
  if (s < 60) return rtf.format(0, "second");
  if (s < 3600) return rtf.format(-Math.floor(s / 60), "minute");
  if (s < day) return rtf.format(-Math.floor(s / 3600), "hour");
  if (s < 30 * day) return rtf.format(-Math.floor(s / day), "day");
  return fmtDate(unix, lang, now);
}

export type Hero = {
  state: Status;
  tone: "ok" | "warn" | "bad" | "off";
  /** active, and the term ends within a week */
  soon: boolean;
  /** the term tile ("Осталось" / "Срок"): the number, its unit, the date under it; shown for an active subscription only */
  showTerm: boolean;
  termL: string;
  big: string;
  unit: string;
  sub: string;
  /** the traffic tile: shown for an active subscription and for used-up traffic */
  showTraffic: boolean;
  usedN: string;
  usedOf: string;
  /** the bar, 0-100 (null = no quota, no bar) */
  pct: number | null;
  /** "resets on 14 October" ("" = none) */
  reset: string;
  /** traffic is 90 % used or more (or all of it): the tile is a warning */
  low: boolean;
  /** the one line about what is left when traffic is nearly used up ("" otherwise) */
  left: string;
  /** days left, at least 1 while it is soon */
  days: number;
};

const tones: Record<Status, Hero["tone"]> = { active: "ok", expired: "bad", limited: "warn", disabled: "off" };

export function hero(d: MgData, lang: Lang, now = Date.now() / 1000): Hero {
  const t = dict[lang];
  const u = d.user;
  const active = u.status === "active";
  const noExpiry = !u.expires_unix;
  const days = noExpiry ? 0 : Math.max(0, Math.ceil((u.expires_unix - now) / day));
  const soon = active && !noExpiry && u.expires_unix - now <= soonDays * day;
  const shown = soon ? Math.max(1, days) : days;

  // traffic, in the unit of the quota (or of the usage when there is no quota)
  const ref = u.quota_bytes || u.used_bytes;
  const i = Math.max(0, Math.min(units.ru.length - 1, Math.floor(Math.log(Math.max(ref, 1)) / Math.log(1024))));
  const unit = units[lang][i] ?? "";
  const quota = u.quota_bytes ? `${fmtNum(u.quota_bytes / 1024 ** i, lang)} ${unit}` : "";
  const pct = u.quota_bytes ? Math.min(100, (u.used_bytes / u.quota_bytes) * 100) : null;
  const low = u.status === "limited" || (active && pct !== null && pct >= lowTraffic);
  const resetDate = u.quota_reset !== "none" && u.next_reset_unix ? fmtDate(u.next_reset_unix, lang, now) : "";
  const rest = u.quota_bytes ? `${fmtNum(Math.max(0, u.quota_bytes - u.used_bytes) / 1024 ** i, lang)} ${unit}` : "";

  return {
    state: u.status,
    tone: soon || low ? "warn" : tones[u.status],
    soon,
    showTerm: active,
    termL: noExpiry ? t.termL : t.leftL,
    big: noExpiry ? "∞" : String(shown),
    unit: noExpiry ? t.noExpiry : daysWord(shown, lang),
    sub: noExpiry ? "" : t.until(fmtDate(u.expires_unix, lang, now)),
    showTraffic: active || u.status === "limited",
    usedN: fmtNum(u.used_bytes / 1024 ** i, lang),
    usedOf: quota ? t.of(quota) : `${unit} · ${t.unlimited}`,
    pct,
    reset: !u.quota_bytes ? "" : resetDate ? t.resets(resetDate) : "",
    low,
    left: active && low && rest ? (resetDate ? t.leftUntil(rest, resetDate) : t.leftOnly(rest)) : "",
    days: shown,
  };
}

/** Amnezia keys are the only thing the device limit applies to; the notice shows when it is reached. */
export const atDeviceLimit = (d: MgData) => d.user.device_limit > 0 && d.user.devices_used >= d.user.device_limit;

/** The page may call the self-service endpoints: the admin allows it and this is not the admin's preview (no address). */
export const selfServe = (d: MgData) => !!d.amnezia && d.amnezia.self_service && d.amnezia.endpoints !== "";

/** "Add a device" is offered: self-service, the user is active, there is a profile to add on and a free slot. */
export const canAddDevice = (d: MgData) => selfServe(d) && d.user.status === "active" && (d.amnezia?.profiles.length ?? 0) > 0 && !atDeviceLimit(d);

/** The Amnezia-app requirements of a config ("AmneziaVPN 5.0.1.5", "AmneziaWG Android v3.1.20260814"). */
export const amneziaClients = (all: MinClient[]) => all.filter((m) => m.client === "amnezia");

const platformApp: [Platform, RegExp][] = [
  ["android", /\bandroid\b/i],
  ["ios", /\b(ios|iphone)\b/i],
  ["windows", /\bwindows\b/i],
  ["macos", /\bmac(os)?\b/i],
  ["linux", /\blinux\b/i],
];

/**
 * The app versions a key needs on one platform: the apps for every platform, plus the ones made for this platform alone
 * (AmneziaWG Android on Android), named without the platform word and without a "v" before the number.
 */
export function versionsFor(platform: string, all: MinClient[]): MinClient[] {
  return amneziaClients(all)
    .filter((m) => {
      const own = platformApp.find(([, re]) => re.test(m.app));
      return !own || own[0] === platform;
    })
    .map((m) => ({ ...m, app: m.app.replace(/\s*\b(android|ios|iphone|windows|macos|mac|linux)\b/gi, "").trim(), min: m.min.replace(/^v(?=\d)/, "") }));
}

/** The devices of the generic list that are not AmneziaWG ones (those have their own rows): what the link apps use. */
export const otherDevices = (d: MgData): Device[] => {
  const awg = new Set((d.amnezia?.devices ?? []).map((x) => x.id));
  return d.devices.filter((x) => !awg.has(x.id));
};

/** The one device every link app shares (no platform, no model: the server cannot tell the apps apart). */
export const isShared = (x: Device) => !x.platform && !x.model;

export const isDesktop = (platform: string) => platform === "windows" || platform === "macos" || platform === "linux";
export const isPhone = (platform: string) => platform === "ios" || platform === "android";

/** The platforms "add a device" offers, in display order. */
export const addPlatforms = ["ios", "android", "windows", "macos", "linux", "other"] as const;

/** What the status card says for a state that is not "active": [the bold first sentence, what happened, what to do]. The "what to do" is dropped without support. */
export function stateText(d: MgData, lang: Lang, brand: string): { head: string; body: string; call: string } | null {
  const t = dict[lang].txt;
  switch (d.user.status) {
    case "expired":
      return { head: d.user.expires_unix ? t.expiredOn(fmtDate(d.user.expires_unix, lang)) : "", ...t.expired(brand) };
    case "limited":
      return { head: "", ...(d.user.next_reset_unix && d.user.quota_reset !== "none" ? t.limitedReset(fmtDate(d.user.next_reset_unix, lang), d.user.quota_reset) : t.limited()) };
    case "disabled":
      return { head: "", ...t.disabled() };
    default:
      return null;
  }
}
