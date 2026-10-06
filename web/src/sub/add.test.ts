import { describe, expect, it } from "vitest";
import { deviceModal, deviceModalLabel } from "./add";
import { newAmzState } from "./amnezia";
import { amzActions, type Api } from "./amz-actions";
import { cases } from "./dev-data";
import { dict } from "./i18n";
import { defaultPane, keyAvailability } from "./logic";
import type { AddPane } from "./state";
import { actions, amzNoop, data, state, text } from "./test-kit";
import type { MgData } from "./types";
import { view } from "./view";

// "Add a device": one sheet for both ways (with an app and the link, with a key), its panes, and what each state of the person does to them.

const support = "https://t.me/example_support";
const noApi = {} as Api;

/** What the dialog holds for a person on `platform`, at `pane`. */
function sheet(d: MgData, pane: AddPane, o: { platform?: "ios" | "android" | "windows" | "macos" | "linux"; lang?: "ru" | "en"; over?: Parameters<typeof state>[1]; a?: ReturnType<typeof actions> } = {}) {
  const platform = o.platform ?? "ios";
  const a = o.a ?? actions();
  const s = state(platform, { amz: { ...newAmzState(platform, "p31", platform), adding: true, pane }, ...o.over }, o.lang ?? "ru");
  const c = { d, s, a, t: dict[s.lang], support };
  const box = document.createElement("div");
  box.append(...(deviceModal(c) as Node[]));
  return { box, a, c, label: deviceModalLabel(c) };
}
const keys = (el: Element) => [...el.querySelectorAll("[data-k]")].map((x) => x.getAttribute("data-k")!);
const dupes = (list: string[]) => list.filter((k, i) => list.indexOf(k) !== i);

describe("where 'Add a device' opens", () => {
  it("with both ways it asks which; with one it goes straight to that way", () => {
    expect(defaultPane(data("many"))).toBe("pick");
    expect(defaultPane(data("happ"))).toBe("link");
    expect(defaultPane(data("keys-only"))).toBe("key");
    expect(defaultPane(data("noapps"))).toBe("key"); // nothing to choose: the old form
  });

  it("the actions: open at the default pane or at the one asked, move between panes, closing is one thing for every pane", () => {
    const d = data("many");
    const st = { lang: "ru" as const, amz: newAmzState("ios", "p31", "ios") };
    let draws = 0;
    const a = amzActions({ data: d, st, render: () => draws++, api: noApi, reveal: () => {} });
    a.add(true);
    expect(st.amz).toMatchObject({ adding: true, pane: "pick" });
    a.pane("link");
    expect(st.amz.pane).toBe("link");
    a.pane("key");
    a.pane("pick");
    st.amz.error = "network";
    a.pane("key");
    expect(st.amz).toMatchObject({ pane: "key", error: "" });
    a.add(false);
    expect(st.amz.adding).toBe(false);
    a.add(true, "key"); // a step of the first-visit steps: straight to the form
    expect(st.amz).toMatchObject({ adding: true, pane: "key" });
    a.add(false);
    const only = { lang: "ru" as const, amz: newAmzState("ios", "p31", "ios") };
    amzActions({ data: data("happ"), st: only, render: () => {}, api: noApi, reveal: () => {} }).add(true);
    expect(only.amz.pane).toBe("link");
    expect(draws).toBeGreaterThan(0);
  });
});

describe("step 1: the way", () => {
  it("two cards: with an app (recommended, first) and with a key; each says what it takes", () => {
    const { box, label } = sheet(data("devices-rich"), "pick");
    expect(label).toBe("Добавить устройство");
    expect(text(box.querySelector("h3"))).toBe("Добавить устройство");
    expect(text(box.querySelector(".sh-head .sub"))).toBe("Как оно будет подключаться?");
    const ways = [...box.querySelectorAll<HTMLButtonElement>("button.opt.go")];
    expect(ways.map((b) => b.getAttribute("data-k"))).toEqual(["add-way-link", "add-way-key"]);
    expect(ways[0]!.hasAttribute("data-autofocus")).toBe(true);
    const link = ways[0]!;
    expect(text(link.querySelector(".opt-t"))).toBe("Через приложениеРекомендуем");
    expect([...link.querySelectorAll(".chip")].map(text)).toEqual(["Happ"]); // the names come from the settings, not from the page
    expect(text(link.querySelector(".wayslot"))).toBe("Не занимает новых мест"); // an app already took the link's slot
    expect(link.querySelector(".wayslot .sq.o")).not.toBeNull(); // hollow: no slot
    const key = ways[1]!;
    expect(text(key.querySelector(".opt-t"))).toBe("Ключом AmneziaVPN");
    expect(text(key.querySelector(".opt-d"))).toBe("Для роутера и приложения AmneziaVPN. Отдельный ключ на каждое устройство.");
    expect(text(key.querySelector(".wayslot"))).toBe("Занимает 1 место · свободно 5");
    expect(key.querySelector(".wayslot .sq:not(.o)")).not.toBeNull(); // filled: a slot
  });

  it("the link not used yet takes the one shared slot at the first app; no limit: no count", () => {
    const first = sheet(data("first"), "pick");
    expect(text(first.box.querySelector("[data-k=add-way-link] .wayslot"))).toBe("Одно место на все приложения");
    const unlimited = sheet(data("first", (d) => (d.user.device_limit = 0)), "pick");
    expect(text(unlimited.box.querySelector("[data-k=add-way-key] .wayslot"))).toBe("Занимает 1 место");
    expect(text(sheet(data("first", (d) => (d.user.device_limit = 2, d.user.devices_used = 1)), "pick").box.querySelector("[data-k=add-way-key] .wayslot"))).toBe("Занимает 1 место · свободно 1");
  });

  it("the apps named are the ones of the person's device", () => {
    const win = sheet(data("first"), "pick", { platform: "windows" });
    expect([...win.box.querySelectorAll("[data-k=add-way-link] .chip")].map(text)).toEqual(["kl!ck", "Happ"]);
  });

  it("a card tells the page which way was taken", () => {
    const a = actions();
    const { box } = sheet(data("many"), "pick", { a });
    box.querySelector<HTMLButtonElement>("[data-k=add-way-link]")!.click();
    box.querySelector<HTMLButtonElement>("[data-k=add-way-key]")!.click();
    expect(a.log).toEqual(["pane link", "pane key"]);
  });

  it("every slot taken: only the key way waits (a quiet card with the reason and 'Write'); the link stays", () => {
    const { box } = sheet(data("devices-full"), "pick");
    expect(box.querySelector("[data-k=add-way-link]")).not.toBeNull();
    expect(box.querySelector("[data-k=add-way-key]")).toBeNull();
    const off = box.querySelector(".opt.off")!;
    expect(off.tagName).toBe("DIV");
    expect(text(off.querySelector(".opt-d"))).toBe("Занято 5 из 5. Удалите устройство, которым больше не пользуетесь, или напишите — добавим место.");
    expect(text(off.querySelector("a"))).toBe("Написать");
    expect(keyAvailability(data("devices-full"))).toBe("limit");
  });

  it("the owner issues the keys, no profile, the preview: the key way says why; without support there is no 'Write'", () => {
    const off = (name: string, f: (d: MgData) => void = () => {}) => sheet(data(name, f), "pick").box.querySelector(".opt.off")!;
    expect(text(off("amnezia-off").querySelector(".opt-d"))).toBe("Ключи AmneziaVPN выдаёт владелец — напишите, для какого устройства нужен.");
    expect(text(off("amnezia-none").querySelector(".opt-d"))).toBe("Сервер ещё настраивается — напишите администратору.");
    const preview = off("preview");
    expect(text(preview.querySelector(".opt-d"))).toBe("В предпросмотре ключи не создаются.");
    expect(preview.querySelector("a")).toBeNull();
    const none = sheet(data("amnezia-off"), "pick");
    const c = { ...none.c, support: "" };
    const box = document.createElement("div");
    box.append(...(deviceModal(c) as Node[]));
    expect(box.querySelector(".opt.off a")).toBeNull();
    expect(["amnezia-off", "amnezia-none", "preview"].map((n) => keyAvailability(data(n)))).toEqual(["admin", "noprofile", "preview"]);
  });

  it("English", () => {
    const { box } = sheet(data("devices-rich"), "pick", { lang: "en" });
    expect(text(box.querySelector("h3"))).toBe("Add a device");
    expect([...box.querySelectorAll(".opt-t")].map(text)).toEqual(["With an appRecommended", "With a key"]);
    expect(text(box.querySelector("[data-k=add-way-key] .wayslot"))).toBe("Takes 1 slot · 5 free");
  });
});

describe("the link branch: the steps of connecting with an app", () => {
  it("this device, the app (link apps only), how; the way back, and the title of the way that was taken", () => {
    const a = actions();
    const { box, label } = sheet(data("devices-rich"), "link", { a });
    expect(label).toBe("Через приложение");
    expect(text(box.querySelector("h3"))).toBe("Через приложение");
    expect(text(box.querySelector(".sh-head .sub"))).toBe("Все серверы сразу — подписка обновляется сама");
    expect([...box.querySelectorAll(".step .step-t")].map(text)).toEqual(["Ваше устройство", "Приложение", "Как подключиться"]);
    expect([...box.querySelectorAll(".plats .plat")].map(text)).toEqual(["iPhone", "Android", "Windows", "Mac", "Linux"]);
    expect([...box.querySelectorAll(".app .app-n")].map(text)).toEqual(["Happ"]); // AmneziaVPN is the other way
    expect([...box.querySelectorAll(".how-i .how-t")].map(text)).toEqual(["Установите Happ", "Добавьте подписку", "Включите VPN в Happ"]);
    expect(box.querySelector("a[href^='happ://add/']")).not.toBeNull();
    box.querySelector<HTMLButtonElement>("[data-k=add-back]")!.click();
    expect(a.log).toEqual(["pane pick"]);
    expect(box.querySelector("[data-k=add-back]")?.getAttribute("aria-label")).toBe("Назад");
  });

  it("a computer's apps: the recommended one first, the other link app under 'Other apps'", () => {
    const { box } = sheet(data("devices-rich"), "link", { platform: "windows" });
    expect([...box.querySelectorAll(".app .app-n")].map(text)).toEqual(["kl!ckРекомендуем", "Happ"]);
    expect(box.querySelector("a[href^='klick://add?url=']")).not.toBeNull();
  });

  it("link alone: no 'Back' (nothing to go back to), the sheet is named for what it does", () => {
    const { box, label } = sheet(data("happ"), "link");
    expect(box.querySelector("[data-k=add-back]")).toBeNull();
    expect(label).toBe("Подключить приложение");
    expect(text(box.querySelector("h3"))).toBe("Подключить приложение");
  });

  it("it is for adding one more: the steps, never 'Done' about this device", () => {
    const { box } = sheet(data("return"), "link", { over: { marked: true } });
    expect(box.querySelector(".donebox")).toBeNull();
    expect(box.querySelector(".sn.done")).toBeNull();
    expect(box.querySelector(".how")).not.toBeNull();
  });

  it("a choice of the device and the app goes to the page as it does on the first visit", () => {
    const a = actions();
    const { box } = sheet(data("devices-rich"), "link", { platform: "windows", a });
    box.querySelector<HTMLButtonElement>("[data-k=add-plat-android]")!.click();
    box.querySelector<HTMLButtonElement>("[data-k='add-app-happ|Happ']")!.click();
    expect(a.log).toEqual(["platform android", "app happ|Happ"]);
  });

  it("copy the link and the QR code for another device: folded on a phone, open on a computer", () => {
    const a = actions();
    const d = data("devices-rich");
    const { box } = sheet(d, "link", { a });
    box.querySelector<HTMLButtonElement>("[data-k=add-link-copy]")!.click();
    expect(a.log).toEqual([`copy ${d.subscription_url}`, "mark"]);
    const row = box.querySelector<HTMLButtonElement>("[data-k=add-qr-row]")!;
    expect(row.getAttribute("aria-expanded")).toBe("false");
    expect(text(row)).toBe("Подключить другое устройствоQR-код откроет эту страницу на нём");
    expect(box.querySelector(".card.only-m .qrbox")).toBeNull();
    expect(box.querySelector(".only-w .qrbox .qr svg")).not.toBeNull(); // the computer's copy is open
    row.click();
    expect(a.log.at(-1)).toBe("qr true");
    const open = sheet(d, "link", { over: { qrOpen: true } }).box;
    expect(open.querySelector(".card.only-m .qrbox .qr svg")).not.toBeNull();
    expect(box.textContent).not.toContain(d.subscription_url); // the link is copied and scanned, never written out
  });

  it("no QR option or no link app: no QR block; a platform without apps still has the link to copy", () => {
    expect(sheet(data("devices-rich", (d) => (d.options.show_qr = false)), "link").box.querySelector(".qrbox, [data-k=add-qr-row]")).toBeNull();
    const none = sheet(data("nolinux", (d) => ((d.amnezia = cases.both!.amnezia), (d.access.amnezia = true))), "link", { platform: "linux" });
    expect(text(none.box.querySelector(".noapps .h3"))).toBe("Для Linux приложений пока нет");
    expect(none.box.querySelector("[data-k=add-link-copy]")).not.toBeNull();
  });
});

describe("the key branch", () => {
  it("is the form of a new device; with both ways it has the way back", () => {
    const a = actions();
    const { box, label } = sheet(data("devices-rich"), "key", { a });
    expect(label).toBe("Новое устройство");
    expect(text(box.querySelector("h3"))).toBe("Новое устройство");
    expect(box.querySelector("[data-k=modal-create]")).not.toBeNull();
    box.querySelector<HTMLButtonElement>("[data-k=add-back]")!.click();
    expect(a.log).toEqual(["pane pick"]);
    expect(sheet(data("keys-only"), "key").box.querySelector("[data-k=add-back]")).toBeNull();
  });

  it("a key's own sheets win over the pane: a device just made shows its key", () => {
    const d = data("devices-rich");
    const am = { ...newAmzState("ios", "p31", "ios"), adding: true, pane: "pick" as const, created: "d3", configs: { d3: [] } };
    const { box } = sheet(d, "pick", { over: { amz: am } });
    expect(text(box.querySelector("h3"))).toBe("Ключ для «iPhone»");
    expect(box.querySelector("[data-k=add-way-link]")).toBeNull();
  });
});

describe("the keys of the page stay unique (the page puts the keyboard back by them)", () => {
  const names = Object.keys(cases).filter((n) => n !== "locked");
  it("every state, page and every pane of the dialog: no key twice, and none shared between the page and the sheet", () => {
    for (const name of names) {
      for (const platform of ["ios", "windows"] as const) {
        const d = data(name);
        const page = keys(view(d, state(platform, { returning: name !== "first" && name !== "happ" }), actions()));
        expect(dupes(page), `${name} ${platform} page`).toEqual([]);
        for (const pane of ["pick", "link", "key"] as const) {
          const s = keys(sheet(d, pane, { platform }).box);
          expect(dupes(s), `${name} ${platform} ${pane}`).toEqual([]);
          expect(s.filter((k) => page.includes(k)), `${name} ${platform} ${pane} shared with the page`).toEqual([]);
        }
      }
    }
  });

  it("open menus and questions add no key twice either", () => {
    const d = data("devices-rich");
    for (const over of [{ menu: "d6" }, { confirm: { id: "d6", kind: "remove" as const } }, { confirm: { id: "d3", kind: "rotate" as const } }, { rename: { id: "d6", value: "Router" } }]) {
      const el = view(d, state("windows", { returning: true, amz: { ...newAmzState("windows", "p31", "windows"), ...over } }), actions());
      expect(dupes(keys(el))).toEqual([]);
    }
    expect(amzNoop.pane).toBeDefined();
  });
});
