import { describe, expect, it } from "vitest";
import { cases } from "./dev-data";
import { dict, dirOf, htmlLang, isLang, langs } from "./i18n";
import { asPlatform, asTheme, atDeviceLimit, awgConfig, awgDevice, describePreset, detectPlatform, fmtAgo, fmtDate, fmtNum, hero, isReturning, kinds, normalize, pickPlatform, safeAddUrl, safeUrl, stateText } from "./logic";
import { actions, data, plain, state, text } from "./test-kit";
import { readFileSync } from "node:fs";
import { annKey, view } from "./view";

const now = 1_800_000_000;
const day = 86400;

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

  it("only offers kinds the user has access to", () => {
    expect(kinds(normalize({ ...cases.first!, access: { happ: true, amnezia: false } }))).toEqual(["happ"]);
    expect(kinds(normalize(cases.first))).toEqual(["happ", "amnezia"]);
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
  it("active: days, date and traffic of the quota", () => {
    const h = hero(user({ status: "active", expires_unix: now + 76 * day - 100, used_bytes: 12 * 1024 ** 3, quota_bytes: 100 * 1024 ** 3, quota_reset: "month", next_reset_unix: now + 9 * day }), "en", now);
    expect([h.big, h.unit, h.termL, h.soon, h.low, h.tone]).toEqual(["76", "days", "Left", false, false, "ok"]);
    expect([h.usedN, h.usedOf, h.pct, h.showTerm, h.showTraffic]).toEqual(["12", "of 100 GB", 12, true, true]);
    expect(h.reset).toMatch(/^resets /);
  });
  it("a week or less to go is 'soon', a warning with the days in it", () => {
    const h = hero(user({ status: "active", expires_unix: now + 3 * day - 600 }), "ru", now);
    expect([h.soon, h.tone, h.big, h.unit]).toEqual([true, "warn", "3", "дня"]);
    expect(hero(user({ status: "active", expires_unix: now + 7 * day - 600 }), "ru", now).soon).toBe(true);
    expect(hero(user({ status: "active", expires_unix: now + 8 * day }), "ru", now).soon).toBe(false);
    expect(hero(user({ status: "active", expires_unix: 0 }), "ru", now).soon).toBe(false);
  });
  it("90 % of the traffic or more is a warning that says how much is left and until when", () => {
    const h = hero(user({ status: "active", used_bytes: 92 * 1024 ** 3, quota_bytes: 100 * 1024 ** 3, quota_reset: "month", next_reset_unix: now + 9 * day }), "ru", now);
    expect([h.low, h.tone]).toEqual([true, "warn"]);
    expect(h.left).toMatch(/^Осталось 8 ГБ до \d+ /);
    expect(hero(user({ status: "active", used_bytes: 92 * 1024 ** 3, quota_bytes: 100 * 1024 ** 3 }), "ru", now).left).toBe("Осталось 8 ГБ");
    expect(hero(user({ status: "active", used_bytes: 89 * 1024 ** 3, quota_bytes: 100 * 1024 ** 3 }), "ru", now).low).toBe(false);
  });
  it("ended and disabled show no numbers; used-up traffic shows the traffic only", () => {
    expect(hero(user({ status: "expired", expires_unix: now - day }), "ru", now)).toMatchObject({ tone: "bad", showTerm: false, showTraffic: false });
    expect(hero(user({ status: "disabled" }), "ru", now)).toMatchObject({ tone: "off", showTerm: false, showTraffic: false });
    expect(hero(user({ status: "limited", used_bytes: 5, quota_bytes: 5, expires_unix: now + day }), "ru", now)).toMatchObject({ tone: "warn", showTerm: false, showTraffic: true, pct: 100, low: true });
  });
  it("no limit has no bar, no term is a sign", () => {
    const h = hero(user({ status: "active", used_bytes: 300 * 1024 ** 2 }), "ru", now);
    expect([h.pct, h.usedN, h.usedOf, h.big, h.unit, h.termL, h.reset]).toEqual([null, "300", "МБ · без лимита", "∞", "без срока", "Срок", ""]);
  });
  it("uses the Russian plural forms", () => {
    const u = (n: number) => hero(user({ status: "active", expires_unix: now + n * day - 10 }), "ru", now).unit;
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
  it("what the status says about a state that is not active", () => {
    const limited = (reset: string) => normalize({ user: { status: "limited", quota_reset: reset, next_reset_unix: now + 9 * day } });
    expect(plain(stateText(limited("month"), "ru", "X")?.body)).toMatch(/^Трафик на этот месяц закончился — до \d+ .+ VPN не работает\.$/);
    expect(plain(stateText(limited("month"), "ru", "X")?.call)).toBe("Нужно раньше — напишите.");
    expect(stateText(limited("day"), "ru", "X")?.body).toMatch(/^Трафик на сегодня закончился/);
    expect(stateText(limited("week"), "en", "X")?.body).toMatch(/^The traffic for this week is used up/);
    expect(plain(stateText(normalize({ user: { status: "limited" } }), "ru", "X")?.call)).toBe("Напишите — увеличим.");
    expect(stateText(normalize({ user: { status: "limited" } }), "ru", "X")?.body).toBe("Лимит трафика исчерпан.");
    const gone = stateText(normalize({ user: { status: "expired", expires_unix: now - day } }), "ru", "Mistgate");
    expect([gone?.body, plain(gone?.call)]).toEqual(["Интернет через Mistgate сейчас не работает.", "Напишите — продлим."]);
    expect(stateText(normalize({ user: { status: "expired", expires_unix: now - day } }), "ru", "X")?.head).toMatch(/^Подписка закончилась \d+ /);
    expect(stateText(normalize({ user: { status: "disabled" } }), "ru", "X")?.body).toBe("Администратор приостановил доступ.");
    expect(stateText(normalize(cases.first), "ru", "X")).toBeNull();
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

describe("view", () => {
  for (const [name, raw] of Object.entries(cases)) {
    it(`renders ${name} in every language, with nothing the page must never show`, () => {
      for (const lang of langs) {
        const d = normalize(raw);
        const el = view(d, state("ios", { returning: isReturning(d, false) }, lang), actions());
        expect(el.querySelector(".hero")).not.toBeNull();
        if (lang !== "zh") expect(el.querySelector(".priv")?.textContent).toContain("—"); // Chinese has no spaced dash
        const all = el.textContent ?? "";
        // nothing the page shows is the panel's jargon, a tunnel address, a node name, a profile name, or the secret link
        expect(all).not.toMatch(/AWG|10\.66\.|fd66:|Old apps|Main ·|nod_|vpn:\/\/|3f9k2v8q/); // the link and the key are copied or scanned, never written out
      }
    });
  }

  it("a problem has the way to write in the status and no connect card; the devices stay", () => {
    const expired = view(data("expired"), state("ios"), actions());
    expect(expired.querySelector(".card[aria-label=Подключение]")).toBeNull();
    expect(expired.querySelector(".srv")).toBeNull();
    const status = expired.querySelector(".hero")!;
    expect(status.classList.contains("bad")).toBe(true);
    expect(text(status.querySelector(".pill"))).toBe("Закончилась");
    expect(text(status.querySelector(".btn"))).toBe("Написать в поддержку");
    expect(text(status.querySelector(".why b"))).toMatch(/^Подписка закончилась \d+ /);
    expect(text(status.querySelector(".why"))).toMatch(/Интернет через Mistgate сейчас не работает\. Напишите — продлим\.$/);
    expect(text(status)).toContain("В приложении вы увидите то же сообщение");
    expect(status.querySelector(".stats")).toBeNull();
    expect(expired.querySelector("section[aria-label='Мои устройства']")).not.toBeNull();
    expect(expired.querySelector(".help")).toBeNull();
  });

  it("without support there is no button and no second sentence", () => {
    const el = view(data("expired-plain"), state("ios"), actions());
    expect(el.querySelector(".hero a.btn")).toBeNull();
    expect(text(el.querySelector(".hero .why"))).toMatch(/^Подписка закончилась \d+ \S+\. Интернет через Mistgate сейчас не работает\.$/);
    // used up with no reset rule: the plain words, and (with support) the offer to raise the limit
    expect(text(view(data("quota-plain"), state("ios"), actions()).querySelector(".hero .why"))).toBe("Лимит трафика исчерпан. Напишите — увеличим.");
    expect(text(view(data("quota-plain", (d) => (d.support_url = "")), state("ios"), actions()).querySelector(".hero .why"))).toBe("Лимит трафика исчерпан.");
  });

  it("used-up traffic: a warning, the traffic only, the reset date, and the way to write", () => {
    const status = view(data("quota"), state("ios"), actions()).querySelector(".hero")!;
    expect(status.classList.contains("warn")).toBe(true);
    expect(text(status.querySelector(".pill"))).toBe("Трафик исчерпан");
    expect(status.querySelectorAll(".stat")).toHaveLength(1);
    expect(text(status.querySelector(".stat-h .hint"))).toMatch(/^обнулится \d+ /);
    expect(text(status.querySelector(".why"))).toMatch(/^Трафик на этот месяц закончился — до \d+ \S+ VPN не работает\. Нужно раньше — напишите\.$/);
    expect(text(status.querySelector(".btn"))).toBe("Написать в поддержку");
  });

  it("disabled: grey, the admin's words", () => {
    const status = view(data("disabled"), state("ios"), actions()).querySelector(".hero")!;
    expect(text(status.querySelector(".pill"))).toBe("Отключена");
    expect(text(status.querySelector(".why"))).toBe("Администратор приостановил доступ. Если это ошибка — напишите.");
  });

  it("ends within a week: a warning with the days and a way to write; 90 % of the traffic: the tile warns and says what is left", () => {
    const soon = view(data("soon"), state("ios"), actions()).querySelector(".hero")!;
    expect(soon.classList.contains("warn")).toBe(true);
    expect(text(soon.querySelector(".pill"))).toBe("Скоро закончится");
    expect(text(soon.querySelector(".why"))).toBe("Осталось 3 дня — напишите, чтобы продлить.");
    expect(text(soon.querySelector("a.btn"))).toBe("Написать");
    const low = view(data("low-traffic"), state("ios"), actions()).querySelector(".hero")!;
    expect(low.classList.contains("warn")).toBe(false);
    expect(low.querySelectorAll(".stat.warn")).toHaveLength(1);
    expect(text(low.querySelector(".sm.row"))).toMatch(/^Осталось 8 ГБ до /);
    expect(text(view(data("plain"), state("ios"), actions()).querySelector(".hero .stats"))).toContain("312");
    const noSupport = view(data("soon", (d) => (d.support_url = "")), state("ios"), actions()).querySelector(".hero")!;
    expect(text(noSupport.querySelector(".why"))).toBe("Осталось 3 дня.");
    expect(noSupport.querySelector("a.btn")).toBeNull();
  });

  it("the status of a calm subscription: the term and the traffic; 'the app got the subscription' once an app did", () => {
    const first = view(data("first"), state("ios"), actions()).querySelector(".hero")!;
    expect([...first.querySelectorAll(".stat .eb")].map(text)).toEqual(["Осталось", "Трафик"]);
    expect(text(first.querySelector(".pill"))).toBe("Активна");
    expect(text(first)).not.toContain("Приложение получило подписку");
    const back = view(data("return"), state("ios", { returning: true }), actions()).querySelector(".hero")!;
    expect(text(back)).toMatch(/Приложение получило подписку 3 часа назад/);
  });

  it("first visit: steps, servers, devices; a returning visit: servers, devices (and the way to connect one more is in them)", () => {
    const heads = (el: HTMLElement) => [...el.querySelectorAll(".shead .h2")].map(text);
    expect(heads(view(data("first"), state("ios"), actions()))).toEqual(["Подключите VPN", "Серверы", "Мои устройства"]);
    const back = view(data("return"), state("ios", { returning: true }), actions());
    expect(heads(back)).toEqual(["Серверы", "Мои устройства"]);
    expect(back.querySelector("[data-k=more]")).toBeNull(); // no block of its own under the devices
    expect(back.querySelector(".step")).toBeNull();
    expect(back.querySelector("[data-k=dev-add]")).not.toBeNull();
  });

  it("puts server text in as text and drops unsafe links", () => {
    const evil = normalize({ ...cases.first, announcement: "<img src=x onerror=alert(1)>", support_url: "javascript:alert(1)", user: { ...cases.first!.user, name: "<b>x</b>" } });
    const el = view(evil, state("ios", {}, "en"), actions());
    expect(el.querySelector("img[onerror]")).toBeNull();
    expect(el.querySelector(".hero h1 b")).toBeNull();
    expect(text(el.querySelector(".hero h1"))).toBe("Hi, <b>x</b>");
    expect(el.querySelector("a[href^='javascript']")).toBeNull();
    expect(el.querySelector(".help")).toBeNull();
  });

  it("opens a tg:// support link as a Telegram link", () => {
    const tg = "tg://resolve?domain=example_support";
    const el = view(data("first", (d) => (d.support_url = tg)), state("ios", {}, "en"), actions());
    expect(text(el.querySelector(".help"))).toContain("We reply in Telegram");
    expect(el.querySelector(`.help a[href='${tg}']`)).not.toBeNull();
  });

  it("lists a Telegram link by its label alone, never by a node or profile name", () => {
    const d = normalize({
      ...cases.first,
      access: { happ: true, amnezia: false, telegram: true },
      telegram: {
        links: [
          { label: "Germany · Frankfurt", country_code: "DE", place: "Frankfurt", url: "tg://proxy?server=de1.example.com&port=443&secret=ee00", node: "fra-edge-07", profile: "private-profile" },
          { label: "Germany · Frankfurt · IPv6", country_code: "DE", place: "Frankfurt", ipv6: true, url: "tg://proxy?server=2001%3Adb8%3A%3A5&port=443&secret=ee00" },
          { label: "evil", url: "https://example.com/not-telegram" },
        ],
      },
    });
    const el = view(d, state("ios", {}, "en"), actions());
    const cards = [...el.querySelectorAll(".srv.tg")];
    expect(cards.map((c) => text(c.querySelector(".srv-name")))).toEqual(["Germany · Frankfurt", "Germany · Frankfurt"]);
    expect(text(el)).not.toContain("fra-edge-07");
    expect(text(el)).not.toContain("private-profile");
  });

  it("gives every Telegram link a card: the twin marked, Open in Telegram, a QR code for a computer, and one line on what happens", () => {
    const url = "tg://proxy?server=198.51.100.20&port=8443&secret=ee00";
    const d = normalize({
      ...cases.first,
      access: { happ: true, amnezia: false, telegram: true },
      telegram: { links: [{ label: "Germany · Frankfurt", country_code: "DE", place: "Frankfurt", relay: true, via: "RU", url }] },
    });
    const el = view(d, state("windows", {}, "en"), actions());
    const card = el.querySelector(".srv.tg")!;
    expect(text(card.querySelector(".srv-top .chip"))).toBe("via RU");
    expect(card.querySelector<HTMLAnchorElement>(".btn.pri")!.getAttribute("href")).toBe(url);
    expect(card.querySelector(".tg-qr svg")?.getAttribute("aria-label")).toBe("QR code of the Telegram proxy: Germany");
    expect(text(el)).toContain("Telegram offers to connect the proxy.");
    // on, but no MTProto profile runs yet
    const none = view(normalize({ ...cases.first, access: { happ: true, amnezia: false, telegram: true }, telegram: { links: [] } }), state("windows", {}, "en"), actions());
    expect(none.querySelector(".srv.tg")).toBeNull();
    expect(text(none.querySelector(".tg-empty"))).toBe("The proxy is still being prepared — message the admin.");
  });

  it("a closed announcement stays closed through redraws; a new text shows again", () => {
    const a = actions();
    const d = data("first");
    const el = view(d, state("ios"), a);
    el.querySelector<HTMLButtonElement>(".ann .x")!.click();
    expect(a.log).toEqual(["closed"]);
    expect(view(d, state("ios", { annClosed: true }), actions()).querySelector(".ann")).toBeNull();
    expect(annKey("a")).not.toBe(annKey("b"));
    expect(annKey(d.announcement)).toBe(annKey(d.announcement));
  });

  it("the language menu: the summary names the current language, a row per language (English, Русский, 简体中文, فارسی, Türkçe), the current one checked", () => {
    const a = actions();
    const el = view(data("first"), state("ios"), a);
    const menu = el.querySelector<HTMLDetailsElement>("details.lang-menu")!;
    expect(menu.open).toBe(false);
    const summary = menu.querySelector("summary")!;
    expect(text(summary)).toBe("RU");
    expect(summary.getAttribute("aria-label")).toBe("Язык: Русский");
    const rows = [...menu.querySelectorAll("button")];
    expect(rows.map((b) => [text(b), b.getAttribute("aria-pressed"), b.getAttribute("lang")])).toEqual([
      ["EnglishEN", "false", "en"],
      ["РусскийRU", "true", "ru"],
      ["简体中文中文", "false", "zh-CN"],
      ["فارسیFA", "false", "fa"],
      ["TürkçeTR", "false", "tr"],
    ]);
    expect(rows.map((b) => b.querySelectorAll("svg").length)).toEqual([0, 1, 0, 0, 0]);
    rows[0]!.click();
    rows[1]!.click(); // already this one: nothing but closing
    expect(a.log).toEqual(["lang en"]);
  });

  it("the language menu closes on a click outside and on Esc, which gives the focus back to the summary", () => {
    const el = view(data("first"), state("ios"), actions());
    document.body.append(el);
    const menu = el.querySelector<HTMLDetailsElement>("details.lang-menu")!;
    menu.open = true;
    el.querySelector<HTMLElement>(".hero")!.click();
    expect(menu.open).toBe(false);
    menu.open = true;
    menu.querySelector("summary")!.click(); // inside: the browser's own toggle, the page stays out of it
    menu.open = true;
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(menu.open).toBe(false);
    expect(document.activeElement).toBe(menu.querySelector("summary"));
    el.remove();
  });

  it("the theme switch in the footer: Auto, Light, Dark", () => {
    const a = actions();
    const el = view(data("first"), state("ios", { theme: "light" }), a);
    const radios = [...el.querySelectorAll(".foot [role=radio]")];
    expect(radios.map((b) => [text(b), b.getAttribute("aria-checked")])).toEqual([["Авто", "false"], ["Светлая", "true"], ["Тёмная", "false"]]);
    (el.querySelector("[data-k=theme-dark]") as HTMLButtonElement).click();
    expect(a.log).toEqual(["theme dark"]);
    expect(text(el.querySelector(".foot .priv"))).toBe("Ссылка личная — не пересылайте её. Работает, пока подписка активна.");
  });

  it("the help card is a card with 'Write' where there is support, and the status has the button only when something is wrong", () => {
    const el = view(data("first"), state("ios"), actions());
    expect(el.querySelectorAll(".help")).toHaveLength(2); // beside the steps on a computer, at the end on a phone
    expect(text(el.querySelector(".help .b"))).toBe("Что-то не работает?");
    expect(el.querySelector(".hero a.btn")).toBeNull();
    expect(view(data("plain"), state("ios"), actions()).querySelector(".help")).toBeNull();
  });

  it("no dash starts a line: the space before it does not break", () => {
    for (const lang of langs) {
      const t = dict[lang];
      for (const s of [t.linkD, t.privacy, t.limit(3, 3, true), t.txt.expired("X").call, t.staleSteps("A").join(" "), t.dnsLinkNote("A", "B"), t.dnsDoneLink(12).join(" "), t.soon(3).join(" ")]) expect(s).not.toMatch(/ —/);
    }
  });
});

describe("Persian and Turkish", () => {
  const GB = 1024 ** 3;
  const t0 = Date.UTC(2026, 11, 1, 12) / 1000; // 10 Azar 1405

  it("Persian is read right to left, the others left to right", () => {
    expect(langs.map((l) => [l, dirOf(l)])).toEqual([
      ["en", "ltr"],
      ["ru", "ltr"],
      ["zh", "ltr"],
      ["fa", "rtl"],
      ["tr", "ltr"],
    ]);
    expect([htmlLang("fa"), htmlLang("tr")]).toEqual(["fa", "tr"]);
    expect([normalize({ lang: "fa" }).lang, normalize({ lang: "tr" }).lang]).toEqual(["fa", "tr"]);
  });

  it("Persian dates are in the Persian calendar, with the Latin digits of the rest of the page; the year shows when it is another Persian year", () => {
    expect(fmtDate(t0, "fa", t0)).toBe("10 آذر");
    expect(fmtDate(Date.UTC(2027, 1, 10, 12) / 1000, "fa", t0)).toBe("21 بهمن"); // a new Gregorian year, still 1405
    expect(fmtDate(Date.UTC(2027, 2, 25, 12) / 1000, "fa", t0)).toBe("5 فروردین 1406");
    expect(fmtDate(Date.UTC(2026, 0, 15, 12) / 1000, "fa", t0)).toBe("25 دی 1404"); // the same Gregorian year, the Persian one before
    expect(fmtNum(1.5, "fa")).toBe("1.5");
    expect(fmtAgo(t0 - 3 * day, "fa", t0)).toBe("3 روز پیش");
    expect(fmtDate(t0, "tr", t0)).toBe("1 Aralık");
    expect(fmtNum(1.5, "tr")).toBe("1,5");
  });

  it("Persian keeps a number with its Latin unit, and a name from data, in their own direction", () => {
    const h = hero(normalize({ user: { status: "active", used_bytes: 96 * GB, quota_bytes: 100 * GB, expires_unix: t0 + 40 * day } }), "fa", t0);
    expect(h.usedOf).toBe("از \u2066100 GB\u2069"); // "100 GB" is one left-to-right piece
    expect(h.left).toBe("\u20664 GB\u2069 باقی مانده");
    expect(hero(normalize({ user: { status: "active", used_bytes: 96 * GB, quota_bytes: 100 * GB } }), "en", t0).usedOf).toBe("of 100 GB"); // no marks elsewhere
    expect(dict.fa.hi("Alice (old)")).toBe("سلام، \u2068Alice (old)\u2069");
    expect(dict.fa.removeT("Mum’s phone")).toBe("«\u2068Mum’s phone\u2069» حذف شود؟");
    expect(dict.fa.qrHow("")).toContain("برنامه‌ای که با لینک کار می‌کند"); // no empty isolate stands for a missing name
    // a name with a Persian letter is right to left even when it starts with a Latin brand: «AdGuard» stays first
    expect(plain(dict.fa.dnsDefault("AdGuard: بدون تبلیغات"))).toBe("پیش‌فرض — \u2067AdGuard: بدون تبلیغات\u2069");
  });

  it("the Persian page: names from data in <bdi>, the icons that point along the line marked to turn round, nothing of the panel shown", () => {
    const el = view(data("return"), state("ios", { returning: true }, "fa"), actions());
    expect(text(el.querySelector(".hero .h1"))).toBe("سلام، \u2068Alice\u2069");
    expect([...el.querySelectorAll(".dev-n bdi")].map((n) => n.textContent)).toEqual(expect.arrayContaining(["Pixel 7", "MacBook"]));
    expect(el.querySelector(".srv-name span bdi")?.textContent).toBe("Франкфурт"); // the owner's place, as the owner wrote it
    expect([...el.querySelectorAll(".srv-app .mono")].map((n) => n.textContent)).toContain("DE · Hysteria2"); // as the apps write it, left to right (sub.css)
    expect(el.querySelectorAll("svg.flip").length).toBeGreaterThan(0);
    expect([...el.querySelectorAll("svg.flip")].length).toBeLessThan(el.querySelectorAll("svg").length);
  });

  it("one stylesheet for both directions: no left or right, the right-to-left rules are there", () => {
    const rules = readFileSync(`${process.cwd()}/src/sub/sub.css`, "utf8").replace(/\/\*[\s\S]*?\*\//g, ""); // vitest runs in web/
    // the spinner's open side and the centred toast are the same either way
    const physical = rules.match(/(?:margin|padding|border)-(?:left|right)\b(?!-color: transparent)|[\s;{](?:left|right)\s*:(?! 50%)|text-align:\s*(?:left|right)|float\s*:/g);
    expect(physical).toBeNull();
    expect(rules).toContain('[dir="rtl"] .i.flip { transform: scaleX(-1); }');
    expect(rules).toContain('[dir="rtl"] :is(.mono, .inp.pw) { direction: ltr; unicode-bidi: isolate; }');
    expect(rules).toMatch(/--sans: "Onest", "Vazirmatn",/);
    expect(rules).toMatch(/:lang\(fa\) body \{ line-height: 1\.7; \}/);
  });

  it("the Turkish page renders in Turkish: the polite «siz», dates without a case ending", () => {
    const el = view(data("return"), state("ios", { returning: true }, "tr"), actions());
    expect(text(el.querySelector(".hero .h1"))).toBe("Merhaba, Alice");
    expect(text(el.querySelector(".hero .pill"))).toBe("Etkin");
    expect(text(el.querySelector(".hero"))).toMatch(/\d+ \S+ tarihine kadar/);
    expect(text(el.querySelector("section[aria-label='Cihazlarım']"))).toContain("Cihaz ekle");
    expect(plain(dict.tr.txt.limitedReset("1 Aralık", "month").body)).toBe("Bu ayki trafik bitti — VPN 1 Aralık tarihine kadar çalışmıyor.");
    expect(plain(dict.tr.txt.limitedReset("1 Aralık", "none").body)).toBe("Trafik bitti — VPN 1 Aralık tarihine kadar çalışmıyor.");
  });
});
