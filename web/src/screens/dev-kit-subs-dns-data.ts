import { DnsCategory, DnsClient, DnsServerKind, DnsTransport } from "@/gen/mistgate/admin/v1/dns_pb";
import type { DnsData, DnsServerN, Preset, SplitRuleN } from "./subscriptions/model";

// Development only: the made-up data of the scenes of the tab "DNS" (dev-kit-subs-dns.tsx): the panel's own catalog of public resolvers
// (internal/panel/dns/providers.go, the addresses its sources name), the built-in presets of the board's list (PnSubs: Cloudflare, Quad9,
// AdGuard without ads, a family one, Russia with the split) under the ids and names the panel gives them, and presets of "alice's"
// panel of its own (Office, a long one) with documentation addresses only (203.0.113.x, example.com).

const variant = (id: string, nameRu: string, nameEn: string, category: DnsCategory, ipv4: string[], noteRu: string, noteEn: string, o: { country?: string; dot?: string; doh?: string; plain?: boolean } = {}) => ({
  id, nameRu, nameEn, category, country: o.country ?? "", ipv4, ipv6: [], plain: o.plain ?? true, dotHost: o.dot ?? "", dotPort: o.dot ? 853 : 0, dohUrl: o.doh ?? "", noteRu, noteEn,
});

/** The catalog: what the panel ships, grouped by operator. */
export const providers = [
  { id: "cloudflare", name: "Cloudflare", variants: [
    variant("cloudflare/standard", "Обычный", "Standard", DnsCategory.REGULAR, ["1.1.1.1", "1.0.0.1"], "Быстрый, без фильтров. DoH к Cloudflare из России блокируется с 08.2026 — внутри туннеля работает.", "Fast, no filtering. DoH to Cloudflare is blocked in Russia since 08.2026 — fine inside the tunnel.", { dot: "one.one.one.one", doh: "https://cloudflare-dns.com/dns-query" }),
    variant("cloudflare/security", "Без вредоносных", "Security", DnsCategory.SECURITY, ["1.1.1.2", "1.0.0.2"], "Блокирует вредоносные и фишинговые сайты. DoH из России блокируется с 08.2026.", "Blocks malware and phishing. DoH is blocked in Russia since 08.2026.", { dot: "security.cloudflare-dns.com", doh: "https://security.cloudflare-dns.com/dns-query" }),
    variant("cloudflare/family", "Семейный", "Family", DnsCategory.FAMILY, ["1.1.1.3", "1.0.0.3"], "Блокирует вредоносные сайты и контент для взрослых. DoH из России блокируется с 08.2026.", "Blocks malware and adult content. DoH is blocked in Russia since 08.2026.", { dot: "family.cloudflare-dns.com", doh: "https://family.cloudflare-dns.com/dns-query" }),
  ] },
  { id: "google", name: "Google", variants: [variant("google/standard", "Обычный", "Standard", DnsCategory.REGULAR, ["8.8.8.8", "8.8.4.4"], "Без фильтров.", "No filtering.", { dot: "dns.google", doh: "https://dns.google/dns-query" })] },
  { id: "quad9", name: "Quad9", variants: [variant("quad9/standard", "Защита от вредоносных", "Secure", DnsCategory.SECURITY, ["9.9.9.9", "149.112.112.112"], "Блокирует известные вредоносные и фишинговые домены.", "Blocks known malware and phishing domains.", { dot: "dns.quad9.net", doh: "https://dns.quad9.net/dns-query" })] },
  { id: "adguard", name: "AdGuard", variants: [
    variant("adguard/default", "Без рекламы", "Default", DnsCategory.NO_ADS, ["94.140.14.14", "94.140.15.15"], "Блокирует рекламу и трекеры.", "Blocks ads and trackers.", { dot: "dns.adguard-dns.com", doh: "https://dns.adguard-dns.com/dns-query" }),
    variant("adguard/family", "Семейный", "Family", DnsCategory.FAMILY, ["94.140.14.15", "94.140.15.16"], "Блокирует рекламу, трекеры и контент для взрослых.", "Blocks ads, trackers and adult content.", { dot: "family.adguard-dns.com", doh: "https://family.adguard-dns.com/dns-query" }),
    variant("adguard/unfiltered", "Без фильтров", "Unfiltered", DnsCategory.REGULAR, ["94.140.14.140", "94.140.14.141"], "Ничего не блокирует.", "Blocks nothing.", { dot: "unfiltered.adguard-dns.com", doh: "https://unfiltered.adguard-dns.com/dns-query" }),
  ] },
  { id: "yandex", name: "Яндекс", variants: [
    variant("yandex/basic", "Базовый", "Basic", DnsCategory.REGIONAL, ["77.88.8.8", "77.88.8.1"], "Российские сайты открываются надёжнее. DoH и DoT из России работают.", "Russian sites resolve more reliably. DoH and DoT work from Russia.", { country: "RU", dot: "common.dot.dns.yandex.net", doh: "https://common.dot.dns.yandex.net/dns-query" }),
    variant("yandex/safe", "Безопасный", "Safe", DnsCategory.SECURITY, ["77.88.8.88", "77.88.8.2"], "Блокирует вредоносные и мошеннические сайты. DoH и DoT из России работают.", "Blocks malware and fraud sites. DoH and DoT work from Russia.", { dot: "safe.dot.dns.yandex.net", doh: "https://safe.dot.dns.yandex.net/dns-query" }),
    variant("yandex/family", "Семейный", "Family", DnsCategory.FAMILY, ["77.88.8.7", "77.88.8.3"], "Блокирует контент для взрослых и вредоносные сайты. DoH и DoT из России работают.", "Blocks adult content and malware. DoH and DoT work from Russia.", { dot: "family.dot.dns.yandex.net", doh: "https://family.dot.dns.yandex.net/dns-query" }),
  ] },
  { id: "alidns", name: "AliDNS", variants: [variant("alidns/standard", "Обычный", "Standard", DnsCategory.REGIONAL, ["223.5.5.5", "223.6.6.6"], "Китайские сайты открываются надёжнее. Только обычный DNS, без DoH и DoT.", "Chinese sites resolve more reliably. Plain DNS only, no DoH or DoT.", { country: "CN" })] },
] as unknown as DnsData["providers"];

/** What each app can carry, in the order the panel lists them. */
export const clientSupport = [
  { client: DnsClient.HAPP, transports: [DnsTransport.DOH, DnsTransport.PLAIN], ipv6: false },
  { client: DnsClient.MIHOMO, transports: [DnsTransport.DOH, DnsTransport.DOT, DnsTransport.PLAIN], ipv6: true },
  { client: DnsClient.AMNEZIAWG, transports: [DnsTransport.PLAIN], ipv6: false },
  { client: DnsClient.XRAY, transports: [DnsTransport.DOH, DnsTransport.PLAIN], ipv6: false },
  { client: DnsClient.SINGBOX, transports: [DnsTransport.DOH, DnsTransport.DOT, DnsTransport.PLAIN], ipv6: false },
] as unknown as DnsData["clientSupport"];

/** A server of the catalog (the panel stores the id alone and fills the addresses in). */
export const cat = (id: string): DnsServerN => ({ kind: DnsServerKind.UNSPECIFIED, address: "", providerVariant: id });
/** A server typed by hand. */
export const own = (address: string, kind: DnsServerKind = DnsServerKind.PLAIN): DnsServerN => ({ kind, address, providerVariant: "" });
/** A split rule. */
export const rule = (suffixes: string[], servers: DnsServerN[], geo: string[] = []): SplitRuleN => ({ suffixes, geo, servers });

type Make = Partial<Preset> & { id: string; name: string };
const preset = (p: Make): Preset => ({
  description: "", builtin: true, servers: [], split: [], ipv4Only: false, userCount: 0, isDefault: false, splitDirect: false, preferredTransport: DnsTransport.PLAIN, category: DnsCategory.REGULAR, country: "", blockAds: false, ...p,
});

/** The ids the board's "Как выбрать" names. */
export const ids = { cf: "dns_builtin_cloudflare", q9: "dns_builtin_quad9", ag: "dns_builtin_adblock", fam: "dns_builtin_cloudflare_family", ru: "dns_builtin_ru_split", proxied: "dns_builtin_ru_proxied", ads: "dns_builtin_ads_device", office: "dns_own_office", long: "dns_own_long", cn: "dns_own_cn" } as const;

/** The board's five presets of the list (Cloudflare the default, "Россия" with the split), under the panel's own ids and names. */
export const boardPresets: Preset[] = [
  preset({ id: ids.cf, name: "Cloudflare: без фильтров", description: "Быстрый публичный резолвер\nFast public resolver", servers: [cat("cloudflare/standard")], preferredTransport: DnsTransport.DOH, userCount: 3, isDefault: true }),
  preset({ id: ids.q9, name: "Quad9: защита от вредоносных", description: "С блокировкой вредоносных доменов\nBlocks malicious domains", servers: [cat("quad9/standard")], preferredTransport: DnsTransport.DOH, userCount: 1, category: DnsCategory.SECURITY }),
  preset({ id: ids.ag, name: "AdGuard: без рекламы", description: "Режет рекламу и трекеры на уровне DNS\nCuts ads and trackers at the DNS level", servers: [cat("adguard/default")], preferredTransport: DnsTransport.DOH, userCount: 1, category: DnsCategory.NO_ADS, blockAds: false }),
  preset({ id: ids.fam, name: "Cloudflare Family: без вредоносных и 18+", description: "Cloudflare Family: без взрослых сайтов\nCloudflare Family: no adult sites", servers: [cat("cloudflare/family")], preferredTransport: DnsTransport.DOH, userCount: 1, category: DnsCategory.FAMILY }),
  preset({
    id: ids.ru, name: "Россия: .ru напрямую", description: "Российские домены через Яндекс, остальное через Cloudflare\nRussian domains through Yandex, the rest through Cloudflare",
    servers: [cat("cloudflare/standard")], split: [rule([".ru", ".su", ".xn--p1ai"], [cat("yandex/basic")])], splitDirect: true, preferredTransport: DnsTransport.DOH, category: DnsCategory.REGIONAL, country: "RU",
  }),
];

const proxied = preset({
  id: ids.proxied, name: "Россия: всё через VPN", description: "Российские домены спрашиваются на Яндексе, но идут через туннель\nRussian domains asked on Yandex, but through the tunnel",
  servers: [cat("cloudflare/standard")], split: [rule([".ru", ".su", ".xn--p1ai"], [cat("yandex/basic")])], preferredTransport: DnsTransport.DOH, category: DnsCategory.REGIONAL, country: "RU", userCount: 2,
});
const adsDevice = preset({
  id: ids.ads, name: "Без рекламы на устройстве", description: "Приложение само режет рекламу и трекеры по списку category-ads-all; DNS — Cloudflare и Google\nThe app cuts ads and trackers itself by the category-ads-all list; DNS — Cloudflare and Google",
  servers: [cat("cloudflare/standard"), cat("google/standard")], category: DnsCategory.NO_ADS, blockAds: true,
});
export const cnSplit = preset({
  id: "dns_builtin_cn_split", name: "Китай: китайские сайты напрямую", description: "Китайские сайты через AliDNS, остальное через Cloudflare\nChinese sites through AliDNS, the rest through Cloudflare",
  servers: [cat("cloudflare/standard")], split: [rule([], [cat("alidns/standard")], ["cn"])], splitDirect: true, category: DnsCategory.REGIONAL, country: "CN",
});

/** An own preset: the Office (a catalog server, a plain one, a split to Yandex, ads blocked), the one that has a person or two. */
export const office = preset({
  id: ids.office, name: "Офис", description: "Для офиса в Москве", builtin: false, servers: [cat("quad9/standard"), own("203.0.113.53")], split: [rule([".ru", ".рф"], [own("203.0.113.77")])],
  splitDirect: true, blockAds: true, userCount: 4, category: DnsCategory.UNSPECIFIED,
});

/** What the panel shows when it has been used a while: the board's five, the other built-ins, and presets of its own with people on them. */
export const manyPresets: Preset[] = [
  ...boardPresets.map((p, i) => (i === 0 ? { ...p, userCount: 14 } : p)),
  proxied,
  adsDevice,
  cnSplit,
  office,
  preset({ id: "dns_own_kids", name: "Детский", description: "Для детей и школы", builtin: false, servers: [cat("cloudflare/family"), cat("yandex/family")], preferredTransport: DnsTransport.DOT, userCount: 6, category: DnsCategory.UNSPECIFIED }),
  preset({ id: "dns_own_spare", name: "Запасной", description: "", builtin: false, servers: [own("203.0.113.9")], category: DnsCategory.UNSPECIFIED }),
];

/** Names and descriptions as long as a person makes them. */
export const longPresets: Preset[] = [
  { ...boardPresets[0]!, name: "Cloudflare для всей семьи, включая бабушку на даче и двоюродных братьев за границей", description: "Быстрый публичный резолвер без фильтров, который подходит и для домашней сети, и для всех, кто подключается к нам через мобильный интернет в поездках по стране\nFast public resolver" },
  preset({
    id: ids.long, name: "Очень длинное название пресета, которое не помещается в одну строку ни на телефоне, ни в таблице", description: "Одна короткая строка, которая на самом деле совсем не короткая: она рассказывает, для чего пресет, кто им пользуется и когда его нужно менять", builtin: false,
    servers: [cat("cloudflare/standard"), cat("google/standard"), cat("quad9/standard"), cat("adguard/default"), own("https://dns-with-a-very-long-name.internal.example.com/dns-query", DnsServerKind.DOH)],
    split: [rule([".example.com", ".example.org", ".internal.example.net", ".тест"], [own("203.0.113.8")])], splitDirect: true, preferredTransport: DnsTransport.DOT, userCount: 1234, category: DnsCategory.UNSPECIFIED,
  }),
  ...boardPresets.slice(1, 3),
];

/** A preset of its own that is the default right now (deleting it is not offered). */
export const ownDefault = [{ ...boardPresets[0]!, isDefault: false }, { ...office, isDefault: true }, ...boardPresets.slice(1)];
