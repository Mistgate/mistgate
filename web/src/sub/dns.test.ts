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
const page = (name: string, st = state("android", { returning: true }), f: (d: ReturnType<typeof data>) => void = () => {}) => view(data(name, f), st, actions());
const cards = (el: HTMLElement) => [...el.querySelectorAll<HTMLElement>(".srv")];

describe("the servers", () => {
  it("one card each, the working ones first: country and place, working, load, ways, the names in the apps", () => {
    const d = data("first");
    d.servers = [d.servers[2]!, d.servers[0]!, d.servers[1]!]; // the one that is down listed first
    const el = view(d, state("android"), actions());
    const c = cards(el);
    expect(c.map((x) => text(x.querySelector(".srv-name")))).toEqual(["Германия · Франкфурт", "Нидерланды", "Финляндия"]);
    expect(c.map((x) => text(x.querySelector(".state")))).toEqual(["работает", "работает", "не отвечает"]);
    expect(c[2]!.classList.contains("off")).toBe(true);
    const de = c[0]!;
    expect(text(de.querySelector(".load"))).toBe("Загрузкавысокая");
    expect(de.querySelectorAll(".meter i.f")).toHaveLength(3);
    expect(de.querySelector(".meter")?.classList.contains("hi")).toBe(true);
    expect([...de.querySelectorAll(".chips .chip")].map(text)).toEqual(["по ссылке", "ключ", "WARP"]);
    expect(de.querySelector(".chip.warp")?.getAttribute("title")).toBe("Тот же сервер, но выход в интернет через Cloudflare");
    expect(de.querySelector(".chips + .hint")).toBeNull();
    expect([...de.querySelectorAll(".srv-app .mono")].map(text)).toEqual(["DE · Hysteria2", "DE · Hysteria2 WARP"]);
    expect(c[1]!.querySelectorAll(".meter i.f")).toHaveLength(2);
    expect(text(c[2]!.querySelector("p.sm"))).toBe("Выберите в приложении другой сервер");
    expect(c[2]!.querySelector(".load, .chips, .dns")).toBeNull();
  });

  it("a server whose level is unknown has no bar and no word, only 'working'", () => {
    const d = data("variants");
    const el = view(d, state("android", { returning: true }), actions());
    const berlin = cards(el).find((x) => text(x.querySelector(".srv-name")).startsWith("Германия · Берлин"))!;
    expect(berlin.querySelector(".load, .meter")).toBeNull();
    expect(text(berlin.querySelector(".state"))).toBe("работает");
    const moscow = cards(el).find((x) => text(x.querySelector(".srv-name")).startsWith("Россия"))!;
    expect([...moscow.querySelectorAll(".chips .chip")].map(text)).toEqual(["ключ"]);
    expect(moscow.querySelector(".srv-app")).toBeNull();
  });

  it("the flag is an emoji; where the system draws no flags (Windows) a chip with the country code", () => {
    const phone = cards(page("first"))[0]!;
    expect(text(phone.querySelector(".flag"))).toBe("🇩🇪");
    expect(phone.querySelector(".flag")?.getAttribute("aria-label")).toBe("Флаг: Германия");
    const win = cards(view(data("first"), state("windows"), actions()))[0]!;
    expect(text(win.querySelector(".cc"))).toBe("DE");
    expect(win.querySelector(".flag")).toBeNull();
  });

  it("a link only Mihomo apps can use says so on the card (all of them, or some), in both languages", () => {
    const only = { way: "link", exit: "direct", app_name: "", profile_id: "", mihomo_only: true } as const;
    const f = (d: ReturnType<typeof data>) => {
      d.servers[1]!.connections = [{ ...only }]; // nl: nothing but Mihomo
      d.servers[1]!.app_names = [];
      d.servers[0]!.connections.push({ ...only }); // de: its own link, and one for Mihomo apps
    };
    const byName = (el: HTMLElement, n: string) => cards(el).find((x) => text(x.querySelector(".srv-name")).startsWith(n))!;
    const ru = page("first", state("android"), f);
    expect(text(byName(ru, "Нидерланды").querySelector(".srv-mihomo"))).toBe("Только в kl!ck и других Mihomo-приложениях");
    expect(text(byName(ru, "Германия").querySelector(".srv-mihomo"))).toBe("Часть подключений — только в kl!ck и других Mihomo-приложениях");
    expect(byName(ru, "Финляндия").querySelector(".srv-mihomo")).toBeNull();
    const en = view(data("first", f), state("android", {}, "en"), actions());
    expect(text(byName(en, "Netherlands").querySelector(".srv-mihomo"))).toBe("Only in kl!ck and other Mihomo apps");
    expect(text(byName(en, "Germany").querySelector(".srv-mihomo"))).toBe("Some connections are only in kl!ck and other Mihomo apps");
    // the field survives reading the page data
    expect(normalize({ servers: [{ id: "a", label: "x", connections: [{ way: "link", mihomo_only: true }, { way: "link" }] }] }).servers.map((s) => s.connections.map((c) => c.mihomo_only === true))).toEqual([[true, false]]);
  });

  it("a busy server suggests the calmest other one; when every server is busy it says to wait", () => {
    expect(text(page("first").querySelector(".note[role=status]"))).toBe("Сервер Германия сильно загружен. Если соединение медленное, попробуйте Нидерланды.");
    const all = page("first", state("android"), (d) => d.servers.forEach((s) => (s.load = s.online ? "high" : null)));
    expect(text(all.querySelector(".note.warn"))).toBe("Сервер Германия сильно загружен. Если соединение медленное, попробуйте позже.");
    expect(page("first", state("android"), (d) => d.servers.forEach((s) => (s.load = "low"))).querySelector(".note[role=status]")).toBeNull();
  });

  it("an older panel: the cards come from server_loads, with no ways, no names and no DNS part", () => {
    const el = page("old");
    expect(cards(el).map((x) => text(x.querySelector(".srv-name")))).toEqual(["Германия · Франкфурт", "Нидерланды", "Финляндия"]);
    expect(cards(el).map((x) => text(x.querySelector(".load b")))).toEqual(["высокая", "средняя", "низкая"]);
    expect(el.querySelector(".chips, .srv-app, .dns, .dns-keys")).toBeNull();
    expect(serversOf(data("old")).every((s) => s.id === "" && s.dns === null)).toBe(true);
    expect(text(el.querySelector(".sec-sub"))).toBe("Все ваши серверы уже есть в приложении — переключайтесь между ними там");
  });

  it("no servers at all: no section", () => {
    expect(page("first", state("android"), (d) => ((d.servers = []), (d.server_loads = []))).querySelector(".srv-grid")).toBeNull();
  });

  it("names a server by country and place, never by the panel's name; the language of the page decides the country", () => {
    const d = data("first");
    expect(serverName(d.servers[0]!, "ru")).toEqual({ country: "Германия", place: "Франкфурт" });
    expect(serverText(d.servers[0]!, "en")).toBe("Germany · Франкфурт");
    expect(serverName({ ...d.servers[0]!, country_code: "", label: "Germany · Frankfurt" }, "en")).toEqual({ country: "Germany", place: "Frankfurt" });
    expect(hasLink(d.servers[0]!) && hasKeys(d.servers[0]!)).toBe(true);
  });
});

describe("DNS of the keys in a card", () => {
  it("a row named by the keys, with what applies and 'Change'; a server that is down has none", () => {
    const c = cards(page("first"));
    expect(c.map((x) => text(x.querySelector(".dns .v")))).toEqual(["AdGuard — без рекламы", "Обычный", ""]);
    expect(c.map((x) => text(x.querySelector(".dns .k")))).toEqual(["DNS ключей", "DNS ключей", ""]);
    expect(c.map((x) => text(x.querySelector(".dns .tlink")))).toEqual(["Изменить", "Изменить", ""]);
    expect(c[0]!.querySelector(".dns .tlink")?.getAttribute("aria-label")).toBe("Изменить: DNS ключей Германия · Франкфурт");
    const en = cards(view(data("first"), state("android", {}, "en"), actions()));
    expect(text(en[0]!.querySelector(".dns .k"))).toBe("Key DNS");
  });

  it("while the owner offers no choice for the link apps, one remark above the list says they share one DNS", () => {
    expect(text(page("first").querySelector(".srv-grid")!.previousElementSibling)).toBe("В приложениях по ссылке DNS пока один на все серверы, AdGuard, без рекламы. Выбор ниже действует для ключей AmneziaVPN.".replace("серверы,", "серверы —"));
    // with a choice for the link apps the card stands there instead
    expect(page("dns-both").querySelector(".srv-grid")!.previousElementSibling?.classList.contains("dns-link")).toBe(true);
    expect(page("dns-both").querySelector(".note:not(.ok):not(.bad)")).toBeNull();
  });

  it("the rows are about the keys: a person who uses only the link apps has neither the rows nor the remark", () => {
    for (const name of ["happ", "dns-link"]) {
      const el = page(name);
      expect(cards(el).some((x) => x.querySelector(".dns"))).toBe(false);
      expect(text(el.querySelector(".srv-grid")!.previousElementSibling)).not.toContain("Выбор ниже действует для ключей");
    }
    // a server only the link reaches has no row either, for anybody
    const d = data("first");
    const linkOnly = { ...d.servers[1]!, connections: d.servers[1]!.connections.filter((x) => x.way === "link") };
    expect([showsKeyDns(d, d.servers[0]!), showsKeyDns(d, linkOnly), canChooseDns(d, d.servers[0]!), canChooseDns(d, linkOnly)]).toEqual([true, false, true, false]);
    // a person who has a key but no longer the app still sees the DNS of their keys
    const kept = data("first", (x) => ((x.access.amnezia = false), x.amnezia!.devices.push({ ...cases.return!.amnezia!.devices[0]! })));
    expect(usesKeys(kept) && showsKeyDns(kept, kept.servers[0]!)).toBe(true);
  });

  it("the owner turned the choice off, the page is the admin's preview, or the server has one option: the row says what applies, without 'Change'", () => {
    for (const el of [page("dns-off"), page("first", state("android"), (d) => (d.dns!.endpoint = "")), page("first", state("android"), (d) => (d.servers[0]!.dns!.options = ["dns_adblock"]))]) {
      const row = cards(el)[0]!.querySelector(".dns")!;
      expect(text(row.querySelector(".v"))).toContain("AdGuard");
      expect(row.querySelector(".tlink")).toBeNull();
    }
    expect(cards(page("dns-off")).every((x) => x.querySelector("[data-k^=dns-nod]") === null)).toBe(true);
  });

  it("a read-only row for a single option names it with its description", () => {
    const el = page("variants");
    const moscow = cards(el).find((x) => text(x.querySelector(".srv-name")).startsWith("Россия"))!;
    expect(text(moscow.querySelector(".dns .v"))).toBe("Яндексроссийские сайты открываются надёжнее");
    expect(moscow.querySelector(".dns .tlink")).toBeNull();
  });

  it("a server without a DNS part, or a preset the page does not know, has no row", () => {
    expect(cards(page("first", state("android"), (d) => (d.dns_presets = []))).every((x) => !x.querySelector(".dns"))).toBe(true);
    expect(cards(page("first", state("android"), (d) => (d.dns = null))).some((x) => x.querySelector(".tlink"))).toBe(false);
  });

  it("'Change' opens the picker for that server", () => {
    const a = actions();
    const el = view(data("first"), state("android"), a);
    el.querySelector<HTMLButtonElement>("[data-k=dns-nod_ams]")!.click();
    expect(a.log).toEqual(["dns-open nod_ams"]);
  });

  it("saving: the row says 'One moment'", () => {
    const st = state("android", { dns: { ...newDnsState(), busy: "nod_ams" } });
    const row = cards(view(data("first"), st, actions()))[1]!.querySelector(".dns")!;
    expect(text(row.querySelector(".busy"))).toBe("Секунду…");
    expect(row.querySelector(".tlink")).toBeNull();
  });

  it("an error stays under the row, with its words; a lost connection offers 'Retry'", () => {
    const a = actions();
    const rate = view(data("first"), state("android", { dns: { ...newDnsState(), error: { server: "nod_ams", code: "too_many_requests", retryMin: 12, preset: "x" } } }), a);
    expect(text(cards(rate)[1]!.querySelector(".dns-err"))).toBe("Слишком много изменений подряд. Попробуйте через 12 мин.");
    expect(cards(rate)[1]!.querySelector(".dns-err .tlink")).toBeNull();
    const net = view(data("first"), state("android", { dns: { ...newDnsState(), error: { server: "nod_ams", code: "network", retryMin: 0, preset: "x" } } }), a);
    net.querySelector<HTMLButtonElement>("[data-k=dns-retry-nod_ams]")!.click();
    expect(a.log).toEqual(["dns-retry"]);
    for (const [code, words] of [["not_allowed", "Этот вариант DNS больше недоступен — выберите другой."], ["dns_disabled", "Выбор DNS выключен администратором."], ["internal", "Не получилось. Попробуйте ещё раз."], ["too_large", "Не получилось. Попробуйте ещё раз."]] as const) {
      expect(plain(t.dnsErr(code, 0))).toBe(words);
    }
  });

  it("after a choice: the DNS is saved for the keys of the server", () => {
    const st = state("android", { dns: { ...newDnsState(), done: { nod_ams: true } } });
    const note = cards(view(data("first"), st, actions()))[1]!.querySelector(".note.ok")!;
    expect(note.getAttribute("role")).toBe("status");
    expect(text(note)).toBe("Готово. DNS сохранён — его получат новые ключи этого сервера.");
  });

  it("keys that hold the old DNS: a block with a button per device", () => {
    const a = actions({ amz: { ...actions().amz, renew: (id) => void a.log.push(`renew ${id}`) } });
    const d = data("stale-dns");
    const st = state("android", { returning: true, dns: { ...newDnsState(), done: { nod_fra: true } } });
    const el = view(d, st, a);
    const block = cards(el)[0]!.querySelector(".dns-keys")!;
    expect(text(block.querySelector(".b"))).toBe("Обновите ключи на 1 устройстве");
    expect(text(block.querySelector(".sm.mut"))).toBe("Новый DNS записывается внутрь ключа. Ключ останется тем же — просто добавьте его в AmneziaVPN ещё раз.");
    expect([...block.querySelectorAll(".dns-keys-row")].map((r) => text(r.querySelector(".b")))).toEqual(["Pixel 7"]);
    expect(block.querySelector(".hint.row")).toBeNull(); // the link apps have a DNS of their own: nothing to say about them here
    expect(cards(el)[0]!.querySelector(".note.ok")).toBeNull(); // the block carries it
    block.querySelector<HTMLButtonElement>("[data-k=dns-renew-nod_fra-d3]")!.click();
    expect(a.log).toEqual(["renew d3"]);
    expect(text(block.querySelector("button"))).toBe("Обновить");
  });

  it("every focus key on the page is its own: the renew buttons of the stale card and of the server cards differ", () => {
    // after a render the page focuses the first element with the key the focused one had, so a shared key moves the focus
    const d = data("stale-dns");
    const el = view(d, state("android", { returning: true }), actions());
    expect(el.querySelector(".stale [data-k]")).not.toBeNull(); // the card that lists the stale keys is on the page
    expect(el.querySelector(".dns-keys [data-k]")).not.toBeNull(); // and so is the block of a server card
    const keys = [...el.querySelectorAll("[data-k]")].map((x) => x.getAttribute("data-k"));
    expect(keys.filter((k, i) => keys.indexOf(k) !== i)).toEqual([]);
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
  const card = (el: HTMLElement) => el.querySelector<HTMLElement>(".dns-link");

  it("one card above the list, named by the apps it reaches, with what applies and 'Change'", () => {
    const el = page("dns-both");
    const c = card(el)!;
    expect(el.querySelector(".srv-grid")!.previousElementSibling).toBe(c);
    expect(text(c.querySelector("h3"))).toBe("DNS в Happ и других приложениях по ссылке");
    expect(text(c.querySelector(".dns-link-v"))).toBe("Cloudflare + GoogleCloudflare и Google для всего");
    const change = c.querySelector<HTMLButtonElement>("[data-k=dns-link]")!;
    expect([text(change), change.getAttribute("aria-label")]).toEqual(["Изменить", "Изменить: DNS в Happ и других приложениях по ссылке"]);
    expect(c.getAttribute("aria-labelledby")).toBe("dns-link-t");
    // on a computer the app the page puts first there names it
    expect(text(card(view(data("dns-both"), state("windows", { returning: true }), actions()))!.querySelector("h3"))).toBe("DNS в kl!ck и других приложениях по ссылке");
    expect(text(card(view(data("dns-both"), state("android", { returning: true }, "en"), actions()))!.querySelector("h3"))).toBe("DNS in Happ and other apps that use the link");
    expect(text(card(view(data("dns-both"), state("android", { returning: true }, "zh"), actions()))!.querySelector("h3"))).toBe("Happ 等订阅类应用的 DNS");
  });

  it("a person with both kinds is told in one line that the keys have their own; one with the link apps only is not, and has no key rows", () => {
    const both = page("dns-both");
    expect(text(card(both)!.querySelector(".dns-link-keys"))).toBe("Ключи AmneziaVPN не затрагивает — их DNS в карточке каждого сервера.");
    expect(cards(both).map((x) => text(x.querySelector(".dns .k")))).toEqual(["DNS ключей", "DNS ключей"]);
    const link = page("dns-link");
    expect(card(link)).not.toBeNull();
    expect(card(link)!.querySelector(".dns-link-keys")).toBeNull();
    expect(cards(link).some((x) => x.querySelector(".dns"))).toBe(false);
  });

  it("no card when the owner offers nothing, switched the choice off, for a person without the link apps, or with one way only", () => {
    expect(card(page("dns-both-off"))).toBeNull();
    expect(text(page("dns-both-off").querySelector(".srv-grid")!.previousElementSibling)).toContain("В приложениях по ссылке DNS пока один на все серверы");
    // the owner's preview: what applies, nothing to change
    const preview = card(page("dns-both", state("android", { returning: true }), (d) => (d.dns!.endpoint = "")))!;
    expect(text(preview.querySelector(".dns-link-v"))).toBe("Cloudflare + GoogleCloudflare и Google для всего");
    expect(preview.querySelector("[data-k=dns-link]")).toBeNull();
    for (const f of [
      (d: ReturnType<typeof data>) => (d.dns!.enabled = false),
      (d: ReturnType<typeof data>) => (d.access.happ = false),
      (d: ReturnType<typeof data>) => (d.dns!.link.options = ["dns_builtin_standard"]), // the default and nothing else
      (d: ReturnType<typeof data>) => (d.dns_presets = []),
    ]) {
      expect(card(page("dns-both", state("android", { returning: true }), f))).toBeNull();
    }
    expect(canChooseLinkDns(data("dns-both"))).toBe(true);
  });

  it("saving, an error, and what happens next", () => {
    const busy = card(view(data("dns-both"), state("android", { returning: true, dns: { ...newDnsState(), busy: LINK } }), actions()))!;
    expect(text(busy.querySelector(".busy"))).toBe("Секунду…");
    expect(busy.querySelector(".tlink")).toBeNull();
    const a = actions();
    const err = card(view(data("dns-both"), state("android", { returning: true, dns: { ...newDnsState(), error: { server: LINK, code: "network", retryMin: 0, preset: "x" } } }), a))!;
    expect(text(err.querySelector(".note.bad"))).toBe("Нет связи. Проверьте интернет и повторите.Повторить");
    err.querySelector<HTMLButtonElement>("[data-k=dns-retry-link]")!.click();
    expect(a.log).toEqual(["dns-retry"]);
    const done = card(view(data("dns-both"), state("android", { returning: true, dns: { ...newDnsState(), done: { [LINK]: true } } }), actions()))!;
    expect(text(done.querySelector(".note.ok[role=status]"))).toBe("Готово. Приложения по ссылке получат новый DNS при обновлении подписки (до 12 ч). Чтобы сразу — обновите подписку в приложении.");
  });

  it("'Change' opens the picker: the default first with its name, the offered ones, 'now', and when the apps get it", () => {
    const a = actions();
    view(data("dns-both"), state("android", { returning: true }), a).querySelector<HTMLButtonElement>("[data-k=dns-link]")!.click();
    expect(a.log).toEqual([`dns-open ${LINK}`]);
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
