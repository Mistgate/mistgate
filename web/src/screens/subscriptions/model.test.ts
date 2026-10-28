import { describe, expect, it } from "vitest";
import { SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { zh } from "@/i18n/zh";
import { DnsCategory, DnsClient, DnsServerKind, DnsTransport } from "@/gen/mistgate/admin/v1/dns_pb";
import { appResults, formatDescKey, formatKey, ruleFormats, catalogOf, chipLabel, chipOf, cleanSplit, cutAnnounce, dnsChips, describePreset, flagOf, move, parseSuffixes, presetLook, presetName, presetProblem, presetSubtitle, previewUrl, providerName, renderName, serverNames, serverProblem, transportOrder, unicodeSuffix, validDownload, validSupportLink, variantLabel, variantServer, type ClientSupport, type Provider } from "./model";

describe("move", () => {
  it("moves one element and leaves the input alone", () => {
    const a = [1, 2, 3, 4];
    expect(move(a, 0, 2)).toEqual([2, 3, 1, 4]);
    expect(move(a, 3, 0)).toEqual([4, 1, 2, 3]);
    expect(a).toEqual([1, 2, 3, 4]);
  });
  it("ignores a move that goes nowhere", () => {
    expect(move([1, 2], 1, 1)).toEqual([1, 2]);
    expect(move([1, 2], 0, -1)).toEqual([1, 2]);
    expect(move([1, 2], 0, 2)).toEqual([1, 2]);
  });
});

describe("renderName", () => {
  const v = { flag: "🇩🇪", country: "Germany", node: "de1", profile: "hy2" };
  it("uses country and profile by default", () => {
    expect(renderName("", v)).toBe("🇩🇪 Germany · hy2");
    expect(renderName("   ", v)).toBe("🇩🇪 Germany · hy2");
  });
  it("fills every placeholder and tidies the spaces", () => {
    expect(renderName("{flag} {country} · {node} · {profile}", v)).toBe("🇩🇪 Germany · de1 · hy2");
    expect(renderName("{flag}  {node}  {profile}", { ...v, flag: "" })).toBe("de1 hy2");
  });
});

// the same cases as internal/panel/subs/names_test.go TestRemarks: the preview names servers as the subscription does
describe("serverNames", () => {
  const country = (code: string) => ({ DE: "Германия", NL: "Нидерланды" })[code] ?? code;
  const s = (node: string, countryCode: string, profile = "p") => ({ node, countryCode, profile });
  it("the default includes profile labels; a custom template can still use numeric suffixes", () => {
    expect(serverNames("", [s("de1", "DE", "fast"), s("de2", "DE", "safe"), s("nl1", "NL")], country)).toEqual(["🇩🇪 Германия · fast", "🇩🇪 Германия · safe", "🇳🇱 Нидерланды · p"]);
    expect(serverNames("{flag} {node}", [s("de1", "DE", "files"), s("de1", "DE", "files · WARP"), s("nl1", "NL")], country)).toEqual(["🇩🇪 de1", "🇩🇪 de1 2", "🇳🇱 nl1"]);
    expect(serverNames("{node}", [s("de1", "DE"), s("de1", "DE"), s("de1", "DE")], country)).toEqual(["de1", "de1 2", "de1 3"]);
    expect(serverNames("{node}", [s("x", ""), s("x 2", ""), s("x", "")], country)).toEqual(["x", "x 2", "x 3"]);
  });
  it("the profile shows by default and missing country does not leave a separator", () => {
    expect(serverNames("{node} {profile}", [s("de1", "DE", "a"), s("de1", "DE", "b")], country)).toEqual(["de1 a", "de1 b"]);
    expect(serverNames("", [s("de1", "", "Hysteria2")], country)).toEqual(["Hysteria2"]);
    expect(serverNames("", [s("de1", "DE", "")], country)).toEqual(["🇩🇪 Германия"]);
    expect(serverNames("{flag}", [s("de1", "")], country)).toEqual(["de1"]);
  });
  it("keeps the load percentage as part of the app server name", () => {
    expect(serverNames("", [{ ...s("de1", "DE", "hy2"), loadPercent: 64 }], country)).toEqual(["🇩🇪 Германия · hy2 · 64%"]);
  });
  it("fits Happ's 30-character server-title limit without dropping the load percentage", () => {
    const names = serverNames("", [
      { ...s("de1", "EE", "Hysteria2 · 443"), loadPercent: 74 },
      { ...s("de2", "EE", "Hysteria2 · 443"), loadPercent: 74 },
    ], country);
    expect(names).toEqual(["🇪🇪 EE · HY2 · 443 · 74%", "🇪🇪 EE · HY2 · 443 · 74% 2"]);
    expect(names.every((name) => name.length <= 30)).toBe(true);
  });
});

// the same cases as internal/panel/subs/names_test.go TestCutAnnounce: the counter and the preview cut like Happ gets it
describe("cutAnnounce", () => {
  it("never splits a word and says that it cut", () => {
    expect(cutAnnounce("short one", 20)).toBe("short one");
    expect(cutAnnounce("В субботу обновляю серверы — может моргнуть", 30)).toBe("В субботу обновляю серверы…");
    expect(cutAnnounce("one two three four", 9)).toBe("one two…");
    expect(cutAnnounce("one two three four", 8)).toBe("one two…");
    expect(cutAnnounce("я".repeat(25), 20)).toBe(`${"я".repeat(19)}…`);
    expect(cutAnnounce("a, b, c, d, e, f, g, h", 10)).toBe("a, b, c…");
    expect([...cutAnnounce("слово ".repeat(60))].length).toBeLessThanOrEqual(200);
  });
});

describe("flagOf", () => {
  it("maps two letters to regional indicators", () => {
    expect(flagOf("de")).toBe("\u{1F1E9}\u{1F1EA}");
    expect(flagOf("RU")).toBe("\u{1F1F7}\u{1F1FA}");
  });
  it("gives nothing for a code that is not two letters", () => {
    expect(flagOf("")).toBe("");
    expect(flagOf("DEU")).toBe("");
    expect(flagOf("1a")).toBe("");
  });
});

describe("validSupportLink", () => {
  it("accepts empty, https:// and Telegram links", () => {
    for (const ok of ["", "  ", "https://t.me/help", "HTTPS://example.com/x", "tg://resolve?domain=x"]) expect(validSupportLink(ok), ok).toBe(true);
  });
  it("refuses everything else, plain http included (the panel does too)", () => {
    for (const bad of ["javascript:alert(1)", "t.me/help", "ftp://x", "https://", "https://a b", "http://example.com/x"]) expect(validSupportLink(bad), bad).toBe(false);
  });
});

describe("validDownload", () => {
  it("takes empty or an https:// link, as the panel does", () => {
    for (const ok of ["", "https://example.com/app.apk"]) expect(validDownload(ok), ok).toBe(true);
    for (const bad of ["http://example.com/app.apk", "tg://resolve?domain=x", "example.com/app.apk", "https://a b"]) expect(validDownload(bad), bad).toBe(false);
  });
});

describe("parseSuffixes", () => {
  it("splits, lower-cases and drops repeats", () => {
    expect(parseSuffixes(" .RU, gosuslugi.ru ;.su  .ru")).toEqual([".ru", "gosuslugi.ru", ".su"]);
    expect(parseSuffixes("  ")).toEqual([]);
  });
});

describe("presetProblem", () => {
  const server = { kind: DnsServerKind.PLAIN, address: "1.1.1.1", providerVariant: "" };
  it("wants a name, a server and complete split rules", () => {
    expect(presetProblem({ name: " ", servers: [server], split: [] })).toBe("subs.dns.needName");
    expect(presetProblem({ name: "a", servers: [], split: [] })).toBe("subs.dns.needServer");
    expect(presetProblem({ name: "a", servers: [{ ...server, address: " " }], split: [] })).toBe("subs.dns.needServer");
    expect(presetProblem({ name: "a", servers: [server], split: [{ suffixes: [], servers: [server] }] })).toBe("subs.dns.needSuffix");
    expect(presetProblem({ name: "a", servers: [server], split: [{ suffixes: [".ru"], servers: [] }] })).toBe("subs.dns.needSuffix");
    expect(presetProblem({ name: "a", servers: [server], split: [{ suffixes: [".ru"], servers: [server] }] })).toBeNull();
    // a rule of country lists needs no suffix, but still a server
    expect(presetProblem({ name: "a", servers: [server], split: [{ suffixes: [], geo: ["cn"], servers: [server] }] })).toBeNull();
    expect(presetProblem({ name: "a", servers: [server], split: [{ suffixes: [".ru"], geo: ["cn"], servers: [server] }] })).toBeNull();
    expect(presetProblem({ name: "a", servers: [server], split: [{ suffixes: [], geo: ["cn"], servers: [] }] })).toBe("subs.dns.needSuffix");
    expect(presetProblem({ name: "a", servers: [server], split: [{ suffixes: [], geo: [], servers: [server] }] })).toBe("subs.dns.needSuffix");
  });
  it("takes a catalog server without an address", () => {
    const v = { kind: DnsServerKind.UNSPECIFIED, address: "", providerVariant: "cloudflare/family" };
    expect(presetProblem({ name: "a", servers: [v], split: [{ suffixes: [".ru"], servers: [v] }] })).toBeNull();
  });
});

describe("serverProblem", () => {
  const { PLAIN, DOH, DOT } = DnsServerKind;
  it("accepts what the panel accepts", () => {
    for (const a of ["1.1.1.1", "77.88.8.8:53", "2606:4700:4700::1111", "[2606:4700:4700::1111]:53"]) expect(serverProblem(PLAIN, a)).toBeNull();
    expect(serverProblem(DOH, "https://dns.adguard-dns.com/dns-query")).toBeNull();
    for (const a of ["dns.example.com", "94.140.14.14:853", "dns.example.com:853"]) expect(serverProblem(DOT, a)).toBeNull();
    expect(serverProblem(PLAIN, "  ")).toBeNull(); // empty is asked for by presetProblem
  });
  it("names the problem", () => {
    expect(serverProblem(PLAIN, "dns.example.com")).toBe("subs.dns.bad.plain");
    expect(serverProblem(PLAIN, "1.1.1.300")).toBe("subs.dns.bad.plain");
    expect(serverProblem(PLAIN, "1.1.1.1:99999")).toBe("subs.dns.bad.plain");
    expect(serverProblem(DOH, "dns.example.com/dns-query")).toBe("subs.dns.bad.doh");
    expect(serverProblem(DOH, "http://dns.example.com/dns-query")).toBe("subs.dns.bad.doh");
    expect(serverProblem(DOT, "https://dns.example.com")).toBe("subs.dns.bad.dot");
    expect(serverProblem(DOT, "dns..example.com")).toBe("subs.dns.bad.dot");
  });
  it("keeps presetProblem from saving a bad address", () => {
    const bad = { kind: DnsServerKind.PLAIN, address: "dns.example.com" };
    expect(presetProblem({ name: "a", servers: [bad], split: [] })).toBe("subs.dns.needServer");
  });
});

describe("unicodeSuffix", () => {
  it("shows punycode as Unicode", () => {
    expect(unicodeSuffix(".xn--p1ai")).toBe(".рф");
    expect(unicodeSuffix("xn--80aswg.xn--p1ai")).toBe("сайт.рф");
    expect(unicodeSuffix("xn--bcher-kva.example")).toBe("bücher.example");
    expect(unicodeSuffix(".ru")).toBe(".ru");
    expect(unicodeSuffix("xn--")).toBe("xn--");
  });
});

describe("describePreset", () => {
  const both = "Cloudflare и Google для всего\nCloudflare and Google for everything";
  it("shows the line of the UI language", () => {
    expect(describePreset(both, "ru")).toBe("Cloudflare и Google для всего");
    expect(describePreset(both, "en")).toBe("Cloudflare and Google for everything");
  });
  it("splits on the first newline only", () => {
    expect(describePreset("Раз\nTwo\nThree", "en")).toBe("Two\nThree");
  });
  it("leaves a text without a newline alone", () => {
    expect(describePreset("Мой резолвер для дома.", "en")).toBe("Мой резолвер для дома.");
    expect(describePreset("My own resolver", "ru")).toBe("My own resolver");
    expect(describePreset("", "en")).toBe("");
  });
});

describe("previewUrl", () => {
  it("follows the admin prefix", () => {
    expect(previewUrl("http://localhost:8081/4hbx/", "usr_1")).toBe("http://localhost:8081/4hbx/preview/user-page/usr_1");
    expect(previewUrl("http://localhost:8081/", "usr_1")).toBe("http://localhost:8081/preview/user-page/usr_1");
  });
});

describe("presetName", () => {
  it("shows the English name of a stock built-in in the English UI only", () => {
    expect(presetName({ id: "dns_builtin_adblock", name: "AdGuard: без рекламы" }, "en")).toBe("AdGuard: no ads");
    expect(presetName({ id: "dns_builtin_adblock", name: "AdGuard: без рекламы" }, "ru")).toBe("AdGuard: без рекламы");
    expect(presetName({ id: "dns_builtin_adblock", name: "Мой блок" }, "en")).toBe("Мой блок");
    expect(presetName({ id: "dns_custom", name: "Офис" }, "en")).toBe("Офис");
  });
  it("shows the Chinese name of a stock built-in in the Chinese UI only", () => {
    expect(presetName({ id: "dns_builtin_adblock", name: "AdGuard: без рекламы" }, "zh")).toBe("AdGuard：无广告");
    expect(presetName({ id: "dns_builtin_ru_split", name: "Россия: .ru напрямую" }, "zh")).toBe("俄罗斯：.ru 直连");
    expect(presetName({ id: "dns_builtin_adblock", name: "Мой блок" }, "zh")).toBe("Мой блок");
    expect(presetName({ id: "dns_custom", name: "Офис" }, "zh")).toBe("Офис");
  });
});

describe("variantLabel", () => {
  const entry = (id: string, nameRu: string, nameEn: string) => ({ provider: { name: "Cloudflare" } as Provider, variant: { id, nameRu, nameEn } as Provider["variants"][number] });
  it("names the variant in the UI language", () => {
    const family = entry("cloudflare/family", "Семейный", "Family");
    expect(variantLabel(family, "ru")).toBe("Cloudflare · Семейный");
    expect(variantLabel(family, "en")).toBe("Cloudflare · Family");
    expect(variantLabel(family, "zh")).toBe("Cloudflare · 儿童");
  });
  it("falls back to English for a catalog variant it has no Chinese name for", () => {
    expect(variantLabel(entry("cloudflare/new", "Новый", "Fresh"), "zh")).toBe("Cloudflare · Fresh");
  });
});

describe("presetSubtitle", () => {
  const plain = (address: string) => ({ kind: DnsServerKind.PLAIN, address, providerVariant: "" });
  const direct = (s: string) => `${s} direct`;
  it("names the providers, once each", () => {
    expect(providerName(plain("1.1.1.1"))).toBe("Cloudflare");
    expect(providerName(plain("8.8.8.8:53"))).toBe("Google");
    expect(providerName({ kind: DnsServerKind.DOH, address: "https://dns.adguard-dns.com/dns-query", providerVariant: "" })).toBe("AdGuard");
    expect(providerName({ kind: DnsServerKind.DOT, address: "dns.example.com:853", providerVariant: "" })).toBe("dns.example.com");
    expect(providerName(plain("203.0.113.7"))).toBe("203.0.113.7");
    expect(presetSubtitle({ servers: [plain("94.140.14.14"), plain("94.140.15.15"), { kind: DnsServerKind.DOH, address: "https://dns.adguard-dns.com/dns-query", providerVariant: "" }], split: [], splitDirect: false }, direct)).toBe("AdGuard");
  });
  it("adds what goes direct", () => {
    const split = [{ suffixes: [".ru", ".su"], geo: [], servers: [plain("77.88.8.8")] }];
    expect(presetSubtitle({ servers: [plain("1.1.1.1"), plain("8.8.8.8")], split, splitDirect: true }, direct)).toBe("Cloudflare · Google · .ru direct");
    expect(presetSubtitle({ servers: [plain("1.1.1.1"), plain("8.8.8.8")], split, splitDirect: false }, direct)).toBe("Cloudflare · Google");
  });
  it("says a country when the split is only country lists", () => {
    const split = [{ suffixes: [], geo: ["cn"], servers: [plain("223.5.5.5")] }];
    expect(presetSubtitle({ servers: [plain("1.1.1.1"), plain("8.8.8.8")], split, splitDirect: true }, direct)).toBe("Cloudflare · Google · CN direct");
    expect(presetSubtitle({ servers: [plain("1.1.1.1")], split, splitDirect: false }, direct)).toBe("Cloudflare");
  });
});

// a two-provider catalog, as ListDnsPresets would hand it over
const variant = (id: string, over: Partial<Provider["variants"][number]> = {}) => ({
  id, nameRu: "Обычный", nameEn: "Standard", category: DnsCategory.REGULAR, country: "", ipv4: ["1.1.1.1"], ipv6: [], plain: true, dotHost: "h.example", dotPort: 853, dohUrl: "https://h.example/dns-query", noteRu: "", noteEn: "", ...over,
});
const providers: Provider[] = [
  { id: "cloudflare", name: "Cloudflare", variants: [variant("cloudflare/standard"), variant("cloudflare/family", { category: DnsCategory.FAMILY })] },
  { id: "mullvad", name: "Mullvad", variants: [variant("mullvad/adblock", { plain: false, category: DnsCategory.NO_ADS })] },
  { id: "opendns", name: "OpenDNS", variants: [variant("opendns/familyshield", { dotHost: "", category: DnsCategory.FAMILY })] },
];
const catalog = catalogOf(providers);
const support: ClientSupport[] = [
  { client: DnsClient.HAPP, transports: [DnsTransport.DOH, DnsTransport.PLAIN], ipv6: false },
  { client: DnsClient.MIHOMO, transports: [DnsTransport.DOH, DnsTransport.DOT, DnsTransport.PLAIN], ipv6: true },
  { client: DnsClient.AMNEZIAWG, transports: [DnsTransport.PLAIN], ipv6: false },
];

describe("transportOrder", () => {
  it("walks DoH, DoT, plain and only then back up (the panel does the same)", () => {
    expect(transportOrder(DnsTransport.DOH)).toEqual([DnsTransport.DOH, DnsTransport.DOT, DnsTransport.PLAIN]);
    expect(transportOrder(DnsTransport.DOT)).toEqual([DnsTransport.DOT, DnsTransport.PLAIN, DnsTransport.DOH]);
    expect(transportOrder(DnsTransport.PLAIN)).toEqual([DnsTransport.PLAIN, DnsTransport.DOH, DnsTransport.DOT]);
    expect(transportOrder(DnsTransport.UNSPECIFIED)[0]).toBe(DnsTransport.PLAIN);
  });
});

describe("appResults", () => {
  const got = (servers: Parameters<typeof appResults>[0], pref: DnsTransport) => appResults(servers, pref, support, catalog).map((r) => r.transport);
  const { PLAIN, DOT, DOH } = DnsTransport;
  it("gives every app the preferred transport it can carry, else the next one down", () => {
    const cf = [variantServer("cloudflare/standard")];
    expect(got(cf, DOH)).toEqual([DOH, DOH, PLAIN]);
    expect(got(cf, DOT)).toEqual([PLAIN, DOT, PLAIN]); // Happ has no DoT
    expect(got(cf, PLAIN)).toEqual([PLAIN, PLAIN, PLAIN]);
  });
  it("skips what a variant lacks: no DoT steps down, no plain DNS steps up, nothing for a plain-only app", () => {
    expect(got([variantServer("opendns/familyshield")], DOT)).toEqual([PLAIN, PLAIN, PLAIN]);
    expect(got([variantServer("mullvad/adblock")], PLAIN)).toEqual([DOH, DOH, null]);
    expect(got([variantServer("mullvad/adblock"), variantServer("cloudflare/standard")], PLAIN)).toEqual([DOH, DOH, PLAIN]); // the first server an app can carry
  });
  it("keeps the kind of a custom server", () => {
    const dot = { kind: DnsServerKind.DOT, address: "dns.example.com", providerVariant: "" };
    expect(got([dot], DOH)).toEqual([null, DOT, null]);
  });
});

describe("appResults for the JSON formats", () => {
  const { PLAIN, DOT, DOH } = DnsTransport;
  // as ListDnsPresets sends them (internal/panel/dns transport.go Clients)
  const all: ClientSupport[] = [
    support[0]!,
    support[1]!,
    { client: DnsClient.XRAY, transports: [DOH, PLAIN], ipv6: false },
    { client: DnsClient.SINGBOX, transports: [DOH, DOT, PLAIN], ipv6: false },
    support[2]!,
  ];
  const two = variantServer("cloudflare/family");
  const pairCatalog = catalogOf([{ id: "cloudflare", name: "Cloudflare", variants: [variant("cloudflare/family", { ipv4: ["1.1.1.3", "1.0.0.3"] })] }]);
  const singbox = (servers: Parameters<typeof appResults>[0], pref: DnsTransport, cat = catalog) => appResults(servers, pref, all, cat).find((r) => r.client === DnsClient.SINGBOX)!;
  it("Xray JSON has no DoT and steps down to plain; sing-box carries DoT", () => {
    expect(appResults([variantServer("cloudflare/standard")], DOT, all, catalog).map((r) => r.transport)).toEqual([PLAIN, DOT, PLAIN, DOT, PLAIN]);
    expect(appResults([{ kind: DnsServerKind.DOT, address: "dns.example.com", providerVariant: "" }], DOH, all, catalog).map((r) => r.transport)).toEqual([null, DOT, null, DOT, null]);
  });
  it("counts the resolvers an app is handed, so the editor can say sing-box keeps only the first", () => {
    expect(singbox([two], PLAIN, pairCatalog).resolvers).toBe(2); // a variant's two plain addresses
    expect(singbox([two], DOH, pairCatalog).resolvers).toBe(1); // one DoH URL
    expect(singbox([variantServer("cloudflare/standard"), variantServer("mullvad/adblock")], DOH).resolvers).toBe(2);
    expect(singbox([variantServer("cloudflare/standard"), { kind: DnsServerKind.PLAIN, address: "9.9.9.9", providerVariant: "" }], DOH).resolvers).toBe(2);
    expect(singbox([], DOH)).toMatchObject({ transport: null, resolvers: 0 });
  });
});

describe("the subtitle and the look of a catalog preset", () => {
  const direct = (s: string) => `${s} direct`;
  const name = (t: DnsTransport) => (t === DnsTransport.DOH ? "DoH" : "DoT");
  const servers = [variantServer("cloudflare/standard"), variantServer("mullvad/adblock")];
  it("names the providers and, off plain, the transport", () => {
    expect(providerName(servers[0]!, catalog)).toBe("Cloudflare");
    expect(presetSubtitle({ servers, split: [], splitDirect: false, preferredTransport: DnsTransport.DOH }, direct, catalog, name)).toBe("Cloudflare · Mullvad · DoH");
    expect(presetSubtitle({ servers, split: [], splitDirect: false, preferredTransport: DnsTransport.PLAIN }, direct, catalog, name)).toBe("Cloudflare · Mullvad");
    // custom servers ignore the preference, so it is not claimed
    const custom = [{ kind: DnsServerKind.PLAIN, address: "1.1.1.1", providerVariant: "" }];
    expect(presetSubtitle({ servers: custom, split: [], splitDirect: false, preferredTransport: DnsTransport.DOH }, direct, catalog, name)).toBe("Cloudflare");
  });
  it("takes the glyph of the category unless the preset has its own", () => {
    expect(presetLook({ id: "dns_builtin_cloudflare_family", category: DnsCategory.FAMILY }).glyph).toBe("family");
    expect(presetLook({ id: "dns_builtin_ru_split", category: DnsCategory.REGIONAL }).tone).toBe("sky");
    expect(presetLook({ id: "dns_custom", category: DnsCategory.REGIONAL }).glyph).toBe("map");
    expect(presetLook({ id: "dns_x", category: DnsCategory.UNSPECIFIED })).toEqual({ glyph: "server", tone: "muted" });
  });
});

describe("the editor keeps the country lists of a split rule", () => {
  const server = { kind: DnsServerKind.PLAIN, address: " 223.5.5.5 ", providerVariant: "" };
  it("sends geo back with the suffixes and the cleaned servers", () => {
    expect(cleanSplit([{ suffixes: [".cn"], geo: ["cn"], servers: [server, variantServer("dnspod/standard")] }])).toEqual([
      { suffixes: [".cn"], geo: ["cn"], servers: [{ kind: DnsServerKind.PLAIN, address: "223.5.5.5" }, { providerVariant: "dnspod/standard" }] },
    ]);
    // the built-in China rule: no suffix, one country list
    expect(cleanSplit([{ suffixes: [], geo: ["cn"], servers: [variantServer("alidns/standard")] }])).toEqual([{ suffixes: [], geo: ["cn"], servers: [{ providerVariant: "alidns/standard" }] }]);
    expect(cleanSplit([{ suffixes: [".ru"], geo: [], servers: [variantServer("yandex/basic")] }])[0]!.geo).toEqual([]);
  });
});

describe("DNS chips", () => {
  const t = Object.assign((k: string) => ({ "subs.dns.cat.russia": "Russia", "subs.dns.cat.china": "China", "subs.dns.cat.regular": "Regular", "subs.dns.cat.own": "Own", "subs.dns.cat.regional": "Regional" })[k] ?? k, { locale: "en" }) as unknown as Parameters<typeof chipLabel>[1];
  const item = (category: DnsCategory, country = "") => ({ category, country });
  it("files a regional preset under its country and puts Russia and China first", () => {
    const chips = dnsChips([item(DnsCategory.REGULAR), item(DnsCategory.REGIONAL, "CN"), item(DnsCategory.UNSPECIFIED), item(DnsCategory.REGIONAL, "RU"), item(DnsCategory.REGIONAL, "CN"), item(DnsCategory.REGIONAL, "IR")]);
    expect(chips).toEqual([chipOf(item(DnsCategory.REGIONAL, "RU")), chipOf(item(DnsCategory.REGIONAL, "CN")), chipOf(item(DnsCategory.REGIONAL, "IR")), String(DnsCategory.REGULAR), String(DnsCategory.UNSPECIFIED)]);
    expect(chips.map((c) => chipLabel(c, t))).toEqual(["Russia", "China", "Iran", "Regular", "Own"]);
  });
  it("has no chip for what nobody is filed under", () => {
    expect(dnsChips([])).toEqual([]);
    expect(dnsChips([item(DnsCategory.FAMILY)])).toEqual([String(DnsCategory.FAMILY)]);
  });
  it("names the country in the three languages and a stock China preset in all of them", () => {
    for (const dict of [en, ru, zh] as Record<string, string>[]) {
      expect(dict["subs.dns.cat.china"]).toBeTruthy();
      expect(dict["subs.dns.cat.regional"]).toBeTruthy();
    }
    expect(presetName({ id: "dns_builtin_cn_split", name: "Китай: китайские сайты напрямую" }, "en")).toBe("China: Chinese sites direct");
    expect(presetName({ id: "dns_builtin_cn_split", name: "Китай: китайские сайты напрямую" }, "zh")).toBe("中国：国内网站直连");
    expect(presetName({ id: "dns_builtin_cn_split", name: "Китай: китайские сайты напрямую" }, "ru")).toBe("Китай: китайские сайты напрямую");
  });
});

describe("format labels", () => {
  it("name every format a rule can hold, in all three languages, so a migrated rule is not shown as the link list", () => {
    for (const f of [SubFormat.BASE64_URIS, SubFormat.MIHOMO_YAML, SubFormat.XRAY_JSON, SubFormat.SINGBOX_JSON]) {
      for (const key of [formatKey[f], formatDescKey[f]]) {
        expect(key).toBeTruthy();
        for (const dict of [en, ru, zh] as Record<string, string>[]) expect(dict[key!]).toBeTruthy();
      }
    }
    expect(en[formatKey[SubFormat.SINGBOX_JSON]!]).toBe("sing-box JSON");
    expect(formatKey[SubFormat.XRAY_JSON]).not.toBe(formatKey[SubFormat.BASE64_URIS]);
  });

  it("offer both JSON formats to a rule, and every new line of theirs is written in all three languages", () => {
    expect(ruleFormats).toEqual([SubFormat.BASE64_URIS, SubFormat.MIHOMO_YAML, SubFormat.XRAY_JSON, SubFormat.SINGBOX_JSON, SubFormat.USER_PAGE, SubFormat.DECOY]);
    const keys = ["subs.fmt.who.singbox", "subs.fmt.xray.out", "subs.fmt.singbox.out", "subs.clientSingbox", "subs.dns.app.xray", "subs.dns.app.singbox", "subs.dns.why.xray", "subs.dns.why.singbox"] as const;
    for (const k of keys) for (const dict of [en, ru, zh]) expect(dict[k].trim(), k).not.toBe("");
    expect(ru["subs.fmt.who.singbox"].split("|")).toHaveLength(3); // one, few, many
    expect(en["subs.fmt.who.singbox"].split("|")).toHaveLength(2);
    for (const k of keys) expect(zh[k], k).not.toMatch(/您|["“”]/); // the glossary: 你 or nothing, quotes are 「」
  });
});
