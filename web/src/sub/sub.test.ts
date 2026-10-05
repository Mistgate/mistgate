import { describe, expect, it } from "vitest";
import { newAmzState } from "./amnezia";
import { cases } from "./dev-data";
import { dict } from "./i18n";
import { atDeviceLimit, detectPlatform, fmtAgo, hero, kinds, normalize, pickPlatform, safeAddUrl, safeUrl, stateText } from "./logic";
import { annKey, view, type Actions, type State } from "./view";

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

  it("only offers apps the user has access to, and falls back to the first platform", () => {
    const d = normalize(cases.happ);
    expect(kinds(d)).toEqual(["happ"]);
    expect(kinds(normalize(cases.both))).toEqual(["happ", "amnezia"]);
    expect(pickPlatform(d, "android")).toBe("android");
    expect(pickPlatform(d, "linux")).toBe("ios"); // nothing for Linux while the user has no Amnezia: the first listed
    expect(pickPlatform(normalize(cases.both), "linux")).toBe("linux");
    expect(pickPlatform(d, null)).toBe("ios");
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
    const loads = normalize({ server_loads: [
      { name: "EE", level: "high", load_percent: 71, rx_bps: 64_000_000 },
      { name: "DE", level: "low" },
      { name: "RU", level: "busy" },
      { name: "", level: "medium" },
      { name: "PL", load_percent: 2, rx_bps: 1_000_000, tx_bps: 0, capacity_mbps: 1000 },
    ] }).server_loads;
    expect(loads).toEqual([{ name: "EE", level: "high" }, { name: "DE", level: "low" }]);
    expect(normalize(null).options.show_qr).toBe(true);
    expect(normalize({ brand: { accent: "javascript:1" } }).brand.accent).toBe("");
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
    const h = hero(user({ status: "active", expires_unix: now + 76 * day - 100, used_bytes: 12 * 1024 ** 3, quota_bytes: 100 * 1024 ** 3 }), "en", now);
    expect(h.big).toBe("76");
    expect(h.word).toBe(false);
    expect(h.unit).toBe("days");
    expect(h.line).toBe("12 / 100 GB");
    expect(h.usedOf).toBe("of 100 GB");
    expect(h.pct).toBe(12);
    expect(h.showTraffic).toBe(true);
  });
  it("ended says so in words with the date under it, and drops the traffic", () => {
    const h = hero(user({ status: "expired", expires_unix: now - day, used_bytes: 5, quota_bytes: 10 }), "ru", now);
    expect([h.big, h.word, h.unit, h.showTraffic]).toEqual(["Закончилась", true, "", false]);
    expect(h.sub).toMatch(/^\d+ \S+/); // "28 мая": the date, not "0 дней"
  });
  it("limited fills the bar, no quota has no bar", () => {
    expect(hero(user({ status: "limited", used_bytes: 5, quota_bytes: 5, expires_unix: now + day }), "ru", now).pct).toBe(100);
    const u = hero(user({ status: "active", used_bytes: 300 * 1024 ** 2 }), "ru", now);
    expect(u.pct).toBeNull();
    expect(u.line).toBe("300 МБ");
    expect(u.big).toBe("∞");
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
  it("used-up traffic says until when the VPN is off, for the period of the quota", () => {
    const limited = (reset: string) => normalize({ user: { status: "limited", quota_reset: reset, next_reset_unix: now + 9 * day } });
    expect(stateText(limited("month"), "ru", "X")?.join(" ")).toMatch(/^Трафик на этот месяц закончился — до \d+ .+ VPN не работает\. Нужно раньше — напишите\.$/);
    expect(stateText(limited("day"), "ru", "X")?.[0]).toMatch(/^Трафик на сегодня закончился/);
    expect(stateText(limited("week"), "en", "X")?.[0]).toMatch(/^The traffic for this week is used up/);
    expect(stateText(normalize({ user: { status: "limited" } }), "ru", "X")).toEqual(["Лимит трафика исчерпан.", "Напишите — увеличим."]);
  });
});

const noop = () => {};
const acts = (over: Partial<Actions> = {}): Actions => ({
  lang: noop,
  platform: noop,
  qrOpen: noop,
  closeAnn: noop,
  copy: noop,
  download: noop,
  amz: { add: noop, form: noop, create: noop, show: noop, renew: noop, node: noop, ask: noop, rotate: noop, remove: noop },
  ...over,
});

describe("view", () => {
  const state = (lang: "ru" | "en", annClosed = false): State => ({ lang, platform: "ios", qrOpen: false, annClosed, amz: newAmzState("ios", "p31", "ios") });
  for (const [name, raw] of Object.entries(cases)) {
    it(`renders ${name} in both languages`, () => {
      for (const lang of ["ru", "en"] as const) {
        const el = view(normalize(raw), state(lang), acts());
        expect(el.querySelector(".hero")).not.toBeNull();
        expect(el.querySelector(".privacy")?.textContent).toContain("—");
        // nothing the page shows is the panel's jargon (the admin has it): no AWG, no tunnel addresses, no profile names
        expect(el.textContent).not.toMatch(/AWG|10\.66\.|fd66:|Old apps|Main/);
      }
    });
  }

  it("a state with a problem has its button in the hero and no second support card", () => {
    const expired = view(normalize(cases.expired), state("ru"), acts());
    expect(expired.querySelector(".ways")).toBeNull();
    expect(expired.querySelector(".hero .btn")?.textContent).toBe("Написать в поддержку");
    expect(expired.querySelector(".sup")).toBeNull();
    expect(expired.querySelector(".hero .big")?.textContent).toBe("Закончилась");
    expect(expired.querySelector(".hero .tr")).toBeNull(); // no traffic for an ended subscription
    const active = view(normalize(cases.happ), state("ru"), acts());
    expect(active.querySelector(".hero .btn")).toBeNull();
    expect(active.querySelector(".sup")).not.toBeNull();
    expect(active.querySelector(".ways a[href^='happ://add/']")).not.toBeNull();
    expect(active.querySelector(".privacy")?.textContent).toBe("Ссылка личная — не пересылайте её. Работает, пока подписка активна.");
    expect(view(normalize(cases.stale), state("ru"), acts()).querySelector(".stale")).not.toBeNull();
    expect(view(normalize(cases.devices), state("ru"), acts()).querySelector(".limit")).not.toBeNull();
  });

  it("puts server text in as text and drops unsafe links", () => {
    const evil = normalize({
      ...cases.happ,
      announcement: "<img src=x onerror=alert(1)>",
      support_url: "javascript:alert(1)",
      user: { ...cases.happ!.user, name: "<b>x</b>" },
    });
    const el = view(evil, state("en"), acts());
    expect(el.querySelector("img[onerror]")).toBeNull();
    expect(el.querySelector(".hello b")).toBeNull();
    expect(el.querySelector(".hello")?.textContent).toBe("Hi, <b>x</b>");
    expect(el.querySelector("a[href^='javascript']")).toBeNull();
    expect(el.querySelector(".sup")).toBeNull();
  });

  it("opens a tg:// support link as a Telegram link", () => {
    const tg = "tg://resolve?domain=example_support";
    const el = view(normalize({ ...cases.happ, support_url: tg }), state("en"), acts());
    expect(el.querySelector(".sup")?.textContent).toContain("We reply in Telegram");
    expect([...el.querySelectorAll(`a[href='${tg}']`)].map((a) => a.textContent)).toContain("Message on Telegram →");
  });

  it("a closed announcement stays closed through redraws; a new text shows again", () => {
    const log: string[] = [];
    const d = normalize(cases.happ);
    const el = view(d, state("ru"), acts({ closeAnn: () => log.push("closed") }));
    el.querySelector<HTMLButtonElement>(".ann .x")!.click();
    expect(log).toEqual(["closed"]);
    expect(view(d, state("ru", true), acts()).querySelector(".ann")).toBeNull();
    expect(annKey("a")).not.toBe(annKey("b"));
    expect(annKey(d.announcement)).toBe(annKey(d.announcement));
  });

  it("the language switch says which language it is and where it goes", () => {
    expect(view(normalize(cases.happ), state("ru"), acts()).querySelector(".lang")?.getAttribute("aria-label")).toBe("Язык: русский. Переключить на English");
    expect(view(normalize(cases.happ), state("en"), acts()).querySelector(".lang")?.getAttribute("aria-label")).toBe("Language: English. Switch to Russian");
  });

  it("no dash starts a line: the space before it does not break", () => {
    for (const lang of ["ru", "en"] as const) {
      const t = dict[lang];
      for (const s of [t.twoWays, t.linkD, t.privacy, t.limit(3, 3, true), t.txt.expired("X").join(" "), t.staleSteps("A").join(" ")]) expect(s).not.toMatch(/ —/);
    }
  });
});
