import { describe, expect, it } from "vitest";
import { ApiError, type DnsAnswer } from "./api";
import { cases } from "./dev-data";
import { dnsActions, type DnsApi } from "./dns-actions";
import { dict } from "./i18n";
import {
  canChooseDns,
  canChooseLinkDns,
  describePreset,
  dnsChoices,
  dnsDefault,
  hasKeys,
  hasLink,
  LINK,
  linkChoices,
  normalize,
  presetDescription,
  presetName,
  serverName,
  serversOf,
  serverText,
  showsKeyDns,
  usesKeys,
} from "./logic";
import { dnsModal } from "./servers";
import { newDnsState, type Ctx } from "./state";
import { actions, data, plain, state, text } from "./test-kit";
import { view } from "./view";

const t = dict.ru;
const settle = () => new Promise((r) => setTimeout(r, 0));
const cards = (el: HTMLElement) => [...el.querySelectorAll<HTMLElement>(".srv")]; // the old cards: the block of the preset names still reads them
const page = (name: string, st = state("android", { returning: true }), f: (d: ReturnType<typeof data>) => void = () => {}) => view(data(name, f), st, actions());

describe("the servers", () => {
  const rows = (el: HTMLElement) => [...el.querySelectorAll<HTMLElement>(".u-tbl .u-tr:not(.u-th)")];
  const phone = (el: HTMLElement) => [...el.querySelectorAll<HTMLElement>(".u-tbl .u-sr")];
  const country = (r: HTMLElement) => text(r.querySelector(".u-name > span:nth-child(2)"));
  const byName = (el: HTMLElement, n: string) => rows(el).find((r) => country(r) === n)!;

  it("one card with a table: the head, then a row each, the working ones first; the same list as rows for a phone", () => {
    const d = data("first");
    d.servers = [d.servers[2]!, d.servers[0]!, d.servers[1]!]; // the one that is down listed first
    const el = view(d, state("android"), actions());
    const card = el.querySelector(".u-card.u-tbl")!;
    expect(card.getAttribute("aria-label")).toBe("Серверы");
    expect([...card.querySelectorAll(".u-th > span")].map(text)).toEqual(["Сервер", "Загрузка", "Маршрут", "DNS ключа", ""]);
    expect(rows(el).map(country)).toEqual(["Германия", "Нидерланды", "Финляндия"]);
    expect(text(rows(el)[0]!.querySelector(".u-name small"))).toBe("Франкфурт");
    expect(rows(el).map((r) => r.classList.contains("off"))).toEqual([false, false, true]);
    // the same servers, in the same order, as the rows of a phone
    expect(phone(el).map((r) => text(r.querySelector(".u-name > span:nth-child(2)")))).toEqual(["Германия", "Нидерланды", "Финляндия"]);
    expect(card.querySelectorAll(".u-tr.only-w, .u-th.only-w")).toHaveLength(4);
    expect([...card.querySelectorAll(".u-sr")].every((r) => r.classList.contains("only-m"))).toBe(true);
    expect(el.querySelector(".srv, .srv-grid")).toBeNull();
  });

  it("the load: three bars and a word, amber when high; a server that does not answer says so and what to do", () => {
    const el = page("first");
    const [de, nl, fi] = rows(el) as [HTMLElement, HTMLElement, HTMLElement];
    expect([de, nl].map((r) => text(r.querySelector(".u-load")))).toEqual(["высокая", "средняя"]);
    expect([de, nl].map((r) => r.querySelectorAll(".u-meter i.f").length)).toEqual([3, 2]);
    expect([de, nl].map((r) => r.querySelectorAll(".u-meter i").length)).toEqual([3, 3]);
    expect(de.querySelector(".u-meter")?.classList.contains("hi")).toBe(true);
    expect(nl.querySelector(".u-meter")?.classList.contains("hi")).toBe(false);
    expect(text(fi.querySelector(".u-load"))).toBe("не отвечает");
    expect(fi.querySelector(".u-load .u-dot.off")).not.toBeNull();
    expect(fi.querySelector(".u-meter, .u-tag, .u-ic")).toBeNull();
    expect(text(fi)).toContain("Выберите в приложении другой сервер");
    // a server whose level is unknown has no bars, only a dash
    const unknown = page("first", state("android"), (d) => (d.servers[1]!.load = null));
    expect(byName(unknown, "Нидерланды").querySelector(".u-meter")).toBeNull();
    expect(text(byName(unknown, "Нидерланды").querySelector(".u-load"))).toBe("—");
  });

  it("the route: WARP, 'через RU' and, when there is neither, 'напрямую'; the hints are in the title; a phone has no 'напрямую'", () => {
    const el = page("first");
    expect(rows(el).map((r) => [...r.querySelectorAll(".u-tag")].map(text))).toEqual([["WARP"], ["напрямую"], []]);
    expect(rows(el)[0]!.querySelector(".u-tag.warp")?.getAttribute("title")).toBe("Тот же сервер, но выход в интернет через Cloudflare");
    expect(rows(el)[1]!.querySelector(".u-tag")?.classList.contains("dir")).toBe(true);
    expect(phone(el).map((r) => [...r.querySelectorAll(".u-tag")].map(text))).toEqual([["WARP"], [], []]);
    const relay = page("relay");
    expect([...byName(relay, "Нидерланды").querySelectorAll(".u-tag")].map(text)).toEqual(["через RU"]);
    expect(byName(relay, "Нидерланды").querySelector(".u-tag.ru")?.getAttribute("title")).toBe(dict.ru.viaHint("RU"));
    expect(byName(relay, "Германия").querySelector(".u-tag.ru")).toBeNull();
    expect(text(byName(view(data("relay"), state("android", { returning: true }, "en"), actions()), "Netherlands").querySelector(".u-tag.ru"))).toBe("via RU");
    // an entry without a country still says it goes another way
    const anon = page("relay", undefined, (d) => (d.servers[0]!.connections.at(-1)!.via = ""));
    expect(text(byName(anon, "Нидерланды").querySelector(".u-tag.ru"))).toBe("через ретрансляцию");
    const read = normalize({ servers: [{ id: "a", label: "x", connections: [{ way: "link", relay: true, via: " ru " }, { way: "link", via: "RU" }] }] }).servers[0]!.connections;
    expect(read.map((c) => [c.relay, c.via])).toEqual([[true, "RU"], [false, ""]]);
  });

  it("the flag is the board's: CSS for the countries it draws, the two letters for any other, a plain tile for none", () => {
    const el = page("first");
    const flags = rows(el).map((r) => r.querySelector(".fl2")!);
    expect(flags.map((f) => f.className)).toEqual(["fl2 de", "fl2 nl", "fl2 cc"]);
    expect(text(flags[2])).toBe("FI");
    expect(flags[0]!.getAttribute("role")).toBe("img");
    expect(flags[0]!.getAttribute("aria-label")).toBe("Флаг: Германия");
    // on a phone the name stands beside it, so the flag says nothing
    expect(phone(el)[0]!.querySelector(".fl2")?.getAttribute("aria-hidden")).toBe("true");
    expect(phone(el)[0]!.querySelector(".fl2")?.getAttribute("role")).toBeNull();
    const old = page("old");
    expect(rows(old).map((r) => r.querySelector(".fl2")!.className)).toEqual(["fl2 nc", "fl2 nc", "fl2 nc"]);
    expect(rows(old)[0]!.querySelector(".fl2")?.getAttribute("aria-hidden")).toBe("true");
  });

  it("twins of a country are told apart by a number, 'DE-2'; one server of its country, or one with a place, has none", () => {
    const d = data("first");
    d.servers = [{ ...d.servers[0]!, place: "", label: "Германия 1" }, { ...d.servers[0]!, id: "nod_b", place: "", label: "Германия 2" }, { ...d.servers[0]!, id: "nod_c", place: "", label: "Германия" }];
    const el = view(d, state("android"), actions());
    expect(rows(el).map((r) => text(r.querySelector(".u-name .id")))).toEqual(["DE-1", "DE-2", "DE-3"]);
    expect(rows(page("first")).map((r) => r.querySelector(".id"))).toEqual([null, null, null]);
  });

  it("the names in the apps are the hint of the name, so a person can find the server there", () => {
    expect(rows(page("first"))[0]!.querySelector(".u-name")?.getAttribute("title")).toBe("В приложении: DE · Hysteria2, DE · Hysteria2 WARP");
    expect(byName(page("variants"), "Россия").querySelector(".u-name")?.hasAttribute("title")).toBe(false);
  });

  it("the bar: whether the servers work and how busy they are; a server that is down changes the words and the dot", () => {
    const bar = (el: HTMLElement) => el.querySelector(".u-bar")!;
    expect(text(bar(page("first")).querySelector(".l"))).toBe("Работают 2 из 3· загрузка высокая");
    expect(bar(page("first")).querySelector(".l .u-dot")?.classList.contains("warn")).toBe(true);
    const calm = page("first", state("android"), (d) => ((d.servers[2]!.online = true), d.servers.forEach((s) => (s.load = "low"))));
    expect(text(bar(calm).querySelector(".l"))).toBe("Все серверы работают· загрузка низкая");
    expect(bar(calm).querySelector(".l .u-dot")?.className).toBe("u-dot");
    const down = page("first", state("android"), (d) => d.servers.forEach((s) => ((s.online = false), (s.load = null))));
    expect(text(bar(down).querySelector(".l"))).toBe("Серверы не отвечают");
    expect(bar(down).querySelector(".l .u-dot")?.classList.contains("off")).toBe(true);
    expect(text(bar(view(data("first"), state("android", {}, "en"), actions())).querySelector(".l"))).toBe("2 of 3 servers are up· load high");
  });

  it("a busy server suggests the calmest other one; when every server is busy it says to wait", () => {
    expect(text(page("first").querySelector(".u-busy[role=status]"))).toBe("Сервер Германия сильно загружен. Если соединение медленное, попробуйте Нидерланды.");
    const all = page("first", state("android"), (d) => d.servers.forEach((s) => (s.load = s.online ? "high" : null)));
    expect(text(all.querySelector(".u-busy"))).toBe("Сервер Германия сильно загружен. Если соединение медленное, попробуйте позже.");
    expect(page("first", state("android"), (d) => d.servers.forEach((s) => (s.load = "low"))).querySelector(".u-busy")).toBeNull();
  });

  it("the legend says what the marks mean, only for what the servers have; a Mihomo-only link is told too", () => {
    const note = (el: HTMLElement) => [...el.querySelectorAll(".u-note > p")].map((p) => [text(p.querySelector("b")), text(p).slice(text(p.querySelector("b")).length)]);
    expect(note(page("first"))).toEqual([
      ["По ссылке и по ключу", "Каждый сервер уже есть в приложении, и для каждого можно получить ключ AmneziaVPN"],
      ["WARP", "Запасной выход, если сайт не открывается"],
    ]);
    expect(note(page("relay")).map((x) => x[0])).toEqual(["По ссылке и по ключу", "через RU", "WARP"]);
    expect(note(page("relay"))[1]![1]).toBe("Вход через узел в России");
    expect(note(page("dns-link"))[0]).toEqual(["По ссылке", "Каждый сервер уже есть в приложении, переключайтесь между ними там"]);
    const only = { way: "link", exit: "direct", app_name: "", profile_id: "", mihomo_only: true } as const;
    expect(text(page("first", state("android"), (d) => d.servers[0]!.connections.push({ ...only })).querySelector(".u-note"))).toContain("Часть подключений — только в kl!ck и других Mihomo-приложениях");
    const all = view(data("first", (d) => ((d.servers[1]!.connections = [{ ...only }]), (d.servers = [d.servers[1]!]))), state("android", {}, "en"), actions());
    expect(text(all.querySelector(".u-note"))).toContain("Only in kl!ck and other Mihomo apps");
    // the field survives reading the page data
    expect(normalize({ servers: [{ id: "a", label: "x", connections: [{ way: "link", mihomo_only: true }, { way: "link" }] }] }).servers.map((s) => s.connections.map((c) => c.mihomo_only === true))).toEqual([[true, false]]);
  });

  it("an AmneziaWG server through a relay: the key stays direct, its twin is a Mihomo-only link 'через RU'", () => {
    const el = page("awg-relay");
    expect([...byName(el, "Германия").querySelectorAll(".u-tag")].map(text)).toEqual(["через RU"]);
    expect(text(el.querySelector(".u-note"))).toContain("Часть подключений — только в kl!ck и других Mihomo-приложениях");
    const linkOnly = page("awg-relay", undefined, (d) => (d.servers[0]!.connections = d.servers[0]!.connections.filter((c) => c.way === "link")));
    expect(text(linkOnly.querySelector(".u-note"))).toContain("Часть подключений — только в kl!ck и других Mihomo-приложениях"); // the other server's link is for every app
  });

  it("an older panel: the rows come from server_loads, with no marks and no column of DNS", () => {
    const el = page("old");
    expect(rows(el).map(country)).toEqual(["Германия", "Нидерланды", "Финляндия"]);
    expect(text(rows(el)[0]!.querySelector(".u-name small"))).toBe("Франкфурт");
    expect(rows(el).map((r) => text(r.querySelector(".u-load")))).toEqual(["высокая", "средняя", "низкая"]);
    expect(el.querySelector(".u-tbl")?.classList.contains("k3")).toBe(true);
    expect(el.querySelector(".u-tag, .u-dns, .u-ic[data-k^=dns]")).toBeNull();
    expect([...el.querySelectorAll(".u-th > span")].map(text)).toEqual(["Сервер", "Загрузка", "Маршрут"]);
    expect(serversOf(data("old")).every((s) => s.id === "" && s.dns === null)).toBe(true);
  });

  it("no servers at all: one line says so", () => {
    const el = page("first", state("android"), (d) => ((d.servers = []), (d.server_loads = [])));
    expect(el.querySelector(".u-tbl")).toBeNull();
    expect(text(el.querySelector(".u-card .u-empty"))).toBe("Серверов пока нет. Они появятся здесь, когда их добавит владелец сервиса");
  });

  it("names a server by country and place, never by the panel's name; the language of the page decides the country", () => {
    const d = data("first");
    expect(serverName(d.servers[0]!, "ru")).toEqual({ country: "Германия", place: "Франкфурт" });
    expect(serverText(d.servers[0]!, "en")).toBe("Germany · Франкфурт");
    expect(serverName({ ...d.servers[0]!, country_code: "", label: "Germany · Frankfurt" }, "en")).toEqual({ country: "Germany", place: "Frankfurt" });
    expect(hasLink(d.servers[0]!) && hasKeys(d.servers[0]!)).toBe(true);
  });

  it("the head, the bar and the legend are in all five languages", () => {
    for (const lang of ["ru", "en", "zh", "fa", "tr"] as const) {
      const el = view(data("first"), state("android", {}, lang), actions());
      expect([...el.querySelectorAll(".u-th > span")].slice(0, 4).every((s) => text(s) !== "")).toBe(true);
      expect(text(el.querySelector(".u-bar .l"))).toContain("2");
      expect(el.querySelectorAll(".u-note > p")).toHaveLength(2);
    }
    expect(text(view(data("first"), state("android", {}, "fa"), actions()).querySelector(".u-th > span"))).toBe("سرور");
  });
});

describe("DNS of the keys in a card", () => {
  const rows = (el: HTMLElement) => [...el.querySelectorAll<HTMLElement>(".u-tbl .u-tr:not(.u-th)")];
  const phone = (el: HTMLElement) => [...el.querySelectorAll<HTMLElement>(".u-tbl .u-sr")];
  const nameOf = (r: HTMLElement) => text(r.querySelector(".u-name > span:nth-child(2)"));

  it("a column named by the keys, with what applies and a pencil; a server that is down has neither", () => {
    const r = rows(page("first"));
    expect(r.map((x) => text(x.querySelector(".u-dns")))).toEqual(["AdGuard — без рекламы", "Обычный", ""]);
    expect(r.map((x) => x.querySelectorAll("button").length)).toEqual([1, 1, 0]);
    const pencil = r[0]!.querySelector("button")!;
    expect(pencil.getAttribute("aria-label")).toBe("Изменить DNS: Германия · Франкфурт");
    expect(pencil.classList.contains("u-ic")).toBe(true);
    expect(pencil.getAttribute("data-k")).toBe("dns-nod_fra");
    const en = rows(view(data("first"), state("android", {}, "en"), actions()));
    expect(text(en[0]!.parentElement!.querySelector(".u-th > span:nth-child(4)"))).toBe("Key DNS");
    expect(en[0]!.querySelector("button")?.getAttribute("aria-label")).toBe("Change DNS: Germany · Франкфурт");
  });

  it("on a phone the whole row is the button; a row that cannot be changed is no button", () => {
    const p = phone(page("first"));
    expect(p.map((x) => x.tagName)).toEqual(["BUTTON", "BUTTON", "DIV"]);
    expect(p[0]!.getAttribute("data-k")).toBe("dns-m-nod_fra");
    expect(p[0]!.getAttribute("aria-label")).toBe("Германия · Франкфурт: работает, загрузка высокая. Изменить DNS: Германия · Франкфурт");
    expect(p[0]!.querySelector(".chv")).not.toBeNull();
    expect(p[2]!.querySelector(".chv")).toBeNull();
    const a = actions();
    view(data("first"), state("android"), a).querySelector<HTMLButtonElement>("[data-k=dns-m-nod_ams]")!.click();
    expect(a.log).toEqual(["dns-open nod_ams"]);
  });

  it("the column is about the keys: a person who uses only the link apps has no column and no pencil", () => {
    for (const name of ["happ", "dns-link"]) {
      const el = page(name);
      expect(el.querySelector(".u-tbl")?.classList.contains("k3")).toBe(true);
      expect(el.querySelector(".u-dns, [data-k^=dns-nod], [data-k^=dns-m-]")).toBeNull();
    }
    // a server only the link reaches has no DNS of its own for anybody
    const d = data("first");
    const linkOnly = { ...d.servers[1]!, connections: d.servers[1]!.connections.filter((x) => x.way === "link") };
    expect([showsKeyDns(d, d.servers[0]!), showsKeyDns(d, linkOnly), canChooseDns(d, d.servers[0]!), canChooseDns(d, linkOnly)]).toEqual([true, false, true, false]);
    const mixed = page("first", state("android"), (x) => (x.servers[1]!.connections = x.servers[1]!.connections.filter((k) => k.way === "link")));
    expect(rows(mixed).map((r) => text(r.querySelector(".u-dns")))).toEqual(["AdGuard — без рекламы", "общий, без выбора", ""]);
    // a person who has a key but no longer the app still sees the DNS of their keys
    const kept = data("first", (x) => ((x.access.amnezia = false), x.amnezia!.devices.push({ ...cases.return!.amnezia!.devices[0]! })));
    expect(usesKeys(kept) && showsKeyDns(kept, kept.servers[0]!)).toBe(true);
  });

  it("the owner turned the choice off, the page is the admin's preview, or the server has one option: the row says what applies, with no pencil", () => {
    for (const el of [page("dns-off"), page("first", state("android"), (d) => (d.dns!.endpoint = "")), page("first", state("android"), (d) => (d.servers[0]!.dns!.options = ["dns_adblock"]))]) {
      expect(text(rows(el)[0]!.querySelector(".u-dns"))).toContain("AdGuard");
      expect(rows(el)[0]!.querySelector("button")).toBeNull();
      expect(phone(el)[0]!.tagName).toBe("DIV");
    }
    expect(rows(page("dns-off")).every((x) => x.querySelector("[data-k^=dns-nod]") === null)).toBe(true);
  });

  it("a read-only row for a single option names it", () => {
    const moscow = rows(page("variants")).find((x) => nameOf(x) === "Россия")!;
    expect(text(moscow.querySelector(".u-dns"))).toBe("Яндекс");
    expect(moscow.querySelector("button")).toBeNull();
  });

  it("a preset the page does not know, or no DNS part at all, says the DNS is shared", () => {
    expect(rows(page("first", state("android"), (d) => (d.dns_presets = []))).map((x) => text(x.querySelector(".u-dns")))).toEqual(["общий, без выбора", "общий, без выбора", ""]);
    expect(page("first", state("android"), (d) => (d.dns = null)).querySelector("[data-k^=dns-]")).toBeNull();
  });

  it("the pencil opens the window of that server", () => {
    const a = actions();
    view(data("first"), state("android"), a).querySelector<HTMLButtonElement>("[data-k=dns-nod_ams]")!.click();
    expect(a.log).toEqual(["dns-open nod_ams"]);
  });

  it("what happens while and after a choice is the window's: the row shows no 'one moment', no error and no 'saved'", () => {
    const st = state("android", { dns: { ...newDnsState(), busy: "nod_ams", done: { nod_fra: true }, error: { server: "nod_ams", code: "network", retryMin: 0, preset: "x" } } });
    const el = view(data("first"), st, actions());
    expect(el.querySelector(".busy, .note, .dns-err, .dns-keys, .dns-note")).toBeNull();
    expect(rows(el)[1]!.querySelector("button")).not.toBeNull(); // the pencil stays: the window says what is going on
    for (const [code, words] of [["not_allowed", "Этот вариант DNS больше недоступен — выберите другой."], ["dns_disabled", "Выбор DNS выключен администратором."], ["internal", "Не получилось. Попробуйте ещё раз."], ["too_large", "Не получилось. Попробуйте ещё раз."]] as const) {
      expect(plain(t.dnsErr(code, 0))).toBe(words);
    }
  });

  it("keys that hold the old DNS are marked on the rows of the devices, with a button for the new key", () => {
    const a = actions({ amz: { ...actions().amz, stale: (id) => void a.log.push(`stale ${id}`) } });
    const el = view(data("stale-dns"), state("android", { returning: true, tab: "devices" }), a);
    const row = el.querySelector<HTMLElement>("#amz-row-d3")!;
    expect(text(row.querySelector(".u-tag.work"))).toBe("нужен новый ключ");
    expect(row.querySelector(".u-tag.work")?.getAttribute("title")).toBe("DNS сервера изменился — получите ключ заново. Сам ключ останется прежним.".replace(" —", " —"));
    row.querySelector<HTMLButtonElement>("[data-k=amz-stale-d3]")!.click();
    expect(a.log).toEqual(["stale d3"]);
  });

  it("every focus key on the page is its own: a computer's pencils and a phone's rows differ", () => {
    // after a render the page focuses the first element with the key the focused one had, so a shared key moves the focus
    const keys = [...page("dns-both").querySelectorAll("[data-k]")].map((x) => x.getAttribute("data-k")!).filter((k) => !k.startsWith("hero-"));
    expect(keys.filter((k, i) => keys.indexOf(k) !== i)).toEqual([]);
    expect(keys).toContain("dns-link");
    expect(keys).toContain("dns-link-m");
  });
});

describe("the DNS picker", () => {
  const ctx = (name: string, pick: string, open = "nod_fra", f: (d: ReturnType<typeof data>) => void = () => {}, here = "android") => {
    const d = data(name, f);
    const st = state(here as "android", { dns: { ...newDnsState(), open, pick } });
    const a = actions();
    const c: Ctx = { d, s: st, a, t, support: "" };
    const box = document.createElement("div");
    box.append(...(dnsModal(c) as Node[]));
    return { box, a };
  };

  it("the server's default first (with its name), then the others; 'now' marks what applies; the picked one is checked", () => {
    const { box } = ctx("first", "dns_family");
    expect(text(box.querySelector("h3"))).toBe("DNS ключей на сервере");
    expect(text(box.querySelector(".sh-head .sub"))).toContain("Германия · Франкфурт");
    expect(text(box.querySelector("#dd-d"))).toBe("Только для ключей AmneziaVPN на этом сервере. На приложения по ссылке не влияет.");
    const opts = [...box.querySelectorAll(".opt")];
    expect(opts.map((o) => text(o.querySelector(".opt-t")))).toEqual(["По умолчанию — AdGuard", "Обычный", "Семейный"]);
    expect(opts.map((o) => text(o.querySelector(".opt-d")))).toEqual(["Блокирует рекламу и трекеры", "Cloudflare и Google, без фильтров", "Без рекламы и сайтов для взрослых"]);
    expect(opts.map((o) => o.getAttribute("aria-checked"))).toEqual(["false", "false", "true"]);
    expect(text(opts[0]!.querySelector(".chip"))).toBe("сейчас");
    expect(box.querySelector(".opt")?.parentElement?.getAttribute("role")).toBe("radiogroup");
  });

  it("choosing, cancelling and applying go to the page", () => {
    const { box, a } = ctx("first", "");
    box.querySelector<HTMLButtonElement>("[data-k=dns-opt-dns_standard]")!.click();
    box.querySelector<HTMLButtonElement>("[data-k=dns-opt-default]")!.click();
    box.querySelector<HTMLButtonElement>("[data-k=dns-apply]")!.click();
    box.querySelector<HTMLButtonElement>("[data-k=dns-cancel]")!.click();
    box.querySelector<HTMLButtonElement>("[data-k=modal-x]")!.click();
    expect(a.log).toEqual(["dns-pick dns_standard", "dns-pick ", "dns-apply", "dns-open null", "dns-open null"]);
    expect(text(box.querySelector(".btnrow"))).toBe("ОтменаПрименить");
  });

  it("says that keys hold the DNS inside; names the devices", () => {
    const { box } = ctx("return", "");
    expect(text(box.querySelector(".note"))).toBe("У ключей «Pixel 7», «MacBook» DNS записан внутри — после смены получите ключи заново.");
    const none = ctx("first", "");
    expect(none.box.querySelector(".note")).toBeNull(); // no key devices yet
  });

  it("a server that is not there, or no picker open: nothing", () => {
    expect(ctx("first", "", "nod_nope").box.childElementCount).toBe(0);
    expect(ctx("first", "", "").box.childElementCount).toBe(0);
  });

  it("the default's name is the one the server says, else what applies when nothing was chosen, else the first allowed", () => {
    const d = data("first");
    expect(dnsDefault({ choice: "", effective: "b", options: ["a", "b"], default: "", keys_to_refresh: [] })).toBe("b");
    expect(dnsDefault({ choice: "a", effective: "a", options: ["z", "a"], default: "", keys_to_refresh: [] })).toBe("z");
    expect(dnsDefault({ choice: "a", effective: "a", options: ["z", "a"], default: "a", keys_to_refresh: [] })).toBe("a");
    expect(dnsChoices(d, d.servers[0]!, "ru").map((o) => [o.id, o.isDefault])).toEqual([["dns_adblock", true], ["dns_standard", false], ["dns_family", false]]);
    // a preset the page has no name for is not offered
    d.servers[0]!.dns!.options.push("dns_unknown");
    expect(dnsChoices(d, d.servers[0]!, "ru")).toHaveLength(3);
  });

  it("a built-in description holds both languages in one field: the page language picks one", () => {
    expect(describePreset("Блокирует рекламу\nBlocks ads", "ru")).toBe("Блокирует рекламу");
    expect(describePreset("Блокирует рекламу\nBlocks ads", "en")).toBe("Blocks ads");
    expect(describePreset(" Only one ", "en")).toBe("Only one");
  });
});

describe("the DNS actions", () => {
  const answer = (over: Partial<DnsAnswer> = {}): DnsAnswer => ({ server: undefined, stale_devices: [], ...over });
  function setup(api: Partial<DnsApi> = {}, name = "return", f: (d: ReturnType<typeof data>) => void = () => {}) {
    const d = data(name, f);
    const st = { dns: newDnsState() };
    const calls: string[] = [];
    const log: string[] = [];
    let locked = 0;
    const a = dnsActions({
      data: d,
      st,
      render: () => log.push("render"),
      api: {
        setDns: async (e, b) => {
          calls.push(`${e} ${"server" in b ? b.server : "link"} "${b.preset}"`);
          return answer();
        },
        ...api,
      },
      focus: (k) => log.push(`focus ${k}`),
      locked: () => locked++,
    });
    return { d, st, a, calls, log, locked: () => locked };
  }

  it("opening starts on what the person has chosen; closing gives the keyboard back to 'Change'", () => {
    const { st, a, log } = setup({}, "return", (d) => (d.servers[0]!.dns!.choice = "dns_family"));
    a.open("nod_fra");
    expect(st.dns).toMatchObject({ open: "nod_fra", pick: "dns_family" });
    a.pick("");
    expect(st.dns.pick).toBe("");
    a.open(null);
    expect(st.dns.open).toBeNull();
    expect(log.at(-1)).toBe("focus dns-nod_fra");
  });

  it("applying posts the choice, the picker closes at once, the server and the keys on it are updated, the card says what happens next", async () => {
    const { d, st, a, calls } = setup({
      setDns: async (_e, b) => {
        calls.push(`post ${"server" in b ? b.server : "link"} "${b.preset}"`);
        const srv = d.servers[0]!;
        return answer({ server: { ...srv, dns: { ...srv.dns!, choice: b.preset, effective: b.preset } }, stale_devices: ["d3"] });
      },
    });
    a.open("nod_fra");
    a.pick("dns_family");
    a.apply();
    expect(st.dns).toMatchObject({ busy: "nod_fra", open: null });
    await settle();
    expect(calls).toEqual(['post nod_fra "dns_family"']);
    expect(d.servers[0]!.dns).toMatchObject({ choice: "dns_family", effective: "dns_family", keys_to_refresh: ["d3"] });
    expect(d.amnezia!.devices.find((x) => x.id === "d3")).toMatchObject({ stale: true, stale_reason: "dns" });
    expect(d.amnezia!.devices.find((x) => x.id === "d4")?.stale).toBe(false);
    expect(st.dns).toMatchObject({ busy: "", done: { nod_fra: true }, error: null });
  });

  it("the default is posted as an empty preset; the same choice posts nothing", async () => {
    const { st, a, calls } = setup({}, "return", (d) => (d.servers[0]!.dns!.choice = "dns_family"));
    a.open("nod_fra");
    a.pick("");
    a.apply();
    await settle();
    expect(calls).toEqual(['dev: nod_fra ""']);
    a.open("nod_fra");
    a.pick("");
    a.apply(); // unchanged now?
    await settle();
    expect(st.dns.open).toBeNull();
  });

  it("a choice that changes nothing just closes", async () => {
    const { st, a, calls } = setup();
    a.open("nod_fra");
    a.apply();
    await settle();
    expect(calls).toEqual([]);
    expect(st.dns.open).toBeNull();
  });

  it("without the server's answer the page keeps the choice it posted", async () => {
    const { d, a } = setup();
    a.open("nod_ams");
    a.pick("dns_family");
    a.apply();
    await settle();
    expect(d.servers[1]!.dns?.choice).toBe("dns_family");
  });

  it("an error says where and why, keeps what was chosen for 'Retry', and sends nothing twice", async () => {
    let n = 0;
    const { st, a, calls } = setup({
      setDns: async () => {
        n++;
        throw new ApiError(429, "too_many_requests", 720);
      },
    });
    a.open("nod_ams");
    a.pick("dns_family");
    a.apply();
    await settle();
    expect(st.dns.error).toEqual({ server: "nod_ams", code: "too_many_requests", retryMin: 12, preset: "dns_family" });
    expect(st.dns.busy).toBe("");
    expect(n).toBe(1);
    a.retry();
    await settle();
    expect(n).toBe(2);
    expect(calls).toEqual([]);
    a.open("nod_ams"); // opening the picker again clears the error
    expect(st.dns.error).toBeNull();
  });

  it("a lost connection and an expired password", async () => {
    const net = setup({ setDns: async () => Promise.reject(new ApiError(0, "network")) });
    net.a.open("nod_ams");
    net.a.pick("dns_family");
    net.a.apply();
    await settle();
    expect(net.st.dns.error?.code).toBe("network");
    const lock = setup({ setDns: async () => Promise.reject(new ApiError(401, "locked")) });
    lock.a.open("nod_ams");
    lock.a.pick("dns_family");
    lock.a.apply();
    await settle();
    expect([lock.locked(), lock.st.dns.error]).toEqual([1, null]);
    const odd = setup({ setDns: async () => Promise.reject(new Error("boom")) });
    odd.a.open("nod_ams");
    odd.a.pick("dns_family");
    odd.a.apply();
    await settle();
    expect(odd.st.dns.error?.code).toBe("failed");
  });

  it("nothing is sent without an address (the admin's preview, the choice off)", async () => {
    const { a, calls, st } = setup({}, "return", (d) => (d.dns!.endpoint = ""));
    a.open("nod_ams");
    a.pick("dns_family");
    a.apply();
    await settle();
    expect(calls).toEqual([]);
    expect(st.dns.busy).toBe("");
  });

  it("the old data: a page with no DNS part has nothing to open", () => {
    expect(normalize(cases.old).dns).toBeNull();
    const { a, calls } = setup({}, "old");
    a.open("");
    a.apply();
    expect(calls).toEqual([]);
  });
});

describe("the DNS of the link apps", () => {
  const right = (el: HTMLElement) => el.querySelector<HTMLElement>(".u-bar .r");

  it("the right side of the bar, named by the apps it reaches: what applies and 'Change for all'; a phone says it shorter", () => {
    const el = page("dns-both");
    const r = right(el)!;
    expect(text(r.querySelector("span.only-w"))).toBe("DNS приложений по ссылке: Cloudflare + Google");
    expect(text(r.querySelector("span.only-m"))).toBe("DNS по ссылке: Cloudflare + Google");
    expect(r.querySelector("span.only-w b")?.textContent).toBe("Cloudflare + Google");
    const desk = r.querySelector<HTMLButtonElement>("[data-k=dns-link]")!;
    const small = r.querySelector<HTMLButtonElement>("[data-k=dns-link-m]")!;
    expect([text(desk), desk.classList.contains("only-w"), desk.getAttribute("aria-label")]).toEqual(["Изменить для всех", true, "Изменить: DNS в Happ и других приложениях по ссылке"]);
    expect([text(small), small.classList.contains("only-m"), small.classList.contains("quiet")]).toEqual(["Изменить", true, true]);
    // on a computer the app the page puts first there names it
    expect(right(view(data("dns-both"), state("windows", { returning: true }), actions()))!.querySelector("button")?.getAttribute("aria-label")).toBe("Изменить: DNS в kl!ck и других приложениях по ссылке");
    expect(text(right(view(data("dns-both"), state("android", { returning: true }, "en"), actions()))!.querySelector("span.only-w"))).toBe("DNS of the link apps: Cloudflare + Google");
  });

  it("a person with both kinds has the choice for the link apps in the bar and the keys' in the rows; one with the link apps only has no rows", () => {
    const both = page("dns-both");
    expect(both.querySelectorAll(".u-dns")).toHaveLength(2);
    expect(right(both)).not.toBeNull();
    const link = page("dns-link");
    expect(right(link)).not.toBeNull();
    expect(link.querySelector(".u-dns")).toBeNull();
  });

  it("while the owner offers no choice for the link apps, the bar says what they get, without a button", () => {
    const off = page("dns-both-off");
    expect(text(right(off)!.querySelector("span.only-w"))).toContain("DNS приложений по ссылке: ");
    expect(right(off)!.querySelector("button")).toBeNull();
    // the owner's preview: what applies, nothing to change
    const preview = right(page("dns-both", state("android", { returning: true }), (d) => (d.dns!.endpoint = "")))!;
    expect(text(preview.querySelector("span.only-w"))).toBe("DNS приложений по ссылке: Cloudflare + Google");
    expect(preview.querySelector("button")).toBeNull();
  });

  it("no part for the link apps for a person without them, with no DNS part, or with a preset the page does not know", () => {
    for (const f of [(d: ReturnType<typeof data>) => (d.access.happ = false), (d: ReturnType<typeof data>) => (d.dns = null), (d: ReturnType<typeof data>) => (d.dns_presets = [])]) {
      expect(right(page("dns-both", state("android", { returning: true }), f))).toBeNull();
    }
    // the owner switched the choice off and the link apps have nothing of their own to say: the keys' rows stay, the bar is quiet
    expect(right(page("dns-both", state("android", { returning: true }), (d) => (d.dns!.link.options = ["dns_builtin_standard"])))?.querySelector("button")).toBeNull();
    expect(canChooseLinkDns(data("dns-both"))).toBe(true);
  });

  it("'Change for all' opens the window: the default first with its name, the offered ones, 'now', and when the apps get it", () => {
    const a = actions();
    const el = view(data("dns-both"), state("android", { returning: true }), a);
    el.querySelector<HTMLButtonElement>("[data-k=dns-link]")!.click();
    el.querySelector<HTMLButtonElement>("[data-k=dns-link-m]")!.click();
    expect(a.log).toEqual([`dns-open ${LINK}`, `dns-open ${LINK}`]);
    const d = data("dns-both", (x) => ((x.dns!.link.choice = "dns_builtin_adblock"), (x.dns!.link.effective = "dns_builtin_adblock")));
    const c: Ctx = { d, s: state("android", { dns: { ...newDnsState(), open: LINK, pick: "dns_builtin_adblock" } }), a, t, support: "" };
    const box = document.createElement("div");
    box.append(...(dnsModal(c) as Node[]));
    expect(text(box.querySelector("h3"))).toBe("DNS в Happ и других приложениях по ссылке");
    expect(text(box.querySelector(".sh-head .sub"))).toBe("Один на все серверы");
    expect(text(box.querySelector("#dd-d"))).toBe("Приложения получат новый DNS при обновлении подписки (до 12 ч).");
    const opts = [...box.querySelectorAll(".opt")];
    expect(opts.map((o) => text(o.querySelector(".opt-t")))).toEqual(["По умолчанию — Cloudflare + Google", "AdGuard: без рекламы", "AdGuard Family: без рекламы и 18+", "Россия: .ru напрямую"]);
    expect(opts.map((o) => o.getAttribute("aria-checked"))).toEqual(["false", "true", "false", "false"]);
    expect(text(opts[1]!.querySelector(".chip"))).toBe("сейчас");
    expect(box.querySelector("[role=radiogroup]")?.getAttribute("aria-describedby")).toBe("dd-d");
    expect(box.querySelector(".note")).toBeNull(); // no key is involved
    expect(linkChoices(d, "ru").map((o) => [o.id, o.isDefault])[0]).toEqual(["dns_builtin_standard", true]);
  });
});

describe("saving the DNS of the link apps", () => {
  function setup(answer?: Partial<DnsAnswer>, name = "dns-both") {
    const d = data(name);
    const st = { dns: newDnsState() };
    const calls: string[] = [];
    const log: string[] = [];
    const a = dnsActions({
      data: d,
      st,
      render: () => log.push("render"),
      api: {
        setDns: async (e, b) => {
          calls.push(`${e} ${JSON.stringify(b)}`);
          return { server: undefined, stale_devices: [], ...answer };
        },
      },
      focus: (k) => log.push(`focus ${k}`),
    });
    return { d, st, a, calls, log };
  }

  it("posts the pick for the link apps, takes the link's DNS from the answer, marks no key, and gives the keyboard back to 'Change'", async () => {
    const { d, st, a, calls, log } = setup({ link: { effective: "dns_builtin_adblock", choice: "dns_builtin_adblock", default: "dns_builtin_standard", options: ["dns_builtin_standard", "dns_builtin_adblock"] } });
    a.open(LINK);
    expect(st.dns).toMatchObject({ open: LINK, pick: "" });
    a.pick("dns_builtin_adblock");
    a.apply();
    expect(st.dns).toMatchObject({ busy: LINK, open: null });
    await settle();
    expect(calls).toEqual(['dev: {"link":true,"preset":"dns_builtin_adblock"}']);
    expect(d.dns!.link).toEqual({ effective: "dns_builtin_adblock", choice: "dns_builtin_adblock", default: "dns_builtin_standard", options: ["dns_builtin_standard", "dns_builtin_adblock"] });
    expect(d.amnezia!.devices.every((x) => !x.stale)).toBe(true);
    expect(st.dns).toMatchObject({ busy: "", done: { [LINK]: true }, error: null });
    expect(log.at(-1)).toBe("focus dns-link");
  });

  it("without the server's answer the page keeps what it posted; back to the default; the same pick posts nothing", async () => {
    const { d, a, calls } = setup();
    a.open(LINK);
    a.pick("dns_builtin_family");
    a.apply();
    await settle();
    expect(d.dns!.link).toMatchObject({ choice: "dns_builtin_family", effective: "dns_builtin_family" });
    a.open(LINK);
    a.pick("");
    a.apply();
    await settle();
    expect(d.dns!.link).toMatchObject({ choice: "", effective: "dns_builtin_standard" });
    a.open(LINK);
    a.apply();
    await settle();
    expect(calls).toHaveLength(2);
  });
});

describe("preset names follow the page's language, not the browser's", () => {
  // the server words the presets in the language of the browser (Accept-Language: en) and sends the stock words of the built-ins
  // in every language; the visitor switched the page to Russian
  const english = (name = "dns-both") =>
    data(name, (d) => {
      d.dns_presets = d.dns_presets.map((p) => ({ ...p, name: p.names?.en ?? p.name, description: p.descriptions?.en ?? p.description }));
      d.dns!.link.effective = d.dns!.link.choice = "dns_builtin_adblock";
      for (const s of d.servers) s.dns!.effective = "dns_builtin_family";
    });

  it("a Russian page in an English browser shows the Russian names, in the card, the rows and the pickers", () => {
    const el = view(english(), state("android", { returning: true }), actions());
    expect(text(el.querySelector(".dns-link .dns-link-v"))).toBe("AdGuard: без рекламыAdGuard DNS: без рекламы и трекеров");
    expect(cards(el).map((x) => text(x.querySelector(".dns .v")))).toEqual(["AdGuard Family: без рекламы и 18+", "AdGuard Family: без рекламы и 18+"]);
    for (const open of [LINK, "de1"]) {
      const c: Ctx = { d: english(), s: state("android", { dns: { ...newDnsState(), open } }), a: actions(), t, support: "" };
      const box = document.createElement("div");
      box.append(...(dnsModal(c) as Node[]));
      const words = [...box.querySelectorAll(".opt")].map((o) => text(o));
      expect(words.join(" ")).toContain("AdGuard: без рекламы");
      expect(words.join(" ")).not.toMatch(/no ads|filtering|for everything/);
    }
  });

  it("the same data on an English and a Chinese page; an owner's own text stays as it is in every language", () => {
    const en = view(english(), state("android", { returning: true }, "en"), actions());
    expect(text(en.querySelector(".dns-link .dns-link-v"))).toBe("AdGuard: no adsAdGuard DNS: no ads, no trackers");
    const zh = view(english(), state("android", { returning: true }, "zh"), actions());
    expect(text(zh.querySelector(".dns-link .dns-link-v"))).toBe("AdGuard：无广告AdGuard DNS：无广告，无跟踪器");
    const own = { id: "x", name: "Home DNS", description: "Router at home", category: "" };
    expect([presetName(own, "ru"), presetDescription(own, "zh")]).toEqual(["Home DNS", "Router at home"]);
  });

  it("the page data: words for the page's languages only, empty ones left out", () => {
    const d = normalize({ dns_presets: [{ id: "a", name: "A", description: "d", names: { ru: "А", en: "A", de: "x", zh: 5 }, descriptions: { ru: " " } }] });
    expect(d.dns_presets[0]).toEqual({ id: "a", name: "A", description: "d", category: "", names: { ru: "А", en: "A" } });
  });
});
