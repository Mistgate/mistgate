import { readFileSync } from "node:fs";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { dialogOpen, dialogView } from "./dialogs";
import { cases } from "./dev-data";
import { scramble } from "./fx";
import { dict, dirOf, htmlLang, isLang, langNames, langs } from "./i18n";
import { asPlatform, asTheme, atDeviceLimit, awgConfig, awgDevice, describePreset, detectPlatform, fmtAgo, fmtDate, fmtNum, hero, isReturning, normalize, pickPlatform, safeAddUrl, safeUrl } from "./logic";
import { figures } from "./status";
import type { Ctx, State } from "./state";
import { actions, amzNoop, data, plain, state, text } from "./test-kit";
import type { Lang, MgData } from "./types";
import { copyButton, DONE_MS, placeToast } from "./ui";
import { annKey, openTab, pageHeader, tabsOf, view } from "./view";

const now = 1_800_000_000;
const day = 86400;
const GB = 1024 ** 3;

describe("platform", () => {
  it("detects from the user agent", () => {
    expect(detectPlatform("Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X)")).toBe("ios");
    expect(detectPlatform("Mozilla/5.0 (Linux; Android 14; Pixel 7)")).toBe("android");
    expect(detectPlatform("Mozilla/5.0 (Windows NT 10.0; Win64; x64)")).toBe("windows");
    expect(detectPlatform("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", 0)).toBe("macos");
    expect(detectPlatform("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)", 5)).toBe("ios"); // iPadOS
    expect(detectPlatform("Mozilla/5.0 (X11; Linux x86_64)")).toBe("linux");
    expect(detectPlatform("curl/8")).toBeNull();
  });

  it("starts on the remembered device, else the detected one, else the first with an app", () => {
    const d = normalize(cases.happ);
    expect(pickPlatform(d, "android", "ios")).toBe("android");
    expect(pickPlatform(d, null, "linux")).toBe("linux"); // a platform without an app is still chosen: the step says there is none
    expect(pickPlatform(d, null, null)).toBe("ios");
    expect(pickPlatform(normalize({ ...cases.happ!, apps: [] }), null, null)).toBe("ios");
  });

  it("reads what storage gave back: a platform or a theme of the page, nothing else", () => {
    expect([asPlatform("macos"), asPlatform("beos"), asPlatform(null)]).toEqual(["macos", null, null]);
    expect([asTheme("dark"), asTheme("light"), asTheme("pink"), asTheme(null)]).toEqual(["dark", "light", "auto", "auto"]);
  });
});

describe("links", () => {
  it("never passes script-carrying schemes", () => {
    expect(safeUrl("https://t.me/x")).toBe("https://t.me/x");
    expect(safeUrl("javascript:alert(1)")).toBe("");
    expect(safeUrl(" JaVaScript:alert(1)")).toBe("");
    expect(safeUrl("data:text/html,x")).toBe("");
    expect(safeUrl("happ://add/https://x")).toBe(""); // not a support or download link
    expect(safeAddUrl("happ://add/https://sub.example.com/s/t")).toBe("happ://add/https://sub.example.com/s/t");
    expect(safeAddUrl("klick://add?url=https%3A%2F%2Fx&name=A")).toMatch(/^klick:/);
    expect(safeAddUrl("javascript:alert(1)")).toBe("");
    expect(safeAddUrl("vbscript:x")).toBe("");
    expect(safeAddUrl("not a url")).toBe("");
  });
});

describe("normalize", () => {
  it("survives garbage", () => {
    const d = normalize({ user: { status: "weird", used_bytes: -5, quota_bytes: "x" }, apps: [{ kind: "x" }, null], amnezia: 5, server_count: "3" });
    expect(d.user.status).toBe("active");
    expect(d.user.used_bytes).toBe(0);
    expect(d.apps).toEqual([]);
    expect(d.amnezia).toBeNull();
    expect(d.server_count).toBe(0);
    // A level only: rates or a percentage from an older server are dropped, a row without a known level is left out.
    const loads = normalize({
      server_loads: [
        { name: "EE", level: "high", load_percent: 71, rx_bps: 64_000_000 },
        { name: "DE", level: "low" },
        { name: "RU", level: "busy" },
        { name: "", level: "medium" },
        { name: "PL", load_percent: 2, rx_bps: 1_000_000, tx_bps: 0, capacity_mbps: 1000 },
      ],
    }).server_loads;
    expect(loads).toEqual([{ name: "EE", level: "high" }, { name: "DE", level: "low" }]);
    expect(normalize(null).options.show_qr).toBe(true);
    expect(normalize({ brand: { accent: "javascript:1" } }).brand.accent).toBe("");
  });

  it("gives every new field a safe default: an older panel has no servers, no DNS part, no stale reason", () => {
    const d = normalize({ user: { name: "A" }, amnezia: { devices: [{ id: "d", stale: true }], profiles: [] } });
    expect([d.servers, d.dns, d.dns_presets]).toEqual([[], null, []]);
    expect(d.amnezia?.devices[0]).toMatchObject({ stale: true, stale_reason: "profile" });
    expect(normalize({ amnezia: { devices: [{ id: "d", stale: true, stale_reason: "dns" }] } }).amnezia?.devices[0]?.stale_reason).toBe("dns");
    expect(normalize({ amnezia: { devices: [{ id: "d", stale_reason: "other" }] } }).amnezia?.devices[0]?.stale_reason).toBe("profile");
  });

  it("reads servers[], dns and dns_presets, and drops what has no name", () => {
    const d = normalize({
      servers: [
        { id: "n1", country_code: "de", place: "X", online: true, load: "high", connections: [{ way: "key", exit: "warp", profile_id: "p" }, { way: "other" }], dns: { choice: "a", effective: "b", options: ["a", "b", 5], keys_to_refresh: ["d1"] } },
        { id: "n2", online: false, load: "heavy", dns: null },
        { id: "n3", label: "Somewhere", online: true },
      ],
      dns: { enabled: true, endpoint: "https://x/dns", refresh_hours: 0, link: { per_server: false, effective: "a", choice: "b", default: "a", options: ["a", "b", 3] } },
      dns_presets: [{ id: "a", name: "A", description: "d" }, { id: "", name: "x" }, { id: "b" }],
    });
    expect(d.servers.map((s) => s.id)).toEqual(["n1", "n3"]); // no country and no label: nothing to call it
    expect(d.servers[0]).toMatchObject({ country_code: "DE", load: "high", connections: [{ way: "key", exit: "warp", profile_id: "p" }, { way: "link", exit: "direct" }] });
    expect(d.servers[0]!.dns).toMatchObject({ choice: "a", effective: "b", options: ["a", "b"], keys_to_refresh: ["d1"], default: "" });
    expect(d.servers[1]!.load).toBeNull();
    expect(d.dns).toEqual({ enabled: true, endpoint: "https://x/dns", refresh_hours: 12, link: { effective: "a", choice: "b", default: "a", options: ["a", "b"] } });
    expect(d.dns_presets.map((p) => p.id)).toEqual(["a"]);
    expect(normalize({ dns: "yes" }).dns).toBeNull();
  });

  it("a key config names its server by `label`; the panel's node name is never read", () => {
    expect(awgConfig({ label: "Германия · Франкфурт", country_code: "DE" })).toMatchObject({ label: "Германия · Франкфурт", country_code: "DE" });
    expect(awgConfig({ node_name: "de2", legacy_name: "de2 · AWG 3.1" })).toEqual(expect.objectContaining({ label: "" }));
    expect(JSON.stringify(awgConfig({ node_name: "de2", legacy_name: "de2 · AWG 3.1", server: "x" }))).not.toMatch(/de2|"x"/);
    expect(awgDevice({ id: "d", stale: true, stale_reason: "dns" }).stale_reason).toBe("dns");
  });

  it("reads what names an AmneziaWG choice: the exit and the countries", () => {
    const d = normalize({ amnezia: { profiles: [{ id: "p", name: "Main · WARP", version: "3.1", egress: "warp", countries: ["DE", 5, ""] }, { id: "q", egress: "elsewhere" }] } });
    expect(d.amnezia?.profiles).toEqual([
      { id: "p", name: "Main · WARP", version: "3.1", egress: "warp", countries: ["DE"] },
      { id: "q", name: "", version: "", egress: "direct", countries: [] },
    ]);
  });
});

describe("hero", () => {
  const user = (u: object) => normalize({ user: { name: "A", ...u } });
  it("active: days, the traffic in the unit of the quota, the quota, the day it resets", () => {
    const h = hero(user({ status: "active", expires_unix: now + 76 * day - 100, used_bytes: 12 * GB, quota_bytes: 100 * GB, quota_reset: "month", next_reset_unix: now + 9 * day }), "en", now);
    expect([h.days, h.soon, h.low, h.usedQ, h.quotaN]).toEqual([76, false, false, "12 GB", "100"]);
    expect(h.reset).toMatch(/^resets /);
  });
  it("a week or less to go is 'soon', and says at least one day", () => {
    expect(hero(user({ status: "active", expires_unix: now + 3 * day - 600 }), "ru", now)).toMatchObject({ soon: true, days: 3 });
    expect(hero(user({ status: "active", expires_unix: now + 7 * day - 600 }), "ru", now).soon).toBe(true);
    expect(hero(user({ status: "active", expires_unix: now + 600 }), "ru", now)).toMatchObject({ soon: true, days: 1 });
    expect(hero(user({ status: "active", expires_unix: now + 8 * day }), "ru", now).soon).toBe(false);
    expect(hero(user({ status: "active", expires_unix: 0 }), "ru", now)).toMatchObject({ soon: false, days: 0 });
    expect(hero(user({ status: "expired", expires_unix: now - day }), "ru", now).soon).toBe(false); // an ended term is not "soon"
  });
  it("90 % of the traffic or more is a warning; used-up traffic always is", () => {
    const used = (gb: number, status = "active") => hero(user({ status, used_bytes: gb * GB, quota_bytes: 100 * GB }), "ru", now).low;
    expect([used(92), used(90), used(89)]).toEqual([true, true, false]);
    expect(used(100, "limited")).toBe(true);
    expect(hero(user({ status: "active", used_bytes: 5 * GB }), "ru", now).low).toBe(false); // no quota, nothing to run out
  });
  it("no limit: the usage in its own unit, no quota, no reset day", () => {
    const h = hero(user({ status: "active", used_bytes: 300 * 1024 ** 2 }), "ru", now);
    expect([h.usedQ, h.quotaN, h.reset]).toEqual(["300 МБ", "", ""]);
  });
  it("the Russian plural forms of the days", () => {
    const u = (n: number) => dict.ru.dayWord(n);
    expect([u(1), u(2), u(5), u(21), u(11)]).toEqual(["день", "дня", "дней", "день", "дней"]);
  });
  it("device limit", () => {
    expect(atDeviceLimit(user({ device_limit: 3, devices_used: 3 }))).toBe(true);
    expect(atDeviceLimit(user({ device_limit: 0, devices_used: 9 }))).toBe(false);
  });
  it("relative times", () => {
    expect(fmtAgo(now - 3 * day, "en", now)).toBe("3 days ago");
    expect(fmtAgo(now - 5, "en", now)).toBe("now");
  });
});

describe("the three figures of the card", () => {
  const c = (u: object, lang: Lang = "ru") => {
    const d = normalize({ user: { name: "A", device_limit: 20, devices_used: 16, ...u } });
    return { d, s: state("ios", {}, lang), t: dict[lang] };
  };
  const GBq = { used_bytes: 64 * GB, quota_bytes: 100 * GB, quota_reset: "month", next_reset_unix: now + 4 * day };

  it("active: the term in days with its date, the traffic of its quota, the places left", () => {
    const f = figures(c({ status: "active", expires_unix: now + 33 * day - 100, ...GBq }), now);
    expect(f.term).toMatchObject({ v: "33 дня", tone: "" });
    expect(f.traffic).toMatchObject({ v: "64 ГБ", tone: "" });
    expect(f.traffic.n).toMatch(/^из 100, обнулится /);
    expect(f.devices).toEqual({ v: "16 из 20", n: "свободно 4 места", tone: "" });
  });
  it("a term of a week or less and traffic of nine tenths are warnings", () => {
    const f = figures(c({ status: "active", expires_unix: now + 3 * day - 600, used_bytes: 95 * GB, quota_bytes: 100 * GB }), now);
    expect([f.term.v, f.term.tone, f.traffic.tone]).toEqual(["3 дня", "warn", "warn"]);
  });
  it("no end and no limit: said so, not a number", () => {
    const f = figures(c({ status: "active", expires_unix: 0, used_bytes: 17 * GB, quota_bytes: 0, device_limit: 0, devices_used: 2 }), now);
    expect(f.term).toEqual({ v: "Без срока", n: "дата окончания не задана", tone: "" });
    expect(f.traffic).toMatchObject({ v: "17 ГБ", n: "без лимита" });
    expect(f.devices).toMatchObject({ v: "2", n: "без ограничения" });
  });
  it("no free place is said in words", () => {
    expect(figures(c({ status: "active", device_limit: 3, devices_used: 3 }), now).devices).toMatchObject({ v: "3 из 3", n: "свободных мест нет" });
    expect(figures(c({ status: "active", device_limit: 20, devices_used: 20 }, "en"), now).devices.n).toBe("no free slots");
  });
  it("ended: the word with the date, grey; turned off: by the owner; used up: the traffic warns", () => {
    const gone = figures(c({ status: "expired", expires_unix: now - 4 * day }), now);
    expect(gone.term).toMatchObject({ v: "Закончилась", tone: "off" });
    expect(gone.term.n).not.toBe("");
    expect(figures(c({ status: "disabled" }), now).term).toEqual({ v: "Отключена", n: "владельцем сервиса", tone: "off" });
    const used = figures(c({ status: "limited", expires_unix: now + 33 * day, used_bytes: 100 * GB, quota_bytes: 100 * GB }), now);
    expect([used.traffic.v, used.traffic.tone, used.traffic.n]).toEqual(["100 ГБ", "warn", "из 100, лимит исчерпан"]);
    expect(used.term.tone).toBe("");
  });
});

describe("visits", () => {
  it("returning: an app fetched the subscription, there are keys, or this device pressed add before", () => {
    expect(isReturning(normalize(cases.first), false)).toBe(false);
    expect(isReturning(normalize(cases.first), true)).toBe(true);
    expect(isReturning(normalize(cases.return), false)).toBe(true);
    expect(isReturning(normalize({ ...cases.first!, amnezia: { ...cases.first!.amnezia!, devices: cases.return!.amnezia!.devices } }), false)).toBe(true);
  });
});

describe("languages", () => {
  it("every language has the page's whole dictionary, and the server's language is read through isLang", () => {
    const keys = (o: object) => Object.keys(o).sort();
    for (const lang of langs) {
      expect(keys(dict[lang]), lang).toEqual(keys(dict.ru));
      expect(isLang(lang)).toBe(true);
    }
    expect(normalize({ lang: "zh" }).lang).toBe("zh");
    expect(normalize({ lang: "fr" }).lang).toBe("ru");
    expect(describePreset("Русский\nEnglish", "zh")).toBe("English");
    expect(describePreset("Русский\nEnglish", "ru")).toBe("Русский");
  });
});

// ---- the page: the shell (the bar, the card, the note, the tabs, the end), after the boards of Main.dc.html and V2Phone.dc.html ----

/** The page of a case as main.ts draws it: a returning visit unless it is the first one. */
function page(name: string, o: { lang?: Lang; over?: Partial<State>; f?: (d: MgData) => void; a?: ReturnType<typeof actions>; platform?: State["platform"] } = {}) {
  const d = data(name, o.f);
  const a = o.a ?? actions();
  const s = state(o.platform ?? "ios", { returning: isReturning(d, false), ...o.over }, o.lang ?? "ru");
  const c: Ctx = { d, s, a, t: dict[s.lang], support: d.options.show_support ? safeUrl(d.support_url) : "" };
  return { el: view(d, s, a), d, s, a, c };
}
const $ = (el: ParentNode, sel: string) => el.querySelector<HTMLElement>(sel);
const $$ = (el: ParentNode, sel: string) => [...el.querySelectorAll<HTMLElement>(sel)];
const word = (el: ParentNode, sel: string) => text($(el, sel));

describe("view", () => {
  for (const [name, raw] of Object.entries(cases)) {
    it(`renders ${name} in every language, with nothing the page must never show`, () => {
      for (const lang of langs) {
        const d = normalize(raw);
        const el = view(d, state("ios", { returning: isReturning(d, false) }, lang), actions());
        expect($(el, ".u-hd"), `${lang} bar`).not.toBeNull();
        expect($(el, ".u-hero"), `${lang} card`).not.toBeNull();
        expect($(el, ".u-ft .u-priv")?.textContent, lang).toBeTruthy();
        // nothing the page shows is the panel's jargon, a tunnel address, a node name, a profile name, or the secret link ...
        const shown = [el.textContent ?? "", ...$$(el, "[aria-label], [title], [alt]").flatMap((x) => [x.getAttribute("aria-label"), x.getAttribute("title"), x.getAttribute("alt")])].join("\n");
        expect(shown).not.toMatch(/AWG(?! [23]\.\d)|10\.66\.|fd66:|Old apps|Main ·|nod_|vpn:\/\/|3f9k2v8q/); // ... the link and the key are copied or scanned, never written out; the version of a key ("AWG 3.1") is a word of the boards
      }
    });
  }

  describe("the bar (.u-hd)", () => {
    it("the logo of 28 px, the name in two weights, the language menu, and the way to the support", () => {
      const { el } = page("return");
      const bar = $(el, "header.u-hd")!;
      expect([$(bar, ".logo")!.getAttribute("width"), $(bar, ".logo")!.getAttribute("height"), $(bar, ".logo")!.getAttribute("alt")]).toEqual(["28", "28", ""]);
      expect([$(bar, ".u-wm")!.firstChild?.textContent, word(bar, ".u-wm b")]).toEqual(["mist", "gate"]);
      expect(word(bar, "a.only-w")).toBe("Поддержка");
      expect($(bar, "a.only-w")!.getAttribute("href")).toBe("https://t.me/example_support");
      expect($(bar, "a.only-m")!.getAttribute("aria-label")).toBe("Поддержка"); // the phone's icon alone
      expect($$(bar, "a").every((x) => x.getAttribute("rel") === "noopener noreferrer" && x.getAttribute("target") === "_blank")).toBe(true);
    });
    it("a title that is not the brand's name is written whole; no support link, no way to it", () => {
      const { el } = page("long");
      expect(word(el, ".u-wm")).toBe("Мой очень длинный VPN для друзей и семьи");
      expect($(el, ".u-wm b")).toBeNull();
      expect($(el, ".u-wm.plain")).not.toBeNull();
      expect($(page("plain").el, "header a")).toBeNull();
      expect($(page("return", { f: (d) => (d.options.show_support = false) }).el, "header a")).toBeNull();
    });
    it("a tg:// support link opens as a Telegram link", () => {
      const tg = "tg://resolve?domain=example_support";
      const { el } = page("return", { f: (d) => (d.support_url = tg) });
      expect($(el, `header a.only-w[href='${tg}']`)).not.toBeNull();
      expect($(el, `.u-ask a[href='${tg}']`)).not.toBeNull();
    });
  });

  describe("the six states of the subscription", () => {
    const status = (el: ParentNode) => ({
      dot: $(el, ".u-hero .u-status .u-dot")!.className,
      word: word(el, ".u-hero .u-status b"),
      sy: word(el, ".u-hero .u-status .sy"),
      h1: word(el, ".u-hero .u-h1"),
      solo: $(el, ".u-hero")!.classList.contains("solo"),
      term: [...$(el, ".u-stat:nth-child(1) .v")!.classList].slice(1).join(" "),
      traffic: [...$(el, ".u-stat:nth-child(2) .v")!.classList].slice(1).join(" "),
      alert: $(el, ".u-alert") && { tone: $(el, ".u-alert")!.className.replace("u-card u-alert", "").trim(), title: word(el, ".u-alert b"), body: word(el, ".u-alert span"), button: word(el, ".u-alert a") },
    });

    it("active: green, no note; a way to connect (the card of the new device), the tabs", () => {
      const { el } = page("return");
      expect(status(el)).toMatchObject({ dot: "u-dot", word: "Активна", h1: "Подписка: Alice", solo: false, alert: null });
      expect(status(el).sy).toMatch(/^приложение получило подписку /);
      expect($(el, "nav.u-tabs")).not.toBeNull();
      expect($(el, "aside.u-hero-c.only-w .u-qr")).not.toBeNull();
    });
    it("the first visit: green, 'the app has not fetched', the note that says what to do (no button: nothing is wrong)", () => {
      const { el } = page("first", { over: { returning: false } });
      expect(status(el)).toMatchObject({ dot: "u-dot", word: "Активна", sy: "приложение ещё не получало подписку", alert: { tone: "", title: "Подключите первое устройство", button: "" } });
      expect(status(el).alert!.body).toBe("Нажмите «Добавить устройство»: выберите приложение, и серверы появятся в нём сами");
      expect(word(el, ".u-stat:nth-child(3) .v")).toBe("0 из 3");
      expect(word(el, ".u-tab[data-k=tab-devices] .n.only-w")).toBe("0/3");
    });
    it("the first visit with nothing to connect with has no note", () => {
      expect(page("noapps", { over: { returning: false } }).el.querySelector(".u-alert")).toBeNull();
    });
    it("ends soon: amber, the days in the status, the note with the date and the way to write", () => {
      const { el } = page("soon");
      const s = status(el);
      expect([s.dot, s.term]).toEqual(["u-dot warn", "warn"]);
      expect(s.word).toMatch(/^Заканчивается через [1-3] (день|дня)$/);
      expect(s.alert).toMatchObject({ tone: "warn", button: "Написать в поддержку" });
      expect(s.alert!.title).toMatch(/^Подписка заканчивается \d+ /);
      expect(s.alert!.body).toBe("После этой даты серверы перестанут работать. Чтобы продлить, напишите в поддержку");
      expect($(el, ".u-alert a.u-btn.pri")!.getAttribute("href")).toBe("https://t.me/example_support");
      expect($(el, "nav.u-tabs")).not.toBeNull(); // still a working subscription
    });
    it("traffic used up: amber, the card alone, the day it works again", () => {
      const { el } = page("quota");
      const s = status(el);
      expect([s.dot, s.word, s.solo, s.traffic, s.term]).toEqual(["u-dot warn", "Трафик исчерпан", true, "warn", ""]);
      expect(s.sy).toMatch(/^лимит обнулится /);
      expect(s.alert).toMatchObject({ tone: "warn", title: "Трафик закончился", button: "Написать в поддержку" });
      expect(s.alert!.body).toMatch(/^Серверы снова заработают .+, когда лимит обнулится\. Если нужно раньше, напишите в поддержку$/);
      expect([$(el, "nav.u-tabs"), $(el, ".u-hero-c"), $(el, ".u-qr")]).toEqual([null, null, null]);
    });
    it("traffic used up with no reset day: no day in the words", () => {
      const s = status(page("quota-plain").el);
      expect(s.sy).toBe("");
      expect(s.alert!.body).toBe("Серверы снова заработают, когда лимит обнулится. Если нужно раньше, напишите в поддержку");
    });
    it("ended: grey, the date, the note keeps the devices and keys", () => {
      const s = status(page("expired").el);
      expect([s.dot, s.word, s.solo, s.term]).toEqual(["u-dot off", "Закончилась", true, "off"]);
      expect(s.sy).toMatch(/^\d+ /);
      expect(s.alert).toMatchObject({ tone: "", button: "Написать в поддержку" });
      expect(s.alert!.title).toMatch(/^Подписка закончилась \d+ /);
      expect(s.alert!.body).toBe("Серверы и прокси не работают. Устройства и ключи сохранены и вернутся после продления");
    });
    it("turned off: grey, 'by the owner of the service'", () => {
      const { el } = page("disabled");
      const s = status(el);
      expect([s.dot, s.word, s.sy, s.solo, s.term]).toEqual(["u-dot off", "Отключена", "серверы и прокси не работают", true, "off"]);
      expect(word(el, ".u-stat:nth-child(1) .n")).toBe("владельцем сервиса");
      expect(s.alert).toMatchObject({ tone: "", title: "Подписку отключил владелец сервиса", body: "Устройства и ключи сохранены. Напишите в поддержку, чтобы узнать причину", button: "Написать в поддержку" });
    });
    it("no way to write: the notes keep their words without the call, and have no button", () => {
      const gone = status(page("expired-plain").el);
      expect(gone.alert).toMatchObject({ button: "" });
      expect(gone.alert!.body).not.toContain("напишите");
      const off = status(page("disabled", { f: (d) => (d.support_url = "") }).el);
      expect([off.alert!.body, off.alert!.button]).toEqual(["Устройства и ключи сохранены.", ""]);
      const quota = status(page("quota", { f: (d) => (d.options.show_support = false) }).el);
      expect(quota.alert!.body).not.toContain("напишите");
      expect(quota.alert!.button).toBe("");
      const soon = status(page("soon", { f: (d) => (d.support_url = "") }).el);
      expect(soon.alert!.body).toBe("После этой даты серверы перестанут работать.");
    });

    it("every state says its own word in every language", () => {
      const want = (lang: Lang) => {
        const t = dict[lang];
        return { return: t.chip.active, first: t.chip.active, soon: t.endsIn(3), quota: t.chip.limited, expired: t.chip.expired, disabled: t.chip.disabled } as Record<string, string>;
      };
      for (const lang of langs) {
        for (const [name, w] of Object.entries(want(lang))) {
          const { el } = page(name, { lang, over: { returning: name !== "first" } });
          expect(word(el, ".u-hero .u-status b"), `${lang} ${name}`).toBe(w);
          const notes = $(el, ".u-alert");
          expect(!!notes, `${lang} ${name} note`).toBe(name !== "return");
          if (notes) expect($(notes, "b")!.textContent, `${lang} ${name} note title`).toBeTruthy();
        }
      }
    });
  });

  describe("the card of the new device (.u-hero-c)", () => {
    it("a computer: the QR code with its title and hint, then the two buttons; a phone: the two buttons only", () => {
      const { el } = page("return");
      const side = $(el, "aside.u-hero-c.only-w")!;
      expect(side.getAttribute("aria-label")).toBe("Новое устройство");
      expect($(side, ".u-qr.u-tilt .u-glare")).not.toBeNull();
      expect($(side, ".u-qi svg[role=img]")!.getAttribute("aria-label")).toBe("QR-код со ссылкой на эту страницу");
      expect($(side, ".u-qi .u-qt img")).not.toBeNull();
      expect([word(side, ".u-t"), word(side, ".u-hint")]).toEqual(["Новое устройство", "Наведите камеру телефона, и эта страница откроется на нём"]);
      expect($$(side, ".u-acts button").map((b) => text(b.querySelector(".u-cpt span:first-child") ?? b))).toEqual(["Добавить устройство", "Скопировать ссылку подписки"]);
      const m = $(el, "div.u-hero-c.only-m")!;
      expect($$(m, "button").map((b) => [b.className, text(b.querySelector(".u-cpt span:first-child") ?? b)])).toEqual([["u-btn pri grow", "Добавить устройство"], ["u-btn u-cpb", "Ссылка"]]);
      // the short word is its own "done" word: any longer one would be wider than the room left next to the add button and push it to a row of its own
      expect([...$(m, "[data-k=hero-copy] .u-cpt")!.querySelectorAll(":scope > span")].map((x) => x.textContent)).toEqual(["Ссылка", "Ссылка"]);
      expect([...$(side, "[data-k=hero-copy] .u-cpt")!.querySelectorAll(":scope > span")].map((x) => x.textContent)).toEqual(["Скопировать ссылку подписки", "Ссылка скопирована"]);
    });
    it("the buttons add a device and copy the link of the page", () => {
      const a = actions();
      const { el } = page("return", { a });
      $(el, "aside [data-k=hero-add]")!.click();
      $(el, "aside [data-k=hero-copy]")!.click();
      expect(a.log).toEqual(["add true", `copy ${cases.return!.subscription_url}`, "mark"]);
    });
    it("no QR code (the owner's choice, or no link app): the buttons stand alone", () => {
      const { el } = page("noqr");
      expect($(el, "aside .u-qrrow")).toBeNull();
      expect($$(el, "aside .u-acts button")).toHaveLength(2);
      expect($(page("keys-only").el, ".u-qr")).toBeNull();
    });
    it("nothing to connect with: no card, the figures stand alone", () => {
      const { el } = page("noapps", { f: (d) => (d.subscription_url = "") });
      expect($(el, ".u-hero-c")).toBeNull();
      expect($(el, ".u-hero")!.classList.contains("solo")).toBe(true);
    });
    it("the devices figure is a button to the tab of the devices while the subscription works", () => {
      const a = actions();
      $(page("return", { a }).el, "button.u-stat")!.click();
      expect(a.log).toEqual(["tab devices"]);
      expect($(page("expired").el, "button.u-stat")).toBeNull();
    });
    it("the figures are written in letter by letter on a computer: the text and the aria-label are there from the start", () => {
      const v = $$(page("return").el, ".u-stat .v");
      expect(v).toHaveLength(3);
      for (const x of v) expect([x.dataset.scr, x.getAttribute("aria-label"), x.dataset.scrK]).toEqual([x.textContent, x.textContent, expect.any(String)]);
    });
  });

  describe("an inactive subscription: the card, the note, the devices (no tabs)", () => {
    for (const name of ["expired", "disabled", "quota"]) {
      it(`${name}: no tabs, no help card, the devices are there, the menu of a key opens`, () => {
        const calls: string[] = [];
        const a = actions({ amz: { ...amzNoop, menu: (id) => calls.push(`menu ${id}`) } });
        const { el, d } = page(name, { a });
        expect([$(el, ".u-tabs"), $(el, ".u-ask"), $(el, ".u-ann")]).toEqual([null, null, null]);
        expect($(el, ".u-dvh")).not.toBeNull();
        expect($(el, ".u-dvl")).not.toBeNull();
        const key = d.amnezia!.devices[0]!.id;
        $(el, `[data-k=amz-more-${key}]`)!.click();
        expect(calls).toEqual([`menu ${key}`]);
      });
    }
    it("the menu of a device offers the name and the removal; the name window and the question open from the state", () => {
      for (const name of ["expired", "disabled"]) {
        const d = data(name);
        const id = d.amnezia!.devices[0]!.id;
        const log: string[] = [];
        const a = actions({ amz: { ...amzNoop, renameStart: (x) => log.push(`rename ${x}`), ask: (c) => log.push(`ask ${c?.kind} ${c?.id}`) } });
        const s = state("ios", {}, "ru");
        s.amz.menu = id;
        const c: Ctx = { d, s, a, t: dict.ru, support: "https://t.me/example_support" };
        expect(dialogOpen(c)).toBe(true);
        const box = document.createElement("div");
        box.append(...(dialogView(c).content as Node[]));
        expect($$(box, ".a-act").map((b) => b.dataset.k)).toEqual(["dlg-m-ren", "dlg-m-del"]); // no "show key" and no "replace": the subscription is off
        $(box, "[data-k=dlg-m-ren]")!.click();
        $(box, "[data-k=dlg-m-del]")!.click();
        expect(log).toEqual([`rename ${id}`, `ask remove ${id}`]);
        s.amz.menu = null;
        s.amz.rename = { id, value: "x" };
        expect(dialogView(c).stage).toBe(`rename:${id}`);
        s.amz.rename = null;
        s.amz.confirm = { id, kind: "remove" };
        expect(dialogView(c).stage).toBe(`confirm:remove:${id}`);
      }
    });
    it("a working subscription: the tabs", () => {
      expect($(page("return").el, ".u-tabs")).not.toBeNull();
    });
  });

  describe("the tabs (.u-tabs)", () => {
    it("the servers and the devices; the Telegram proxies and TrustTunnel only when they are on", () => {
      const ids = (name: string, f?: (d: MgData) => void) => {
        const d = data(name, f);
        return tabsOf({ d, t: dict.ru }).map((x) => x.id);
      };
      expect(ids("return")).toEqual(["servers", "devices"]);
      expect(ids("telegram")).toEqual(["servers", "telegram", "devices"]);
      expect(ids("trusttunnel")).toEqual(["servers", "trusttunnel", "devices"]);
      expect(ids("telegram", (d) => ((d.access.trust_tunnel = true), (d.trust_tunnel = { links: [] })))).toEqual(["servers", "telegram", "trusttunnel", "devices"]);
      expect(ids("return", (d) => (d.access.telegram = false))).toEqual(["servers", "devices"]);
    });
    it("the counts: the servers, the proxies, the places taken of the limit (the number alone without one)", () => {
      const t = dict.ru;
      const count = (d: MgData) => Object.fromEntries(tabsOf({ d, t }).map((x) => [x.id, [x.n, x.nShort]]));
      expect(count(data("telegram"))).toEqual({ servers: ["3", "3"], telegram: [String(data("telegram").telegram!.links.length), String(data("telegram").telegram!.links.length)], devices: ["3/4", "3"] });
      expect(count(data("dev-16")).devices).toEqual(["16/20", "16"]);
      expect(count(data("dev-nolimit")).devices).toEqual(["5", "5"]);
      expect(count(data("servers-30")).servers).toEqual(["30", "30"]);
    });
    it("the bar: a button per tab, the open one pressed; a computer has the full names, a phone the short ones", () => {
      const { el } = page("telegram", { over: { tab: "telegram" } });
      const bar = $(el, "nav.u-tabs")!;
      expect(bar.getAttribute("aria-label")).toBe("Разделы");
      expect($$(bar, ".u-tab").map((b) => [b.dataset.k, b.getAttribute("aria-pressed"), b.classList.contains("on")])).toEqual([
        ["tab-servers", "false", false],
        ["tab-telegram", "true", true],
        ["tab-devices", "false", false],
      ]);
      const tg = $(bar, "[data-k=tab-telegram]")!;
      expect([word(tg, ".only-w"), word(tg, ".only-m")]).toEqual(["Telegram-прокси", "Telegram"]);
      expect([word(tg, ".n.only-w"), word(tg, ".n.only-m")]).toEqual(["4", "4"]);
      // the count of the servers is the figure the page writes in (data-scr); the others are plain
      expect($(bar, "[data-k=tab-servers] .n.only-w")!.dataset.scr).toBe("3");
      expect($(bar, "[data-k=tab-devices] .n.only-w")!.dataset.scr).toBeUndefined();
    });
    it("a tab that is not there is the servers; a click asks for its tab", () => {
      const asked = (name: string, tab: State["tab"]) => openTab(page(name, { over: { tab } }).c);
      expect([asked("return", "telegram"), asked("return", "trusttunnel"), asked("telegram", "telegram"), asked("trusttunnel", "trusttunnel"), asked("return", "devices")]).toEqual(["servers", "servers", "telegram", "trusttunnel", "devices"]);
      const a = actions();
      const { el } = page("telegram", { a });
      $(el, "[data-k=tab-telegram]")!.click();
      $(el, "[data-k=tab-devices]")!.click();
      expect(a.log).toEqual(["tab telegram", "tab devices"]);
    });
    it("each tab draws its own part under the bar", () => {
      const open = (name: string, tab: State["tab"]) => page(name, { over: { tab } }).el;
      expect($(open("return", "servers"), ".u-tbl")).not.toBeNull();
      expect($(open("return", "devices"), ".u-dvl")).not.toBeNull();
      expect($(open("telegram", "telegram"), ".u-tg")).not.toBeNull();
      expect($(open("trusttunnel", "trusttunnel"), ".u-tg, .u-prow, .u-pane")).not.toBeNull();
      expect($(open("return", "telegram"), ".u-tbl")).not.toBeNull(); // not on: the servers
    });
  });

  describe("the announcement of the owner (.u-ann)", () => {
    it("a tile, the first line as the title, the rest under it, the cross", () => {
      const { el } = page("long");
      const ann = $(el, "section.u-ann")!;
      expect(ann.getAttribute("role")).toBe("status");
      expect(word(ann, ".t b")).toBe("Первая строка объявления.");
      expect(word(ann, ".m")).toMatch(/^Вторая строка: длинный текст/);
      expect($(ann, ".u-ic")!.getAttribute("aria-label")).toBe("Скрыть объявление");
      const one = $(page("first").el, ".u-ann")!;
      expect(word(one, ".t b")).toBe("В субботу с 02:00 до 03:00 обновляю серверы — может моргнуть на пару минут.");
      expect($(one, ".m")).toBeNull();
    });
    it("it is text, never markup", () => {
      const { el } = page("first", { f: (d) => (d.announcement = "<img src=x onerror=alert(1)>") });
      expect($(el, ".u-ann img")).toBeNull();
      expect(word(el, ".u-ann .t b")).toBe("<img src=x onerror=alert(1)>");
    });
    it("a cross closes it; a closed one stays closed through the redraws, a new text shows again", () => {
      const a = actions();
      const d = data("first");
      $(view(d, state("ios"), a), ".u-ann .u-ic")!.click();
      expect(a.log).toEqual(["closed"]);
      expect($(view(d, state("ios", { annClosed: true }), actions()), ".u-ann")).toBeNull();
      expect(annKey("a")).not.toBe(annKey("b"));
      expect(annKey(d.announcement)).toBe(annKey(d.announcement));
      expect(annKey(d.announcement)).toMatch(/^ann:[0-9a-z]+$/);
    });
    it("none when the owner has none or turned it off; and not above a subscription that does not work (the boards draw it only there)", () => {
      expect($(page("return").el, ".u-ann")).toBeNull();
      expect($(page("first", { f: (d) => (d.options.show_announcement = false) }).el, ".u-ann")).toBeNull();
      for (const name of ["expired", "disabled", "quota"]) expect($(page(name, { f: (d) => (d.announcement = "x") }).el, ".u-ann"), name).toBeNull();
    });
  });

  describe("the language menu (.u-lang)", () => {
    it("the button names the language (a phone shows its code), the list has every language by its own name, the current one checked", () => {
      const a = actions();
      const { el } = page("return", { a });
      const menu = $(el, "details.u-lang") as HTMLDetailsElement;
      expect(menu.open).toBe(false);
      const sum = $(menu, "summary.u-btn")!;
      expect([word(sum, ".lb"), word(sum, ".cd")]).toEqual(["Русский", "RU"]);
      expect([sum.getAttribute("aria-label"), sum.getAttribute("aria-haspopup")]).toEqual(["Язык: Русский", "listbox"]);
      expect($$(sum, "svg")).toHaveLength(2); // the globe and the chevron
      const list = $(menu, ".u-menu")!;
      expect([list.getAttribute("role"), list.getAttribute("aria-label")]).toEqual(["listbox", "Язык"]);
      const rows = $$(list, "button");
      expect(rows.map((b) => [text(b), b.getAttribute("role"), b.getAttribute("aria-selected"), b.getAttribute("lang"), b.className])).toEqual([
        ["EnglishEN", "option", "false", "en", ""],
        ["РусскийRU", "option", "true", "ru", "on"],
        ["简体中文中文", "option", "false", "zh-CN", ""],
        ["فارسیFA", "option", "false", "fa", ""],
        ["TürkçeTR", "option", "false", "tr", ""],
      ]);
      expect(rows.every((b) => $(b, "svg.ck"))).toBe(true);
    });
    it("a row asks for its language (the same one only closes the menu)", () => {
      const a = actions();
      const { el } = page("return", { a });
      const menu = $(el, "details.u-lang") as HTMLDetailsElement;
      menu.open = true;
      $(menu, "[data-k=lang-ru]")!.click();
      expect(a.log).toEqual([]);
      expect(menu.open).toBe(false);
      for (const l of langs.filter((x) => x !== "ru")) $(menu, `[data-k=lang-${l}]`)!.click();
      expect(a.log).toEqual(["lang en", "lang zh", "lang fa", "lang tr"]);
    });
    it("it closes on a click outside and on Esc, which gives the focus back to its button", () => {
      const { el } = page("return");
      document.body.append(el);
      const menu = $(el, "details.u-lang") as HTMLDetailsElement;
      menu.open = true;
      $(el, ".u-hero")!.click();
      expect(menu.open).toBe(false);
      menu.open = true;
      $(menu, "summary")!.click(); // inside: the browser's own toggle, the page stays out of it
      menu.open = true;
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      expect(menu.open).toBe(false);
      expect(document.activeElement).toBe($(menu, "summary"));
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); // nothing open: nothing happens
      el.remove();
    });
    it("every language is in every language's list under its own name", () => {
      for (const lang of langs) {
        const rows = $$(page("return", { lang }).el, ".u-menu button span:first-child").map((x) => x.textContent);
        expect(rows, lang).toEqual(langs.map((l) => langNames[l]));
      }
    });
    it("the password form has the same bar", () => {
      const a = actions();
      const bar = pageHeader(data("locked"), "en", (l) => a.lang(l));
      expect($(bar, ".u-lang")).not.toBeNull();
      expect($(bar, "a")).toBeNull(); // no support link given
      $(bar, "[data-k=lang-tr]")!.click();
      expect(a.log).toEqual(["lang tr"]);
    });
  });

  describe("the end of the page", () => {
    it("the footer: the privacy line and the three buttons of the theme, the chosen one checked", () => {
      const a = actions();
      const { el } = page("return", { a, over: { theme: "light" } });
      const foot = $(el, "footer.u-ft")!;
      expect(word(foot, ".u-priv")).toBe("Ссылка личная — не пересылайте её. Работает, пока подписка активна.");
      expect($(foot, ".u-priv svg")).not.toBeNull();
      const group = $(foot, ".u-theme")!;
      expect([group.getAttribute("role"), group.getAttribute("aria-label")]).toEqual(["radiogroup", "Тема"]);
      expect($$(group, "[role=radio]").map((b) => [b.getAttribute("aria-label"), b.getAttribute("aria-checked"), b.className, b.textContent])).toEqual([
        ["Как в системе", "false", "", ""],
        ["Светлая", "true", "on", ""],
        ["Тёмная", "false", "", ""],
      ]);
      $(group, "[data-k=theme-dark]")!.click();
      $(group, "[data-k=theme-auto]")!.click();
      expect(a.log).toEqual(["theme dark", "theme auto"]);
    });
    it("the theme says it for every language and every choice", () => {
      for (const lang of langs) {
        for (const theme of ["auto", "light", "dark"] as const) {
          const radios = $$(page("return", { lang, over: { theme } }).el, ".u-theme [role=radio]");
          expect(radios.map((b) => b.getAttribute("aria-checked")), `${lang} ${theme}`).toEqual((["auto", "light", "dark"] as const).map((x) => String(x === theme)));
          expect(radios.map((b) => b.getAttribute("aria-label"))).toEqual([dict[lang].themeAria.auto, dict[lang].themeAria.light, dict[lang].themeAria.dark]);
        }
      }
    });
    it("the help card 'Did not find the answer' only where there is a way to write, under a subscription that works", () => {
      const { el } = page("return");
      const ask = $(el, ".u-below > .u-card.u-ask")!;
      expect(word(ask, "p")).toBe("Не нашли ответ");
      expect(word(ask, "a.u-btn")).toBe("Написать в поддержку");
      expect($(ask, "a.u-btn svg")).not.toBeNull();
      expect($(ask, "a")!.getAttribute("href")).toBe("https://t.me/example_support");
      expect($(page("plain").el, ".u-ask")).toBeNull();
      expect($(page("nosupport").el, ".u-ask")).toBeNull();
      expect($(page("return", { f: (d) => (d.options.show_support = false) }).el, ".u-ask")).toBeNull();
      for (const name of ["expired", "disabled", "quota"]) expect($(page(name).el, ".u-ask"), name).toBeNull(); // there the note has the button
    });
    it("the last parts are the help card and the footer, in that order", () => {
      const kids = [...page("return").el.children].map((x) => x.className.split(" ")[0]);
      expect(kids.slice(-2)).toEqual(["u-below", "u-ft"]);
    });
  });

  describe("what comes from the data is text, and only safe links are links", () => {
    it("a name, an announcement and a link that are markup or script stay text or are dropped", () => {
      const evil = normalize({ ...cases.first, announcement: "<img src=x onerror=alert(1)>", support_url: "javascript:alert(1)", user: { ...cases.first!.user, name: "<b>x</b>" } });
      const el = view(evil, state("ios", {}, "en"), actions());
      expect($(el, "img[onerror]")).toBeNull();
      expect($(el, ".u-hero h1 b")).toBeNull();
      expect(word(el, ".u-hero h1")).toBe("<b>x</b>’s subscription");
      expect($(el, "a[href^='javascript']")).toBeNull();
      expect([$(el, ".u-ask"), $(el, "header a")]).toEqual([null, null]);
    });
    it("a subscription without a name has a word of its own", () => {
      expect(word(page("return", { f: (d) => (d.user.name = "  ") }).el, ".u-hero .u-h1")).toBe("Ваша подписка");
    });
  });

  it("no dash starts a line: the space before it does not break", () => {
    for (const lang of langs) {
      const t = dict[lang];
      for (const s of [t.privacy, t.alFirstD, t.alSoonD(true), t.alLimD("1 Jan", true), t.alOffD(true), t.alExpD, t.newDevH, t.syOff]) expect(s).not.toMatch(/ —/);
    }
  });
});

describe("right to left (Persian) and the others", () => {
  it("only Persian is read right to left", () => {
    expect(langs.map((l) => [l, dirOf(l)])).toEqual([
      ["en", "ltr"],
      ["ru", "ltr"],
      ["zh", "ltr"],
      ["fa", "rtl"],
      ["tr", "ltr"],
    ]);
    expect([htmlLang("zh"), htmlLang("fa"), htmlLang("tr")]).toEqual(["zh-CN", "fa", "tr"]);
    expect([normalize({ lang: "fa" }).lang, normalize({ lang: "tr" }).lang]).toEqual(["fa", "tr"]);
  });

  it("each language row carries its own lang, so a foreign script is set in its own face and direction", () => {
    const rows = $$(page("return", { lang: "fa" }).el, ".u-menu button").map((b) => b.getAttribute("lang"));
    expect(rows).toEqual(["en", "ru", "zh-CN", "fa", "tr"]);
    const sum = $(page("return", { lang: "fa" }).el, "summary")!;
    expect($$(sum, ".lb, .cd").map((x) => x.getAttribute("lang"))).toEqual(["fa", "fa"]);
  });

  it("the Persian page: the name isolated, the figures in their own direction, the icons that point along the line marked", () => {
    const { el } = page("return", { lang: "fa" });
    expect(word(el, ".u-hero .u-h1")).toBe("اشتراک ⁨Alice⁩");
    expect(word(el, ".u-status b")).toBe(dict.fa.chip.active);
    expect(word(el, ".u-stat:nth-child(2) .v")).toBe("⁦64 GB⁩"); // "64 GB" is one left-to-right piece
    expect($$(el, ".u-hero svg.flip, header svg.flip").length).toBeGreaterThan(0);
    expect($$(el, "svg.flip").length).toBeLessThan($$(el, "svg").length);
    expect($$(el, ".u-tbl bdi").some((n) => n.textContent === "Франкфурт")).toBe(true); // the owner's place, as the owner wrote it
  });

  it("Persian dates are in the Persian calendar, with the Latin digits of the rest of the page; the year shows when it is another Persian year", () => {
    const t0 = Date.UTC(2026, 11, 1, 12) / 1000; // 10 Azar 1405
    expect(fmtDate(t0, "fa", t0)).toBe("10 آذر");
    expect(fmtDate(Date.UTC(2027, 1, 10, 12) / 1000, "fa", t0)).toBe("21 بهمن"); // a new Gregorian year, still 1405
    expect(fmtDate(Date.UTC(2027, 2, 25, 12) / 1000, "fa", t0)).toBe("5 فروردین 1406");
    expect(fmtDate(Date.UTC(2026, 0, 15, 12) / 1000, "fa", t0)).toBe("25 دی 1404"); // the same Gregorian year, the Persian one before
    expect(fmtNum(1.5, "fa")).toBe("1.5");
    expect(fmtAgo(t0 - 3 * day, "fa", t0)).toBe("3 روز پیش");
    expect(fmtDate(t0, "tr", t0)).toBe("1 Aralık");
    expect(fmtNum(1.5, "tr")).toBe("1,5");
  });

  it("Persian keeps a number with its Latin unit in its own direction, and the other languages do not mark it", () => {
    const u = { status: "active", used_bytes: 96 * GB, quota_bytes: 100 * GB };
    expect(hero(normalize({ user: u }), "fa", now).usedQ).toBe("⁦96 GB⁩");
    expect(hero(normalize({ user: u }), "en", now).usedQ).toBe("96 GB");
    expect(dict.fa.subOf("Alice (old)")).toBe("اشتراک ⁨Alice (old)⁩");
  });

  it("one stylesheet for both directions: no left or right, the right-to-left rules are there", () => {
    const rules = readFileSync(`${process.cwd()}/src/sub/sub.css`, "utf8").replace(/\/\*[\s\S]*?\*\//g, ""); // vitest runs in web/
    // the spinner's open side and the centred toast are the same either way
    const physical = rules.match(/(?:margin|padding|border)-(?:left|right)\b(?!-color: transparent)|[\s;{](?:left|right)\s*:(?! 50%)|text-align:\s*(?:left|right)|float\s*:/g);
    expect(physical).toBeNull();
    expect(rules).toContain(':root[dir="rtl"] .i.flip { transform: scaleX(-1); }');
    expect(rules).toMatch(/--sans: "Onest", "Vazirmatn",/);
  });

  it("the Turkish page: the polite «siz», the term in its own words, dates without a case ending", () => {
    const { el } = page("return", { lang: "tr" });
    expect(word(el, ".u-hero .u-h1")).toBe("Alice aboneliği");
    expect(word(el, ".u-status b")).toBe("Etkin");
    expect(word(el, ".u-stat:nth-child(1) .n")).toMatch(/^\d+ \S+( \d{4})? tarihine kadar$/);
    expect(word(el, ".u-hero-c [data-k=hero-add]")).toBe("Cihaz ekle");
    expect(plain(dict.tr.alLimD("1 Aralık", true))).toBe("Limit 1 Aralık tarihinde sıfırlanınca sunucular yeniden çalışır. Daha erken gerekiyorsa destek ekibine yazın");
  });

  it("Chinese and English say the states in their own words", () => {
    expect(word(page("expired", { lang: "zh" }).el, ".u-status b")).toBe(dict.zh.chip.expired);
    expect(word(page("quota", { lang: "en" }).el, ".u-alert b")).toBe("The traffic has run out");
  });
});

// ---- the motion of the boards that CSS cannot do (fx.ts) ----

describe("the figures written in letter by letter", () => {
  let queue: ((t: number) => void)[] = [];
  let calm = false;
  let wide = true;
  const old = { raf: globalThis.requestAnimationFrame, mm: globalThis.matchMedia };
  beforeEach(() => {
    queue = [];
    calm = false;
    wide = true;
    globalThis.requestAnimationFrame = ((cb: (t: number) => void) => queue.push(cb)) as typeof requestAnimationFrame;
    globalThis.matchMedia = ((q: string) => ({ matches: q.includes("reduced-motion") ? calm : wide, media: q, addEventListener() {}, removeEventListener() {} })) as unknown as typeof matchMedia;
  });
  afterEach(() => {
    globalThis.requestAnimationFrame = old.raf;
    globalThis.matchMedia = old.mm;
    document.body.replaceChildren();
  });

  /** The page drawn under .u-anim, as main.ts has it. */
  const drawn = (key: string, value = "64 ГБ") => {
    const box = document.createElement("div");
    box.className = "u-anim";
    box.innerHTML = `<span class="v" data-scr="${value}" data-scr-k="${key}" aria-label="${value}">${value}</span>`;
    document.body.append(box);
    return { box, v: box.querySelector<HTMLElement>(".v")! };
  };
  /** Frames until the page is at rest. */
  const run = () => {
    let t = performance.now();
    for (let i = 0; i < 400 && queue.length; i++) {
      const q = queue;
      queue = [];
      t += 20;
      for (const cb of q) cb(t);
    }
  };

  it("on a computer it plays: letters come in one by one, and the text ends as the figure says, with the aria-label there all along", () => {
    const { box, v } = drawn("t-play");
    scramble(box);
    expect(v.querySelectorAll(".scr-c").length).toBe("64 ГБ".length);
    expect(v.getAttribute("aria-label")).toBe("64 ГБ");
    run();
    expect(v.textContent).toBe("64 ГБ");
    expect(v.querySelectorAll(".scr-c")).toHaveLength(0); // at rest it is plain text again
  });
  it("a figure drawn again with the same value is not played again; one that changed is", () => {
    const first = drawn("t-again");
    scramble(first.box);
    run();
    const again = drawn("t-again");
    scramble(again.box);
    expect(again.v.querySelectorAll(".scr-c")).toHaveLength(0);
    expect(again.v.textContent).toBe("64 ГБ");
    const changed = drawn("t-again", "65 ГБ");
    scramble(changed.box);
    expect(changed.v.querySelectorAll(".scr-c").length).toBeGreaterThan(0);
    run();
    expect(changed.v.textContent).toBe("65 ГБ");
  });
  it("not at all for a person who asked for calm", () => {
    calm = true;
    const { box, v } = drawn("t-calm");
    scramble(box);
    expect([v.querySelectorAll(".scr-c").length, v.textContent, queue.length]).toEqual([0, "64 ГБ", 0]);
  });
  it("not at all on a phone", () => {
    wide = false;
    const { box, v } = drawn("t-phone");
    scramble(box);
    expect([v.querySelectorAll(".scr-c").length, v.textContent, queue.length]).toEqual([0, "64 ГБ", 0]);
  });
  it("the text of a figure is its data-scr whenever it is looked at: the page's own figures too", () => {
    const { el } = page("return");
    document.body.append(el);
    el.classList.add("u-anim");
    scramble(el);
    run();
    for (const v of $$(el, "[data-scr]")) expect(v.textContent, v.dataset.scrK).toBe(v.dataset.scr);
  });
});

describe("the copy button", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("is an icon that turns into a tick and a label that rolls up into the done word", () => {
    const a = actions();
    const btn = copyButton(a, { text: "https://x", label: "Скопировать", done: "Скопировано", toast: "Ссылка скопирована" });
    expect([btn.className, btn.dataset.cp]).toEqual(["u-btn u-cpb", ""]);
    expect(btn.querySelector(".u-cpi")!.getAttribute("aria-hidden")).toBe("true");
    expect([...btn.querySelectorAll(".u-cpi svg")].map((s) => s.getAttribute("class"))).toEqual(["i", "i ck"]);
    expect([...btn.querySelectorAll(".u-cpt > span")].map((s) => s.textContent)).toEqual(["Скопировать", "Скопировано"]);
  });
  it("a click copies, tells the page with the button in hand, rolls the label over and, after a moment, back", () => {
    const seen: unknown[] = [];
    const after = vi.fn();
    const a = actions({ copy: (value, done, message, from) => (seen.push([value, message, from]), done?.()) });
    const btn = copyButton(a, { text: "https://x", label: "Скопировать", done: "Скопировано", toast: "Ссылка скопирована", after });
    btn.click();
    expect(seen).toEqual([["https://x", "Ссылка скопирована", btn]]);
    expect(btn.dataset.cp).toBe("done");
    expect(after).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(DONE_MS - 1);
    expect(btn.dataset.cp).toBe("done");
    vi.advanceTimersByTime(2);
    expect(btn.dataset.cp).toBe("");
  });
  it("when the clipboard refuses, nothing rolls over", () => {
    const after = vi.fn();
    const a = actions({ copy: () => {} });
    const btn = copyButton(a, { text: "x", label: "L", toast: "T", after });
    btn.click();
    expect([btn.dataset.cp, after.mock.calls.length]).toEqual(["", 0]);
  });
  it("without a label it is the icon alone and its name is given", () => {
    const btn = copyButton(actions(), { text: "x", toast: "T", cls: "u-ic", aria: "Copy the link" });
    expect([btn.className, btn.getAttribute("aria-label"), btn.querySelector(".u-cpt")]).toEqual(["u-ic u-cpb", "Copy the link", null]);
  });
  it("the toast of a control in an open window lives in the window, and goes back to the page after", () => {
    const toast = document.createElement("div");
    const win = document.createElement("dialog");
    win.setAttribute("open", "");
    const inside = win.appendChild(document.createElement("button"));
    const outside = document.body.appendChild(document.createElement("button"));
    document.body.append(win);
    placeToast(toast, inside);
    expect([toast.parentElement, toast.classList.contains("in")]).toEqual([win, true]);
    placeToast(toast, outside);
    expect([toast.parentElement, toast.classList.contains("in")]).toEqual([document.body, false]);
    win.remove();
    outside.remove();
    toast.remove();
  });
  it("the card's buttons are such buttons", () => {
    const btn = $(page("return").el, "aside [data-k=hero-copy]")!;
    expect([btn.classList.contains("u-cpb"), btn.dataset.cp]).toEqual([true, ""]);
  });
});
