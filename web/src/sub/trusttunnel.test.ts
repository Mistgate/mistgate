import { describe, expect, it } from "vitest";
import { cases } from "./dev-data";
import { dict, langs } from "./i18n";
import { normalize, safeTtUrl } from "./logic";
import { qrSvg } from "./qr";
import { actions, data, state, text } from "./test-kit";
import { ttAppsUrl } from "./trusttunnel";
import { view } from "./view";

// The TrustTunnel key on the person's own page (subs/page_trusttunnel.go): one card per tt:// link, the marks, a card with no link
// for a self-signed server, the key among the devices (nothing to remove), and the words in all five languages.

const link = (over: object = {}) => ({ label: "Germany · Frankfurt", country_code: "DE", place: "Frankfurt", has_ipv6: false, relay: false, via: "", note: "", url: "tt://?AAEBAQ9kZTEuZXhhbXBsZS5jb20", ...over });
const withTt = (links: object[]) => normalize({ ...cases.first, access: { happ: true, amnezia: false, trust_tunnel: true }, trust_tunnel: { links } });
const cards = (el: Element) => [...el.querySelectorAll<HTMLElement>(".srv.tt")];

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
  it("is not there when it is off for the person", () => {
    const el = view(data("first"), state("windows", {}, "en"), actions());
    expect(el.querySelector(".srv.tt")).toBeNull();
    expect(text(el)).not.toContain("TrustTunnel");
  });

  it("gives every link a card: Open in the app, Copy, a QR code for a computer, the marks of the twins, and the steps in one line", () => {
    const el = view(withTt([link(), link({ has_ipv6: true, url: "tt://?AAEB2" }), link({ relay: true, via: "RU", url: "tt://?AAEB3" })]), state("windows", {}, "en"), actions());
    const [direct, v6, relay] = cards(el);
    expect(text(direct!.querySelector(".srv-name"))).toBe("Germany · Frankfurt");
    expect(direct!.querySelector(".srv-top .chip")).toBeNull();
    expect(text(v6!.querySelector(".srv-top .chip"))).toBe("IPv6");
    expect(text(relay!.querySelector(".srv-top .chip"))).toBe("via RU");
    // several links: none leads; the link itself is an ordinary tt:// anchor
    expect(direct!.querySelector<HTMLAnchorElement>("a.btn")!.getAttribute("href")).toBe("tt://?AAEBAQ9kZTEuZXhhbXBsZS5jb20");
    expect(direct!.querySelector("a.btn")!.classList.contains("sec")).toBe(true);
    expect(text(direct!.querySelector("[data-k=tt-copy-0]"))).toBe("Copy link");
    expect(direct!.querySelector(".tt-qr svg")?.getAttribute("aria-label")).toBe("QR code of the TrustTunnel link: Germany");
    expect(text(el)).toContain("Install the TrustTunnel app, then scan the QR code or open the link.");
  });

  it("makes the one link of the person the main action, and copies the link itself", () => {
    const a = actions();
    const el = view(withTt([link()]), state("ios", {}, "en"), a);
    expect(el.querySelector(".srv.tt a.btn.pri")?.getAttribute("href")).toBe("tt://?AAEBAQ9kZTEuZXhhbXBsZS5jb20");
    el.querySelector<HTMLButtonElement>("[data-k=tt-copy-0]")!.click();
    expect(a.log).toContain("copy tt://?AAEBAQ9kZTEuZXhhbXBsZS5jb20");
  });

  it("points to the official page of the apps, the one the project's README names", () => {
    expect(ttAppsUrl).toBe("https://github.com/TrustTunnel/TrustTunnel#clients");
    const a = view(withTt([link()]), state("ios", {}, "en"), actions()).querySelector<HTMLAnchorElement>("[data-k=tt-get]")!;
    expect(a.getAttribute("href")).toBe(ttAppsUrl);
    expect(a.getAttribute("rel")).toBe("noopener noreferrer");
    expect(a.getAttribute("target")).toBe("_blank");
  });

  it("says a self-signed server has no link yet, and offers nothing to open or copy there", () => {
    const el = view(withTt([link({ url: "", note: "self_signed", label: "Finland", country_code: "FI", place: "" })]), state("ios", {}, "en"), actions());
    const card = cards(el)[0]!;
    expect(card.classList.contains("off")).toBe(true);
    expect(text(card.querySelector("[data-k=tt-nolink-0]"))).toBe("There is no link for this server yet — message the admin.");
    expect(card.querySelector("a")).toBeNull();
    expect(card.querySelector("button")).toBeNull();
    expect(card.querySelector(".tt-qr")).toBeNull();
  });

  it("says the keys are still being prepared when no TrustTunnel profile runs", () => {
    const el = view(withTt([]), state("windows", {}, "en"), actions());
    expect(cards(el)).toHaveLength(0);
    expect(text(el.querySelector(".tg-empty"))).toBe("The keys are still being prepared — message the admin.");
  });

  it("lists the key among the devices with a name of its own and nothing to remove or rename", () => {
    const el = view(data("trusttunnel"), state("windows", {}, "en"), actions());
    const rows = [...el.querySelectorAll<HTMLElement>(".dev")].filter((r) => text(r).includes("TrustTunnel key"));
    expect(rows).toHaveLength(1);
    expect(rows[0]!.querySelector("button")).toBeNull();
    expect(text(rows[0])).toContain("connected");
  });

  it("shows no block on a page that is not active, only the key among the devices", () => {
    const el = view(data("trusttunnel-disabled"), state("ios", {}, "en"), actions());
    expect(el.querySelector(".srv.tt")).toBeNull();
    expect(text(el)).toContain("TrustTunnel key");
  });

  it("draws a QR code for a link as long as a real one, of a size a phone camera reads", () => {
    const real = "tt://?AAEBAQ9kZTEuZXhhbXBsZS5jb20FEHVhbGljZTIzNDU2NzIzNDUGIGFCMjM0NTY3Y0QyMzQ1NjdlRjIzNDU2N2dIMjM0NTY3AhNkZTEuZXhhbXBsZS5jb206NDQzBAEACQEBDANkZTENFgoxOTIuMC4yLjUzCjE5Mi4wLjIuNTQ";
    const svg = qrSvg(real, "x");
    expect(svg).not.toBeNull();
    const modules = Number(svg!.getAttribute("viewBox")!.split(" ")[2]);
    // about 60 modules: at the 176 px of .tt-qr (less its padding) a module is 2.5 px
    expect(modules).toBeGreaterThan(40);
    expect(modules).toBeLessThanOrEqual(69);
  });
});

describe("the words of the TrustTunnel block", () => {
  it("are in all five languages", () => {
    const keys = ["ttT", "ttS", "ttNoLinks", "ttOpen", "ttCopy", "ttHowPhone", "ttHowDesk", "ttGet", "ttNoLink", "ttKey"] as const;
    for (const lang of langs) {
      for (const k of keys) expect(dict[lang][k], `${lang} ${k}`).toBeTruthy();
      expect(dict[lang].ttQr("X"), lang).toContain("X");
    }
    // the steps say what to do: install the app, then scan or open
    expect(dict.ru.ttHowDesk).toContain("отсканируйте QR");
    expect(dict.zh.ttHowDesk).toContain("扫描二维码");
  });
});
