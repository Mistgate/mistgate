import { describe, expect, it } from "vitest";
import { cases } from "./dev-data";
import { dict, langs } from "./i18n";
import { normalize, safeUrl } from "./logic";
import { actions, data, state, text } from "./test-kit";
import { view } from "./view";

// The Telegram proxy panel, as the board draws it: a computer has the list of countries (one is chosen) and the pane of the chosen
// one with the QR code, "Open in Telegram" and "Copy the link"; a phone has a row for each proxy. The panel is the one TrustTunnel uses.

const tg = (st: Parameters<typeof state>[1] = {}, lang: "ru" | "en" | "zh" | "fa" | "tr" = "ru", name = "telegram", a = actions()) => ({ el: view(data(name), state("windows", { tab: "telegram", ...st }, lang), a), a });
const pane = (el: HTMLElement) => el.querySelector<HTMLElement>(".u-tg .u-pane")!;
const picks = (el: HTMLElement) => [...el.querySelectorAll<HTMLButtonElement>(".u-tg .u-pick")];
const prows = (el: HTMLElement) => [...el.querySelectorAll<HTMLElement>(".u-prow")];

describe("the Telegram proxy panel", () => {
  it("a computer: the list of the proxies (the first is chosen) and the pane of the chosen one", () => {
    const { el } = tg();
    const tgEl = el.querySelector<HTMLElement>(".u-tg")!;
    expect(tgEl.classList.contains("only-w")).toBe(true);
    expect(text(tgEl.querySelector(".u-cap"))).toBe("Страна прокси");
    expect(picks(el).map((b) => [text(b.querySelector("span:nth-of-type(2)")), b.getAttribute("aria-pressed")])).toEqual([
      ["ГерманияФранкфурт", "true"],
      ["ГерманияФранкфурт", "false"],
      ["ГерманияФранкфурт", "false"],
      ["Финляндия", "false"],
    ]);
    expect(picks(el)[0]!.classList.contains("on")).toBe(true);
    expect(picks(el).map((b) => text(b.querySelector(".u-tag")))).toEqual(["", "IPv6", "через RU", ""]);
    expect(picks(el).map((b) => b.querySelector(".fl2")?.className)).toEqual(["fl2 de", "fl2 de", "fl2 de", "fl2 cc"]);
    expect(picks(el)[0]!.querySelector(".ck")).not.toBeNull();
    const p = pane(el);
    expect(p.getAttribute("aria-label")).toBe("Подключение прокси");
    expect(text(p.querySelector(".u-h2 > span:nth-child(2)"))).toBe("ГерманияФранкфурт");
    expect(text(p.querySelector(".u-hint"))).toBe("Откройте ссылку на этом компьютере или наведите камеру телефона на QR-код. Telegram сам предложит подключить прокси.");
    expect(p.querySelector(".u-qr.big svg")?.getAttribute("aria-label")).toBe("QR-код прокси Telegram: Германия");
    expect(p.querySelector(".u-qr.big .u-qt img")).not.toBeNull();
    expect(p.querySelector(".u-acts.col .u-btn.pri")?.getAttribute("href")).toBe(cases.telegram!.telegram!.links[0]!.url);
    expect(text(p.querySelector(".u-acts.col .u-btn.pri"))).toBe("Открыть в Telegram");
    expect(text(p.querySelector("[data-k=tg-copy-0]"))).toBe("Скопировать ссылкуСсылка скопирована");
  });

  it("choosing a country changes the pane at once and keeps the focus; the copy button copies the chosen link", () => {
    const { el, a } = tg();
    const host = el.querySelector<HTMLElement>(".u-tg")!;
    document.body.append(el);
    picks(el)[3]!.click();
    const next = el.querySelector<HTMLElement>(".u-tg")!;
    expect(next).toBe(host); // the same block, drawn again inside
    expect(picks(el).map((b) => b.getAttribute("aria-pressed"))).toEqual(["false", "false", "false", "true"]);
    expect(text(pane(el).querySelector(".u-h2 > span:nth-child(2)"))).toBe("Финляндия");
    expect(pane(el).querySelector("[data-k=tg-open-3]")?.getAttribute("href")).toBe(cases.telegram!.telegram!.links[3]!.url);
    expect(document.activeElement?.getAttribute("data-k")).toBe("tg-pick-3");
    pane(el).querySelector<HTMLButtonElement>("[data-k=tg-copy-3]")!.click();
    expect(a.log).toEqual([`copy ${cases.telegram!.telegram!.links[3]!.url}`]);
    el.remove();
    // the choice is remembered while the page is drawn again
    const again = tg().el;
    expect(picks(again).map((b) => b.getAttribute("aria-pressed"))).toEqual(["false", "false", "false", "true"]);
    picks(again)[0]!.click();
  });

  it("a phone: one line of what happens and a row for each proxy with 'Open' and a copy button that names the country", () => {
    const { el, a } = tg();
    const hint = el.querySelector<HTMLElement>("p.u-hint.only-m")!;
    expect(text(hint)).toBe("Нажмите «Открыть», и Telegram предложит подключить прокси");
    expect(hint.nextElementSibling?.classList.contains("only-m")).toBe(true);
    expect(prows(el)).toHaveLength(4);
    expect(prows(el).map((r) => text(r.querySelector(".grow > span:nth-child(2)")))).toEqual(["ГерманияФранкфурт", "ГерманияФранкфурт", "ГерманияФранкфурт", "Финляндия"]);
    expect(prows(el).map((r) => text(r.querySelector(".u-tag")))).toEqual(["", "IPv6", "через RU", ""]);
    const open = prows(el)[3]!.querySelector<HTMLAnchorElement>("[data-k=tg-open-m-3]")!;
    expect([text(open), open.getAttribute("href")]).toEqual(["Открыть", cases.telegram!.telegram!.links[3]!.url]);
    const copy = prows(el)[3]!.querySelector<HTMLButtonElement>("[data-k=tg-copy-m-3]")!;
    expect([copy.getAttribute("aria-label"), copy.classList.contains("u-ic")]).toEqual(["Скопировать ссылку прокси: Финляндия", true]);
    copy.click();
    expect(a.log).toEqual([`copy ${cases.telegram!.telegram!.links[3]!.url}`]);
    expect(prows(el)[0]!.querySelector(".fl2")?.getAttribute("aria-label")).toBe("Флаг: Германия");
  });

  it("the Web Proxy of Telegram Web is a link of its own, tagged \"Web\" in the list, the pane and the phone's row, and opens as the others do", () => {
    const web = cases["telegram-web"]!.telegram!.links[1]!;
    expect(web.web).toBe(true);
    expect(web.url).toMatch(/^tg:\/\/webproxy\?/);
    const { el } = tg({}, "ru", "telegram-web");
    expect(picks(el).map((b) => text(b.querySelector(".u-tag")))).toEqual(["", "Web", ""]);
    expect(prows(el).map((r) => text(r.querySelector(".u-tag")))).toEqual(["", "Web", ""]);
    expect(prows(el)[1]!.querySelector(".u-tag")?.getAttribute("title")).toBe("Прокси для Telegram Web, который открывается в браузере");
    expect(prows(el)[1]!.querySelector("[data-k=tg-open-m-1]")?.getAttribute("href")).toBe(web.url);
    picks(el)[1]!.click();
    expect(text(pane(el).querySelector(".u-h2 .u-tag"))).toBe("Web");
    expect(pane(el).querySelector("[data-k=tg-open-1]")?.getAttribute("href")).toBe(web.url);
    picks(el)[0]!.click();
    // the page keeps the flag of the panel's link, and tg://webproxy passes the page's own check of a link
    const d = normalize({ ...cases.first, access: { happ: true, amnezia: false, telegram: true }, telegram: { links: [{ label: "Germany", country_code: "DE", web: true, url: web.url }, { label: "Germany", country_code: "DE", url: "tg://proxy?server=de1.example.com" }] } });
    expect(d.telegram?.links.map((l) => l.web)).toEqual([true, undefined]);
    expect(safeUrl(web.url)).toBe(web.url);
  });

  it("with one proxy there is nothing to choose: no list, the pane alone", () => {
    const { el } = tg({}, "ru", "telegram-first");
    const single = view(data("telegram-first", (d) => (d.telegram!.links = d.telegram!.links.slice(0, 1))), state("windows", { tab: "telegram" }), actions());
    expect(single.querySelector(".u-tg.one")).not.toBeNull();
    expect(single.querySelector(".u-tg .u-list")).toBeNull();
    expect(pane(single)).not.toBeNull();
    expect(picks(el).length).toBeGreaterThan(1);
  });

  it("no proxy yet: one line says so, on both sizes", () => {
    const el = view(data("telegram-soon"), state("windows", { tab: "telegram" }), actions());
    expect(text(el.querySelector(".u-card .u-empty"))).toBe("Прокси ещё готовится — напишите администратору.");
    expect(el.querySelector(".u-tg, .u-prow")).toBeNull();
  });

  it("a link that is not a Telegram one never reaches the page", () => {
    const d = normalize({
      ...cases.first,
      access: { happ: true, amnezia: false, telegram: true },
      telegram: { links: [{ label: "evil", url: "https://example.com/not-telegram" }, { label: "Germany", country_code: "DE", url: "javascript:alert(1)" }] },
    });
    expect(d.telegram?.links).toEqual([]);
  });

  it("the words are in all five languages, and the pane names the country in the QR picture", () => {
    for (const lang of langs) {
      const w = dict[lang];
      for (const k of ["tgCap", "tgPane", "tgHintDesk", "tgHintPhone", "tgWeb", "tgWebHint", "openShort", "telegramOpen", "telegramCopy"] as const) expect(w[k], `${lang} ${k}`).toBeTruthy();
      expect(w.tgCopyAria("X"), lang).toContain("X");
      const { el } = tg({}, lang);
      expect(text(el.querySelector(".u-cap"))).toBe(w.tgCap);
      expect(pane(el).querySelector(".u-qr svg")?.getAttribute("aria-label"), lang).toBeTruthy();
    }
    expect(dict.zh.tgHintPhone).toBe("点击“打开”，Telegram 会提示你连接代理");
  });
});
