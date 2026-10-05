import { describe, expect, it } from "vitest";
import { DnsCategory, DnsClient, DnsServerKind, DnsTransport } from "@/gen/mistgate/admin/v1/dns_pb";
import { appResults, catalogOf, cutAnnounce, describePreset, flagOf, move, parseSuffixes, presetLook, presetName, presetProblem, presetSubtitle, previewUrl, providerName, renderName, serverNames, serverProblem, transportOrder, unicodeSuffix, validDownload, validSupportLink, variantServer, type ClientSupport, type Provider } from "./model";

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
    const split = [{ suffixes: [".ru", ".su"], servers: [plain("77.88.8.8")] }];
    expect(presetSubtitle({ servers: [plain("1.1.1.1"), plain("8.8.8.8")], split, splitDirect: true }, direct)).toBe("Cloudflare · Google · .ru direct");
    expect(presetSubtitle({ servers: [plain("1.1.1.1"), plain("8.8.8.8")], split, splitDirect: false }, direct)).toBe("Cloudflare · Google");
  });
});

// a two-provider catalog, as ListDnsPresets would hand it over
const variant = (id: string, over: Partial<Provider["variants"][number]> = {}) => ({
  id, nameRu: "Обычный", nameEn: "Standard", category: DnsCategory.REGULAR, ipv4: ["1.1.1.1"], ipv6: [], plain: true, dotHost: "h.example", dotPort: 853, dohUrl: "https://h.example/dns-query", noteRu: "", noteEn: "", ...over,
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
    expect(presetLook({ id: "dns_builtin_ru_split", category: DnsCategory.RUSSIA }).tone).toBe("sky");
    expect(presetLook({ id: "dns_x", category: DnsCategory.UNSPECIFIED })).toEqual({ glyph: "server", tone: "muted" });
  });
});
