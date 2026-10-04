import { dict, type Dict } from "./i18n";
import type { AmneziaData, AppEntry, AwgConfig, AwgDevice, AwgProfile, Device, Kind, Lang, MgData, MinClient, Platform, ServerLoad, Status } from "./types";

// Pure functions of the page data: normalisation, platform detection, the numbers in the hero. No DOM here.

export const platformOrder: Platform[] = ["ios", "android", "windows", "macos", "linux"];
const kindOrder: Kind[] = ["happ", "amnezia"];
const statuses: Status[] = ["active", "expired", "limited", "disabled"];
const resets = ["none", "day", "week", "month", "rolling_month"] as const;
const day = 86400;

// --- reading mg-data ---------------------------------------------------------------------------------------

const str = (v: unknown, d = "") => (typeof v === "string" ? v : d);
const num = (v: unknown) => (typeof v === "number" && Number.isFinite(v) && v > 0 ? v : 0);
const rec = (v: unknown): Record<string, unknown> => (v && typeof v === "object" ? (v as Record<string, unknown>) : {});
const list = (v: unknown): Record<string, unknown>[] => (Array.isArray(v) ? v.map(rec) : []);

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
    min_clients: minClients(d.min_clients),
  };
}

/** One config answered by the endpoints; the strings are used as text only. */
export function awgConfig(raw: unknown): AwgConfig {
  const c = rec(raw);
  return {
    node_id: str(c.node_id),
    node_name: str(c.node_name),
    country_code: str(c.country_code),
    version: str(c.version),
    conf: str(c.conf),
    vpn_key: str(c.vpn_key),
    filename: str(c.filename),
    stale: c.stale === true,
    warnings: Array.isArray(c.warnings) ? c.warnings.map((w) => str(w)).filter(Boolean) : [],
    min_clients: minClients(c.min_clients),
  };
}

const awgProfile = (p: Record<string, unknown>): AwgProfile => ({
  id: str(p.id),
  name: str(p.name),
  version: str(p.version),
  egress: p.egress === "warp" ? "warp" : "direct",
  countries: Array.isArray(p.countries) ? p.countries.map((c) => str(c)).filter(Boolean) : [],
});

export const isHex = (v: string) => /^#[0-9a-f]{6}$/i.test(v);

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
    server_loads: list(r.server_loads).flatMap((s): ServerLoad[] => {
      const capacity = num(s.capacity_mbps);
      const percent = typeof s.load_percent === "number" && Number.isFinite(s.load_percent) ? s.load_percent : undefined;
      if (typeof s.name !== "string" || !s.name.trim()) return [];
      const loadPercent = percent !== undefined && percent >= 0 && percent <= 100 ? percent : undefined;
      return [{ name: str(s.name), load_percent: loadPercent, rx_bps: num(s.rx_bps), tx_bps: num(s.tx_bps), capacity_mbps: capacity }];
    }),
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

/** Every app for a platform, in the settings' order: one card each. */
export const appsOn = (d: MgData, platform: Platform): AppEntry[] => usableApps(d).filter((a) => a.platform === platform);

/** The platform to preselect: the detected one when there is an app for it, else the first listed. */
export function pickPlatform(d: MgData, want: Platform | null): Platform | null {
  const have = platformsOf(d);
  return want && have.includes(want) ? want : (have[0] ?? null);
}

/** How an app connects: by the subscription link ("link") or by a per-device AmneziaWG key ("key"). */
export const connectBy = (a: AppEntry): "link" | "key" => (a.kind === "amnezia" ? "key" : "link");

/** The two ways to connect and what each is about: the subscription link (every server, it updates itself)
 * and the AmneziaVPN key (one per device). */
export type Way = "link" | "key";

/**
 * The ways the page shows, in order: only the ones the user has. Both are main; the key way leads
 * only where the platform's recommended app is a key app and no link app is recommended.
 */
export function ways(d: MgData, platform: Platform | null): Way[] {
  const out: Way[] = [];
  if (d.access.happ) out.push("link");
  if (d.access.amnezia && d.amnezia) out.push("key");
  if (out.length === 2 && platform) {
    const here = appsOn(d, platform);
    if (here.some((a) => a.kind === "amnezia" && a.recommended) && !here.some((a) => a.kind === "happ" && a.recommended)) out.reverse();
  }
  return out;
}

/** The apps of one kind on a platform: the recommended one first (the way leads with it), the rest in the settings' order. */
export function appsFor(d: MgData, platform: Platform | null, kind: Kind): AppEntry[] {
  const here = platform ? appsOn(d, platform).filter((a) => a.kind === kind) : [];
  const i = here.findIndex((a) => a.recommended);
  return i > 0 ? [here[i]!, ...here.filter((_, k) => k !== i)] : here;
}

/** The names of a way's apps on a platform, for its title ("Happ, FlClash"), without repeats. */
export const appNames = (apps: AppEntry[]) => [...new Set(apps.map((a) => a.name).filter(Boolean))];

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

/** What a device row draws: a phone, a computer, or a plain device. */
export function deviceIcon(platform: string): "phone" | "laptop" | "device" {
  if (platform === "ios" || platform === "android") return "phone";
  return isDesktop(platform) ? "laptop" : "device";
}

/** The platform as a word ("iPhone", "Android"); "" for none or one the page does not know. */
export const platformWord = (platform: string, t: Dict) => (platform && platform !== "other" ? (t.platforms[platform as keyof Dict["platforms"]] ?? "") : "");

// --- countries and choices ---------------------------------------------------------------------------------

const flagOf = (cc: string) => String.fromCodePoint(...[...cc].map((c) => 0x1f1e6 + c.charCodeAt(0) - 65));

/**
 * "🇩🇪 Германия": the flag and the country in the page language; "" for no country. `flags` off where the system has no
 * flag emoji (Windows draws the two letters, "DE Германия"): the name alone reads better there.
 */
export function countryLabel(cc: string, lang: Lang, flags = true): string {
  const code = cc.trim().toUpperCase();
  if (!/^[A-Z]{2}$/.test(code)) return "";
  let name = code;
  try {
    name = new Intl.DisplayNames([locale(lang)], { type: "region" }).of(code) ?? code;
  } catch {
    // a browser without region names: the code
  }
  return flags ? `${flagOf(code)} ${name}` : name;
}

/** The choices of a device's configs, one per node: the country, with the node's name only where a country repeats. */
export function nodeLabels(configs: AwgConfig[], lang: Lang, flags = true): string[] {
  const count = new Map<string, number>();
  for (const c of configs) count.set(c.country_code.toUpperCase(), (count.get(c.country_code.toUpperCase()) ?? 0) + 1);
  return configs.map((c, k) => {
    const country = countryLabel(c.country_code, lang, flags);
    if (!country) return c.node_name || String(k + 1);
    return (count.get(c.country_code.toUpperCase()) ?? 0) > 1 && c.node_name ? `${country} · ${c.node_name}` : country;
  });
}

const profileRank = (p: AwgProfile) => (p.version === "2.0" ? 2 : p.egress === "warp" ? 1 : 0);

/**
 * "Add a device" names a profile by what a friend can tell apart, never by the panel's name: the countries of the main
 * one, "the spare exit" for a WARP one, "for old versions" for 2.0. The main one (3.x, direct) first; a label that repeats
 * gets a number.
 */
export function profileChoices(profiles: AwgProfile[], t: Dict, lang: Lang, app: string, flags = true): { id: string; label: string }[] {
  const sorted = [...profiles].sort((a, b) => profileRank(a) - profileRank(b));
  const seen = new Map<string, number>();
  return sorted.map((p) => {
    const base =
      p.version === "2.0" ? t.profileOld(app) : p.egress === "warp" ? t.profileWarp : p.countries.map((c) => countryLabel(c, lang, flags)).filter(Boolean).join(", ") || t.profileMain;
    const n = (seen.get(base) ?? 0) + 1;
    seen.set(base, n);
    return { id: p.id, label: n > 1 ? `${base} (${n})` : base };
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
  tone: "ok" | "bad" | "warn" | "off";
  /** the big thing: days left, "∞", or a word ("Ended") when `word` */
  big: string;
  word: boolean;
  unit: string;
  sub: string;
  /** term bar, 0-100 */
  termPct: number;
  /** traffic: the number, the "of 100 GB" part, the one-line form, the bar (null = unlimited) and the caption; `showTraffic`
   * is off where traffic no longer matters (the subscription ended) */
  usedN: string;
  usedOf: string;
  line: string;
  pct: number | null;
  caption: string;
  showTraffic: boolean;
};

const tones: Record<Status, Hero["tone"]> = { active: "ok", expired: "bad", limited: "warn", disabled: "off" };

export function hero(d: MgData, lang: Lang, now = Date.now() / 1000): Hero {
  const t = dict[lang];
  const u = d.user;
  const ended = u.status === "expired";
  const days = ended || !u.expires_unix ? 0 : Math.max(0, Math.ceil((u.expires_unix - now) / day));
  const noExpiry = !u.expires_unix && !ended;

  // traffic, in the unit of the quota (or of the usage when there is no quota)
  const ref = u.quota_bytes || u.used_bytes;
  const i = Math.max(0, Math.min(units.ru.length - 1, Math.floor(Math.log(Math.max(ref, 1)) / Math.log(1024))));
  const unit = units[lang][i] ?? "";
  const used = fmtNum(u.used_bytes / 1024 ** i, lang);
  const quota = u.quota_bytes ? `${fmtNum(u.quota_bytes / 1024 ** i, lang)} ${unit}` : "";
  const caption = !u.quota_bytes
    ? t.unlimited
    : u.quota_reset !== "none" && u.next_reset_unix
      ? t.resets(fmtDate(u.next_reset_unix, lang, now))
      : "";

  return {
    state: u.status,
    tone: tones[u.status],
    // an ended subscription says so in words, with the date under it: "0 days" read like a number to count down
    big: ended ? t.chip.expired : noExpiry ? "∞" : String(days),
    word: ended,
    unit: ended ? "" : noExpiry ? t.noExpiry : daysWord(days, lang),
    sub: ended ? (u.expires_unix ? fmtDate(u.expires_unix, lang, now) : "") : noExpiry ? "" : t.until(fmtDate(u.expires_unix, lang, now)),
    // The page does not know the term's start, so the bar scales against 90 days
    termPct: noExpiry ? 100 : Math.min(100, (days / 90) * 100),
    usedN: used,
    usedOf: quota ? t.of(quota) : unit,
    line: quota ? `${used} / ${quota}` : `${used} ${unit}`,
    pct: u.quota_bytes ? Math.min(100, (u.used_bytes / u.quota_bytes) * 100) : null,
    caption,
    showTraffic: !ended,
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

/** The one-line explanation under the hero for a state that is not "active": [what happened, what to do]. */
export function stateText(d: MgData, lang: Lang, brand: string): [string, string] | null {
  const t = dict[lang].txt;
  switch (d.user.status) {
    case "expired":
      return t.expired(brand);
    case "limited":
      return d.user.next_reset_unix && d.user.quota_reset !== "none" ? t.limitedReset(fmtDate(d.user.next_reset_unix, lang), d.user.quota_reset) : t.limited();
    case "disabled":
      return t.disabled();
    default:
      return null;
  }
}
