import { describe, expect, it } from "vitest";
import { cases, sampleQr } from "./dev-data";
import { dict, langs } from "./i18n";
import { normalize, safeTtUrl } from "./logic";
import { actions, data, state, text } from "./test-kit";
import { ttAppsUrl } from "./trusttunnel";
import { view } from "./view";

// The TrustTunnel key on the person's own page (subs/page_trusttunnel.go): the panel of the proxies (telegram.ts) with its own words, a place per tt:// link, the marks, a pane with no link
// for a self-signed server, the key among the devices (nothing to remove), and the words in all five languages.

const link = (over: object = {}) => ({ label: "Germany · Frankfurt", country_code: "DE", place: "Frankfurt", has_ipv6: false, relay: false, via: "", note: "", url: "tt://?AAEBAQ9kZTEuZXhhbXBsZS5jb20", qr: sampleQr, ...over });
const withTt = (links: object[]) => normalize({ ...cases.first, access: { happ: true, amnezia: false, trust_tunnel: true }, trust_tunnel: { links } });

describe("the TrustTunnel links of the page data", () => {
  it("keeps tt:// links, and a row with a note and no link; drops any other scheme without a word", () => {
    const d = normalize({
      trust_tunnel: { links: [link(), link({ url: "javascript:alert(1)" }), link({ url: "https://example.com/x" }), link({ url: "", note: "self_signed", label: "Finland" }), link({ url: "" })] },
    });
    expect(d.trust_tunnel?.links.map((l) => [l.url, l.note])).toEqual([
      ["tt://?AAEBAQ9kZTEuZXhhbXBsZS5jb20", ""],
      ["", "self_signed"],
    ]);
    expect(safeTtUrl("TT://?AAEB")).toBe("TT://?AAEB");
    expect(safeTtUrl("tg://proxy?server=a")).toBe("");
    expect(normalize({}).trust_tunnel).toBeNull();
    expect(normalize({ access: { trust_tunnel: true } }).access.trust_tunnel).toBe(true);
    expect(normalize({ devices: [{ id: "k", kind: "trusttunnel" }] }).devices[0]?.kind).toBe("trusttunnel");
  });
});

describe("the TrustTunnel block", () => {
  const tt = (d: ReturnType<typeof withTt>, lang: "ru" | "en" | "zh" | "fa" | "tr" = "en", a = actions()) => view(d, state("windows", { tab: "trusttunnel" }, lang), a);
  const pane = (el: Element) => el.querySelector<HTMLElement>(".u-tg .u-pane")!;
  const picks = (el: Element) => [...el.querySelectorAll<HTMLButtonElement>(".u-tg .u-pick")];
  const prows = (el: Element) => [...el.querySelectorAll<HTMLElement>(".u-prow")];

  it("is not there when it is off for the person", () => {
    const el = view(data("first"), state("windows", {}, "en"), actions());
    expect(el.querySelector(".u-tg, .u-prow, [data-k=tab-trusttunnel]")).toBeNull();
    expect(text(el)).not.toContain("TrustTunnel");
  });

  it("is a tab with the number of its links, and the panel of the proxies with its own words", () => {
    const el = tt(withTt([link(), link({ has_ipv6: true, url: "tt://?AAEB2" })]));
    expect(text(el.querySelector("[data-k=tab-trusttunnel]"))).toContain("TrustTunnel");
    expect(el.querySelector("[data-k=tab-trusttunnel] .n.only-m")?.textContent).toBe("2");
    expect(text(el.querySelector(".u-tg .u-cap"))).toBe("Server");
    expect(pane(el).getAttribute("aria-label")).toBe("TrustTunnel connection");
  });

  it("gives every link a place in the list: the marks of the twins; the chosen one has the pane with its QR code, 'Open in the app' and Copy", () => {
    const el = tt(withTt([link(), link({ has_ipv6: true, url: "tt://?AAEB2" }), link({ relay: true, via: "RU", url: "tt://?AAEB3" })]));
    expect(picks(el).map((b) => text(b.querySelector("span:nth-of-type(2)")))).toEqual(["GermanyFrankfurt", "GermanyFrankfurt", "GermanyFrankfurt"]);
    expect(picks(el).map((b) => text(b.querySelector(".u-tag")))).toEqual(["", "IPv6", "via RU"]);
    expect(picks(el).map((b) => b.getAttribute("aria-pressed"))).toEqual(["true", "false", "false"]);
    const p = pane(el);
    expect(p.querySelector(".u-qr.big svg")?.getAttribute("aria-label")).toBe("QR code of the TrustTunnel link: Germany");
    // the link itself is an ordinary tt:// anchor
    const open = p.querySelector<HTMLAnchorElement>("[data-k=tt-open-0]")!;
    expect([open.getAttribute("href"), text(open), open.classList.contains("pri")]).toEqual(["tt://?AAEBAQ9kZTEuZXhhbXBsZS5jb20", "Open in the app", true]);
    expect(text(p.querySelector("[data-k=tt-copy-0]"))).toContain("Copy link");
    expect(text(p.querySelector(".u-hint"))).toContain("Install the TrustTunnel app, then scan the QR code or open the link.");
    picks(el)[2]!.click();
    expect(pane(el).querySelector("[data-k=tt-open-2]")?.getAttribute("href")).toBe("tt://?AAEB3");
    picks(el)[0]!.click();
  });

  it("copies the link itself, and a phone has a row for each with 'Open' and a copy button", () => {
    const a = actions();
    const el = tt(withTt([link(), link({ url: "tt://?AAEB2" })]), "en", a);
    pane(el).querySelector<HTMLButtonElement>("[data-k=tt-copy-0]")!.click();
    expect(a.log).toEqual(["copy tt://?AAEBAQ9kZTEuZXhhbXBsZS5jb20"]);
    expect(prows(el)).toHaveLength(2);
    expect(text(el.querySelector("p.u-hint.only-m"))).toContain("Install the TrustTunnel app, then tap “Open in the app”.");
    const open = prows(el)[1]!.querySelector<HTMLAnchorElement>("[data-k=tt-open-m-1]")!;
    expect([text(open), open.getAttribute("href")]).toEqual(["Open", "tt://?AAEB2"]);
    const copy = prows(el)[1]!.querySelector<HTMLButtonElement>("[data-k=tt-copy-m-1]")!;
    expect(copy.getAttribute("aria-label")).toBe("Copy the TrustTunnel link: Germany");
    copy.click();
    expect(a.log.at(-1)).toBe("copy tt://?AAEB2");
  });

  it("points to the official page of the apps, the one the project's README names, on both sizes", () => {
    expect(ttAppsUrl).toBe("https://github.com/TrustTunnel/TrustTunnel#clients");
    for (const key of ["tt-get", "tt-get-m"]) {
      const a = tt(withTt([link()])).querySelector<HTMLAnchorElement>(`[data-k=${key}]`)!;
      expect(a.getAttribute("href")).toBe(ttAppsUrl);
      expect(a.getAttribute("rel")).toBe("noopener noreferrer");
      expect(a.getAttribute("target")).toBe("_blank");
      expect(text(a)).toBe("Where to get the app");
    }
  });

  it("says a self-signed server has no link yet, and offers nothing to open or copy there", () => {
    const el = tt(withTt([link({ url: "", note: "self_signed", label: "Finland", country_code: "FI", place: "" })]));
    expect(text(pane(el).querySelector(".u-hint"))).toBe("There is no link for this server yet — message the admin.");
    expect(pane(el).querySelector("a, button, .u-qr")).toBeNull();
    const row = prows(el)[0]!;
    expect(row.classList.contains("nolink")).toBe(true);
    expect(text(row.querySelector(".u-hint"))).toBe("There is no link for this server yet — message the admin.");
    expect(row.querySelector("a, button")).toBeNull();
  });

  it("says the keys are still being prepared when no TrustTunnel profile runs", () => {
    const el = tt(withTt([]));
    expect(picks(el)).toHaveLength(0);
    expect(text(el.querySelector(".u-card .u-empty"))).toBe("The keys are still being prepared — message the admin.");
  });

  it("lists the key among the devices with a name of its own and nothing to remove or rename", () => {
    const el = view(data("trusttunnel"), state("windows", { tab: "devices", devicesOpen: true }, "en"), actions());
    const rows = [...el.querySelectorAll<HTMLElement>(".u-dv")].filter((r) => text(r).includes("TrustTunnel key"));
    expect(rows).toHaveLength(1);
    expect(rows[0]!.querySelector("button")).toBeNull();
    expect(text(rows[0]!.querySelector(".when"))).toContain("connected");
  });

  it("shows no block on a page that is not active, only the key among the devices", () => {
    const el = view(data("trusttunnel-disabled"), state("ios", {}, "en"), actions());
    expect(el.querySelector(".u-tg, .u-prow")).toBeNull();
    expect(text(el)).toContain("TrustTunnel key");
  });

  it("draws the code the server drew for the link, and none for a link it drew none for", () => {
    const real = "tt://?AAEBAQ9kZTEuZXhhbXBsZS5jb20FEHVhbGljZTIzNDU2NzIzNDUGIGFCMjM0NTY3Y0QyMzQ1NjdlRjIzNDU2N2dIMjM0NTY3AhNkZTEuZXhhbXBsZS5jb206NDQzBAEACQEBDANkZTENFgoxOTIuMC4yLjUzCjE5Mi4wLjIuNTQ";
    const svg = pane(tt(withTt([link({ url: real })]))).querySelector(".u-qr.big svg");
    expect(Number(svg!.getAttribute("viewBox")!.split(" ")[2])).toBe(sampleQr.n);
    expect(pane(tt(withTt([link({ url: real, qr: null })]))).querySelector(".u-qr")).toBeNull();
  });
});

describe("the words of the TrustTunnel block", () => {
  it("are in all five languages", () => {
    const keys = ["ttT", "ttNoLinks", "ttOpen", "ttCopy", "ttHowPhone", "ttHowDesk", "ttGet", "ttNoLink", "ttKey", "ttCap", "ttPane"] as const;
    for (const lang of langs) {
      for (const k of keys) expect(dict[lang][k], `${lang} ${k}`).toBeTruthy();
      expect(dict[lang].ttQr("X"), lang).toContain("X");
      expect(dict[lang].ttCopyAria("X"), lang).toContain("X");
    }
    // the steps say what to do: install the app, then scan or open
    expect(dict.ru.ttHowDesk).toContain("отсканируйте QR");
    expect(dict.zh.ttHowDesk).toContain("扫描二维码");
  });
});
