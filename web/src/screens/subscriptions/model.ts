import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { DnsCategory, DnsClient, DnsServerKind, DnsTransport, type ClientDnsSupport, type DnsPreset, type DnsProvider } from "@/gen/mistgate/admin/v1/dns_pb";
import { Platform, SubFormat, type SubscriptionSettings } from "@/gen/mistgate/admin/v1/subscription_pb";
import type { Key, Tx } from "@/screens/users/t";
import type { Plain } from "@/lib/plain";

// Plain shapes of what the screens edit (64-bit free, no protobuf bookkeeping): see lib/plain.ts.
export type Settings = Plain<SubscriptionSettings>;
export type Rule = Settings["rules"][number];
export type PlatformApp = Settings["apps"][number];
export type Preset = Plain<DnsPreset>;
export type DnsServerN = Preset["servers"][number];
export type SplitRuleN = Preset["split"][number];
export type Provider = Plain<DnsProvider>;
export type Variant = Provider["variants"][number];
export type ClientSupport = Plain<ClientDnsSupport>;
/** What ListDnsPresets returns: the presets, the built-in provider catalog and what each app can carry. */
export type DnsData = { presets: Preset[]; providers: Provider[]; clientSupport: ClientSupport[] };

export const formatKey: Partial<Record<SubFormat, Key>> = {
  [SubFormat.BASE64_URIS]: "subs.fmt.base64",
  [SubFormat.USER_PAGE]: "subs.fmt.page",
  [SubFormat.DECOY]: "subs.fmt.decoy",
  [SubFormat.MIHOMO_YAML]: "subs.fmt.mihomo",
  [SubFormat.XRAY_JSON]: "subs.fmt.xray",
  [SubFormat.SINGBOX_JSON]: "subs.fmt.singbox",
};
export const formatDescKey: Partial<Record<SubFormat, Key>> = {
  [SubFormat.BASE64_URIS]: "subs.fmt.base64.desc",
  [SubFormat.USER_PAGE]: "subs.fmt.page.desc",
  [SubFormat.DECOY]: "subs.fmt.decoy.desc",
  [SubFormat.MIHOMO_YAML]: "subs.fmt.mihomo.desc",
  [SubFormat.XRAY_JSON]: "subs.fmt.xray.desc",
  [SubFormat.SINGBOX_JSON]: "subs.fmt.singbox.desc",
};
/** The formats a rule can hand out, in the order the pickers list them. */
export const ruleFormats = [SubFormat.BASE64_URIS, SubFormat.MIHOMO_YAML, SubFormat.XRAY_JSON, SubFormat.SINGBOX_JSON, SubFormat.USER_PAGE, SubFormat.DECOY] as const;

/** The rule a User-Agent lands on, as the panel picks it (internal/panel/subs/format.go Choose): the first one whose text it contains, case aside; -1 for none. */
export const ruleFor = (rules: readonly Rule[], ua: string): number => rules.findIndex((r) => r.uaContains !== "" && ua.toLowerCase().includes(r.uaContains.toLowerCase()));

/** What the official sing-box apps send as their User-Agent (sing-box-for-android HTTPClient.kt, sing-box-for-apple HTTPClient.swift). */
export const singboxAgents = ["SFA (sing-box 1.14.3; language en_US)", "SFI (sing-box 1.14.3; language en_US)", "SFM (sing-box 1.14.3; language en_US)"] as const;

export const platformKey: Partial<Record<Platform, Key>> = {
  [Platform.IOS]: "subs.platform.ios",
  [Platform.ANDROID]: "subs.platform.android",
  [Platform.WINDOWS]: "subs.platform.windows",
  [Platform.MACOS]: "subs.platform.macos",
  [Platform.LINUX]: "subs.platform.linux",
};
export const platforms = [Platform.IOS, Platform.ANDROID, Platform.WINDOWS, Platform.MACOS, Platform.LINUX] as const;

export const kindKey: Partial<Record<App, Key>> = { [App.HAPP]: "subs.kind.happ", [App.AMNEZIA]: "subs.kind.amnezia" };
export const kinds = [App.HAPP, App.AMNEZIA] as const;

export const dnsKindKey: Partial<Record<DnsServerKind, Key>> = {
  [DnsServerKind.PLAIN]: "subs.dns.kind.plain",
  [DnsServerKind.DOH]: "subs.dns.kind.doh",
  [DnsServerKind.DOT]: "subs.dns.kind.dot",
};
export const dnsAddrKey: Partial<Record<DnsServerKind, Key>> = {
  [DnsServerKind.PLAIN]: "subs.dns.addr.plain",
  [DnsServerKind.DOH]: "subs.dns.addr.doh",
  [DnsServerKind.DOT]: "subs.dns.addr.dot",
};
export const dnsKinds = [DnsServerKind.PLAIN, DnsServerKind.DOH, DnsServerKind.DOT] as const;

/** The categories of the DNS tab in the order of the chips; UNSPECIFIED is a preset of the admin's own. */
export const dnsCategories = [DnsCategory.REGIONAL, DnsCategory.REGULAR, DnsCategory.NO_ADS, DnsCategory.FAMILY, DnsCategory.SECURITY] as const;
export const categoryKey: Record<DnsCategory, Key> = {
  [DnsCategory.UNSPECIFIED]: "subs.dns.cat.own",
  [DnsCategory.REGIONAL]: "subs.dns.cat.regional",
  [DnsCategory.REGULAR]: "subs.dns.cat.regular",
  [DnsCategory.NO_ADS]: "subs.dns.cat.noAds",
  [DnsCategory.FAMILY]: "subs.dns.cat.family",
  [DnsCategory.SECURITY]: "subs.dns.cat.security",
};

// A regional preset or catalog variant is filed under its country: one chip each ("1:CN"), Russia and China first.
const countryKey: Record<string, Key> = { RU: "subs.dns.cat.russia", CN: "subs.dns.cat.china" };
const countryOrder = Object.keys(countryKey);
/** The chip a preset or a catalog variant is filed under: its category, and for a regional one its country. */
export const chipOf = (x: { category: DnsCategory; country: string }): string => (x.category === DnsCategory.REGIONAL ? `${x.category}:${x.country}` : String(x.category));
/** The chips that some of the items are filed under, in display order: the regional countries, the other categories, then "own". */
export function dnsChips(items: readonly { category: DnsCategory; country: string }[]): string[] {
  const have = new Set(items.map(chipOf));
  const regional = [...have].filter((c) => c.startsWith(`${DnsCategory.REGIONAL}:`)).sort((a, b) => {
    const ia = countryOrder.indexOf(a.split(":")[1]!);
    const ib = countryOrder.indexOf(b.split(":")[1]!);
    return (ia < 0 ? countryOrder.length : ia) - (ib < 0 ? countryOrder.length : ib) || a.localeCompare(b);
  });
  const rest = [...dnsCategories.filter((c) => c !== DnsCategory.REGIONAL), DnsCategory.UNSPECIFIED].map(String).filter((c) => have.has(c));
  return [...regional, ...rest];
}
/** The text of a chip: the country of a regional one (a country the panel has no word for is named by the browser), else the category. */
export function chipLabel(chip: string, t: Tx): string {
  const [cat = "", country = ""] = chip.split(":");
  if (Number(cat) !== DnsCategory.REGIONAL) return t(categoryKey[Number(cat) as DnsCategory]);
  const key = countryKey[country];
  if (key) return t(key);
  try {
    return (country && new Intl.DisplayNames([t.locale], { type: "region" }).of(country)) || t(categoryKey[DnsCategory.REGIONAL]);
  } catch {
    return country || t(categoryKey[DnsCategory.REGIONAL]);
  }
}

/** The switch of a preset, left to right. */
export const dnsTransports = [DnsTransport.PLAIN, DnsTransport.DOT, DnsTransport.DOH] as const;
export const transportKey: Record<DnsTransport, Key> = {
  [DnsTransport.UNSPECIFIED]: "subs.dns.tr.plain",
  [DnsTransport.PLAIN]: "subs.dns.tr.plain",
  [DnsTransport.DOT]: "subs.dns.tr.dot",
  [DnsTransport.DOH]: "subs.dns.tr.doh",
};
const kindTransport: Record<DnsServerKind, DnsTransport> = {
  [DnsServerKind.UNSPECIFIED]: DnsTransport.PLAIN,
  [DnsServerKind.PLAIN]: DnsTransport.PLAIN,
  [DnsServerKind.DOT]: DnsTransport.DOT,
  [DnsServerKind.DOH]: DnsTransport.DOH,
};

// The panel's fallback (internal/panel/dns/transport.go, order): the preferred transport, then down DoH -> DoT -> plain,
// and only as a last resort the ones above it.
const chain = [DnsTransport.DOH, DnsTransport.DOT, DnsTransport.PLAIN];
export function transportOrder(pref: DnsTransport): DnsTransport[] {
  const i = chain.indexOf(pref);
  const at = i < 0 ? chain.length - 1 : i;
  return [...chain.slice(at), ...chain.slice(0, at)];
}

/** Catalog variants by id, each with its provider. */
export type Catalog = ReadonlyMap<string, { provider: Provider; variant: Variant }>;
export const catalogOf = (providers: readonly Provider[]): Catalog => new Map(providers.flatMap((provider) => provider.variants.map((variant) => [variant.id, { provider, variant }] as const)));

/** "Cloudflare · Family": the provider and the variant in the UI language. */
export function variantLabel(e: { provider: Provider; variant: Variant }, lang: string): string {
  const name = lang === "ru" ? e.variant.nameRu : lang === "zh" ? (variantNamesZh[e.variant.id] ?? e.variant.nameEn) : e.variant.nameEn;
  return `${e.provider.name} · ${name}`;
}
// The catalog (internal/panel/dns/providers.go) names its variants in Russian and English only; this is the Chinese for each.
const variantNamesZh: Record<string, string> = {
  "cloudflare/standard": "标准",
  "cloudflare/security": "安全",
  "cloudflare/family": "儿童",
  "google/standard": "标准",
  "quad9/standard": "安全",
  "adguard/default": "默认",
  "adguard/family": "儿童",
  "adguard/unfiltered": "不过滤",
  "yandex/basic": "基础",
  "yandex/safe": "安全",
  "yandex/family": "儿童",
  "mullvad/standard": "标准",
  "mullvad/adblock": "无广告",
  "mullvad/base": "广告与恶意网站",
  "opendns/familyshield": "FamilyShield",
  "dnssb/standard": "标准",
};
// The catalog's notes are Russian and English only; this is the Chinese for each variant.
const variantNotesZh: Record<string, string> = {
  "cloudflare/standard": "速度快，不过滤。2026 年 8 月起，俄罗斯封锁了到 Cloudflare 的 DoH，在隧道内不受影响。",
  "cloudflare/security": "拦截恶意和钓鱼网站。2026 年 8 月起，俄罗斯封锁了 DoH。",
  "cloudflare/family": "拦截恶意网站和成人内容。2026 年 8 月起，俄罗斯封锁了 DoH。",
  "google/standard": "不过滤。",
  "quad9/standard": "拦截已知的恶意和钓鱼域名。",
  "adguard/default": "拦截广告和跟踪器。",
  "adguard/family": "拦截广告、跟踪器和成人内容。",
  "adguard/unfiltered": "不拦截任何内容。",
  "yandex/basic": "俄罗斯网站解析更稳定。在俄罗斯可使用 DoH 和 DoT。",
  "yandex/safe": "拦截恶意和诈骗网站。在俄罗斯可使用 DoH 和 DoT。",
  "yandex/family": "拦截成人内容和恶意网站。在俄罗斯可使用 DoH 和 DoT。",
  "mullvad/standard": "仅支持 DoH/DoT，没有普通 DNS。该服务将于 2026-11-02 停止运营。",
  "mullvad/adblock": "拦截广告和跟踪器。仅支持 DoH/DoT。该服务将于 2026-11-02 停止运营。",
  "mullvad/base": "拦截广告、跟踪器和恶意网站。仅支持 DoH/DoT。该服务将于 2026-11-02 停止运营。",
  "opendns/familyshield": "拦截成人内容。没有 DoT：主机名尚未确认。",
  "dnssb/standard": "不记录日志，支持 DNSSEC，不过滤。",
};
export const variantNote = (v: Variant, lang: string) => (lang === "ru" ? v.noteRu : lang === "zh" ? (variantNotesZh[v.id] ?? v.noteEn) : v.noteEn);

type ServerLike = { address: string; kind?: DnsServerKind; providerVariant?: string };

const hasTransport = (v: Variant, t: DnsTransport) => (t === DnsTransport.PLAIN ? v.plain : t === DnsTransport.DOT ? v.dotHost !== "" : v.dohUrl !== "");

/**
 * What one app gets out of a preset's servers: the transport of the first server it can carry (null when it carries none)
 * and about how many resolvers it is handed (a catalog variant in plain DNS is its IPv4 addresses; no IPv6, the link apps
 * take IPv4 only). sing-box keeps only the first of them.
 */
export type AppResult = { client: DnsClient; transport: DnsTransport | null; resolvers: number };
export function appResults(servers: readonly ServerLike[], preferred: DnsTransport, support: readonly ClientSupport[], catalog: Catalog): AppResult[] {
  return support.map(({ client, transports }) => {
    const got = servers.flatMap((s) => {
      if (!s.providerVariant) return s.kind !== undefined && transports.includes(kindTransport[s.kind]) ? [{ t: kindTransport[s.kind], n: 1 }] : [];
      const v = catalog.get(s.providerVariant)?.variant;
      const t = v && transportOrder(preferred).find((x) => transports.includes(x) && hasTransport(v, x));
      return v && t !== undefined ? [{ t, n: t === DnsTransport.PLAIN ? Math.max(1, v.ipv4.length) : 1 }] : [];
    });
    return { client, transport: got[0]?.t ?? null, resolvers: got.reduce((n, g) => n + g.n, 0) };
  });
}

/** A copy of `items` with the element at `from` moved to `to` (out-of-range or same place: the same array). */
export function move<T>(items: readonly T[], from: number, to: number): T[] {
  if (from === to || from < 0 || to < 0 || from >= items.length || to >= items.length) return [...items];
  const out = [...items];
  const [m] = out.splice(from, 1);
  out.splice(to, 0, m!);
  return out;
}

/** The two regional-indicator letters of a country code: a flag where the system draws one. */
export function flagOf(code: string): string {
  if (!/^[A-Za-z]{2}$/.test(code)) return "";
  return [...code.toUpperCase()].map((c) => String.fromCodePoint(0x1f1e6 + c.charCodeAt(0) - 65)).join("");
}

/** The default server-name template (subsettings.DefaultNameTemplate): country and profile, including WARP twins. */
export const defaultNameTemplate = "{flag} {country} · {profile}";

/** The server name the subscription would carry: placeholders filled, spaces tidied, and dangling separators removed. */
export function renderName(template: string, v: { flag: string; country: string; node: string; profile: string }): string {
  const tpl = template.trim() === "" ? defaultNameTemplate : template;
  return tpl
    .replaceAll("{flag}", v.flag)
    .replaceAll("{country}", v.country)
    .replaceAll("{node}", v.node)
    .replaceAll("{profile}", v.profile)
    .replace(/\s+/g, " ")
    .trim()
    .replace(/^(?:·\s*)+|(?:\s*·)+$/g, "")
    .trim();
}

/**
 * The names of one person's servers, in order, by the rules of the subscription (internal/panel/subs/names.go remarks):
 * the template, the node when it renders to nothing, and a number from 2 on for a rendered name that is already taken
 * ("🇩🇪 DE · Hysteria2", "🇩🇪 DE · Hysteria2 · 64%"). A load percentage is kept in the name before duplicates are numbered.
 */
export function serverNames(template: string, servers: readonly { node: string; countryCode: string; profile: string; loadPercent?: number }[], country: (code: string) => string): string[] {
  const taken = new Set<string>();
  return servers.map((s) => {
    const baseName = renderName(template, { flag: flagOf(s.countryCode), country: s.countryCode ? country(s.countryCode.toUpperCase()) : "", node: s.node, profile: s.profile }) || s.node.trim() || "server";
    const loadSuffix = s.loadPercent === undefined ? "" : ` · ${s.loadPercent}%`;
    let name = fitHappRemark(baseName, loadSuffix, s.profile);
    for (let k = 2; taken.has(name); k++) name = fitHappRemark(baseName, `${loadSuffix} ${k}`, s.profile);
    taken.add(name);
    return name;
  });
}

const happNameMaxLength = 30;

function fitHappRemark(base: string, suffix: string, profile: string): string {
  if (base.length + suffix.length <= happNameMaxLength) return base + suffix;

  const compactProfile = compactProfileName(profile);
  if (compactProfile !== profile) {
    base = base.replaceAll(profile, compactProfile);
    if (base.length + suffix.length <= happNameMaxLength) return base + suffix;
  }

  const budget = Math.max(0, happNameMaxLength - suffix.length);
  let shortened = "";
  for (const char of base) {
    if (shortened.length + char.length > budget) break;
    shortened += char;
  }
  shortened = shortened.replace(/[ ·]+$/u, "");
  if (shortened === "") shortened = "server".slice(0, budget);
  return shortened + suffix;
}

function compactProfileName(profile: string): string {
  for (const [full, short] of [["Hysteria2", "HY2"], ["AmneziaWG", "AWG"], ["WireGuard", "WG"]] as const) {
    if (profile.slice(0, full.length).toLowerCase() === full.toLowerCase()) return short + profile.slice(full.length);
  }
  return profile;
}

/** How much of the announcement Happ shows. */
export const announceMax = 200;

/**
 * The announcement as Happ gets it (internal/panel/subs/names.go cutAnnounce): whole when it fits, else cut at the last
 * word boundary of the second half with "…", never in the middle of a word.
 */
export function cutAnnounce(text: string, max = announceMax): string {
  const r = [...text.trim()];
  if (r.length <= max) return r.join("");
  let cut = r.slice(0, max - 1);
  if (!/\s/u.test(r[max - 1]!)) {
    for (let i = cut.length - 1; i >= Math.floor(max / 2); i--) {
      if (/\s/u.test(cut[i]!)) {
        cut = cut.slice(0, i);
        break;
      }
    }
  }
  return `${cut.join("").replace(/[\s,;:—–-]+$/u, "")}…`;
}

/** "", https://… and tg://… are links a support button can open; anything else (plain http too) is refused, as by the panel. */
export const validSupportLink = (s: string) => s.trim() === "" || /^(https:\/\/|tg:\/\/)\S+$/i.test(s.trim());

/** "" or https://…: a download link, as the panel takes it (internal/panel/subsettings httpsURL). */
export const validDownload = (u: string) => u.trim() === "" || /^https:\/\/\S+$/i.test(u.trim());

/** Suffixes typed in one go: separated by commas, semicolons or spaces, lower-cased, without repeats. */
export function parseSuffixes(text: string): string[] {
  return [...new Set(text.toLowerCase().split(/[\s,;]+/).filter(Boolean))];
}


// A client-side look at a server address. It is here to tell the admin early and is lenient: the panel's own check
// (internal/panel/dns/validate.go) has the last word.
const hostRe = /^(?=.{1,253}$)[\p{L}\p{N}]([\p{L}\p{N}-]{0,61}[\p{L}\p{N}])?(\.[\p{L}\p{N}]([\p{L}\p{N}-]{0,61}[\p{L}\p{N}])?)*$/u;
const v4Re = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/;
const isV4 = (h: string) => {
  const m = v4Re.exec(h);
  return m !== null && m.slice(1).every((o) => Number(o) <= 255);
};
const isV6 = (h: string) => (h.match(/:/g)?.length ?? 0) >= 2 && /^[0-9a-f:.]+$/i.test(h) && (h.match(/::/g)?.length ?? 0) <= 1 && h.length <= 45;
const portOk = (p: string) => /^\d{1,5}$/.test(p) && Number(p) >= 1 && Number(p) <= 65535;

/** "ip", "ip:port", "[v6]" or "[v6]:port": true when it parses as an address. */
function isIpAddr(a: string): boolean {
  const br = /^\[([^\]]+)\](?::(\d+))?$/.exec(a);
  if (br) return isV6(br[1]!) && (br[2] === undefined || portOk(br[2]));
  if (isV6(a)) return true;
  const at = a.lastIndexOf(":");
  return isV4(at < 0 ? a : a.slice(0, at)) && (at < 0 || portOk(a.slice(at + 1)));
}

/** The problem with a server address as a message key. Empty text is not one here: presetProblem asks for it. */
export function serverProblem(kind: DnsServerKind, address: string): Key | null {
  const a = address.trim();
  if (a === "") return null;
  if (kind === DnsServerKind.DOH) {
    try {
      const u = new URL(a);
      return u.protocol === "https:" && u.hostname !== "" && !u.username && !u.hash ? null : "subs.dns.bad.doh";
    } catch {
      return "subs.dns.bad.doh";
    }
  }
  if (kind === DnsServerKind.DOT) {
    if (/[\s/?#@]/.test(a)) return "subs.dns.bad.dot";
    if (isIpAddr(a)) return null;
    const at = a.lastIndexOf(":");
    return hostRe.test(at < 0 ? a : a.slice(0, at)) && (at < 0 || portOk(a.slice(at + 1))) ? null : "subs.dns.bad.dot";
  }
  return isIpAddr(a) ? null : "subs.dns.bad.plain";
}

// a catalog server is valid by construction (the panel checks the id); a custom one needs a good address
const serverBad = (s: ServerLike) => !s.providerVariant && (s.address.trim() === "" || (s.kind !== undefined && serverProblem(s.kind, s.address) !== null));

/** The problem with a preset draft, as a message key, or null when it can be saved. */
export function presetProblem(p: { name: string; servers: readonly ServerLike[]; split: readonly { suffixes: readonly string[]; geo?: readonly string[]; servers: readonly ServerLike[] }[] }): Key | null {
  if (p.name.trim() === "") return "subs.dns.needName";
  if (p.servers.length === 0 || p.servers.some(serverBad)) return "subs.dns.needServer";
  // a rule needs suffixes or country lists (or both), and a server
  if (p.split.some((r) => (r.suffixes.length === 0 && (r.geo ?? []).length === 0) || r.servers.length === 0 || r.servers.some(serverBad))) return "subs.dns.needSuffix";
  return null;
}

// a catalog server goes by its id alone; the panel fills in the addresses
export const cleanServers = (l: readonly DnsServerN[]) => l.map((s) => (s.providerVariant ? { providerVariant: s.providerVariant } : { kind: s.kind, address: s.address.trim() }));
/** The split rules as the panel takes them: every field of a rule is sent back, the country lists as much as the suffixes. */
export const cleanSplit = (split: readonly SplitRuleN[]) => split.map((r) => ({ suffixes: r.suffixes, geo: r.geo, servers: cleanServers(r.servers) }));

export const emptyServer = (): DnsServerN => ({ kind: DnsServerKind.PLAIN, address: "", providerVariant: "" });
export const variantServer = (id: string): DnsServerN => ({ kind: DnsServerKind.UNSPECIFIED, address: "", providerVariant: id });

/** A URL a preview iframe can load: the admin prefix is whatever <base href> says. */
export const previewUrl = (base: string, userId: string) => new URL(`preview/user-page/${encodeURIComponent(userId)}`, base).href;

/**
 * A preset description in the UI language. The built-ins store "Russian text\nEnglish text": the first line is
 * Russian, what follows the first newline English. A description without a newline (an instance's own, or one
 * language only) comes back as it is.
 */
export function describePreset(text: string, lang: string): string {
  const at = text.indexOf("\n");
  if (at < 0) return text.trim();
  return (lang === "ru" ? text.slice(0, at) : text.slice(at + 1)).trim();
}

const punyBase = 36;
/** RFC 3492 decoding of one label without its "xn--" prefix; the input comes back unchanged when it is not valid. */
function punyDecode(input: string): string {
  const out: number[] = [];
  const basic = input.lastIndexOf("-");
  for (let j = 0; j < Math.max(basic, 0); j++) out.push(input.charCodeAt(j));
  let i = 0;
  let n = 128;
  let bias = 72;
  for (let idx = basic > 0 ? basic + 1 : 0; idx < input.length; ) {
    const oldi = i;
    for (let w = 1, k = punyBase; ; k += punyBase) {
      if (idx >= input.length) return input;
      const c = input.charCodeAt(idx++);
      const digit = c - 48 < 10 ? c - 22 : c - 65 < 26 ? c - 65 : c - 97 < 26 ? c - 97 : punyBase;
      if (digit >= punyBase) return input;
      i += digit * w;
      const t = k <= bias ? 1 : k >= bias + 26 ? 26 : k - bias;
      if (digit < t) break;
      w *= punyBase - t;
    }
    const len = out.length + 1;
    let delta = oldi === 0 ? Math.floor(i / 700) : Math.floor((i - oldi) / 2);
    delta += Math.floor(delta / len);
    let k = 0;
    while (delta > 455) {
      delta = Math.floor(delta / 35);
      k += punyBase;
    }
    bias = k + Math.floor((36 * delta) / (delta + 38));
    n += Math.floor(i / len);
    i %= len;
    out.splice(i++, 0, n);
  }
  return String.fromCodePoint(...out);
}

/** A split suffix for people: ".xn--p1ai" is shown as ".рф"; anything that is not punycode stays as it is. */
export const unicodeSuffix = (s: string) => s.split(".").map((l) => (l.toLowerCase().startsWith("xn--") ? punyDecode(l.slice(4)) || l : l)).join(".");

/** The host of a server address for a chip: a DoH URL is cut to its host, the rest is shown as is. */
export function shortAddress(kind: DnsServerKind, address: string): string {
  if (kind !== DnsServerKind.DOH) return address;
  try {
    return new URL(address).host;
  } catch {
    return address;
  }
}

/** What a card's glyph stands for: the built-in presets by id, anything else is a plain resolver. */
export type PresetLook = { glyph: "map" | "globe" | "shieldOff" | "family" | "bugShield" | "letter" | "server"; tone: "sky" | "lavender" | "mint" | "sand" | "sage" | "rose" | "muted" };
const looks: Record<string, PresetLook> = {
  dns_builtin_ru_split: { glyph: "map", tone: "sky" },
  dns_builtin_ru_proxied: { glyph: "map", tone: "lavender" },
  dns_builtin_cn_split: { glyph: "map", tone: "mint" },
  dns_builtin_standard: { glyph: "globe", tone: "mint" },
  dns_builtin_adblock: { glyph: "shieldOff", tone: "sand" },
  dns_builtin_family: { glyph: "family", tone: "sage" },
  dns_builtin_quad9: { glyph: "bugShield", tone: "rose" },
  dns_builtin_yandex: { glyph: "letter", tone: "sand" },
  dns_builtin_yandex_family: { glyph: "letter", tone: "sage" },
  dns_builtin_yandex_safe: { glyph: "letter", tone: "rose" },
};
const categoryLooks: Partial<Record<DnsCategory, PresetLook>> = {
  [DnsCategory.REGIONAL]: { glyph: "map", tone: "sky" },
  [DnsCategory.REGULAR]: { glyph: "globe", tone: "mint" },
  [DnsCategory.NO_ADS]: { glyph: "shieldOff", tone: "sand" },
  [DnsCategory.FAMILY]: { glyph: "family", tone: "sage" },
  [DnsCategory.SECURITY]: { glyph: "bugShield", tone: "rose" },
};
/** The glyph of a preset: the first built-ins keep their own, the rest take the look of their category. */
export const presetLook = (p: { id: string; category?: DnsCategory }): PresetLook => looks[p.id] ?? categoryLooks[p.category ?? DnsCategory.UNSPECIFIED] ?? { glyph: "server", tone: "muted" };

// The built-in presets have one stored name (Russian). An English or Chinese UI shows these while the stored name is still
// the stock one; a name an admin edited is shown as stored. The Chinese names are the ones the user page shows
// (internal/panel/dns/node.go builtinNames).
const builtinNames: Record<string, { ru: string; en: string; zh: string }> = {
  dns_builtin_ru_split: { ru: "Россия: .ru напрямую", en: "Russia: .ru direct", zh: "俄罗斯：.ru 直连" },
  dns_builtin_ru_proxied: { ru: "Россия: всё через VPN", en: "Russia: all through VPN", zh: "俄罗斯：全部经 VPN" },
  dns_builtin_cn_split: { ru: "Китай: китайские сайты напрямую", en: "China: Chinese sites direct", zh: "中国：国内网站直连" },
  dns_builtin_standard: { ru: "Cloudflare + Google", en: "Cloudflare + Google", zh: "Cloudflare + Google" },
  dns_builtin_adblock: { ru: "AdGuard: без рекламы", en: "AdGuard: no ads", zh: "AdGuard：无广告" },
  dns_builtin_family: { ru: "AdGuard Family: без рекламы и 18+", en: "AdGuard Family: no ads, no 18+", zh: "AdGuard Family：无广告和成人内容" },
  dns_builtin_quad9: { ru: "Quad9: защита от вредоносных", en: "Quad9: malware blocking", zh: "Quad9：拦截恶意网站" },
  dns_builtin_yandex: { ru: "Яндекс DNS", en: "Yandex DNS", zh: "Yandex DNS" },
  dns_builtin_cloudflare: { ru: "Cloudflare: без фильтров", en: "Cloudflare: no filtering", zh: "Cloudflare：无过滤" },
  dns_builtin_google: { ru: "Google: без фильтров", en: "Google: no filtering", zh: "Google：无过滤" },
  dns_builtin_dnssb: { ru: "DNS.SB: без фильтров", en: "DNS.SB: no filtering", zh: "DNS.SB：无过滤" },
  dns_builtin_cloudflare_family: { ru: "Cloudflare Family: без вредоносных и 18+", en: "Cloudflare Family: no malware, no 18+", zh: "Cloudflare Family：无恶意网站和成人内容" },
  dns_builtin_yandex_family: { ru: "Яндекс семейный: без 18+", en: "Yandex Family: no 18+", zh: "Yandex Family：无成人内容" },
  dns_builtin_opendns_family: { ru: "OpenDNS FamilyShield: без 18+", en: "OpenDNS FamilyShield: no 18+", zh: "OpenDNS FamilyShield：无成人内容" },
  dns_builtin_cloudflare_security: { ru: "Cloudflare Security: защита от вредоносных", en: "Cloudflare Security: malware blocking", zh: "Cloudflare Security：拦截恶意网站" },
  dns_builtin_yandex_safe: { ru: "Яндекс безопасный: защита от вредоносных", en: "Yandex Safe: malware blocking", zh: "Yandex Safe：拦截恶意网站" },
};

/** The name of a preset in the UI language (see builtinNames). */
export function presetName(p: { id: string; name: string }, lang: string): string {
  const b = builtinNames[p.id];
  return b && lang !== "ru" && p.name === b.ru ? (lang === "zh" ? b.zh : b.en) : p.name;
}

// Who runs a resolver, by address: what a person knows it by ("Cloudflare · Google"), not the IPs.
const byAddress: readonly [RegExp, string][] = [
  [/^(1\.[01]\.[01]\.[1-3]$|2606:4700:4700:)/i, "Cloudflare"],
  [/^(8\.8\.[48]\.[48]$|2001:4860:4860:)/i, "Google"],
  [/^(9\.9\.9\.\d+$|149\.112\.112\.\d+$|2620:fe:)/i, "Quad9"],
  [/^94\.140\.1[45]\.\d+$/, "AdGuard"],
  [/^77\.88\.8\.\d+$/, "Яндекс"],
  [/^208\.67\.2(22\.222|20\.220)$/, "OpenDNS"],
];
const byHost: readonly [RegExp, string][] = [
  [/(^|\.)cloudflare-dns\.com$/, "Cloudflare"],
  [/(^|\.)dns\.google$/, "Google"],
  [/(^|\.)quad9\.net$/, "Quad9"],
  [/(^|\.)adguard-dns\.com$/, "AdGuard"],
  [/(^|\.)dns\.yandex\.(ru|net|com)$/, "Яндекс"],
  [/(^|\.)nextdns\.io$/, "NextDNS"],
  [/(^|\.)opendns\.com$/, "OpenDNS"],
];

/** "Cloudflare", "Google", "AdGuard"… for a resolver that is known (a catalog server by its provider), else its address (a DoH URL cut to its host). */
export function providerName(s: DnsServerN, catalog?: Catalog): string {
  if (s.providerVariant) return catalog?.get(s.providerVariant)?.provider.name ?? s.providerVariant;
  let host = (s.kind === DnsServerKind.DOH ? shortAddress(s.kind, s.address) : s.address).replace(/^\[([^\]]+)\](:\d+)?$/, "$1"); // "[v6]:53" -> "v6"
  if ((host.match(/:/g)?.length ?? 0) === 1) host = host.split(":")[0]!; // "host:853", "1.1.1.1:53": without the port
  const hit = [...byAddress, ...byHost].find(([re]) => re.test(host.toLowerCase()));
  return hit ? hit[1] : host;
}

/**
 * The line that says what a preset holds: its main resolvers by provider, the transport when it is not plain and some
 * server is from the catalog, then what goes direct ("Cloudflare · Google · DoH · .ru direct"). `direct` turns the first
 * split suffix into that phrase in the UI language; `transport` names DoT/DoH.
 */
export function presetSubtitle(
  p: { servers: readonly DnsServerN[]; split: readonly SplitRuleN[]; splitDirect: boolean; preferredTransport?: DnsTransport },
  direct: (suffix: string) => string,
  catalog?: Catalog,
  transport?: (t: DnsTransport) => string,
): string {
  const parts = [...new Set(p.servers.map((s) => providerName(s, catalog)))];
  const pref = p.preferredTransport ?? DnsTransport.PLAIN;
  if (transport && pref !== DnsTransport.PLAIN && pref !== DnsTransport.UNSPECIFIED && p.servers.some((s) => s.providerVariant)) parts.push(transport(pref));
  // the first thing that goes direct: a suffix, else (a rule of country lists only) the country code
  const rule = p.split.find((r) => r.suffixes.length > 0 || r.geo.length > 0);
  const first = rule?.suffixes[0] !== undefined ? unicodeSuffix(rule.suffixes[0]) : rule?.geo[0]?.toUpperCase();
  if (p.splitDirect && first) parts.push(direct(first));
  return parts.join(" · ");
}
