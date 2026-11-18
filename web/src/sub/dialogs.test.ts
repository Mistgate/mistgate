import { afterEach, describe, expect, it } from "vitest";
import { amzActions } from "./amz-actions";
import { awgConfig, awgDevice } from "./logic";
import { dialogClosed, dialogOpen, dialogView } from "./dialogs";
import { dnsActions } from "./dns-actions";
import { dict } from "./i18n";
import { genitive } from "./i18n-dlg";
import { LINK } from "./logic";
import { createModal } from "./modal";
import type { Ctx, State } from "./state";
import { actions, amzNoop, data, plain, state, text } from "./test-kit";
import type { MgData } from "./types";
import type { Lang } from "./types";

// The small windows after the boards V2Dlg*: which window is open (priority), what each holds (classes, words, buttons in the
// board's order), what its buttons ask the page, a call in flight and its failure, the DNS choice and its result.

type Over = { amz?: Partial<State["amz"]>; dns?: Partial<State["dns"]>; lang?: Lang; a?: ReturnType<typeof actions>; f?: (d: MgData) => void; platform?: State["platform"] };

function ctx(name: string, o: Over = {}): Ctx & { a: ReturnType<typeof actions> } {
  const d = data(name, o.f);
  const s = state(o.platform ?? "android", { returning: true }, o.lang ?? "ru");
  Object.assign(s.amz, o.amz);
  Object.assign(s.dns, o.dns);
  const a = o.a ?? actions();
  return { d, s, a, t: dict[s.lang], support: "" };
}
const draw = (c: Ctx) => {
  const box = document.createElement("div");
  box.append(...(dialogView(c).content as Node[]));
  return box;
};
const q = <T extends Element>(el: Element, sel: string) => el.querySelector<T>(sel)!;
const all = (el: Element, sel: string) => [...el.querySelectorAll<HTMLElement>(sel)];
const click = (el: Element, k: string) => q<HTMLButtonElement>(el, `[data-k="${k}"]`).click();
/** Actions that note what the windows ask of the page. */
const asked = () => {
  const log: string[] = [];
  const n = (s: string) => () => void log.push(s);
  const a = actions({
    amz: {
      ...amzNoop,
      add: (o) => log.push(`add ${o}`),
      show: (id) => log.push(`show ${id}`),
      renew: (id) => log.push(`renew ${id}`),
      stale: (id) => log.push(`stale ${id}`),
      ask: (c) => log.push(`ask ${c ? `${c.kind}:${c.id}` : "null"}`),
      rotate: (id) => log.push(`rotate ${id}`),
      remove: (id) => log.push(`remove ${id}`),
      menu: (id) => log.push(`menu ${id}`),
      renameStart: (id) => log.push(`renameStart ${id}`),
      renameInput: (v) => log.push(`input ${v}`),
      renameSave: n("save"),
      renameCancel: n("cancel"),
    },
    dns: { open: (id) => log.push(`dns-open ${id}`), pick: (p) => log.push(`dns-pick ${p}`), apply: n("dns-apply"), retry: n("dns-retry"), scope: (x) => log.push(`dns-scope ${x}`) },
  });
  return { a, log };
};
/** The name of a DNS option: the text of its .t without the "now" tag. */
const own = (o: Element) => plain(o.querySelector(".t")!.firstChild!.textContent);
const head = (box: Element) => ({ cls: q(box, ".a-dlg").className, role: q(box, ".a-dlg").getAttribute("role"), modal: q(box, ".a-dlg").getAttribute("aria-modal"), title: text(q(box, ".a-h .t")) });
afterEach(() => document.body.replaceChildren());

describe("which window is open", () => {
  it("nothing is open on a quiet page", () => {
    const c = ctx("many");
    expect(dialogOpen(c)).toBe(false);
  });

  it("each state opens its window; the windows lie over each other in a fixed order", () => {
    const c = ctx("dns-both", { amz: { menu: "d3" } });
    expect(dialogView(c).stage).toBe("menu:d3");
    c.s.dns.open = "de1";
    expect(dialogView(c).stage).toBe("menu:d3"); // the menu is over the DNS window
    c.s.amz.rename = { id: "d3", value: "x" };
    expect(dialogView(c).stage).toBe("rename:d3");
    c.s.amz.confirm = { id: "d3", kind: "remove" };
    expect(dialogView(c).stage).toBe("confirm:remove:d3");
    c.s.amz.stale = "d3";
    expect(dialogView(c).stage).toBe("stale:d3");
    c.s.amz.adding = true;
    c.s.amz.pane = "key";
    expect(dialogView(c).stage).toBe("key"); // "New device" is over all of them
    expect(q(draw(c), ".a-dlg .a-h .t").id).toBe("dlg-t");
  });

  it("the DNS window alone: the choice, then its result; the link apps' too", () => {
    const c = ctx("dns-both", { dns: { open: "de1" } });
    expect(dialogOpen(c)).toBe(true);
    expect(dialogView(c).stage).toBe("dns:pick");
    c.s.dns.done = { de1: true };
    expect(dialogView(c).stage).toBe("dns:done");
    const l = ctx("dns-both", { dns: { open: LINK } });
    expect(dialogView(l).stage).toBe("dns:pick");
  });

  it("a window for a device the page does not have, or with nothing to offer, is not opened", () => {
    expect(dialogOpen(ctx("many", { amz: { menu: "nope" } }))).toBe(false);
    expect(dialogOpen(ctx("many", { amz: { rename: { id: "nope", value: "" } } }))).toBe(false);
    expect(dialogOpen(ctx("many", { amz: { confirm: { id: "nope", kind: "remove" } } }))).toBe(false);
    expect(dialogOpen(ctx("many", { amz: { stale: "nope" } }))).toBe(false);
    expect(dialogOpen(ctx("many", { dns: { open: "nope" } }))).toBe(false);
    // the owner issues the keys: nothing to do with one
    expect(dialogOpen(ctx("amnezia-off", { amz: { menu: "d3" } }))).toBe(false);
    // the shared place of the link apps has no menu
    expect(dialogOpen(ctx("many", { amz: { menu: "d1" } }))).toBe(false);
  });

  it("Esc and the backdrop close the window that is open, and no other", () => {
    const { a, log } = asked();
    const run = (o: Over) => dialogClosed(ctx("dns-both", { ...o, a }));
    run({ dns: { open: "de1" } });
    run({ dns: { open: "de1" }, amz: { menu: "d3" } });
    run({ dns: { open: "de1" }, amz: { menu: "d3", rename: { id: "d3", value: "x" } } });
    run({ amz: { confirm: { id: "d3", kind: "rotate" } } });
    run({ amz: { stale: "d3" } });
    run({ amz: { adding: true } });
    expect(log).toEqual(["dns-open null", "menu null", "cancel", "ask null", "stale null", "add false"]);
  });
});

describe("the menu of a device", () => {
  it("a key: its key, the name, the replacement, the removal; the board's classes and order", () => {
    const { a, log } = asked();
    const box = draw(ctx("many", { amz: { menu: "d3" }, a }));
    expect(head(box)).toEqual({ cls: "a-dlg sm", role: "dialog", modal: "true", title: "Pixel 7" });
    expect(q(box, ".a-dlg").getAttribute("aria-label")).toBe("Действия с устройством");
    expect(box.querySelector(".a-h .sp")).not.toBeNull();
    expect(q(box, ".a-h .u-ic").getAttribute("aria-label")).toBe("Закрыть");
    const acts = all(box, ".a-acts .a-act");
    expect(acts.map(text)).toEqual(["Показать ключ", "Переименовать", "Заменить ключ", "Удалить устройство"]);
    expect(acts.map((x) => x.className)).toEqual(["a-act", "a-act", "a-act", "a-act bad"]);
    expect(acts.every((x) => x.querySelector("svg.i"))).toBe(true);
    expect(acts[0]!.hasAttribute("data-autofocus")).toBe(true);
    acts.forEach((x) => x.click());
    q<HTMLButtonElement>(box, ".a-h .u-ic").click();
    expect(log).toEqual(["show d3", "renameStart d3", "ask rotate:d3", "ask remove:d3", "menu null"]);
  });

  it("a device of an app on the link: rename and remove only, as far as the page allows", () => {
    const { a } = asked();
    const c = ctx("hwid-full", { amz: { menu: "l1" }, a });
    const box = draw(c);
    expect(text(q(box, ".a-h .t"))).toBe("iPhone 15");
    expect(all(box, ".a-act").map(text)).toEqual(["Переименовать", "Удалить устройство"]);
    c.d.devices.find((x) => x.id === "l1")!.can_remove = false;
    expect(all(draw(c), ".a-act").map(text)).toEqual(["Переименовать"]);
    c.d.devices.find((x) => x.id === "l1")!.can_rename = false;
    expect(dialogOpen(c)).toBe(false);
  });

  it("a key of a subscription that is not active: no key to show or replace, only the name and the removal", () => {
    const box = draw(ctx("many", { amz: { menu: "d3" }, f: (d) => (d.user.status = "expired") }));
    expect(all(box, ".a-act").map(text)).toEqual(["Переименовать", "Удалить устройство"]);
  });

  it("English and a right-to-left page", () => {
    const en = draw(ctx("many", { amz: { menu: "d3" }, lang: "en" }));
    expect(all(en, ".a-act").map(text)).toEqual(["Show key", "Rename", "Replace key", "Remove device"]);
    expect(q(en, ".a-dlg").getAttribute("aria-label")).toBe("Device actions");
    const fa = draw(ctx("many", { amz: { menu: "d3" }, lang: "fa" }));
    expect(all(fa, ".a-act")).toHaveLength(4);
    expect(text(q(fa, ".a-h .t"))).toBe("Pixel 7"); // the name stands as it is: the page isolates it in CSS (sub-dlg.css)
  });
});

describe("rename", () => {
  const setup = (o: Over = {}, name = "many") => {
    const { a, log } = asked();
    const c = ctx(name, { amz: { rename: { id: "d3", value: "Pixel 7" } }, a, ...o });
    return { c, box: draw(c), log };
  };

  it("the name, the field with its limit, the foot: Cancel then Save", () => {
    const { box, log } = setup();
    expect(head(box)).toEqual({ cls: "a-dlg sm", role: "dialog", modal: "true", title: "Название устройства" });
    expect(q(box, ".a-dlg").getAttribute("aria-label")).toBe("Переименовать");
    expect(text(q(box, ".a-lbl"))).toBe("Названиедо 40 знаков");
    const f = q<HTMLInputElement>(box, "input.a-inp");
    expect([f.value, f.maxLength, f.id, q(box, ".a-lbl").getAttribute("for"), f.hasAttribute("data-autofocus")]).toEqual(["Pixel 7", 40, "dlg-nm", "dlg-nm", true]);
    expect(all(box, ".a-f .u-btn").map((x) => [text(x), x.className])).toEqual([["Отмена", "u-btn"], ["Сохранить", "u-btn pri"]]);
    f.value = "Phone";
    f.dispatchEvent(new Event("input", { bubbles: true }));
    f.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", cancelable: true }));
    click(box, "dlg-yes");
    click(box, "dlg-no");
    click(box, "modal-x");
    expect(log).toEqual(["input Phone", "save", "save", "cancel", "cancel"]);
  });

  it("a call in flight: Save waits with a spinner; a failure says what happened under the field", () => {
    const busy = setup({ amz: { rename: { id: "d3", value: "x" }, busy: "rename:d3" } });
    const go = q<HTMLButtonElement>(busy.box, "[data-k=dlg-yes]");
    expect([go.disabled, text(go), go.querySelector(".spin") !== null]).toEqual([true, "Секунду…", true]);
    click(busy.box, "dlg-yes");
    expect(busy.log).toEqual([]);
    const bad = setup({ amz: { rename: { id: "d3", value: "x" }, error: "not_found", errorAt: "rename:d3" } });
    expect(text(q(bad.box, ".a-fld .a-err"))).toBe("Такого устройства уже нет. Обновите страницу.");
    expect(q(bad.box, ".a-err").getAttribute("role")).toBe("alert");
    expect(q(bad.box, "input").getAttribute("aria-invalid")).toBe("true");
    // another device's failure is not shown here
    expect(setup({ amz: { rename: { id: "d3", value: "x" }, error: "not_found", errorAt: "rename:d4" } }).box.querySelector(".a-err")).toBeNull();
  });

  it("a device of an app on the link is renamed the same way; the English words", () => {
    const c = ctx("hwid-full", { amz: { rename: { id: "l1", value: "iPhone 15" } }, lang: "en" });
    const box = draw(c);
    expect(text(q(box, ".a-h .t"))).toBe("Device name");
    expect(text(q(box, ".a-lbl"))).toBe("Nameup to 40 characters");
    expect(all(box, ".a-f .u-btn").map(text)).toEqual(["Cancel", "Save"]);
  });
});

describe("delete and replace the key", () => {
  it("delete a key: the board's question, Cancel first (the safe answer takes the keyboard), then the red Delete with the bin", () => {
    const { a, log } = asked();
    const box = draw(ctx("many", { amz: { confirm: { id: "d3", kind: "remove" } }, a }));
    expect(head(box)).toEqual({ cls: "a-dlg sm", role: "dialog", modal: "true", title: "Удалить Pixel 7" });
    expect(q(box, ".a-dlg").getAttribute("aria-label")).toBe("Удалить устройство");
    expect(text(q(box, ".a-b .a-lead"))).toBe("Ключ этого устройства перестанет работать, а место освободится. Вернуть устройство можно будет только новым ключом");
    const [no, yes] = all(box, ".a-f .u-btn") as [HTMLElement, HTMLElement];
    expect([text(no), no.className, no.hasAttribute("data-autofocus")]).toEqual(["Отмена", "u-btn", true]);
    expect([text(yes), yes.className, yes.querySelector("svg.i") !== null, yes.hasAttribute("data-autofocus")]).toEqual(["Удалить", "u-btn bad", true, false]);
    no.click();
    yes.click();
    q<HTMLButtonElement>(box, ".a-h .u-ic").click();
    expect(log).toEqual(["ask null", "remove d3", "ask null"]);
  });

  it("delete a device of an app on the link: the same question by its meaning, with what the app said about itself", () => {
    const box = draw(ctx("hwid-full", { amz: { confirm: { id: "l1", kind: "remove" } } }));
    expect(text(q(box, ".a-h .t"))).toBe("Удалить iPhone 15");
    expect(text(q(box, ".a-lead"))).toBe("Приложение на этом устройстве перестанет подключаться, а место освободится");
    expect(text(q(box, ".u-hint"))).toContain("Happ");
  });

  it("replace the key: the question with the name of the app, Cancel first, Replace with the refresh icon", () => {
    const { a, log } = asked();
    const box = draw(ctx("many", { amz: { confirm: { id: "d3", kind: "rotate" } }, a }));
    expect(head(box).title).toBe("Заменить ключ Pixel 7");
    expect(text(q(box, ".a-lead"))).toBe("Старый ключ перестанет работать сразу, а новый нужно будет добавить в AmneziaVPN заново");
    const [no, yes] = all(box, ".a-f .u-btn") as [HTMLElement, HTMLElement];
    expect([text(no), no.hasAttribute("data-autofocus"), text(yes), yes.className]).toEqual(["Отмена", true, "Заменить", "u-btn pri"]);
    expect(yes.querySelector("svg.i")).not.toBeNull();
    yes.click();
    expect(log).toEqual(["rotate d3"]);
  });

  it("a call in flight waits with a spinner and cannot be sent twice; a failure is said in the window", () => {
    const { a, log } = asked();
    const busy = draw(ctx("many", { amz: { confirm: { id: "d3", kind: "remove" }, busy: "d3" }, a }));
    const yes = q<HTMLButtonElement>(busy, "[data-k=dlg-yes]");
    expect([yes.disabled, text(yes), yes.querySelector(".spin") !== null]).toEqual([true, "Секунду…", true]);
    yes.click();
    expect(log).toEqual([]);
    const bad = draw(ctx("many", { amz: { confirm: { id: "d3", kind: "rotate" }, error: "too_many_requests", errorAt: "d3", retryMin: 5 } }));
    expect(text(q(bad, ".a-b .a-err"))).toBe("Слишком много действий подряд. Попробуйте через 5 мин.");
  });

  it("English and Chinese", () => {
    const en = draw(ctx("many", { amz: { confirm: { id: "d3", kind: "remove" } }, lang: "en" }));
    expect(text(q(en, ".a-h .t"))).toBe("Remove Pixel 7");
    expect(all(en, ".a-f .u-btn").map(text)).toEqual(["Cancel", "Remove"]);
    const zh = draw(ctx("many", { amz: { confirm: { id: "d3", kind: "rotate" } }, lang: "zh" }));
    expect(text(q(zh, ".a-lead"))).toBe("旧密钥会立即失效，需要把新密钥重新添加到 AmneziaVPN");
  });
});

describe("a key that is no longer valid", () => {
  it("the board's question: what happened, three steps, Later and Get a new key", () => {
    const { a, log } = asked();
    const box = draw(ctx("stale", { amz: { stale: "d3" }, a }));
    expect(head(box)).toEqual({ cls: "a-dlg", role: "dialog", modal: "true", title: "Нужен новый ключ для Pixel 7" });
    expect(text(q(box, ".a-lead"))).toBe("Серверы обновились, и старый ключ этого устройства больше не работает");
    expect(all(box, ".a-steps .a-st").map((x) => [text(x.querySelector(".a-n")), text(x.querySelector(".tx"))])).toEqual([
      ["1", "Нажмите «Получить новый ключ»"],
      ["2", "Добавьте его в AmneziaVPN, как в первый раз"],
      ["3", "Удалите в AmneziaVPN старое подключение этого устройства"],
    ]);
    const [later, get] = all(box, ".a-f .u-btn") as [HTMLElement, HTMLElement];
    expect([text(later), later.className, text(get), get.className, get.querySelector("svg.i") !== null]).toEqual(["Позже", "u-btn", "Получить новый ключ", "u-btn pri", true]);
    later.click();
    get.click();
    q<HTMLButtonElement>(box, ".a-h .u-ic").click();
    expect(log).toEqual(["stale null", "renew d3", "stale null"]);
  });

  it("a key that is stale only because of a DNS says the key itself stays; a request waits with a spinner; a failure is said", () => {
    const box = draw(ctx("stale-dns", { amz: { stale: "d3" } }));
    expect(text(q(box, ".a-lead"))).toBe("DNS сервера изменился — получите ключ заново. Сам ключ останется прежним");
    const busy = draw(ctx("stale", { amz: { stale: "d3", busy: "renew:d3" } }));
    const get = q<HTMLButtonElement>(busy, "[data-k=dlg-yes]");
    expect([get.disabled, text(get), get.querySelector(".spin") !== null]).toEqual([true, "Секунду…", true]);
    const bad = draw(ctx("stale", { amz: { stale: "d3", error: "no_inbound", errorAt: "renew:d3" } }));
    expect(text(q(bad, ".a-b .a-err"))).toBe("Этот вариант пока нигде не запущен — напишите в поддержку.");
  });

  it("the English words", () => {
    const box = draw(ctx("stale", { amz: { stale: "d3" }, lang: "en" }));
    expect(text(q(box, ".a-h .t"))).toBe("New key needed for Pixel 7");
    expect(all(box, ".a-st .tx").map(text)).toEqual(["Press “Get a new key”", "Add it to AmneziaVPN like the first time", "Delete this device’s old connection in AmneziaVPN"]);
    expect(all(box, ".a-f .u-btn").map(text)).toEqual(["Later", "Get a new key"]);
  });
});

describe("the DNS choice", () => {
  const radios = (box: Element) => all(box, ".a-opt").map((o) => [own(o), text(o.querySelector(".d")), o.getAttribute("aria-checked")]);

  it("several servers: the scope (all servers first), the DNS list, the hint and Cancel / Save", () => {
    const { a, log } = asked();
    const c = ctx("dns-both", { dns: { open: "de1", pick: "", all: true }, a });
    const box = draw(c);
    expect(head(box)).toEqual({ cls: "a-dlg", role: "dialog", modal: "true", title: "DNS для ключей" });
    expect(text(q(box, ".a-lead"))).toBe("Действует для ключей AmneziaVPN. В приложениях по ссылке DNS пока один на все серверы");
    const flds = all(box, ".a-fld");
    expect(flds.map((x) => text(x.querySelector(".a-lbl")))).toEqual(["Для каких серверов", "DNS"]);
    const chips = all(box, ".a-seg .a-chip");
    expect(chips.map((x) => [text(x), x.className, x.getAttribute("role"), x.getAttribute("aria-checked")])).toEqual([["Все серверы", "a-chip on", "radio", "true"], ["Только Германия", "a-chip", "radio", "false"]]);
    expect(q(box, ".a-seg").getAttribute("role")).toBe("radiogroup");
    expect(radios(box)).toEqual([
      ["По умолчанию — Cloudflare + Google", "Cloudflare и Google для всего", "true"],
      ["AdGuard: без рекламы", "AdGuard DNS: без рекламы и трекеров", "false"],
      ["AdGuard Family: без рекламы и 18+", "AdGuard Family: без рекламы и сайтов для взрослых", "false"],
    ]);
    expect(q(box, ".a-opt .t .tag").textContent).toBe("сейчас");
    expect(all(box, ".a-opt").every((x) => x.querySelector(".a-rd"))).toBe(true);
    expect(text(q(box, ".a-f .u-hint"))).toBe("После смены ключи нужно получить заново");
    expect(all(box, ".a-f .u-btn").map((x) => [text(x), x.className])).toEqual([["Отмена", "u-btn"], ["Сохранить", "u-btn pri"]]);
    chips[1]!.click();
    chips[0]!.click();
    click(box, "dns-opt-dns_builtin_adblock");
    click(box, "dns-apply");
    click(box, "dns-cancel");
    click(box, "modal-x");
    expect(log).toEqual(["dns-scope false", "dns-scope true", "dns-pick dns_builtin_adblock", "dns-apply", "dns-open null", "dns-open null"]);
  });

  it("twin servers of one country are told apart by their number in the chips, as in the table", () => {
    const c = ctx("dns-both", { dns: { open: "de1", all: true }, f: (d) => d.servers.push({ ...structuredClone(d.servers[0]!), id: "de2", label: "Германия 2" }) });
    expect(all(draw(c), ".a-seg .a-chip").map(text)).toEqual(["Все серверы", "Только Германия DE-1"]);
    c.s.dns.open = "de2";
    expect(all(draw(c), ".a-seg .a-chip").map(text)).toEqual(["Все серверы", "Только Германия DE-2"]);
  });

  it("one server of the choice: no scope chips; the picked one is checked and takes the keyboard", () => {
    const box = draw(ctx("dns-both", { dns: { open: "de1", pick: "dns_builtin_family" }, f: (d) => d.servers.pop() }));
    expect(all(box, ".a-fld .a-lbl").map(text)).toEqual(["DNS"]);
    const on = q(box, ".a-opt.on");
    expect([on.getAttribute("aria-checked"), on.hasAttribute("data-autofocus"), own(on)]).toEqual(["true", true, "AdGuard Family: без рекламы и 18+"]);
  });

  it("the default names the server's own default; for all servers whose defaults differ, it does not", () => {
    const c = ctx("dns-both", { dns: { open: "de1", all: true }, f: (d) => (d.servers[1]!.dns!.default = "dns_builtin_adblock") });
    const first = all(draw(c), ".a-opt")[0]!;
    expect(own(first)).toBe("По умолчанию");
    expect(first.querySelector(".d")).toBeNull();
    c.s.dns.all = false;
    expect(own(all(draw(c), ".a-opt")[0]!)).toContain("По умолчанию — ");
  });

  it("the hint about the keys is there only when the server has keys; a save waits with a spinner; a failure keeps the window and offers Retry", () => {
    const { a, log } = asked();
    const none = draw(ctx("dns-both", { dns: { open: "de1" }, f: (d) => d.servers.forEach((s) => (s.connections = s.connections.filter((x) => x.way !== "key"))) }));
    expect(none.querySelector(".a-f .u-hint")).toBeNull();
    const busy = draw(ctx("dns-both", { dns: { open: "de1", busy: "de1" }, a }));
    const go = q<HTMLButtonElement>(busy, "[data-k=dns-apply]");
    expect([go.disabled, text(go), go.querySelector(".spin") !== null]).toEqual([true, "Секунду…", true]);
    const bad = draw(ctx("dns-both", { dns: { open: "de1", all: false, error: { server: "de1", code: "too_many_requests", retryMin: 9, preset: "dns_builtin_adblock" } }, a }));
    expect(text(q(bad, ".a-b .a-err"))).toBe("Слишком много изменений подряд. Попробуйте через 9 мин.");
    const retry = q<HTMLButtonElement>(bad, "[data-k=dns-apply]");
    expect(text(retry)).toBe("Повторить");
    retry.click();
    expect(log).toEqual(["dns-retry"]);
    // a failure in the middle of "all servers" names the server
    const mid = draw(ctx("dns-both", { dns: { open: "de1", all: true, error: { server: "nl1", code: "failed", retryMin: 0, preset: "x" } } }));
    expect(text(q(mid, ".a-err"))).toMatch(/^Нидерланды: /);
  });

  it("the link apps: the same window without the scope and without the hint about keys", () => {
    const { a, log } = asked();
    const c = ctx("dns-both", { dns: { open: LINK, pick: "dns_builtin_adblock" }, a, platform: "android" });
    const box = draw(c);
    expect(text(q(box, ".a-h .t"))).toBe("DNS в Happ и других приложениях по ссылке");
    expect(text(q(box, ".a-lead"))).toBe("Приложения получат новый DNS при обновлении подписки (до 12 ч)");
    expect(all(box, ".a-fld .a-lbl").map(text)).toEqual(["DNS"]);
    expect(radios(box).map((x) => x[0])).toEqual(["По умолчанию — Cloudflare + Google", "AdGuard: без рекламы", "AdGuard Family: без рекламы и 18+", "Россия: .ru напрямую"]);
    expect(box.querySelector(".a-f .u-hint")).toBeNull();
    click(box, "dns-apply");
    expect(log).toEqual(["dns-apply"]);
    expect(dialogView(c).label).toBe("DNS в Happ и других приложениях по ссылке");
  });

  it("English", () => {
    const box = draw(ctx("dns-both", { dns: { open: "de1", all: true }, lang: "en" }));
    expect(text(q(box, ".a-h .t"))).toBe("DNS for keys");
    expect(all(box, ".a-seg .a-chip").map(text)).toEqual(["All servers", "Only Germany"]);
    expect(all(box, ".a-fld .a-lbl").map(text)).toEqual(["Which servers", "DNS"]);
    expect(all(box, ".a-f .u-btn").map(text)).toEqual(["Cancel", "Save"]);
  });
});

describe("DNS changed", () => {
  const done = (o: Over = {}, name = "stale-dns") => {
    const { a, log } = asked();
    const c = ctx(name, { dns: { open: "nod_fra", done: { nod_fra: true } }, a, ...o });
    return { c, box: draw(c), log };
  };

  it("what applies now and which keys must be fetched again, each with its flag and its button; Done closes", () => {
    const { box, log, c } = done({ dns: { open: "nod_fra", done: { nod_fra: true }, all: false } });
    expect(head(box)).toEqual({ cls: "a-dlg", role: "dialog", modal: "true", title: "DNS изменён" });
    expect(text(q(box, ".a-lead"))).toBe("Теперь на сервере Германия · Франкфурт Семейный, без рекламы и сайтов для взрослых. Сам ключ остался прежним, но каждое устройство с ключом должно получить его заново");
    const rows = all(box, ".a-opts .a-opt");
    expect(rows).toHaveLength(1);
    expect(rows[0]!.querySelector(".fl2.de")).not.toBeNull();
    expect(text(rows[0]!.querySelector(".t"))).toBe("Pixel 7");
    expect(text(rows[0]!.querySelector(".d"))).toBe("Ключ для Германии нужно получить заново");
    const get = q<HTMLButtonElement>(rows[0]!, "button");
    expect([text(get), get.className, get.getAttribute("aria-label")]).toEqual(["Получить", "u-btn", "Получить новый ключ для Pixel 7"]);
    expect(all(box, ".a-f .u-btn").map((x) => [text(x), x.className])).toEqual([["Готово", "u-btn"]]);
    get.click();
    click(box, "dlg-done");
    click(box, "modal-x");
    expect(log).toEqual(["renew d3", "dns-open null", "dns-open null"]);
    expect(dialogView(c).label).toBe("DNS изменён");
  });

  it("for all servers: the same sentence about all of them; the servers that lack the choice are named", () => {
    const { box } = done({ dns: { open: "de1", done: { de1: true }, all: true, skipped: ["nl1"] } }, "dns-both");
    expect(text(q(box, ".a-lead"))).toBe("DNS сохранён — его получат новые ключи"); // no key is stale: nothing to fetch again
    expect(box.querySelector(".a-opts")).toBeNull();
    expect(text(q(box, ".u-hint"))).toBe("Этого варианта нет не на всех серверах: Нидерланды остались как были");
    const stale = done({ dns: { open: "de1", done: { de1: true }, all: true } }, "dns-both");
    stale.c.d.amnezia!.devices[0]!.stale = true;
    stale.c.d.amnezia!.devices[0]!.stale_reason = "dns";
    expect(text(q(draw(stale.c), ".a-lead"))).toBe("Теперь на всех серверах Cloudflare + Google, Cloudflare и Google для всего. Сами ключи остались прежними, но каждое устройство с ключом должно получить его заново");
  });

  it("no key to fetch again: the short word about new keys; the link apps have their own", () => {
    const one = done({ dns: { open: "nod_fra", done: { nod_fra: true }, all: false } }, "return");
    expect(text(q(one.box, ".a-lead"))).toBe("DNS сохранён — его получат новые ключи");
    expect(one.box.querySelector(".a-opts")).toBeNull();
    const link = done({ dns: { open: LINK, done: { [LINK]: true } } }, "dns-both");
    expect(text(q(link.box, ".a-lead"))).toBe("Приложения по ссылке получат новый DNS при обновлении подписки (до 12 ч). Чтобы сразу — обновите подписку в приложении");
    expect(link.box.querySelector(".a-opts")).toBeNull();
  });

  it("a key being fetched waits with a spinner on its row; a failure is said under the list", () => {
    const busy = done({ amz: { busy: "renew:d3" } });
    const get = q<HTMLButtonElement>(busy.box, ".a-opt button");
    expect([get.disabled, text(get)]).toEqual([true, "Секунду…"]);
    const bad = done({ amz: { error: "no_inbound", errorAt: "renew:d3" } });
    expect(text(q(bad.box, ".a-b .a-err"))).toBe("Этот вариант пока нигде не запущен — напишите в поддержку.");
  });

  it("a key whose server the page does not know has no flag; English", () => {
    const c = ctx("stale-dns", { dns: { open: "nod_fra", done: { nod_fra: true } }, lang: "en", f: (d) => (d.amnezia!.devices[0]!.profile_id = "p_none") });
    const box = draw(c);
    expect(box.querySelector(".a-opt .fl2")).toBeNull();
    expect(text(q(box, ".a-opt .d"))).toBe("The key must be fetched again");
    expect(text(q(box, ".a-h .t"))).toBe("DNS changed");
    expect(all(box, ".a-f .u-btn").map(text)).toEqual(["Done"]);
  });
});

describe("the Russian name of a country after 'for'", () => {
  it("is declined by its ending; what the rules do not know is said without a case", () => {
    const g = ["Германия", "Нидерланды", "Россия", "Польша", "Литва", "Украина", "Канада", "Казахстан", "Китай", "США"].map((x) => genitive(x));
    expect(g).toEqual(["Германии", "Нидерландов", "России", "Польши", "Литвы", "Украины", "Канады", "Казахстана", "Китая", "США"]);
    expect([genitive("Беларусь"), genitive("Южная Корея"), genitive("")]).toEqual([null, null, null]);
    const c = ctx("stale-dns", { dns: { open: "nod_fra", done: { nod_fra: true } }, f: (d) => (d.servers[0]!.country_code = "BY") });
    expect(text(q(draw(c), ".a-opt .d"))).toBe("Беларусь: ключ нужно получить заново");
  });
});

describe("what every window shares", () => {
  const windows: [string, string, Over][] = [
    ["many", "menu", { amz: { menu: "d3" } }],
    ["many", "rename", { amz: { rename: { id: "d3", value: "x" } } }],
    ["many", "remove", { amz: { confirm: { id: "d3", kind: "remove" } } }],
    ["many", "rotate", { amz: { confirm: { id: "d3", kind: "rotate" } } }],
    ["stale", "stale", { amz: { stale: "d3" } }],
    ["dns-both", "dns", { dns: { open: "de1", all: true } }],
    ["dns-both", "dns-link", { dns: { open: LINK } }],
    ["stale-dns", "done", { dns: { open: "nod_fra", done: { nod_fra: true } } }],
  ];
  const langs: Lang[] = ["ru", "en", "zh", "fa", "tr"];

  it("every control has its own data-k, in every language", () => {
    for (const lang of langs) {
      for (const [name, label, o] of windows) {
        const box = draw(ctx(name, { ...o, lang }));
        const keys = all(box, "[data-k]").map((x) => x.dataset.k);
        expect(new Set(keys).size, `${label} ${lang}`).toBe(keys.length);
        expect(box.textContent, `${label} ${lang}`).not.toMatch(/undefined|NaN|\[object/);
        expect(all(box, ".a-dlg").length).toBe(1);
        expect(q(box, ".a-dlg").getAttribute("aria-label")).toBeTruthy();
      }
    }
  });

  it("every window is the single section the dialog holds, with the title and the close button first", () => {
    for (const [name, , o] of windows) {
      const content = dialogView(ctx(name, o)).content;
      expect(content).toHaveLength(1);
      const box = draw(ctx(name, o));
      expect(box.firstElementChild!.tagName).toBe("SECTION");
      expect(all(box, ".a-dlg > *").map((x) => x.tagName)[0]).toBe("HEADER");
      expect(q(box, ".a-h .u-ic").getAttribute("data-k")).toBe("modal-x");
    }
  });

  it("the words exist in all five languages for every window (no key of one is missing in another)", () => {
    for (const k of Object.keys(dict.ru).filter((x) => x.startsWith("dg"))) for (const l of langs) expect(k in dict[l], `${l}.${k}`).toBe(true);
  });
});

describe("the entrance of a window plays once per stage", () => {
  it("a window drawn again with the same stage is still; another stage plays it", () => {
    const modal = createModal({ onClose: () => {}, refocus: () => {}, label: () => "w" });
    const body = () => [document.createElement("section")];
    modal.sync(true, body(), "menu");
    expect(modal.el.querySelector(".mbox")!.classList.contains("still")).toBe(false); // opening plays it
    modal.sync(true, body(), "menu");
    expect(modal.el.querySelector(".mbox")!.classList.contains("still")).toBe(true);
    modal.sync(true, body(), "rename:d3");
    expect(modal.el.querySelector(".mbox")!.classList.contains("still")).toBe(false);
    modal.sync(true, body(), "rename:d3");
    expect(modal.el.querySelector(".mbox")!.classList.contains("still")).toBe(true);
    modal.sync(false, []);
    modal.sync(true, body(), "rename:d3"); // opened again: plays
    expect(modal.el.querySelector(".mbox")!.classList.contains("still")).toBe(false);
  });
});

describe("the windows with the page's real actions", () => {
  const settle = () => new Promise((r) => setTimeout(r, 0));
  const cfg = awgConfig({ node_id: "de1", label: "Германия", country_code: "DE", version: "3.1", conf: "[Interface]\n", vpn_key: "vpn://k", filename: "a.conf", warnings: [] });
  function page(name: string) {
    const d = data(name);
    const s = state("android", { returning: true });
    const a = actions();
    const render = () => {};
    const api = {
      addDevice: async () => ({ device: awgDevice({ id: "dev_new" }), configs: [cfg] }),
      getConfigs: async () => ({ configs: [cfg] }),
      rotateKey: async () => ({ configs: [cfg] }),
      revokeDevice: async () => {},
      renameDevice: async (_e: string, id: string, label: string) => ({ device: awgDevice({ id, label, platform: "android", profile_id: "p31" }), configs: [] }),
    };
    a.amz = amzActions({ data: d, st: s, render, api, reveal: () => {} });
    a.dns = dnsActions({ data: d, st: s, render, api: { setDns: async () => ({ server: undefined, stale_devices: ["d3"] }) } });
    const c: Ctx = { d, s, a, t: dict.ru, support: "" };
    return { c, d, s, a };
  }
  const stage = (c: Ctx) => (dialogOpen(c) ? dialogView(c).stage : "-");

  it("menu, rename, save: the menu gives way to the name, the name to nothing; the device has its new name", async () => {
    const { c, a, d } = page("many");
    a.amz.menu("d3");
    expect(stage(c)).toBe("menu:d3");
    a.amz.renameStart("d3");
    expect(stage(c)).toBe("rename:d3");
    a.amz.renameInput("Phone");
    a.amz.renameSave();
    expect(c.s.amz.busy).toBe("rename:d3");
    await settle();
    expect(stage(c)).toBe("-");
    expect(d.amnezia!.devices[0]!.label).toBe("Phone");
  });

  it("menu, delete: asked, then the device is gone and no window is left; cancelling closes it too", async () => {
    const { c, a, d } = page("many");
    a.amz.menu("d4");
    a.amz.ask({ id: "d4", kind: "remove" });
    expect(stage(c)).toBe("confirm:remove:d4");
    a.amz.ask(null);
    expect(stage(c)).toBe("-");
    a.amz.ask({ id: "d4", kind: "remove" });
    a.amz.remove("d4");
    await settle();
    expect(stage(c)).toBe("-");
    expect(d.amnezia!.devices.map((x) => x.id)).not.toContain("d4");
  });

  it("replace the key: asked, then the new key is in the window of the key", async () => {
    const { c, a } = page("many");
    a.amz.ask({ id: "d3", kind: "rotate" });
    a.amz.rotate("d3");
    await settle();
    expect(c.s.amz).toMatchObject({ confirm: null, open: "d3" });
    expect(stage(c)).toBe("ready:d3:shown");
  });

  it("a stale key: the question, then the key's window once it is fetched", async () => {
    const { c, a } = page("stale");
    a.amz.stale("d3");
    expect(stage(c)).toBe("stale:d3");
    a.amz.renew("d3");
    await settle();
    expect(stage(c)).toBe("ready:d3:renew");
    a.amz.add(false);
    expect(stage(c)).toBe("-");
  });

  it("DNS: the choice for all servers, saved, becomes the result; 'Get' opens the key, and closing it brings the result back with the rest", async () => {
    const { c, a, d } = page("dns-both");
    a.dns.open("de1");
    expect(stage(c)).toBe("dns:pick");
    a.dns.pick("dns_builtin_family");
    a.dns.apply();
    expect(stage(c)).toBe("dns:pick"); // in flight: the window stays and shows it
    expect(c.s.dns.busy).toBe("de1");
    await settle();
    expect(stage(c)).toBe("dns:done");
    expect(d.servers.map((x) => x.dns?.choice)).toEqual(["dns_builtin_family", "dns_builtin_family"]);
    expect(all(draw(c), ".a-opts .a-opt")).toHaveLength(1);
    a.amz.renew("d3");
    await settle();
    expect(stage(c)).toBe("ready:d3:renew"); // the key window is over the result
    a.amz.add(false);
    expect(stage(c)).toBe("dns:done");
    expect(draw(c).querySelector(".a-opts")).toBeNull(); // the key was fetched: nothing left to fetch again
    a.dns.open(null);
    expect(stage(c)).toBe("-");
  });
});
